package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

var base_163 = "https://music.163.com"
var base_lrclib = "https://lrclib.net"
var base_ttml = "https://raw.githubusercontent.com/amll-dev/amll-ttml-db/main/ncm-lyrics"

// version 为当前发布版本，可用 -ldflags "-X main.version=…" 覆盖
var version = "1.5.8"

// 歌词来源标识，会追加到 Waybar tooltip 中
const (
	lyricsSourceAPI    = "API"    // 播放器 API 自带歌词（SPlayer-Next）
	lyricsSourceTTML   = "TTML"   // AMLL TTML DB
	lyricsSourceNCM    = "NCM"    // 网易云音乐
	lyricsSourceLrcLib = "LRCLib" // LRCLib
)

// LyricData 定义歌词API响应结构
type LyricData struct {
	Lrc struct {
		Version int    `json:"version"`
		Lyric   string `json:"lyric"`
	} `json:"lrc"`
	Tlyric struct {
		Version int    `json:"version"`
		Lyric   string `json:"lyric"`
	} `json:"tlyric"`
	Code int `json:"code"`
}

// LyricResult 定义歌词返回结果
type LyricResult struct {
	Original    string // 原歌词
	Translation string // 翻译歌词
}

type WaybarOutput struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class,omitempty"`
}

type TimedLyricLine struct {
	TimeMs int64
	Text   string
}

type ParsedLyrics struct {
	Original    []TimedLyricLine
	Translation []TimedLyricLine
	Source      string // 歌词来源，取值见 lyricsSource* 常量
}

// SearchResult 定义搜索API响应结构
type SearchResult struct {
	Result struct {
		Songs []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
		} `json:"songs"`
		SongCount int `json:"songCount"`
	} `json:"result"`
	Code int `json:"code"`
}

// LrcLibResult 定义 lrclib 搜索响应
type LrcLibResult struct {
	ID           int    `json:"id"`
	TrackName    string `json:"trackName"`
	ArtistName   string `json:"artistName"`
	AlbumName    string `json:"albumName"`
	Duration     int    `json:"duration"`
	Instrumental bool   `json:"instrumental"`
	PlainLyrics  string `json:"plainLyrics"`
	SyncedLyrics string `json:"syncedLyrics"`
}

// PlayerMetadata 定义播放器元数据
type PlayerMetadata struct {
	Player   string // 播放器名称
	TrackID  string // 歌曲ID (splayer用)
	Source   string // 歌曲来源平台 (splayer-next 用，例如 netease)
	Title    string
	Artist   string
	Album    string
	Duration int64 // 时长 (微秒)
	Position int64 // 播放位置 (微秒)
	ArtUrl   string
	Lyrics   *ParsedLyrics // 播放器 API 已提供的歌词
}

func main() {
	options, err := parseCLI(os.Args[1:], os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误：", err)
		os.Exit(2)
	}
	if options.ShowVersion {
		fmt.Printf("waybar-lyrics %s\n", version)
		return
	}

	appConfig, err = loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	appConfig, err = applyModeOverride(appConfig, options.Mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误：", err)
		os.Exit(1)
	}
	if options.Command != "" {
		// 不启动歌词循环，也不打开/清空正在运行的歌词进程日志。
		if err := controlPlayer(options.Command); err != nil {
			fmt.Fprintln(os.Stderr, "播放控制失败：", err)
			os.Exit(1)
		}
		return
	}
	logFile, err := openLogFile(appConfig.Log.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开日志文件失败：", err)
		os.Exit(1)
	}
	defer logFile.Close()
	configureLogLevel(appConfig.Log.Level)
	logEvent("INFO", "程序启动: mode=%s latency_ms=%d mpris_ms=%d mpris_fallback=%t", appConfig.Mode, appConfig.SPlayer.LatencyMS, appConfig.MPRISMS, mprisFallbackEnabled(appConfig))
	if appConfig.Mode == "mpris" && appConfig.MPRISFallback {
		logEvent("WARN", "mpris 模式本身即 MPRIS，mpris-fallback 配置无效，已忽略")
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	lastSongKey := ""
	lastTrackID := ""
	var idRetry nextIDRetry
	lastRendered := ""
	var currentLyrics *ParsedLyrics
	lastResolve := time.Time{}
	songStartedAt := time.Now()
	apiLyricsRefreshed := false
	playerUnavailable := false
	lyricsFailureLogged := false

	for {
		metadata := getPlayerMetadata()
		if metadata == nil {
			if !playerUnavailable {
				logEvent("WARN", "播放器不可用或没有播放中的曲目")
				playerUnavailable = true
			}
			empty := renderWaybar(WaybarOutput{Text: "", Tooltip: "", Class: "stopped"})
			if empty != lastRendered {
				fmt.Println(empty)
				lastRendered = empty
			}
			lastSongKey = ""
			lastTrackID = ""
			idRetry = nextIDRetry{}
			<-ticker.C
			continue
		}

		if playerUnavailable {
			logEvent("INFO", "播放器已恢复")
			playerUnavailable = false
		}
		songKey := buildSongKey(metadata)
		trackID := metadata.TrackID
		if trackID == "0" {
			trackID = ""
		}
		trackChanged := metadata.Player == "splayer-next" && lastTrackID != "" && trackID != "" && lastTrackID != trackID
		idArrived := metadata.Player == "splayer-next" && lastTrackID == "" && trackID != ""
		if songKey != lastSongKey || trackChanged {
			logTrackChange(metadata)
			lastSongKey = songKey
			currentLyrics = nil
			lastResolve = time.Time{}
			songStartedAt = time.Now()
			apiLyricsRefreshed = false
			lyricsFailureLogged = false
			idRetry = nextIDRetry{}
			lastTrackID = ""
		} else if idArrived && currentLyrics != nil && currentLyrics.Source != lyricsSourceAPI {
			currentLyrics = nil
			lastResolve = time.Time{}
		}
		if trackID != "" {
			lastTrackID = trackID
		}

		if currentLyrics == nil {
			waitingForID := idRetry.shouldWait(metadata, time.Now())
			if hasPlayerLyrics(metadata) || (!waitingForID && !shouldWaitForPlayerLyrics(metadata, time.Since(songStartedAt))) {
				if lastResolve.IsZero() || lyricsNeedRefresh(currentLyrics, metadata, time.Since(lastResolve)) {
					// 播放器大多在切歌后才异步去取歌词，首次解析可能落空。
					// 这里在歌词为空时持续重试，避免整首歌都卡在“歌手 - 歌名”。
					currentLyrics = resolveLyrics(metadata)
					logLyricsResult("解析歌词", currentLyrics, &lyricsFailureLogged)
					lastResolve = time.Now()
					apiLyricsRefreshed = apiLyricsUpdateDue(currentLyrics, time.Since(songStartedAt))
				}
			}
		} else if shouldRefreshAPILyrics(currentLyrics, metadata, time.Since(songStartedAt), apiLyricsRefreshed) {
			// SPlayer-Next 可能在切歌后继续请求质量更高、优先级更高的歌词，
			// 5 秒后再读取一次，避免翻译等字段与播放器显示的不一致。
			currentLyrics = resolveLyrics(metadata)
			logLyricsResult("刷新 API 歌词", currentLyrics, &lyricsFailureLogged)
			lastResolve = time.Now()
			apiLyricsRefreshed = true
		} else if shouldUpgradeLyrics(currentLyrics, metadata) {
			// 先用了外部歌词源，之后播放器才拿到自己的歌词。切回播放器歌词，
			// 避免同一首歌的歌词与 SPlayer 显示的不一致。
			currentLyrics = resolveLyrics(metadata)
			logLyricsResult("切换至播放器歌词", currentLyrics, &lyricsFailureLogged)
			lastResolve = time.Now()
			apiLyricsRefreshed = apiLyricsUpdateDue(currentLyrics, time.Since(songStartedAt))
		}

		text := buildCurrentLyricText(metadata, currentLyrics)
		if text == "" {
			text = buildText(metadata)
		}

		out := WaybarOutput{
			Text:    text,
			Tooltip: buildTooltip(metadata, currentLyrics),
			Class:   "lyrics",
		}
		rendered := renderWaybar(out)
		if rendered != lastRendered {
			fmt.Println(rendered)
			lastRendered = rendered
		}

		<-ticker.C
	}
}

func buildSongKey(metadata *PlayerMetadata) string {
	trackID := metadata.TrackID
	if metadata.Player == "splayer-next" {
		trackID = ""
	}
	return strings.Join([]string{
		metadata.Player,
		trackID,
		metadata.Source,
		metadata.Title,
		metadata.Artist,
		metadata.Album,
	}, "|")
}

func buildText(metadata *PlayerMetadata) string {
	if metadata.Artist == "" {
		return metadata.Title
	}
	return metadata.Artist + " - " + metadata.Title
}

// buildTooltip 在“歌手 - 歌名”后追加歌词来源，未解析到歌词时不显示来源。
func buildTooltip(metadata *PlayerMetadata, lyrics *ParsedLyrics) string {
	text := buildText(metadata)
	if lyrics == nil || lyrics.Source == "" {
		return text
	}
	return text + " - " + lyrics.Source
}

func renderWaybar(out WaybarOutput) string {
	buf, err := json.Marshal(out)
	if err != nil {
		return `{"text":"","tooltip":"","class":"error"}`
	}
	return string(buf)
}

// lyricRetryInterval 歌词解析失败后的重试间隔
const lyricRetryInterval = 5 * time.Second

// nextIDRetry counts spaced ID checks after the initial missing ID.
type nextIDRetry struct {
	attempts    int
	lastAttempt time.Time
}

func (r *nextIDRetry) shouldWait(metadata *PlayerMetadata, now time.Time) bool {
	if metadata.Player != "splayer-next" || appConfig.SPlayer.SongIDMode == "search" {
		return false
	}
	if id := extractTrackID(metadata.TrackID); id != "" && id != "0" {
		return false
	}
	if r.lastAttempt.IsZero() {
		r.lastAttempt = now
		return true
	}
	if r.attempts < 5 && now.Sub(r.lastAttempt) >= lyricRetryInterval {
		r.attempts++
		r.lastAttempt = now
	}
	return r.attempts < 5
}

// lyricAPIGracePeriod 切歌后等待播放器 API 歌词的时间。
// 播放器的歌词通常晚于播放状态就绪，立即回退到外部源会导致歌词与播放器不一致。
const lyricAPIGracePeriod = 2 * time.Second

// lyricAPIUpdateDelay 使用 API 歌词时，切歌后再次读取歌词的延迟。
// SPlayer 可能在切歌后继续请求质量更高、优先级更高的歌词，翻译等字段会随之变化。
const lyricAPIUpdateDelay = 5 * time.Second

func hasPlayerLyrics(metadata *PlayerMetadata) bool {
	return metadata != nil && metadata.Lyrics != nil && len(metadata.Lyrics.Original) > 0
}

// apiLyricsUpdateDue 判断当前是否已经进入 API 歌词的二次更新窗口。
func apiLyricsUpdateDue(current *ParsedLyrics, songElapsed time.Duration) bool {
	return current != nil && current.Source == lyricsSourceAPI && songElapsed >= lyricAPIUpdateDelay
}

// shouldRefreshAPILyrics 判断是否需要在切歌 5 秒后重新读取播放器 API 歌词。
func shouldRefreshAPILyrics(current *ParsedLyrics, metadata *PlayerMetadata, songElapsed time.Duration, refreshed bool) bool {
	return !refreshed && apiLyricsUpdateDue(current, songElapsed) && hasPlayerLyrics(metadata)
}

// shouldWaitForPlayerLyrics 判断是否还应等待播放器自带歌词，暂时不要回退到外部源。
func shouldWaitForPlayerLyrics(metadata *PlayerMetadata, songElapsed time.Duration) bool {
	if !isSPlayerNextMetadata(metadata) || hasPlayerLyrics(metadata) {
		return false
	}
	return songElapsed < lyricAPIGracePeriod
}

// shouldUpgradeLyrics 判断是否应从外部歌词源切换到播放器自带歌词。
func shouldUpgradeLyrics(current *ParsedLyrics, metadata *PlayerMetadata) bool {
	return current != nil && current.Source != lyricsSourceAPI && hasPlayerLyrics(metadata)
}

// lyricsNeedRefresh 判断是否需要在同一首歌内重新解析歌词
func lyricsNeedRefresh(current *ParsedLyrics, metadata *PlayerMetadata, sinceLast time.Duration) bool {
	if current != nil {
		return false
	}
	if metadata.Lyrics != nil && len(metadata.Lyrics.Original) > 0 {
		return true
	}
	return sinceLast >= lyricRetryInterval
}

func resolveLyrics(metadata *PlayerMetadata) *ParsedLyrics {
	lyrics := lookupLyrics(metadata)
	if lyrics == nil || (len(lyrics.Original) == 0 && len(lyrics.Translation) == 0) {
		// 网易云对一些歌曲会返回空歌词（例如 "sfy":true 的版权限制），
		// 必须当作解析失败，否则空结果会被当成“已解析”而不再重试，
		// 整首歌都会卡在“歌手 - 歌名”。
		return nil
	}
	return lyrics
}

func lookupLyrics(metadata *PlayerMetadata) *ParsedLyrics {
	if metadata.Lyrics != nil && len(metadata.Lyrics.Original) > 0 {
		if metadata.Lyrics.Source == "" {
			metadata.Lyrics.Source = lyricsSourceAPI
		}
		return metadata.Lyrics
	}

	// 旧版 SPlayer 与 SPlayer-Next 会直接提供歌曲 ID，普通 MPRIS 播放器
	// 只有标题和歌手，需要调用网易云搜索接口。
	songID := resolveSongID(metadata)
	if songID != "" {
		if ttml := getTTMLLyrics(songID); ttml != nil {
			return ttml
		}

		result := get_lyrics(songID)
		if result != nil {
			if strings.TrimSpace(result.Translation) == "" {
				orig, trans := parseLRCWithInlineTranslation(result.Original)
				if len(trans) > 0 {
					return &ParsedLyrics{
						Original:    orig,
						Translation: trans,
						Source:      lyricsSourceNCM,
					}
				}
			}

			parsed := &ParsedLyrics{
				Original:    parseLRC(result.Original),
				Translation: parseLRC(result.Translation),
				Source:      lyricsSourceNCM,
			}
			if len(parsed.Original) > 0 || len(parsed.Translation) > 0 {
				return parsed
			}
		}
	}

	durationSec := int(metadata.Duration / 1000000)
	lrcLibResult := searchLrcLib(metadata.Title, metadata.Artist, metadata.Album, durationSec)
	if lrcLibResult != nil {
		if lrcLibResult.SyncedLyrics != "" {
			return &ParsedLyrics{Original: parseLRC(lrcLibResult.SyncedLyrics), Source: lyricsSourceLrcLib}
		}
		if lrcLibResult.PlainLyrics != "" {
			return &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: lrcLibResult.PlainLyrics}}, Source: lyricsSourceLrcLib}
		}
	}

	return nil
}

// resolveSongID 选择查询歌词用的网易云歌曲 ID。普通 MPRIS 播放器的
// mpris:trackid 只是播放器内部路径，不能当作歌曲 ID，必须按歌名和歌手搜索。
func resolveSongID(metadata *PlayerMetadata) string {
	if appConfig.SPlayer.SongIDMode != "search" {
		// SPlayer-Next 的 ID 只有来源为网易云时才能用于查询网易云歌词。
		if (metadata.Player == "splayer-next" && (metadata.Source == "" || metadata.Source == "netease")) ||
			(metadata.Player != "splayer-next" && matchesPlayerMode(metadata.Player, "splayer")) {
			if id := extractTrackID(metadata.TrackID); id != "" && id != "0" {
				return id
			}
		}
	}
	return searchSong(metadata.Title, metadata.Artist)
}

type ttmlContent struct {
	Text  string
	Child *ttmlNode
}

type ttmlNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Content  []ttmlContent
	Children []*ttmlNode
}

func parseTTML(content string) *ParsedLyrics {
	decoder := xml.NewDecoder(strings.NewReader(content))
	var stack []*ttmlNode
	var root *ttmlNode

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}

		switch token := token.(type) {
		case xml.StartElement:
			node := &ttmlNode{Name: token.Name, Attrs: token.Attr}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.Content = append(parent.Content, ttmlContent{Child: node})
				parent.Children = append(parent.Children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Content = append(stack[len(stack)-1].Content, ttmlContent{Text: string(token)})
			}
		}
	}

	if root == nil {
		return nil
	}

	parsed := &ParsedLyrics{}
	collectTTMLParagraphs(root, parsed)
	if len(parsed.Original) == 0 {
		return nil
	}
	sort.SliceStable(parsed.Original, func(i, j int) bool {
		return parsed.Original[i].TimeMs < parsed.Original[j].TimeMs
	})
	sort.SliceStable(parsed.Translation, func(i, j int) bool {
		return parsed.Translation[i].TimeMs < parsed.Translation[j].TimeMs
	})
	return parsed
}

func collectTTMLParagraphs(node *ttmlNode, parsed *ParsedLyrics) {
	if node.Name.Local == "p" {
		var original strings.Builder
		var translation strings.Builder
		var firstWordTime int64 = -1
		collectTTMLText(node, &original, &translation, &firstWordTime, false)
		startMs, ok := parseTTMLTime(attrValue(node, "begin"))
		if !ok {
			startMs = firstWordTime
		}
		originalText := strings.TrimSpace(original.String())
		translationText := strings.TrimSpace(translation.String())
		if startMs >= 0 && originalText != "" {
			parsed.Original = append(parsed.Original, TimedLyricLine{TimeMs: startMs, Text: originalText})
			if translationText != "" {
				parsed.Translation = append(parsed.Translation, TimedLyricLine{TimeMs: startMs, Text: translationText})
			}
		}
		return
	}
	for _, child := range node.Children {
		collectTTMLParagraphs(child, parsed)
	}
}

func collectTTMLText(node *ttmlNode, original, translation *strings.Builder, firstWordTime *int64, inTranslation bool) {
	role := attrValue(node, "role")
	if strings.Contains(role, "roman") {
		return
	}
	inTranslation = inTranslation || strings.Contains(role, "translation")
	if node.Name.Local == "span" && attrValue(node, "begin") != "" {
		if beginMs, ok := parseTTMLTime(attrValue(node, "begin")); ok && (*firstWordTime < 0 || beginMs < *firstWordTime) {
			*firstWordTime = beginMs
		}
	}
	for _, item := range node.Content {
		if item.Child != nil {
			collectTTMLText(item.Child, original, translation, firstWordTime, inTranslation)
			continue
		}
		if inTranslation {
			translation.WriteString(item.Text)
		} else {
			original.WriteString(item.Text)
		}
	}
}

func attrValue(node *ttmlNode, localName string) string {
	for _, attr := range node.Attrs {
		if attr.Name.Local == localName {
			return attr.Value
		}
	}
	return ""
}

func parseTTMLTime(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	for _, unit := range []struct {
		suffix string
		factor float64
	}{{"ms", 1}, {"h", 3600000}, {"m", 60000}, {"s", 1000}} {
		if strings.HasSuffix(value, unit.suffix) {
			number, err := strconv.ParseFloat(strings.TrimSuffix(value, unit.suffix), 64)
			if err != nil {
				return 0, false
			}
			return int64(number * unit.factor), true
		}
	}

	parts := strings.Split(value, ":")
	if len(parts) == 3 {
		hours, err1 := strconv.ParseFloat(parts[0], 64)
		minutes, err2 := strconv.ParseFloat(parts[1], 64)
		seconds, err3 := strconv.ParseFloat(parts[2], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, false
		}
		return int64((hours*3600 + minutes*60 + seconds) * 1000), true
	}

	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return int64(seconds * 1000), true
}

func getTTMLLyrics(songID string) *ParsedLyrics {
	requestURL := strings.TrimRight(base_ttml, "/") + "/" + url.PathEscape(songID) + ".ttml"
	client := &http.Client{Timeout: 8 * time.Second}
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil
	}
	request.Header.Set("User-Agent", "waybar-lyrics")

	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil
	}
	lyrics := parseTTML(string(body))
	if lyrics != nil {
		lyrics.Source = lyricsSourceTTML
	}
	return lyrics
}

// lrcLinePattern 匹配 LRC 风格的时间戳。网易云大部分歌词用「[mm:ss.xx]」，
// 但也有用户上传的歌词用冒号分隔毫秒（「[mm:ss:xx]」，如不少同人曲），
// 两种都必须认，否则整首歌会被当成没有时间轴。
var lrcLinePattern = regexp.MustCompile(`\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]`)

// lrcMatchTimeMs 把一条时间戳匹配折算成毫秒。
func lrcMatchTimeMs(match []string) (int64, bool) {
	mm, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	ss, err := strconv.Atoi(match[2])
	if err != nil {
		return 0, false
	}

	ms := 0
	if len(match) > 3 && match[3] != "" {
		frac := match[3]
		switch len(frac) {
		case 1:
			frac += "00"
		case 2:
			frac += "0"
		}
		ms, _ = strconv.Atoi(frac)
	}

	return int64(mm*60*1000 + ss*1000 + ms), true
}

func parseLRC(content string) []TimedLyricLine {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	var result []TimedLyricLine

	for _, line := range strings.Split(content, "\n") {
		matches := lrcLinePattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		text := strings.TrimSpace(lrcLinePattern.ReplaceAllString(line, ""))
		for _, m := range matches {
			timeMs, ok := lrcMatchTimeMs(m)
			if !ok {
				continue
			}
			result = append(result, TimedLyricLine{TimeMs: timeMs, Text: text})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TimeMs < result[j].TimeMs
	})

	return result
}

func parseLRCWithInlineTranslation(content string) ([]TimedLyricLine, []TimedLyricLine) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	var original []TimedLyricLine
	var translation []TimedLyricLine

	for _, line := range strings.Split(content, "\n") {
		matches := lrcLinePattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		text := strings.TrimSpace(lrcLinePattern.ReplaceAllString(line, ""))
		parts := strings.SplitN(text, "|", 2)
		origText := strings.TrimSpace(parts[0])
		transText := ""
		if len(parts) == 2 {
			transText = strings.TrimSpace(parts[1])
		}

		for _, m := range matches {
			timeMs, ok := lrcMatchTimeMs(m)
			if !ok {
				continue
			}
			if origText != "" {
				original = append(original, TimedLyricLine{TimeMs: timeMs, Text: origText})
			}
			if transText != "" {
				translation = append(translation, TimedLyricLine{TimeMs: timeMs, Text: transText})
			}
		}
	}

	sort.Slice(original, func(i, j int) bool {
		return original[i].TimeMs < original[j].TimeMs
	})
	sort.Slice(translation, func(i, j int) bool {
		return translation[i].TimeMs < translation[j].TimeMs
	})

	return original, translation
}

func currentLineAt(lines []TimedLyricLine, positionMs int64) string {
	if len(lines) == 0 {
		return ""
	}

	idx := sort.Search(len(lines), func(i int) bool {
		return lines[i].TimeMs > positionMs
	})
	if idx == 0 {
		return ""
	}

	return strings.TrimSpace(lines[idx-1].Text)
}

func currentLineIndex(lines []TimedLyricLine, positionMs int64) int {
	if len(lines) == 0 {
		return -1
	}
	idx := sort.Search(len(lines), func(i int) bool {
		return lines[i].TimeMs > positionMs
	})
	return idx - 1
}

// translationForLine 取原歌词第 idx 行对应的翻译。
// 翻译行可能只覆盖部分歌词（甚至只有第 0 行），因此必须按时间窗口
// 限定在当前行内，不能沿用“最后一个早于 position 的翻译行”。
func translationForLine(lyrics *ParsedLyrics, idx int) string {
	if idx < 0 || idx >= len(lyrics.Original) {
		return ""
	}
	start := lyrics.Original[idx].TimeMs
	end := int64(-1)
	if idx+1 < len(lyrics.Original) {
		end = lyrics.Original[idx+1].TimeMs
	}

	i := sort.Search(len(lyrics.Translation), func(i int) bool {
		return lyrics.Translation[i].TimeMs >= start
	})
	if i >= len(lyrics.Translation) {
		return ""
	}
	if end >= 0 && lyrics.Translation[i].TimeMs >= end {
		return ""
	}
	return strings.TrimSpace(lyrics.Translation[i].Text)
}

// lyricLatencyMS 返回当前元数据使用的歌词补偿（毫秒）：SPlayer-Next 的进度
// 经 HTTP API 返回，用 splayer.latency_ms；其余从 MPRIS 读取的歌曲（mpris
// 模式以及 splayer / splayer-next 的回退）用顶层 mpris-ms。
func lyricLatencyMS(metadata *PlayerMetadata) int {
	if metadata == nil {
		return 0
	}
	if isSPlayerNextMetadata(metadata) {
		return appConfig.SPlayer.LatencyMS
	}
	return appConfig.MPRISMS
}

func buildCurrentLyricText(metadata *PlayerMetadata, lyrics *ParsedLyrics) string {
	if lyrics == nil {
		return ""
	}

	positionMs := metadata.Position / 1000
	if latency := lyricLatencyMS(metadata); latency != 0 {
		// 正数提前歌词以抵消进度滞后；负数延后歌词，用于修正播放器进度偏快。
		positionMs += int64(latency)
	}
	idx := currentLineIndex(lyrics.Original, positionMs)

	original := ""
	translation := ""
	if idx >= 0 {
		original = strings.TrimSpace(lyrics.Original[idx].Text)
		translation = translationForLine(lyrics, idx)
	} else {
		// 只有翻译、没有原文的歌词源
		translation = currentLineAt(lyrics.Translation, positionMs)
	}

	if original == "" && translation == "" {
		return ""
	}

	if translation == "" || translation == original {
		return original
	}

	if original == "" {
		return translation
	}

	return original + "\n" + translation
}

// mprisBusPrefix 是 MPRIS 播放器在会话总线上的服务名前缀。
const mprisBusPrefix = "org.mpris.MediaPlayer2."

// mprisBusNames 读取会话总线上的全部服务名，测试可替换它固定播放器列表，
// 避免本机正在播放的播放器干扰选择结果。
var mprisBusNames = func(conn *dbus.Conn) ([]string, error) {
	var names []string
	if err := conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil, err
	}
	return names, nil
}

// mprisPlayerData 是一台 MPRIS 播放器的读取结果，用于在多个播放器之间挑选。
type mprisPlayerData struct {
	name     string // 播放器短名，如 vlc、firefox.instance_1_2
	status   string // PlaybackStatus：Playing、Paused 等
	metadata *PlayerMetadata
}

// getMPRISMetadata 通过 D-Bus MPRIS 接口获取播放器元数据。
// 浏览器等播放器即使没有在播放也会常驻注册 MPRIS 服务，Metadata 里只有时长没有歌名，
// 而 ListNames 的返回顺序并不代表谁在播放，因此不能直接取第一个匹配项：
// 必须遍历所有符合模式的播放器，跳过读不到歌名的服务，并优先选择正在播放的那个。
func getMPRISMetadata(preferred string) *PlayerMetadata {
	return getMPRISMetadataFiltered(preferred, false)
}

// getMPRISMetadataStatus 返回选中播放器的元数据与 PlaybackStatus。
func getMPRISMetadataStatus(preferred string) (*PlayerMetadata, string) {
	return readMPRISMetadata(preferred, false)
}

// getMPRISMetadataFiltered 读取 MPRIS 元数据；playingOnly 为 true 时只考虑
// 正在播放的播放器，用于 SPlayer 暂停后切到别的媒体源。
func getMPRISMetadataFiltered(preferred string, playingOnly bool) *PlayerMetadata {
	metadata, _ := readMPRISMetadata(preferred, playingOnly)
	return metadata
}

func readMPRISMetadata(preferred string, playingOnly bool) (*PlayerMetadata, string) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, ""
	}
	// 会话总线是进程内共享连接，关闭它会同时断开其他使用者（例如同一进程里
	// 导出的假 MPRIS 服务），因此这里只复用、不关闭。

	names, err := mprisBusNames(conn)
	if err != nil {
		return nil, ""
	}

	mode := preferred
	if mode == "" {
		mode = "mpris"
	}

	var candidates []mprisPlayerData
	for _, name := range names {
		if !strings.HasPrefix(name, mprisBusPrefix) {
			continue
		}
		shortName := strings.TrimPrefix(name, mprisBusPrefix)
		// 按模式选择播放器：旧 SPlayer 不回退到其他 MPRIS，普通 MPRIS 排除 SPlayer。
		if !matchesPlayerMode(shortName, mode) {
			continue
		}
		metadata, status, ok := readMPRISPlayer(conn, name, shortName)
		if !ok {
			continue
		}
		if playingOnly && mprisStatusRank(status) != 0 {
			continue
		}
		candidates = append(candidates, mprisPlayerData{name: shortName, status: status, metadata: metadata})
	}

	chosen := chooseMPRISPlayerData(candidates)
	if chosen == nil {
		return nil, ""
	}
	return chosen.metadata, chosen.status
}

// chooseMPRISPlayer 在候选播放器里挑一个：正在播放优先，其次暂停，
// 同一状态保留先出现的那个。没有可用候选时返回 nil。
func chooseMPRISPlayer(candidates []mprisPlayerData) *PlayerMetadata {
	chosen := chooseMPRISPlayerData(candidates)
	if chosen == nil {
		return nil
	}
	return chosen.metadata
}

// chooseMPRISPlayerData 与 chooseMPRISPlayer 一致，但连同状态一起返回。
func chooseMPRISPlayerData(candidates []mprisPlayerData) *mprisPlayerData {
	var best *mprisPlayerData
	bestRank := 0
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.metadata == nil || candidate.metadata.Title == "" {
			continue
		}
		rank := mprisStatusRank(candidate.status)
		if best == nil || rank < bestRank {
			best, bestRank = candidate, rank
		}
	}
	return best
}

// mprisStatusRank 把 PlaybackStatus 折算成优先级，数值越小越优先。
func mprisStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "playing":
		return 0
	case "paused":
		return 1
	default:
		return 2
	}
}

// readMPRISPlaybackStatus 只读一台播放器的 PlaybackStatus，用于控制命令挑选目标。
func readMPRISPlaybackStatus(conn *dbus.Conn, busName string) (string, bool) {
	var variant dbus.Variant
	err := conn.Object(busName, "/org/mpris/MediaPlayer2").
		Call("org.freedesktop.DBus.Properties.Get", 0, "org.mpris.MediaPlayer2.Player", "PlaybackStatus").
		Store(&variant)
	if err != nil {
		return "", false
	}
	status, ok := variant.Value().(string)
	if !ok || strings.TrimSpace(status) == "" {
		return "", false
	}
	return status, true
}

// readMPRISPlayer 读取单个播放器的元数据。没有可用歌名（未播放、没有曲目、
// 或只有占位标题）时返回 ok=false，让调用方继续尝试其他播放器。
func readMPRISPlayer(conn *dbus.Conn, busName, playerName string) (*PlayerMetadata, string, bool) {
	var props map[string]dbus.Variant
	err := conn.Object(busName, "/org/mpris/MediaPlayer2").
		Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.mpris.MediaPlayer2.Player").Store(&props)
	if err != nil {
		return nil, "", false
	}

	metadata, status, ok := metadataFromMPRISProps(props, playerName)
	if !ok {
		return nil, "", false
	}
	// MPRIS 模式下取到的标题往往只是文件名或网页标题，启用 LLM 时用它提取歌名歌手。
	// LLM 判定不是歌曲时按没识别到歌曲处理，跳过这台播放器。
	if llmEnabled(appConfig) && !applyLLMExtraction(metadata, mprisMetadataText(props)) {
		return nil, "", false
	}
	return metadata, status, true
}

// metadataFromMPRISProps 把 GetAll 拿到的属性转换成元数据。
// 各播放器给出的字段差异很大（Chromium 有完整的标题歌手，Firefox 只有网页标题，
// mpv 播放网络流时只有 xesam:url，Telegram 只有语音消息占位标题），
// 统一在这里清洗，读不到可用歌名时返回 ok=false。
func metadataFromMPRISProps(props map[string]dbus.Variant, playerName string) (*PlayerMetadata, string, bool) {
	metadataMap, ok := props["Metadata"].Value().(map[string]dbus.Variant)
	if !ok || len(metadataMap) == 0 {
		return nil, "", false
	}

	metadata := &PlayerMetadata{
		Player:  playerName,
		TrackID: mprisString(metadataMap["mpris:trackid"]),
		Title:   mprisString(metadataMap["xesam:title"]),
		// Chromium / Edge 等会把多个歌手放进列表，拼起来显示和搜索都更准。
		Artist: mprisJoinedString(metadataMap["xesam:artist"], ", "),
		Album:  mprisString(metadataMap["xesam:album"]),
		ArtUrl: mprisString(metadataMap["mpris:artUrl"]),
	}
	if length, ok := mprisInt64(metadataMap["mpris:length"]); ok {
		metadata.Duration = length
	}
	if position, ok := mprisInt64(props["Position"]); ok {
		metadata.Position = position
	}

	// 网络流和语音消息通常只在 xesam:url 里带文件名，歌名为空时用它兜底。
	if !normalizeMPRISMetadata(metadata, mprisString(metadataMap["xesam:url"])) {
		return nil, "", false
	}
	return metadata, mprisString(props["PlaybackStatus"]), true
}

// mprisStrings 取出属性里的全部非空字符串。同一个属性在不同播放器里可能是
// 字符串、字符串列表甚至 ObjectPath；列表里还可能出现空字符串
// （Firefox 播放本地文件时 xesam:artist 就是一个只含空串的列表）。
func mprisStrings(value dbus.Variant) []string {
	var values []string
	appendValue := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			values = append(values, text)
		}
	}
	switch v := value.Value().(type) {
	case string:
		appendValue(v)
	case dbus.ObjectPath:
		appendValue(string(v))
	case []string:
		for _, item := range v {
			appendValue(item)
		}
	case []interface{}:
		for _, item := range v {
			if text, ok := item.(string); ok {
				appendValue(text)
			}
		}
	}
	return values
}

// mprisString 取第一个非空字符串，用于歌名、专辑这类单值属性。
func mprisString(value dbus.Variant) string {
	values := mprisStrings(value)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// mprisJoinedString 把列表属性拼成一个字符串，用于多个歌手。
func mprisJoinedString(value dbus.Variant, separator string) string {
	return strings.Join(mprisStrings(value), separator)
}

// mprisInt64 读取 MPRIS 的整数属性。规范要求 int64，但有些播放器返回 uint64。
func mprisInt64(value dbus.Variant) (int64, bool) {
	switch v := value.Value().(type) {
	case int64:
		return v, true
	case uint64:
		return int64(v), true
	case int32:
		return int64(v), true
	case uint32:
		return int64(v), true
	case int:
		return int64(v), true
	}
	return 0, false
}

// audioFileExtension 本地文件、网络流和语音消息的歌名会带上扩展名，
// 不去掉就搜不到歌词（也可能搜到别的东西）。
var audioFileExtension = regexp.MustCompile(`(?i)\.(flac|mp3|m4a|aac|ogg|oga|opus|wav|wma|aiff?|ape|wv|dsf|dff|mka|webm|mp4|m4v|mkv|mov|avi|ts|m3u8?|pls|xspf)$`)

// browserTitleSuffixes 浏览器把网页标题直接当作歌名，需要去掉站点后缀。
var browserTitleSuffixes = []string{
	" - YouTube Music",
	" - YouTube",
	" - Spotify",
	" | Spotify",
	" - SoundCloud",
	// B 站网页标题固定以「_哔哩哔哩_bilibili」结尾，长的写法必须排在前面。
	"_哔哩哔哩_bilibili",
	"_bilibili",
	"_哔哩哔哩",
	" - 哔哩哔哩",
	" - bilibili",
}

// leadingTrackNumber 文件名里的音轨号，例如 "01 - 歌手 - 歌名.flac" 的前缀。
var leadingTrackNumber = regexp.MustCompile(`^\d{1,3}\s*[.\-]\s+`)

// mprisPlaceholderTitles 播放器没有曲目信息时给出的占位标题，
// 拿这些去搜索只会得到不相干的歌词，应当视为“没有歌名”。
var mprisPlaceholderTitles = []string{
	"unknown",
	"unknown track",
	"unknown title",
	"no title",
	"untitled",
	"voice message",
	"video message",
	"未知曲目",
	"未知",
	"语音消息",
	"视频消息",
}

// normalizeMPRISMetadata 修正播放器给的歌名和歌手，返回是否得到了可用歌名。
// 本地文件、浏览器和网络流的标题往往是文件名或网页标题（"歌手 - 歌名.flac"、
// "歌名 - YouTube Music"），歌手字段还可能是空字符串。不清洗就会拿着
// 整个文件名去搜索，结果一条歌词都查不到。
func normalizeMPRISMetadata(metadata *PlayerMetadata, mediaURL string) bool {
	metadata.Artist = strings.TrimSpace(metadata.Artist)
	metadata.Album = strings.TrimSpace(metadata.Album)

	title := cleanMPRISTitle(metadata.Title)
	if title == "" && mediaURL != "" {
		// mpv 播放网络流时歌名常常是空的，只能从 xesam:url 的文件名推断。
		// 占位标题（如 Telegram 的“语音消息”）不算空，不能拿 URL 里的
		// voice_messages_1 这类文件名去搜索。
		title = cleanMPRISTitle(fileNameFromURL(mediaURL))
	}

	// 播放器没有提供歌手时，尝试从 "歌手 - 歌名" 形式的标题里拆出来，
	// 顺带去掉文件名开头的音轨号。
	if metadata.Artist == "" {
		if artist, name, ok := splitTitleArtist(leadingTrackNumber.ReplaceAllString(title, "")); ok {
			metadata.Artist, title = artist, name
		}
	}

	metadata.Title = title
	return usableMPRISTitle(title, metadata.Player)
}

// usableMPRISTitle 判断歌名是否可用于显示和搜索。
func usableMPRISTitle(title, playerName string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	lower := strings.ToLower(title)
	for _, placeholder := range mprisPlaceholderTitles {
		if lower == placeholder {
			return false
		}
	}
	// VLC 等播放器在没有曲目时会把播放器名当歌名。
	base := strings.SplitN(playerName, ".", 2)[0]
	return !strings.EqualFold(title, playerName) && !strings.EqualFold(title, base)
}

// cleanMPRISTitle 去掉 URL、文件名扩展名和网页标题后缀，得到可搜索的歌名。
func cleanMPRISTitle(title string) string {
	title = strings.TrimSpace(title)
	if looksLikeURL(title) {
		title = fileNameFromURL(title)
	}
	title = strings.TrimSpace(audioFileExtension.ReplaceAllString(title, ""))
	for _, suffix := range browserTitleSuffixes {
		if len(title) > len(suffix) && strings.EqualFold(title[len(title)-len(suffix):], suffix) {
			title = strings.TrimSpace(title[:len(title)-len(suffix)])
			break
		}
	}
	return strings.TrimSpace(title)
}

// looksLikeURL 判断播放器是不是直接把播放地址当成了歌名。
func looksLikeURL(value string) bool {
	return strings.Contains(value, "://") && !strings.ContainsAny(value, " \t")
}

// fileNameFromURL 从 xesam:url 里取出文件名（含扩展名，保留给后续清洗）。
func fileNameFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		return ""
	}
	name := path.Base(parsed.Path)
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	if name == "/" || name == "." {
		return ""
	}
	return name
}

// splitTitleArtist 按第一个 " - " 拆分标题；拆完任一侧为空时放弃，
// 避免把本来就是歌名的标题拆坏。
func splitTitleArtist(title string) (string, string, bool) {
	const separator = " - "
	index := strings.Index(title, separator)
	if index <= 0 {
		return "", "", false
	}
	artist := strings.TrimSpace(title[:index])
	name := strings.TrimSpace(title[index+len(separator):])
	if artist == "" || name == "" {
		return "", "", false
	}
	return artist, name, true
}

// searchSong 搜索歌曲，返回歌曲ID
func searchSong(title, artist string) string {
	// 构建搜索词：艺术家 标题；播放器没有歌手时只用标题。
	query := strings.TrimSpace(artist + " " + title)

	// 调用网易云搜索API
	encodedQuery := url.QueryEscape(query)
	searchURL := base_163 + "/api/search/get/web?s=" + encodedQuery + "&type=1&limit=1"

	client := &http.Client{}
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return ""
	}

	req.Header.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (HTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0")

	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return ""
	}

	var searchResult SearchResult
	err = json.Unmarshal(body, &searchResult)
	if err != nil {
		return ""
	}

	if searchResult.Code != 200 {
		return ""
	}

	if len(searchResult.Result.Songs) == 0 {
		return ""
	}

	// 返回第一个结果的ID
	song_id := strconv.Itoa(searchResult.Result.Songs[0].ID)
	return song_id
}

// searchLrcLib 在 lrclib 中搜索歌词
func searchLrcLib(trackName, artistName, albumName string, duration int) *LrcLibResult {
	// 构建查询参数
	params := url.Values{}
	params.Add("track_name", trackName)
	params.Add("artist_name", artistName)
	params.Add("album_name", albumName)
	params.Add("duration", strconv.Itoa(duration))

	searchURL := base_lrclib + "/api/get?" + params.Encode()

	client := &http.Client{}
	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil
	}

	req.Header.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (HTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0")

	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil
	}

	// 处理 404
	if res.StatusCode == 404 {
		return nil
	}

	if res.StatusCode != 200 {
		return nil
	}

	var lrcLibResult LrcLibResult
	err = json.Unmarshal(body, &lrcLibResult)
	if err != nil {
		return nil
	}

	return &lrcLibResult
}

func get_lyrics(song_id string) *LyricResult {
	//获取网易云歌词接口
	url := base_163 + "/api/song/lyric?id=" + song_id + "&lv=1&tv=1"
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		return nil
	}
	req.Header.Add("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (HTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0")

	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil
	}

	// 解析JSON
	var data LyricData
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil
	}

	if data.Code != 200 {
		return nil
	}

	// 返回解析结果
	return &LyricResult{
		Original:    data.Lrc.Lyric,
		Translation: data.Tlyric.Lyric,
	}
}
