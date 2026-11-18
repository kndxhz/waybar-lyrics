package main

import "testing"

func TestBuildCurrentLyricTextCarriesOverEmptyLine(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.MPRISMS = 0
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := &ParsedLyrics{
		Source: lyricsSourceNCM,
		Original: []TimedLyricLine{
			{TimeMs: 0, Text: "第一行"},
			{TimeMs: 5000, Text: ""},    // 间奏空行
			{TimeMs: 9000, Text: "   "}, // 只有空白的行
			{TimeMs: 12000, Text: "第四行"},
		},
	}
	metadata := &PlayerMetadata{Player: "chromium.instance100036"}

	// 落在空行上时要沿用最近一条非空歌词，不能返回空串让界面退回「歌手 - 歌名」。
	for _, positionMs := range []int64{5000, 6000, 9000, 11000} {
		metadata.Position = positionMs * 1000
		if got := buildCurrentLyricText(metadata, lyrics); got != "第一行" {
			t.Fatalf("position=%dms 时应沿用上一行歌词，got %q", positionMs, got)
		}
	}

	metadata.Position = 12000 * 1000
	if got := buildCurrentLyricText(metadata, lyrics); got != "第四行" {
		t.Fatalf("position=12000ms 应显示第四行，got %q", got)
	}

	// 第一行之前没有可沿用的歌词，返回空串由调用方回退显示「歌手 - 歌名」。
	metadata.Position = -2 * 1000 // 进度早于第一行（Position 单位是微秒）
	if got := buildCurrentLyricText(metadata, lyrics); got != "" {
		t.Fatalf("第一行之前应返回空串，got %q", got)
	}
}

func TestBuildCurrentLyricTextKeepsTranslationOnEmptyOriginal(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	appConfig.MPRISMS = 0
	t.Cleanup(func() { appConfig = oldConfig })

	lyrics := &ParsedLyrics{
		Source: lyricsSourceNCM,
		Original: []TimedLyricLine{
			{TimeMs: 0, Text: ""},
			{TimeMs: 4000, Text: "下一行"},
		},
		Translation: []TimedLyricLine{
			{TimeMs: 0, Text: "只有翻译"},
		},
	}
	metadata := &PlayerMetadata{Player: "vlc", Position: 1000 * 1000}
	if got := buildCurrentLyricText(metadata, lyrics); got != "只有翻译" {
		t.Fatalf("原文为空但该行有翻译时应显示翻译，got %q", got)
	}
}

func TestMergeLyricOutcomeKeepsLyricsWhenRefreshFails(t *testing.T) {
	current := &ParsedLyrics{Source: lyricsSourceNCM, Original: []TimedLyricLine{{TimeMs: 0, Text: "歌词"}}}

	// 刷新 API 歌词、切回播放器歌词时解析落空，保留已有歌词。
	for _, reason := range []string{resolveReasonRefreshAPI, resolveReasonUpgradePlayer} {
		got := mergeLyricOutcome(current, lyricOutcome{reason: reason})
		if got != current {
			t.Fatalf("%s 落空时应保留原歌词", reason)
		}
	}

	// 首次解析落空就应当清空，好让界面显示「歌手 - 歌名」并继续重试。
	if got := mergeLyricOutcome(current, lyricOutcome{reason: resolveReasonInitial}); got != nil {
		t.Fatalf("首次解析落空时应清空歌词，got %+v", got)
	}

	// 有结果时正常替换。
	next := &ParsedLyrics{Source: lyricsSourceAPI, Original: []TimedLyricLine{{TimeMs: 0, Text: "新歌词"}}}
	if got := mergeLyricOutcome(current, lyricOutcome{reason: resolveReasonRefreshAPI, lyrics: next}); got != next {
		t.Fatalf("有结果时应替换歌词")
	}
}
