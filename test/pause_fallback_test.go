package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SPlayer-Next 的 /api/now-playing 快照，playing/state 表示播放状态。
const (
	pausedSPlayerNowPlaying  = `{"track":{"id":"123","source":"netease","title":"暂停中的歌","artists":[{"name":"歌手甲"}],"album":{"name":"专辑"},"duration":200000},"position":1000,"duration":200000,"playing":false,"state":"paused"}`
	playingSPlayerNowPlaying = `{"track":{"id":"456","source":"netease","title":"正在播的歌","artists":[{"name":"歌手乙"}],"album":{"name":"专辑"},"duration":210000},"position":2000,"duration":210000,"playing":true,"state":"playing"}`
	legacySPlayerNowPlaying  = `{"track":{"id":"789","source":"netease","title":"旧接口的歌","artists":[{"name":"歌手丙"}],"album":{"name":"专辑"},"duration":220000}}`
)

func newSPlayerNextServer(t *testing.T, nowPlaying string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/now-playing":
			_, _ = io.WriteString(w, nowPlaying)
		case "/api/lyrics":
			_, _ = io.WriteString(w, `{"lyric":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSPlayerPauseFallbackEnabledFlag(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		mpris  bool
		pause  bool
		expect bool
	}{
		{"默认开启", "splayer-next", true, true, true},
		{"关掉暂停回退", "splayer-next", true, false, false},
		{"关掉 mpris-fallback", "splayer-next", false, true, false},
		{"mpris 模式忽略", "mpris", true, true, false},
	}
	for _, c := range cases {
		config := defaultConfig()
		config.Mode = c.mode
		config.MPRISFallback = c.mpris
		config.SPlayer.PauseMPRISFallback = c.pause
		if got := splayerPauseFallbackEnabled(config); got != c.expect {
			t.Errorf("%s: splayerPauseFallbackEnabled = %t，期望 %t", c.name, got, c.expect)
		}
	}
}

func TestGetPlayerMetadataPausedSPlayerUsesPlayingMPRIS(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.pausecheck", "别的媒体", "别人", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausecheck")
	server := newSPlayerNextServer(t, pausedSPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "pausecheck" || metadata.Title != "别的媒体" {
		t.Fatalf("SPlayer 暂停时应显示正在播放的别的媒体源，实际 %+v", metadata)
	}
}

func TestGetPlayerMetadataPrefersPlayingSPlayer(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.pausecheck", "别的媒体", "别人", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausecheck")
	server := newSPlayerNextServer(t, playingSPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "splayer-next" || metadata.Title != "正在播的歌" {
		t.Fatalf("SPlayer 在播放时应优先显示它，实际 %+v", metadata)
	}
}

func TestGetPlayerMetadataPausedSPlayerKeepsSelfWhenNothingPlaying(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.pausecheck", "暂停的媒体", "别人", "Paused")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausecheck")
	server := newSPlayerNextServer(t, pausedSPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "splayer-next" {
		t.Fatalf("没有别的媒体在播放时应显示 SPlayer，实际 %+v", metadata)
	}
}

func TestGetPlayerMetadataPausedSPlayerWithoutOption(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.pausecheck", "别的媒体", "别人", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausecheck")
	server := newSPlayerNextServer(t, pausedSPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)
	appConfig.SPlayer.PauseMPRISFallback = false

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "splayer-next" {
		t.Fatalf("关闭 pause-mpris-fallback 后应显示 SPlayer，实际 %+v", metadata)
	}
}

func TestGetPlayerMetadataPausedSPlayerWithoutMPRISFallback(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.pausecheck", "别的媒体", "别人", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausecheck")
	server := newSPlayerNextServer(t, pausedSPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)
	appConfig.MPRISFallback = false

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "splayer-next" {
		t.Fatalf("关闭 mpris-fallback 后不应切到别的媒体源，实际 %+v", metadata)
	}
}

func TestGetSPlayerNextPlaybackLegacySnapshotIsPlaying(t *testing.T) {
	server := newSPlayerNextServer(t, legacySPlayerNowPlaying)
	configureControlTest(t, "splayer-next", server.URL)

	metadata, playing := getSPlayerNextPlayback()
	if metadata == nil {
		t.Fatal("应读到 SPlayer 元数据")
	}
	if !playing {
		t.Fatal("旧接口没有播放状态字段时应按正在播放处理")
	}
}

// splayer 模式经 MPRIS 读取，暂停时同样切到正在播放的其他媒体源。
func TestGetPlayerMetadataPausedSPlayerMPRISModeUsesPlayingOther(t *testing.T) {
	fakeMPRIS(t, "org.mpris.MediaPlayer2.splayer", "暂停中的歌", "歌手甲", "Paused")
	fakeMPRIS(t, "org.mpris.MediaPlayer2.othersource", "别的媒体", "别人", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.splayer", "org.mpris.MediaPlayer2.othersource")
	oldConfig := appConfig
	t.Cleanup(func() { appConfig = oldConfig })
	appConfig = defaultConfig()
	appConfig.Mode = "splayer"

	metadata := getPlayerMetadata()
	if metadata == nil {
		t.Fatal("应读到元数据")
	}
	if metadata.Player != "othersource" {
		t.Fatalf("SPlayer 暂停时应显示正在播放的别的媒体源，实际 %+v", metadata)
	}
}
