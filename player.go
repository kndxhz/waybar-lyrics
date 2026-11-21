package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// splayerNextSnapshot 是 GET /api/now-playing 的轻量快照。
// Playing 为 nil 表示旧版本接口没有该字段。
type splayerNextSnapshot struct {
	Track      json.RawMessage `json:"track"`
	NowPlaying json.RawMessage `json:"nowPlaying"`
	Position   int64           `json:"position"`
	Duration   int64           `json:"duration"`
	Playing    *bool           `json:"playing"`
	State      string          `json:"state"`
}

type splayerNextTrack struct {
	ID      string `json:"id"`
	ExtID   string `json:"extId"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
	Duration int64  `json:"duration"`
	Cover    string `json:"cover"`
	Plugin   struct {
		MusicInfo struct {
			ID string `json:"id"`
		} `json:"musicInfo"`
	} `json:"plugin"`
}

func getPlayerMetadata() *PlayerMetadata {
	switch appConfig.Mode {
	case "splayer-next":
		metadata, playing := getSPlayerNextPlayback()
		if metadata == nil {
			return getMPRISFallbackMetadata()
		}
		if playing || !splayerPauseFallbackEnabled(appConfig) {
			return metadata
		}
		// SPlayer 暂停时，别的媒体源在播放就切过去；没人在播仍显示 SPlayer。
		if fallback := getMPRISFallbackMetadataPlaying(); fallback != nil {
			return fallback
		}
		return metadata
	case "splayer":
		metadata, status := getMPRISMetadataStatus("splayer")
		if metadata == nil {
			return getMPRISFallbackMetadata()
		}
		if mprisStatusRank(status) == 0 || !splayerPauseFallbackEnabled(appConfig) {
			return metadata
		}
		if fallback := getMPRISFallbackMetadataPlaying(); fallback != nil {
			return fallback
		}
		return metadata
	default:
		return getMPRISMetadata("")
	}
}

// mprisFallbackEnabled 判断某模式是否启用 MPRIS 回退。
// mpris 模式本身就走 MPRIS，配置无意义，一律忽略。
func mprisFallbackEnabled(config AppConfig) bool {
	return config.Mode != "mpris" && config.MPRISFallback
}

// splayerPauseFallbackEnabled 判断 SPlayer 暂停时是否允许改用 MPRIS 显示
// 其他正在播放的媒体源。需要同时开启 mpris-fallback；mpris 模式没有 SPlayer，
// 该配置没有意义，一律忽略。
func splayerPauseFallbackEnabled(config AppConfig) bool {
	return config.Mode != "mpris" && config.MPRISFallback && config.SPlayer.PauseMPRISFallback
}

// isSPlayerNextMetadata 判断元数据是否来自 SPlayer-Next 的 HTTP API。
// MPRIS 回退得到的元数据不经过 HTTP API，因此不做网络延迟补偿，也不等待 API 歌词。
func isSPlayerNextMetadata(metadata *PlayerMetadata) bool {
	return metadata != nil && metadata.Player == "splayer-next"
}

// getMPRISFallbackMetadata 在 SPlayer 读不到歌曲时，按 mpris 模式的筛选规则
// 读取其他播放器的元数据；关闭 mpris-fallback 后直接返回 nil。
func getMPRISFallbackMetadata() *PlayerMetadata {
	if !mprisFallbackEnabled(appConfig) {
		return nil
	}
	metadata := getMPRISMetadata("")
	if metadata == nil {
		return nil
	}
	logEvent("DEBUG", "SPlayer 不可用，回退到 MPRIS 播放器：%s - %s", metadata.Artist, metadata.Title)
	return metadata
}

// getMPRISFallbackMetadataPlaying 只在其他播放器确实正在播放时返回它的元数据，
// 用于 SPlayer 暂停时切到别的媒体源；回退关闭或没有播放中的播放器时返回 nil。
func getMPRISFallbackMetadataPlaying() *PlayerMetadata {
	if !splayerPauseFallbackEnabled(appConfig) {
		return nil
	}
	metadata := getMPRISMetadataFiltered("", true)
	if metadata == nil {
		return nil
	}
	// 这条日志每个刷新周期都会走到，只在换源时记一次，免得刷屏。
	if signature := metadata.Player + "\x00" + metadata.Title; signature != lastPauseFallbackLog {
		lastPauseFallbackLog = signature
		logEvent("DEBUG", "SPlayer 已暂停，改用 MPRIS 播放器：%s", metadataLabel(metadata))
	}
	return metadata
}

// lastPauseFallbackLog 记录上一次记日志的暂停回退来源，用于去重。
var lastPauseFallbackLog string

// metadataLabel 把元数据拼成「歌手 - 歌名」，没有歌手时只显示歌名。
func metadataLabel(metadata *PlayerMetadata) string {
	if metadata == nil {
		return ""
	}
	if metadata.Artist == "" {
		return metadata.Title
	}
	return metadata.Artist + " - " + metadata.Title
}

func isPlayerCommand(command string) bool {
	switch command {
	case "play", "pause", "play-pause", "next", "previous", "stop":
		return true
	default:
		return false
	}
}

// matchesPlayerMode 与歌词读取共用筛选规则，避免旧版 SPlayer 控制其他播放器。
func matchesPlayerMode(name, mode string) bool {
	isSPlayer := strings.Contains(strings.ToLower(name), "splayer")
	return (mode == "splayer" && isSPlayer) || (mode == "mpris" && !isSPlayer)
}

// controlPlayer 是一次性控制入口。SPlayer 读不到（API 失败、找不到 SPlayer）
// 且启用 mpris-fallback 时，控制命令与歌词读取一样回退到 playerctl 控制其他播放器。
func controlPlayer(command string) error {
	if !isPlayerCommand(command) {
		return fmt.Errorf("不支持的控制命令：%s", command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// SPlayer 暂停、界面已经切到别的媒体源时，控制也跟着走，避免按钮控制的对象
	// 和显示的内容对不上；这一步失败再按原来的方式控制 SPlayer。
	if target := pauseFallbackControlTarget(); target != "" {
		if err := controlFallbackPlayer(ctx, target, command); err == nil {
			return nil
		} else {
			logEvent("WARN", "控制暂停回退的媒体源 %s 失败，改为控制 SPlayer：%v", target, err)
		}
	}
	switch appConfig.Mode {
	case "splayer-next":
		err := controlSPlayerNext(ctx, command)
		if err == nil {
			return nil
		}
		if controlPlayerctlFallback(ctx, err, command) == nil {
			return nil
		}
		return err
	case "splayer":
		err := controlPlayerctlInMode(ctx, "splayer", command)
		if err == nil {
			return nil
		}
		if controlPlayerctlFallback(ctx, err, command) == nil {
			return nil
		}
		return err
	case "mpris":
		return controlPlayerctlInMode(ctx, "mpris", command)
	default:
		return fmt.Errorf("不支持的播放器模式：%s", appConfig.Mode)
	}
}

// pauseFallbackControlTarget 返回暂停回退当前显示的 MPRIS 播放器；
// 空字符串表示暂停回退没有生效，控制命令仍按原方式发给 SPlayer。
// 抽成变量便于测试注入。
var pauseFallbackControlTarget = func() string {
	if !splayerPauseFallbackEnabled(appConfig) {
		return ""
	}
	switch appConfig.Mode {
	case "splayer-next":
		metadata, playing := getSPlayerNextPlayback()
		if metadata == nil || playing {
			return ""
		}
	case "splayer":
		metadata, status := getMPRISMetadataStatus("splayer")
		if metadata == nil || mprisStatusRank(status) == 0 {
			return ""
		}
	default:
		return ""
	}
	fallback := getMPRISFallbackMetadataPlaying()
	if fallback == nil {
		return ""
	}
	return fallback.Player
}

// controlFallbackPlayer 用 playerctl 控制指定的 MPRIS 播放器。
func controlFallbackPlayer(ctx context.Context, player, command string) error {
	if _, err := runPlayerctl(ctx, "--player", player, command); err != nil {
		return err
	}
	logEvent("INFO", "SPlayer 已暂停，已用 playerctl 控制 %s：%s", player, command)
	return nil
}

// controlPlayerctlFallback 在启用 mpris-fallback 时按 mpris 模式筛选并控制其他播放器。
func controlPlayerctlFallback(ctx context.Context, cause error, command string) error {
	if !mprisFallbackEnabled(appConfig) {
		return fmt.Errorf("未启用 mpris-fallback：%w", cause)
	}
	if err := controlPlayerctlInMode(ctx, "mpris", command); err != nil {
		return err
	}
	logEvent("WARN", "SPlayer 不可用（%v），已回退到 playerctl 控制其他播放器", cause)
	return nil
}

func controlSPlayerNext(ctx context.Context, command string) error {
	baseURL := splayerBaseURL()
	if baseURL == "" {
		return errors.New("SPlayer-Next API 地址无效")
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		// 控制请求不跟随重定向，避免 POST 被转成 GET 或发往其他服务。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if command == "play-pause" {
		var status struct {
			State string `json:"state"`
		}
		if err := requestSPlayerControl(ctx, client, baseURL, http.MethodGet, "/status", &status); err != nil {
			return err
		}
		state := strings.ToLower(strings.TrimSpace(status.State))
		if state == "" {
			return errors.New("SPlayer-Next API 未返回播放状态")
		}
		command = "play"
		if state == "playing" {
			command = "pause"
		}
	}
	if command == "previous" {
		command = "prev"
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := requestSPlayerControl(ctx, client, baseURL, http.MethodPost, "/"+command, &result); err != nil {
		return err
	}
	if !result.OK {
		if result.Error != "" {
			return fmt.Errorf("SPlayer-Next API 控制失败：%s", result.Error)
		}
		return errors.New("SPlayer-Next API 未确认控制成功（缺少 ok: true）")
	}
	return nil
}

func requestSPlayerControl(ctx context.Context, client *http.Client, baseURL, method, path string, target any) error {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return fmt.Errorf("创建 SPlayer-Next API 请求失败：%w", err)
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("SPlayer-Next API %s %s 失败：%w", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("读取 SPlayer-Next API 响应失败：%w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("SPlayer-Next API %s %s 返回 %s：%s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析 SPlayer-Next API %s 响应失败：%w", path, err)
	}
	return nil
}

// controlPlayerctlInMode 按指定模式筛选第一个匹配的播放器并交给 playerctl 执行命令。
func controlPlayerctlInMode(ctx context.Context, mode, command string) error {
	target := mprisControlTarget(mode)
	if target == "" {
		return fmt.Errorf("未找到 %s 模式可控制的 MPRIS 播放器", mode)
	}
	// 显式指定要控制的播放器，不依赖 playerctl --list-all 的返回顺序。
	_, err := runPlayerctl(ctx, "--player", target, command)
	return err
}

// mprisControlTarget 选出要控制的 MPRIS 播放器短名。与歌词读取用同一份播放器
// 列表和筛选规则（模式过滤 + PlaybackStatus 排名），不再靠 --list-all 猜。
// 抽成变量便于测试注入。
var mprisControlTarget = func(mode string) string {
	conn, err := dbus.SessionBus()
	if err != nil {
		return ""
	}
	names, err := mprisBusNames(conn)
	if err != nil {
		return ""
	}
	best := ""
	bestRank := 3
	for _, name := range names {
		if !strings.HasPrefix(name, mprisBusPrefix) {
			continue
		}
		shortName := strings.TrimPrefix(name, mprisBusPrefix)
		if !matchesPlayerMode(shortName, mode) {
			continue
		}
		status, ok := readMPRISPlaybackStatus(conn, name)
		if !ok {
			continue
		}
		if rank := mprisStatusRank(status); rank < bestRank {
			best, bestRank = shortName, rank
		}
	}
	return best
}

func runPlayerctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "playerctl", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("未找到 playerctl，请安装 playerctl 后再使用 MPRIS 播放控制")
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("playerctl 执行超时：%w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("playerctl %s 失败：%w；%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func buildSPlayerURL(port int, apiPath string) string {
	address := strings.TrimSpace(appConfig.SPlayer.Address)
	if address == "" {
		address = "127.0.0.1"
	}
	if !strings.Contains(address, "://") {
		if net.ParseIP(address) != nil && strings.Contains(address, ":") {
			address = "[" + address + "]"
		}
		address = "http://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	if parsed.Port() == "" && port > 0 {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(port))
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + apiPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func splayerBaseURL() string {
	return buildSPlayerURL(appConfig.SPlayer.Port, "/api")
}

// fetchJSONAt 请求 SPlayer-Next 的 JSON 接口，失败时返回带接口名的错误，
// 便于调用方按「有没有兜底」决定记 WARN 还是 ERROR。
func fetchJSONAt(baseURL, path string, target any) error {
	if baseURL == "" {
		return errors.New("SPlayer-Next API 地址为空")
	}
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	res, err := client.Get(baseURL + path)
	if err != nil {
		return fmt.Errorf("请求 %s 失败：%w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 返回 %s", path, res.Status)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("读取 %s 响应失败：%w", path, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("解析 %s 响应失败：%w", path, err)
	}
	return nil
}

func fetchJSON(path string, target any) error {
	return fetchJSONAt(splayerBaseURL(), path, target)
}

// lastSPlayerNextError 用于去重：同一台播放器的同一个错误只在第一次出现时记录，
// 避免 SPlayer 没开时每 250ms 刷一条日志。
var lastSPlayerNextError string
var lastSPlayerLyricsError string

// logSPlayerNextUnavailable 按有没有兜底决定日志等级：能回退到 MPRIS 属于已处理的
// 错误，记 WARN；mpris-fallback 关闭时功能直接不可用，记 ERROR。
func logSPlayerNextUnavailable(err error) {
	message := err.Error()
	if message == lastSPlayerNextError {
		return
	}
	lastSPlayerNextError = message
	if mprisFallbackEnabled(appConfig) {
		logEvent("WARN", "读取 SPlayer-Next 曲目失败，已回退到 MPRIS：%v", err)
		return
	}
	logEvent("ERROR", "读取 SPlayer-Next 曲目失败，且 mpris-fallback 已关闭：%v", err)
}

func getSPlayerNextMetadata() *PlayerMetadata {
	metadata, _ := getSPlayerNextPlayback()
	return metadata
}

// getSPlayerNextPlayback 读取 SPlayer-Next 当前曲目，并返回它是否正在播放。
// 旧版本接口没有 state / playing 字段时按正在播放处理，保持原有行为。
func getSPlayerNextPlayback() (*PlayerMetadata, bool) {
	var raw splayerNextSnapshot
	if err := fetchJSON("/now-playing", &raw); err != nil {
		logSPlayerNextUnavailable(err)
		return nil, false
	}
	lastSPlayerNextError = ""
	playing := true
	switch state := strings.ToLower(strings.TrimSpace(raw.State)); {
	case state != "":
		playing = state == "playing"
	case raw.Playing != nil:
		playing = *raw.Playing
	}
	trackData := raw.Track
	if len(trackData) == 0 {
		var nested struct {
			Track json.RawMessage `json:"track"`
		}
		if json.Unmarshal(raw.NowPlaying, &nested) == nil && len(nested.Track) > 0 {
			trackData = nested.Track
		} else {
			trackData = raw.NowPlaying
		}
	}
	var track splayerNextTrack
	if len(trackData) == 0 || json.Unmarshal(trackData, &track) != nil {
		return nil, false
	}
	if track.Title == "" {
		return nil, false
	}
	artist := ""
	if len(track.Artists) > 0 {
		artist = track.Artists[0].Name
	}
	if artist == "" {
		return nil, false
	}
	metadata := &PlayerMetadata{Player: "splayer-next", TrackID: track.ID, Source: track.Source, Title: track.Title, Artist: artist, Album: track.Album.Name, Duration: track.Duration * 1000, ArtUrl: track.Cover}
	if raw.Position > 0 {
		metadata.Position = raw.Position * 1000
	}
	if raw.Duration > 0 {
		metadata.Duration = raw.Duration * 1000
	}
	// track.id 是当前平台的歌曲 ID；extId / plugin.musicInfo.id 只在一级 ID 缺失时兜底，
	// 否则会把网易云等平台的 ID 覆盖成其他 ID，导致歌词查错。
	if metadata.TrackID == "" {
		if track.ExtID != "" {
			metadata.TrackID = track.ExtID
		}
		if track.Plugin.MusicInfo.ID != "" {
			metadata.TrackID = track.Plugin.MusicInfo.ID
		}
	}
	metadata.Lyrics = getSPlayerNextLyrics()
	return metadata, playing
}

func getSPlayerNextLyrics() *ParsedLyrics {
	var response json.RawMessage
	if err := fetchJSON("/lyrics", &response); err != nil {
		// 播放器没有歌词属于常态，后续会按歌名搜索；同一条错误只记一次。
		if message := err.Error(); message != lastSPlayerLyricsError {
			lastSPlayerLyricsError = message
			logEvent("WARN", "读取 SPlayer-Next 歌词失败，改为按歌名搜索：%v", err)
		}
		return nil
	}
	lastSPlayerLyricsError = ""
	return parseSPlayerNextLyrics(response)
}

func parseSPlayerNextLyrics(data []byte) *ParsedLyrics {
	var envelope struct {
		Lyric  json.RawMessage `json:"lyric"`
		Lyrics json.RawMessage `json:"lyrics"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if len(envelope.Lyric) > 0 && string(envelope.Lyric) != "null" {
			data = envelope.Lyric
		} else if len(envelope.Lyrics) > 0 {
			data = envelope.Lyrics
		}
	}
	var lines []struct {
		StartTime  int64  `json:"startTime"`
		EndTime    int64  `json:"endTime"`
		Translated string `json:"translatedLyric"`
		Words      []struct {
			Word string `json:"word"`
		} `json:"words"`
	}
	if json.Unmarshal(data, &lines) != nil || len(lines) == 0 {
		return nil
	}
	result := &ParsedLyrics{}
	for _, line := range lines {
		var text strings.Builder
		for _, word := range line.Words {
			text.WriteString(word.Word)
		}
		if text.Len() == 0 {
			continue
		}
		result.Original = append(result.Original, TimedLyricLine{TimeMs: line.StartTime, Text: text.String()})
		if strings.TrimSpace(line.Translated) != "" {
			result.Translation = append(result.Translation, TimedLyricLine{TimeMs: line.StartTime, Text: line.Translated})
		}
	}
	if len(result.Original) == 0 {
		return nil
	}
	return result
}

func extractTrackID(value string) string {
	if match := regexp.MustCompile(`(?:/|^)([0-9]+)$`).FindStringSubmatch(strings.TrimSpace(value)); len(match) > 1 {
		return match[1]
	}
	return ""
}
