package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseTTMLSkipsRomanization(t *testing.T) {
	lyrics := parseTTML(`<tt xmlns:ttm="http://www.w3.org/ns/ttml#metadata"><body><p begin="2s"><span begin="2s" end="2.5s">歌</span><span ttm:role="x-roman">ge</span><span ttm:role="x-translation">song</span></p></body></tt>`)
	if lyrics == nil {
		t.Fatal("parseTTML returned nil")
	}
	if len(lyrics.Original) != 1 || lyrics.Original[0].Text != "歌" {
		t.Fatalf("unexpected original lyrics: %#v", lyrics.Original)
	}
	if len(lyrics.Translation) != 1 || lyrics.Translation[0].Text != "song" {
		t.Fatalf("unexpected translation lyrics: %#v", lyrics.Translation)
	}
}

func TestResolveLyricsPrefersTTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/12345.ttml" {
			writer.Write([]byte(`<tt><body><p begin="1s"><span begin="1s" end="1.5s">hello</span></p></body></tt>`))
			return
		}
		t.Errorf("unexpected request to TTML source: %s", request.URL.Path)
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	restoreTTML := base_ttml
	base_ttml = server.URL
	t.Cleanup(func() { base_ttml = restoreTTML })

	metadata := &PlayerMetadata{Player: "splayer", TrackID: "org.mpris.MediaPlayer2.splayer/track/12345"}
	lyrics := resolveLyrics(metadata)
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "hello" {
		t.Fatalf("unexpected lyrics: %#v", lyrics)
	}
	if lyrics.Source != lyricsSourceTTML {
		t.Fatalf("unexpected lyrics source: %q", lyrics.Source)
	}
}

func TestResolveLyricsFallsBackToNCM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/12345.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case request.URL.Path == "/api/song/lyric":
			writer.Write([]byte(`{"lrc":{"lyric":"[00:01.00]from ncm"},"tlyric":{"lyric":""},"code":200}`))
		default:
			t.Errorf("unexpected request: %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163 := base_ttml, base_163
	base_ttml, base_163 = server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163 = restoreTTML, restore163 })

	metadata := &PlayerMetadata{Player: "splayer", TrackID: "org.mpris.MediaPlayer2.splayer/track/12345"}
	lyrics := resolveLyrics(metadata)
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "from ncm" {
		t.Fatalf("unexpected lyrics: %#v", lyrics)
	}
	if lyrics.Source != lyricsSourceNCM {
		t.Fatalf("unexpected lyrics source: %q", lyrics.Source)
	}
}

func TestParseTTMLMergesWordSpansIntoLines(t *testing.T) {
	lyrics := parseTTML(`<tt xmlns="http://www.w3.org/ns/ttml" xmlns:ttm="http://www.w3.org/ns/ttml#metadata"><body><p begin="1.25s"><span begin="1.25s" end="1.5s">Hel</span><span begin="1.5s" end="2s">lo</span> <span begin="2s" end="2.5s">world</span><span ttm:role="x-translation" xml:lang="zh-CN">你好，世界</span></p></body></tt>`)
	if lyrics == nil {
		t.Fatal("parseTTML returned nil")
	}
	if len(lyrics.Original) != 1 || lyrics.Original[0].TimeMs != 1250 || lyrics.Original[0].Text != "Hello world" {
		t.Fatalf("unexpected original lyrics: %#v", lyrics.Original)
	}
	if len(lyrics.Translation) != 1 || lyrics.Translation[0].TimeMs != 1250 || lyrics.Translation[0].Text != "你好，世界" {
		t.Fatalf("unexpected translation lyrics: %#v", lyrics.Translation)
	}
}

func buildTooltipTestMetadata() *PlayerMetadata {
	return &PlayerMetadata{Artist: "歌手", Title: "歌名"}
}

func TestBuildTooltipAppendsLyricsSource(t *testing.T) {
	metadata := buildTooltipTestMetadata()
	tests := []struct {
		name   string
		lyrics *ParsedLyrics
		want   string
	}{
		{"未解析到歌词", nil, "歌手 - 歌名"},
		{"来源为空", &ParsedLyrics{}, "歌手 - 歌名"},
		{"TTML 来源", &ParsedLyrics{Source: lyricsSourceTTML}, "歌手 - 歌名 - TTML"},
		{"NCM 来源", &ParsedLyrics{Source: lyricsSourceNCM}, "歌手 - 歌名 - NCM"},
	}
	for _, test := range tests {
		if got := buildTooltip(metadata, test.lyrics); got != test.want {
			t.Errorf("%s: buildTooltip() = %q, want %q", test.name, got, test.want)
		}
	}

	metadata.Artist = ""
	if got := buildTooltip(metadata, &ParsedLyrics{Source: lyricsSourceAPI}); got != "歌名 - API" {
		t.Errorf("无歌手时应仍追加来源: %q", got)
	}
}

func TestResolveLyricsSourceFromLrcLib(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/2694839533.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/song/lyric":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/get":
			_, _ = writer.Write([]byte(`{"syncedLyrics":"[00:01.00]from lrclib"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163, restoreLrcLib := base_ttml, base_163, base_lrclib
	base_ttml, base_163, base_lrclib = server.URL, server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163, base_lrclib = restoreTTML, restore163, restoreLrcLib })

	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := resolveLyrics(&PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "2694839533", Title: "歌", Artist: "手"})
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "from lrclib" {
		t.Fatalf("unexpected lyrics: %#v", lyrics)
	}
	if lyrics.Source != lyricsSourceLrcLib {
		t.Fatalf("unexpected lyrics source: %q", lyrics.Source)
	}
}

func TestParseTTMLTime(t *testing.T) {
	tests := []struct {
		value string
		want  int64
	}{
		{"1.25s", 1250},
		{"1250ms", 1250},
		{"00:01:01.500", 61500},
		{"1.5", 1500},
	}
	for _, test := range tests {
		got, ok := parseTTMLTime(test.value)
		if !ok || got != test.want {
			t.Errorf("parseTTMLTime(%q) = (%d, %t), want (%d, true)", test.value, got, ok, test.want)
		}
	}
}

func TestCurrentLineIndex(t *testing.T) {
	lines := []TimedLyricLine{{TimeMs: 0, Text: "a"}, {TimeMs: 1000, Text: "b"}, {TimeMs: 2000, Text: "c"}}
	tests := []struct {
		position int64
		want     int
	}{
		{-1, -1},
		{0, 0},
		{999, 0},
		{1000, 1},
		{1999, 1},
		{2000, 2},
		{999999, 2},
	}
	for _, test := range tests {
		if got := currentLineIndex(lines, test.position); got != test.want {
			t.Errorf("currentLineIndex(%d) = %d, want %d", test.position, got, test.want)
		}
	}
	if got := currentLineIndex(nil, 0); got != -1 {
		t.Errorf("空歌词应返回 -1，got %d", got)
	}
}

func TestTranslationForLineStaysInWindow(t *testing.T) {
	lyrics := &ParsedLyrics{
		Original: []TimedLyricLine{{TimeMs: 0, Text: "a"}, {TimeMs: 1000, Text: "b"}, {TimeMs: 2000, Text: "c"}},
		// 稀疏翻译：只有第一行有翻译，后面的行不应复用第一行翻译
		Translation: []TimedLyricLine{{TimeMs: 0, Text: "A"}},
	}
	if got := translationForLine(lyrics, 0); got != "A" {
		t.Errorf("第一行翻译 = %q, want A", got)
	}
	if got := translationForLine(lyrics, 1); got != "" {
		t.Errorf("第二行不应复用第一行翻译，got %q", got)
	}
	if got := translationForLine(lyrics, 2); got != "" {
		t.Errorf("第三行不应复用第一行翻译，got %q", got)
	}

	// 翻译时间戳落在当前行窗口内时才应生效
	lyrics.Translation = []TimedLyricLine{{TimeMs: 0, Text: "A"}, {TimeMs: 1500, Text: "B"}, {TimeMs: 2000, Text: "C"}}
	if got := translationForLine(lyrics, 1); got != "B" {
		t.Errorf("窗口内翻译 = %q, want B", got)
	}
	if got := translationForLine(lyrics, 2); got != "C" {
		t.Errorf("末行翻译 = %q, want C", got)
	}
}

func TestBuildCurrentLyricTextSparseTranslation(t *testing.T) {
	lyrics := &ParsedLyrics{
		Original:    []TimedLyricLine{{TimeMs: 0, Text: "いつか"}, {TimeMs: 1000, Text: "二人はじまった場所"}},
		Translation: []TimedLyricLine{{TimeMs: 0, Text: "曾经"}},
	}
	// Position 单位为微秒，buildCurrentLyricText 内部换算成毫秒
	if got := buildCurrentLyricText(&PlayerMetadata{Position: 1500 * 1000}, lyrics); got != "二人はじまった場所" {
		t.Errorf("第二行不应复用第一行翻译，got %q", got)
	}
	if got := buildCurrentLyricText(&PlayerMetadata{Position: 500 * 1000}, lyrics); got != "いつか\n曾经" {
		t.Errorf("第一行应显示翻译，got %q", got)
	}
}

func TestBuildCurrentLyricTextTranslationOnlySource(t *testing.T) {
	lyrics := &ParsedLyrics{Translation: []TimedLyricLine{{TimeMs: 0, Text: "hello"}}}
	if got := buildCurrentLyricText(&PlayerMetadata{Position: 0}, lyrics); got != "hello" {
		t.Errorf("只有翻译的歌词源应显示翻译，got %q", got)
	}
}

func TestBuildCurrentLyricTextSPlayerNextLeadsByLatency(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := &ParsedLyrics{
		Original: []TimedLyricLine{{TimeMs: 0, Text: "a"}, {TimeMs: 1000, Text: "b"}},
	}
	// 900ms 时原本还没到第二行；SPlayer-Next 提前 100ms 后应显示第二行。
	metadata := &PlayerMetadata{Position: 900 * 1000}

	appConfig.Mode = "mpris"
	if got := buildCurrentLyricText(metadata, lyrics); got != "a" {
		t.Errorf("mpris 模式不应提前歌词，got %q", got)
	}

	appConfig.Mode = "splayer-next"
	if got := buildCurrentLyricText(metadata, lyrics); got != "b" {
		t.Errorf("splayer-next 模式应提前 100ms，got %q", got)
	}

	// 自定义延迟生效
	appConfig.SPlayer.LatencyMS = 200
	metadata.Position = 800 * 1000
	if got := buildCurrentLyricText(metadata, lyrics); got != "b" {
		t.Errorf("latency_ms=200 时应提前 200ms，got %q", got)
	}

	// 0 表示关闭补偿
	appConfig.SPlayer.LatencyMS = 0
	metadata.Position = 900 * 1000
	if got := buildCurrentLyricText(metadata, lyrics); got != "a" {
		t.Errorf("latency_ms=0 时不应提前歌词，got %q", got)
	}
}

func TestLyricsNeedRefresh(t *testing.T) {
	lyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "x"}}}
	tests := []struct {
		name     string
		current  *ParsedLyrics
		metadata *PlayerMetadata
		since    time.Duration
		want     bool
	}{
		{"已有歌词不重试", lyrics, &PlayerMetadata{}, 0, false},
		{"播放器随后提供了歌词，立即可用", nil, &PlayerMetadata{Lyrics: lyrics}, 0, true},
		{"未到重试间隔", nil, &PlayerMetadata{}, lyricRetryInterval - time.Millisecond, false},
		{"到达重试间隔", nil, &PlayerMetadata{}, lyricRetryInterval, true},
	}
	for _, test := range tests {
		if got := lyricsNeedRefresh(test.current, test.metadata, test.since); got != test.want {
			t.Errorf("%s: lyricsNeedRefresh() = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestShouldWaitForPlayerLyrics(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	playerLyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "api"}}}
	tests := []struct {
		name     string
		mode     string
		metadata *PlayerMetadata
		elapsed  time.Duration
		want     bool
	}{
		{"splayer-next 切歌后等待", "splayer-next", &PlayerMetadata{}, 0, true},
		{"splayer-next 等待期结束", "splayer-next", &PlayerMetadata{}, lyricAPIGracePeriod, false},
		{"播放器歌词已就绪", "splayer-next", &PlayerMetadata{Lyrics: playerLyrics}, 0, false},
		{"普通 MPRIS 不等待", "mpris", &PlayerMetadata{}, 0, false},
	}
	for _, test := range tests {
		appConfig.Mode = test.mode
		if got := shouldWaitForPlayerLyrics(test.metadata, test.elapsed); got != test.want {
			t.Errorf("%s: shouldWaitForPlayerLyrics() = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestShouldUpgradeLyrics(t *testing.T) {
	apiLyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "api"}}}
	external := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "ext"}}, Source: lyricsSourceNCM}
	tests := []struct {
		name     string
		current  *ParsedLyrics
		metadata *PlayerMetadata
		want     bool
	}{
		{"无当前歌词", nil, &PlayerMetadata{Lyrics: apiLyrics}, false},
		{"已是播放器歌词", &ParsedLyrics{Source: lyricsSourceAPI}, &PlayerMetadata{Lyrics: apiLyrics}, false},
		{"外部歌词可升级", external, &PlayerMetadata{Lyrics: apiLyrics}, true},
		{"播放器暂无歌词", external, &PlayerMetadata{}, false},
	}
	for _, test := range tests {
		if got := shouldUpgradeLyrics(test.current, test.metadata); got != test.want {
			t.Errorf("%s: shouldUpgradeLyrics() = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestShouldRefreshAPILyrics(t *testing.T) {
	apiLyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "api"}}}
	apiMetadata := &PlayerMetadata{Lyrics: apiLyrics}
	currentAPI := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "api"}}, Source: lyricsSourceAPI}
	currentExternal := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "ext"}}, Source: lyricsSourceNCM}
	tests := []struct {
		name      string
		current   *ParsedLyrics
		metadata  *PlayerMetadata
		elapsed   time.Duration
		refreshed bool
		want      bool
	}{
		{"未到 5 秒", currentAPI, apiMetadata, lyricAPIUpdateDelay - time.Millisecond, false, false},
		{"到达 5 秒", currentAPI, apiMetadata, lyricAPIUpdateDelay, false, true},
		{"已经刷新过", currentAPI, apiMetadata, lyricAPIUpdateDelay * 2, true, false},
		{"外部歌词不刷新", currentExternal, apiMetadata, lyricAPIUpdateDelay, false, false},
		{"API 暂无歌词", currentAPI, &PlayerMetadata{}, lyricAPIUpdateDelay, false, false},
		{"无当前歌词", nil, apiMetadata, lyricAPIUpdateDelay, false, false},
	}
	for _, test := range tests {
		if got := shouldRefreshAPILyrics(test.current, test.metadata, test.elapsed, test.refreshed); got != test.want {
			t.Errorf("%s: shouldRefreshAPILyrics() = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestSPlayerModeUsesMPRISTrackID(t *testing.T) {
	searchCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/2694839533.ttml":
			_, _ = writer.Write([]byte(`<tt><body><p begin="1s"><span begin="1s" end="1.5s">来自 TTML</span></p></body></tt>`))
		case "/api/search/get/web":
			searchCalled = true
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163 := base_ttml, base_163
	base_ttml, base_163 = server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163 = restoreTTML, restore163 })

	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.Mode = "splayer"
	t.Cleanup(func() { appConfig = oldConfig })

	metadata := &PlayerMetadata{
		Player:  "splayer",
		TrackID: "org.mpris.MediaPlayer2.splayer/track/2694839533",
		Title:   "歌",
		Artist:  "手",
	}
	lyrics := resolveLyrics(metadata)
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "来自 TTML" {
		t.Fatalf("未使用 MPRIS 歌曲 ID 解析歌词: %#v", lyrics)
	}
	if lyrics.Source != lyricsSourceTTML {
		t.Fatalf("歌词来源错误: %q", lyrics.Source)
	}
	if searchCalled {
		t.Fatal("MPRIS 提供 ID 后不应再调用搜索接口")
	}
}

func TestMPRISModeSearchesSongID(t *testing.T) {
	searchCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/search/get/web":
			searchCalled = true
			_, _ = writer.Write([]byte(`{"code":200,"result":{"songs":[{"id":2694839533}]}}`))
		case "/2694839533.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/song/lyric":
			_, _ = writer.Write([]byte(`{"lrc":{"lyric":"[00:00.00]搜索得到"},"tlyric":{"lyric":""},"code":200}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163 := base_ttml, base_163
	base_ttml, base_163 = server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163 = restoreTTML, restore163 })

	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.Mode = "mpris"
	t.Cleanup(func() { appConfig = oldConfig })

	// 普通 MPRIS 的 trackid 是播放器内部路径，末尾数字不能当作歌曲 ID。
	lyrics := resolveLyrics(&PlayerMetadata{Player: "vlc", TrackID: "/org/mpris/MediaPlayer2/TrackList/2", Title: "歌", Artist: "手"})
	if !searchCalled {
		t.Fatal("普通 MPRIS 应调用搜索接口获取歌曲 ID")
	}
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "搜索得到" {
		t.Fatalf("未按搜索结果解析歌词: %#v", lyrics)
	}
}

func TestResolveLyricsUsesNeteaseTrackIDDirectly(t *testing.T) {
	searchCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/2694839533.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/song/lyric":
			if got := request.URL.Query().Get("id"); got != "2694839533" {
				t.Errorf("歌词接口使用了错误的 ID: %q", got)
			}
			_, _ = writer.Write([]byte(`{"lrc":{"lyric":"[00:00.00]来自网易云"},"tlyric":{"lyric":""},"code":200}`))
		case "/api/search/get/web":
			searchCalled = true
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163 := base_ttml, base_163
	base_ttml, base_163 = server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163 = restoreTTML, restore163 })

	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := resolveLyrics(&PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "2694839533"})
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "来自网易云" {
		t.Fatalf("未按网易云 ID 解析歌词: %#v", lyrics)
	}
	if searchCalled {
		t.Fatal("netease 来源不应再调用搜索接口")
	}
}

func TestResolveLyricsSearchesForNonNeteaseSource(t *testing.T) {
	var lyricIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/search/get/web":
			_, _ = writer.Write([]byte(`{"code":200,"result":{"songs":[{"id":777}]}}`))
		case "/777.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/song/lyric":
			lyricIDs = append(lyricIDs, request.URL.Query().Get("id"))
			_, _ = writer.Write([]byte(`{"lrc":{"lyric":"[00:00.00]搜索结果歌词"},"tlyric":{"lyric":""},"code":200}`))
		default:
			// 裸数字 TrackID 不应被当作网易云 ID 直接使用
			if request.URL.Path == "/123456.ttml" || request.URL.Query().Get("id") == "123456" {
				t.Errorf("非网易云来源不应直接使用 TrackID: %s", request.URL.String())
			}
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163 := base_ttml, base_163
	base_ttml, base_163 = server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163 = restoreTTML, restore163 })

	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := resolveLyrics(&PlayerMetadata{Player: "splayer-next", Source: "qq", TrackID: "123456", Title: "歌", Artist: "手"})
	if lyrics == nil || len(lyrics.Original) != 1 || lyrics.Original[0].Text != "搜索结果歌词" {
		t.Fatalf("非网易云来源未回退到搜索: %#v", lyrics)
	}
	if len(lyricIDs) != 1 || lyricIDs[0] != "777" {
		t.Fatalf("搜索得到的 ID 未被使用: %v", lyricIDs)
	}
}

func TestResolveLyricsTreatsEmptyResultAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/2694839533.ttml":
			writer.WriteHeader(http.StatusNotFound)
		case "/api/song/lyric":
			// 网易云对版权受限歌曲返回空歌词
			_, _ = writer.Write([]byte(`{"sgc":false,"sfy":true,"lrc":{"lyric":""},"tlyric":{"lyric":""},"code":200}`))
		case "/api/get":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	restoreTTML, restore163, restoreLrcLib := base_ttml, base_163, base_lrclib
	base_ttml, base_163, base_lrclib = server.URL, server.URL, server.URL
	t.Cleanup(func() { base_ttml, base_163, base_lrclib = restoreTTML, restore163, restoreLrcLib })

	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := resolveLyrics(&PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "2694839533", Title: "歌", Artist: "手"})
	if lyrics != nil {
		t.Fatalf("空歌词应视为解析失败以便重试，got %#v", lyrics)
	}
}
