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
	logEvent("INFO", "程序启动: mode=%s latency_ms=%d", appConfig.Mode, appConfig.SPlayer.LatencyMS)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	lastSongKey := ""
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
			<-ticker.C
			continue
		}

		if playerUnavailable {
			logEvent("INFO", "播放器已恢复")
			playerUnavailable = false
		}
		songKey := buildSongKey(metadata)
		if songKey != lastSongKey {
			logTrackChange(metadata)
			lastSongKey = songKey
			currentLyrics = nil
			lastResolve = time.Time{}
			songStartedAt = time.Now()
			apiLyricsRefreshed = false
			lyricsFailureLogged = false
		}

		if currentLyrics == nil {
			if !shouldWaitForPlayerLyrics(metadata, time.Since(songStartedAt)) {
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
	return strings.Join([]string{
		metadata.Player,
		metadata.TrackID,
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
	if appConfig.Mode != "splayer-next" || hasPlayerLyrics(metadata) {
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

			return &ParsedLyrics{
				Original:    parseLRC(result.Original),
				Translation: parseLRC(result.Translation),
				Source:      lyricsSourceNCM,
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
		if metadata.Player == "splayer" ||
			(metadata.Player == "splayer-next" && (metadata.Source == "" || metadata.Source == "netease")) {
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
func parseLRC(content string) []TimedLyricLine {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	lineRe := regexp.MustCompile(`\[(\d{2}):(\d{2})(?:\.(\d{1,3}))?\]`)
	var result []TimedLyricLine

	for _, line := range strings.Split(content, "\n") {
		matches := lineRe.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		text := strings.TrimSpace(lineRe.ReplaceAllString(line, ""))
		for _, m := range matches {
			mm, err1 := strconv.Atoi(m[1])
			ss, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil {
				continue
			}

			ms := 0
			if m[3] != "" {
				frac := m[3]
				switch len(frac) {
				case 1:
					frac += "00"
				case 2:
					frac += "0"
				}
				ms, _ = strconv.Atoi(frac)
			}

			timeMs := int64(mm*60*1000 + ss*1000 + ms)
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

	lineRe := regexp.MustCompile(`\[(\d{2}):(\d{2})(?:\.(\d{1,3}))?\]`)
	var original []TimedLyricLine
	var translation []TimedLyricLine

	for _, line := range strings.Split(content, "\n") {
		matches := lineRe.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		text := strings.TrimSpace(lineRe.ReplaceAllString(line, ""))
		parts := strings.SplitN(text, "|", 2)
		origText := strings.TrimSpace(parts[0])
		transText := ""
		if len(parts) == 2 {
			transText = strings.TrimSpace(parts[1])
		}

		for _, m := range matches {
			mm, err1 := strconv.Atoi(m[1])
			ss, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil {
				continue
			}

			ms := 0
			if m[3] != "" {
				frac := m[3]
				switch len(frac) {
				case 1:
					frac += "00"
				case 2:
					frac += "0"
				}
				ms, _ = strconv.Atoi(frac)
			}

			timeMs := int64(mm*60*1000 + ss*1000 + ms)
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

func buildCurrentLyricText(metadata *PlayerMetadata, lyrics *ParsedLyrics) string {
	if lyrics == nil {
		return ""
	}

	positionMs := metadata.Position / 1000
	if appConfig.Mode == "splayer-next" && appConfig.SPlayer.LatencyMS > 0 {
		// API 播放位置存在网络延迟，提前配置的毫秒数抵消，避免歌词慢半拍。
		positionMs += int64(appConfig.SPlayer.LatencyMS)
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

// getPlayerMetadata 通过 D-Bus MPRIS 接口获取播放器元数据
func getMPRISMetadata(preferred string) *PlayerMetadata {
	// 连接到 D-Bus
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil
	}
	defer conn.Close()

	// 查找所有 MPRIS 播放器服务
	var names []string
	err = conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return nil
	}

	var mediaPlayer dbus.BusObject
	var playerName string
	var foundPlayer bool

	var targetPlayer string
	// 按模式选择播放器：旧 SPlayer 不回退到其他 MPRIS，普通 MPRIS 排除 SPlayer。
	for _, name := range names {
		if !strings.HasPrefix(name, "org.mpris.MediaPlayer2.") {
			continue
		}
		shortName := strings.TrimPrefix(name, "org.mpris.MediaPlayer2.")
		mode := preferred
		if mode == "" {
			mode = "mpris"
		}
		if matchesPlayerMode(shortName, mode) {
			targetPlayer = name
			break
		}
	}

	if targetPlayer != "" {
		mediaPlayer = conn.Object(targetPlayer, "/org/mpris/MediaPlayer2")
		playerName = strings.TrimPrefix(targetPlayer, "org.mpris.MediaPlayer2.")
		foundPlayer = true
	}

	if !foundPlayer {
		return nil
	}

	metadata := &PlayerMetadata{
		Player: playerName,
		Title:  "",
		Artist: "",
		Album:  "",
	}

	// 获取元数据 - 使用 GetAll 获取所有属性
	var allProps map[string]dbus.Variant
	err = mediaPlayer.Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.mpris.MediaPlayer2.Player").Store(&allProps)
	if err != nil {
		return nil
	}

	// 从 Metadata 属性中获取元数据
	var metadata_map map[string]dbus.Variant
	if metadataVar, ok := allProps["Metadata"]; ok {
		if m, ok := metadataVar.Value().(map[string]dbus.Variant); ok {
			metadata_map = m
		}
	}

	if metadata_map == nil {
		return nil
	}

	if val, ok := allProps["Position"]; ok {
		if pos, ok := val.Value().(int64); ok {
			metadata.Position = pos
		}
	}

	// 解析元数据
	if val, ok := metadata_map["mpris:trackid"]; ok {
		if trackid, ok := val.Value().(dbus.ObjectPath); ok {
			metadata.TrackID = string(trackid)
		}
	}

	// 处理 xesam:title - 可能是 string 或 []string
	if val, ok := metadata_map["xesam:title"]; ok {
		v := val.Value()
		switch v := v.(type) {
		case string:
			metadata.Title = v
		case []string:
			if len(v) > 0 {
				metadata.Title = v[0]
			}
		case []interface{}:
			if len(v) > 0 {
				if str, ok := v[0].(string); ok {
					metadata.Title = str
				}
			}
		}
	}

	// 处理 xesam:artist - 可能是 string 或 []string
	if val, ok := metadata_map["xesam:artist"]; ok {
		v := val.Value()
		switch v := v.(type) {
		case string:
			metadata.Artist = v
		case []string:
			if len(v) > 0 {
				metadata.Artist = v[0]
			}
		case []interface{}:
			if len(v) > 0 {
				if str, ok := v[0].(string); ok {
					metadata.Artist = str
				}
			}
		}
	}

	if val, ok := metadata_map["xesam:album"]; ok {
		if album, ok := val.Value().(string); ok {
			metadata.Album = album
		}
	}

	if val, ok := metadata_map["mpris:length"]; ok {
		if length, ok := val.Value().(int64); ok {
			metadata.Duration = length
		}
	}

	if val, ok := metadata_map["mpris:artUrl"]; ok {
		if artUrl, ok := val.Value().(string); ok {
			metadata.ArtUrl = artUrl
		}
	}

	if metadata.Title == "" || metadata.Artist == "" {
		return nil
	}

	return metadata
}

// searchSong 搜索歌曲，返回歌曲ID
func searchSong(title, artist string) string {
	// 构建搜索词：艺术家 标题
	query := artist + " " + title

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
