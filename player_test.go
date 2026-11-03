package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "waybar-lyrics")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("mode: splayer-next\nsplayer:\n  address: 127.0.0.1\n  port: 15558\n  song_id_mode: search\n")
	if err := os.WriteFile(filepath.Join(configDir, "waybar-lyrics.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("XDG_CONFIG_HOME")
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("XDG_CONFIG_HOME", old)
	config := loadConfig()
	if config.Mode != "splayer-next" || config.SPlayer.Port != 15558 || config.SPlayer.SongIDMode != "search" {
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
	config := loadConfig()
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
	if metadata == nil || metadata.Title != "歌曲" || metadata.Lyrics == nil {
		t.Fatalf("API 元数据异常: %#v", metadata)
	}
}
