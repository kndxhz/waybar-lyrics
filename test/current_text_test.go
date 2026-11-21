package main

import "testing"

func TestCurrentTextHidesTextWhenLyricGap(t *testing.T) {
	appConfig = defaultConfig()
	appConfig.MPRISMS = 0
	lyrics := &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 5000, Text: "第一句"}}}
	metadata := &PlayerMetadata{Title: "歌名", Artist: "歌手", Position: 1000 * 1000}

	text, show := currentText(metadata, lyrics)
	if show || text != "" {
		t.Fatalf("前奏没有对应歌词时不应输出任何文本，got %q show=%t", text, show)
	}

	metadata.Position = 6000 * 1000
	text, show = currentText(metadata, lyrics)
	if !show || text != "第一句" {
		t.Fatalf("进入歌词后应输出第一句，got %q show=%t", text, show)
	}
}

func TestCurrentTextFallsBackToTitleWithoutLyrics(t *testing.T) {
	appConfig = defaultConfig()
	metadata := &PlayerMetadata{Title: "歌名", Artist: "歌手", Position: 1000 * 1000}

	text, show := currentText(metadata, nil)
	if !show || text != "歌手 - 歌名" {
		t.Fatalf("没有歌词时应输出「歌手 - 歌名」，got %q show=%t", text, show)
	}
}

func TestCurrentTextKeepsPreviousLineOnEmptyLine(t *testing.T) {
	appConfig = defaultConfig()
	appConfig.MPRISMS = 0
	lyrics := &ParsedLyrics{Original: []TimedLyricLine{
		{TimeMs: 0, Text: "第一句"},
		{TimeMs: 2000, Text: ""},
	}}
	metadata := &PlayerMetadata{Title: "歌名", Artist: "歌手", Position: 3000 * 1000}

	text, show := currentText(metadata, lyrics)
	if !show || text != "第一句" {
		t.Fatalf("空行应沿用上一句，got %q show=%t", text, show)
	}
}
