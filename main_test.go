package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
