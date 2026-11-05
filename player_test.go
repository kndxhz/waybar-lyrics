package main

import (
	"bytes"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSPlayerNextLyrics(t *testing.T) {
	lyrics := parseSPlayerNextLyrics([]byte(`{"lyric":[{"startTime":1200,"endTime":2000,"words":[{"word":"你好"},{"word":"世界"}],"translatedLyric":"Hello world"}]}`))
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "你好世界" || lyrics.Translation[0].Text != "Hello world" {
		t.Fatalf("解析 SPlayer-Next 歌词结果异常: %#v", lyrics)
	}
}

func TestResolveLyricsUsesPlayerLyrics(t *testing.T) {
	lyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "接口歌词"}}}
	got := resolveLyrics(&PlayerMetadata{Title: "歌曲", Artist: "歌手", Lyrics: lyrics})
	if got != lyrics {
		t.Fatalf("未优先使用播放器歌词: %#v", got)
	}
	if got.Source != lyricsSourceAPI {
		t.Fatalf("播放器歌词来源应为 API: %q", got.Source)
	}
}

func TestLoadConfigCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	old := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("XDG_CONFIG_HOME", old)

	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "mpris" || config.SPlayer.Address != "127.0.0.1" || config.SPlayer.Port != 14558 || config.SPlayer.ControlPort != 25884 {
		t.Fatalf("默认配置异常: %#v", config)
	}
	if _, err := os.Stat(configPath()); err != nil {
		t.Fatalf("默认配置文件未自动创建: %v", err)
	}
}

func TestParseCLI(t *testing.T) {
	var output bytes.Buffer
	mode, err := parseCLI([]string{"--mode", "splayer-next"}, &output)
	if err != nil || mode != "splayer-next" {
		t.Fatalf("解析 --mode 异常: mode=%q err=%v", mode, err)
	}
	mode, err = parseCLI([]string{"-m", "splayer"}, &output)
	if err != nil || mode != "splayer" {
		t.Fatalf("解析 -m 异常: mode=%q err=%v", mode, err)
	}
	if _, err = parseCLI([]string{"--invalid"}, &output); err == nil {
		t.Fatal("未知参数应返回错误")
	}
}

func TestApplyModeOverride(t *testing.T) {
	config := defaultConfig()
	config.Mode = "splayer"
	got, err := applyModeOverride(config, "splayer-next")
	if err != nil || got.Mode != "splayer-next" {
		t.Fatalf("命令行模式未覆盖配置: config=%#v err=%v", got, err)
	}
	got, err = applyModeOverride(config, "invalid")
	if err == nil || got.Mode != "invalid" {
		t.Fatalf("非法模式应校验失败: config=%#v err=%v", got, err)
	}
}

func TestParseCLIHelp(t *testing.T) {
	var output bytes.Buffer
	if _, err := parseCLI([]string{"--help"}, &output); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help 应返回 flag.ErrHelp，实际为 %v", err)
	}
	if !strings.Contains(output.String(), "用法：waybar-lyrics") || !strings.Contains(output.String(), "-mode string") {
		t.Fatalf("帮助内容不完整: %s", output.String())
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "waybar-lyrics")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("mode: splayer-next\nsplayer:\n  address: 127.0.0.1\n  port: 15558\n  control_port: 26000\n  song_id_mode: search\n")
	if err := os.WriteFile(filepath.Join(configDir, "waybar-lyrics.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("XDG_CONFIG_HOME", old)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "splayer-next" || config.SPlayer.Port != 15558 || config.SPlayer.ControlPort != 26000 || config.SPlayer.SongIDMode != "search" {
		t.Fatalf("配置解析异常: %#v", config)
	}
}

func TestLoadConfigNestedAPI(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "waybar-lyrics")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("mode: splayer-next\nsplayer:\n  api:\n    address: 192.168.1.20\n    port: 16666\n")
	if err := os.WriteFile(filepath.Join(configDir, "waybar-lyrics.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("XDG_CONFIG_HOME", old)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.SPlayer.Address != "192.168.1.20" || config.SPlayer.Port != 16666 {
		t.Fatalf("嵌套 API 配置解析异常: %#v", config)
	}
}

func TestSPlayerNextMetadataAndLyrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/now-playing":
			_, _ = w.Write([]byte(`{"track":{"id":"local-id","source":"netease","title":"歌曲","artists":[{"name":"歌手"}],"duration":180000}}`))
		case "/api/lyrics":
			_, _ = w.Write([]byte(`{"lyric":[{"startTime":0,"words":[{"word":"接口歌词"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.Mode = "splayer-next"
	appConfig.SPlayer.Address = server.URL
	appConfig.SPlayer.Port = 0
	defer func() { appConfig = oldConfig }()
	metadata := getSPlayerNextMetadata()
	if metadata == nil || metadata.Title != "歌曲" || metadata.Source != "netease" || metadata.Lyrics == nil {
		t.Fatalf("API 元数据异常: %#v", metadata)
	}
}

func TestSPlayerNextMetadataPrefersTrackID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/now-playing":
			_, _ = writer.Write([]byte(`{"track":{"id":"2694839533","extId":"999","source":"netease","title":"歌曲","artists":[{"name":"歌手"}],"plugin":{"musicInfo":{"id":"888"}}}}`))
		case "/api/lyrics":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.Mode = "splayer-next"
	appConfig.SPlayer.Address = server.URL
	appConfig.SPlayer.Port = 0
	defer func() { appConfig = oldConfig }()

	metadata := getSPlayerNextMetadata()
	if metadata == nil {
		t.Fatal("getSPlayerNextMetadata returned nil")
	}
	if metadata.TrackID != "2694839533" {
		t.Fatalf("应优先使用 track.id，got %q", metadata.TrackID)
	}
}

func TestSPlayerBaseURL(t *testing.T) {
	oldConfig := appConfig
	defer func() { appConfig = oldConfig }()

	tests := []struct {
		name    string
		address string
		port    int
		want    string
	}{
		{name: "host and configured port", address: "127.0.0.1", port: 14558, want: "http://127.0.0.1:14558/api"},
		{name: "explicit port", address: "http://localhost:15558", port: 14558, want: "http://localhost:15558/api"},
		{name: "base path", address: "https://example.com/player/", port: 14558, want: "https://example.com:14558/player/api"},
		{name: "ipv6 host", address: "::1", port: 14558, want: "http://[::1]:14558/api"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appConfig = defaultConfig()
			appConfig.SPlayer.Address = test.address
			appConfig.SPlayer.Port = test.port
			if got := splayerBaseURL(); got != test.want {
				t.Fatalf("API 地址错误: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestSPlayerControlBaseURL(t *testing.T) {
	oldConfig := appConfig
	defer func() { appConfig = oldConfig }()

	tests := []struct {
		name        string
		address     string
		controlPort int
		want        string
	}{
		{name: "default control port", address: "127.0.0.1", controlPort: 25884, want: "http://127.0.0.1:25884/api/control"},
		{name: "explicit port", address: "http://localhost:26000", controlPort: 25884, want: "http://localhost:26000/api/control"},
		{name: "base path", address: "http://example.com/player/", controlPort: 25884, want: "http://example.com:25884/player/api/control"},
		{name: "ipv6 host", address: "::1", controlPort: 25884, want: "http://[::1]:25884/api/control"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appConfig = defaultConfig()
			appConfig.SPlayer.Address = test.address
			appConfig.SPlayer.ControlPort = test.controlPort
			if got := splayerControlBaseURL(); got != test.want {
				t.Fatalf("ControlAPI 地址错误: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestGetSPlayerSongID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/control/song-info" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = writer.Write([]byte(`{"code":200,"message":"获取当前播放信息成功","data":{"id":2694839533,"name":"歌","artists":"手"}}`))
	}))
	defer server.Close()

	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.SPlayer.Address = server.URL
	appConfig.SPlayer.ControlPort = 0
	defer func() { appConfig = oldConfig }()

	if got := getSPlayerSongID(); got != "2694839533" {
		t.Fatalf("ControlAPI 歌曲 ID 错误: %q", got)
	}
}

func TestParseSPlayerSongID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"数字 ID", `{"code":200,"data":{"id":2694839533}}`, "2694839533"},
		{"字符串 ID", `{"code":200,"data":{"id":"12345"}}`, "12345"},
		{"直接返回歌曲对象", `{"id":12345}`, "12345"},
		{"本地歌曲", `{"code":200,"data":{"id":0,"path":"/music/a.flac"}}`, ""},
		{"零 ID", `{"code":200,"data":{"id":0}}`, ""},
		{"错误码", `{"code":500,"message":"失败","data":null}`, ""},
		{"空数据", `{"code":200,"data":null}`, ""},
		{"非法 ID", `{"code":200,"data":{"id":"abc"}}`, ""},
	}
	for _, test := range tests {
		if got := parseSPlayerSongID([]byte(test.body)); got != test.want {
			t.Errorf("%s: parseSPlayerSongID() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestExtractTrackID(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"2694839533", "2694839533"},
		{"org.mpris.MediaPlayer2.splayer/track/12345", "12345"},
		{"local-id", ""},
		{"", ""},
		{"org.mpris.MediaPlayer2.splayer/track/", ""},
	}
	for _, test := range tests {
		if got := extractTrackID(test.value); got != test.want {
			t.Errorf("extractTrackID(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}
