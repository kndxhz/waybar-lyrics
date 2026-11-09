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
)

type splayerNextSnapshot struct {
	Track    json.RawMessage `json:"track"`
	Playing  json.RawMessage `json:"nowPlaying"`
	State    string          `json:"state"`
	Position int64           `json:"position"`
	Duration int64           `json:"duration"`
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
		return getSPlayerNextMetadata()
	case "splayer":
		return getMPRISMetadata("splayer")
	default:
		return getMPRISMetadata("")
	}
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

// controlPlayer 是一次性控制入口；API 失败时不回退到其他播放器。
func controlPlayer(command string) error {
	if !isPlayerCommand(command) {
		return fmt.Errorf("不支持的控制命令：%s", command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	switch appConfig.Mode {
	case "splayer-next":
		return controlSPlayerNext(ctx, command)
	case "mpris", "splayer":
		return controlWithPlayerctl(ctx, command)
	default:
		return fmt.Errorf("不支持的播放器模式：%s", appConfig.Mode)
	}
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

func controlWithPlayerctl(ctx context.Context, command string) error {
	output, err := runPlayerctl(ctx, "--list-all")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(output), "\n") {
		player := strings.TrimSpace(line)
		if player != "" && matchesPlayerMode(player, appConfig.Mode) {
			_, err := runPlayerctl(ctx, "--player", player, command)
			return err
		}
	}
	return fmt.Errorf("未找到 %s 模式可控制的 MPRIS 播放器", appConfig.Mode)
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

func fetchJSONAt(baseURL, path string, target any) bool {
	if baseURL == "" {
		return false
	}
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	res, err := client.Get(baseURL + path)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(res.Body)
	return err == nil && json.Unmarshal(body, target) == nil
}

func fetchJSON(path string, target any) bool {
	return fetchJSONAt(splayerBaseURL(), path, target)
}

func getSPlayerNextMetadata() *PlayerMetadata {
	var raw struct {
		Track      json.RawMessage `json:"track"`
		NowPlaying json.RawMessage `json:"nowPlaying"`
		Position   int64           `json:"position"`
		Duration   int64           `json:"duration"`
	}
	if !fetchJSON("/now-playing", &raw) {
		return nil
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
		return nil
	}
	if track.Title == "" {
		return nil
	}
	artist := ""
	if len(track.Artists) > 0 {
		artist = track.Artists[0].Name
	}
	if artist == "" {
		return nil
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
	return metadata
}

func getSPlayerNextLyrics() *ParsedLyrics {
	var response json.RawMessage
	if !fetchJSON("/lyrics", &response) {
		return nil
	}
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
