package main

import (
	"sync"
	"testing"
	"time"
)

// waitResolverIdle 等到后台任务结束、pending 被清空。
func waitResolverIdle(t *testing.T, resolver *lyricResolver, identity string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resolver.drain(identity, func(lyricOutcome) {})
		if resolver.pending == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("歌词解析后台任务没有结束")
}

func TestLyricResolverRequestDeduplicates(t *testing.T) {
	oldHook := resolveLyricsHook
	t.Cleanup(func() { resolveLyricsHook = oldHook })

	block := make(chan struct{})
	resolveLyricsHook = func(metadata *PlayerMetadata) *ParsedLyrics {
		<-block
		return &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "歌词"}}, Source: "API"}
	}

	resolver := newLyricResolver()
	metadata := &PlayerMetadata{Player: "chromium.instance1", Title: "歌名"}

	if !resolver.request("key", metadata, resolveReasonInitial) {
		t.Fatal("首次应能投递任务")
	}
	if resolver.request("key", metadata, resolveReasonInitial) {
		t.Fatal("同一曲目已有任务在跑时不应重复投递")
	}

	close(block)
	waitResolverIdle(t, resolver, "key")

	// 结果处理完之后，同一曲目仍可再次投递（歌词为空时需要重试）。
	if !resolver.request("key", metadata, resolveReasonInitial) {
		t.Fatal("pending 清空后应能再次投递")
	}
	waitResolverIdle(t, resolver, "key")
}

func TestLyricResolverDropsStaleOutcome(t *testing.T) {
	oldHook := resolveLyricsHook
	t.Cleanup(func() { resolveLyricsHook = oldHook })

	release := make(chan struct{})
	var oldStarted sync.WaitGroup
	oldStarted.Add(1)
	resolveLyricsHook = func(metadata *PlayerMetadata) *ParsedLyrics {
		if metadata.Title == "旧歌" {
			oldStarted.Done()
			<-release
			return &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "旧歌词"}}, Source: "API"}
		}
		return &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "新歌词"}}, Source: "API"}
	}

	resolver := newLyricResolver()
	resolver.request("old", &PlayerMetadata{Player: "chromium.instance1", Title: "旧歌"}, resolveReasonInitial)
	oldStarted.Wait()

	// 旧任务还没跑完就切歌：新曲目要能立刻投递。
	resolver.reset()
	if !resolver.request("new", &PlayerMetadata{Player: "chromium.instance1", Title: "新歌"}, resolveReasonInitial) {
		t.Fatal("切歌后应能投递新任务")
	}
	close(release)

	var applied []string
	deadline := time.Now().Add(2 * time.Second)
	for len(applied) == 0 && time.Now().Before(deadline) {
		resolver.drain("new", func(outcome lyricOutcome) {
			applied = append(applied, outcome.lyrics.Original[0].Text)
		})
		time.Sleep(5 * time.Millisecond)
	}
	if len(applied) != 1 || applied[0] != "新歌词" {
		t.Fatalf("applied = %v, want [新歌词]（旧曲目的结果应被丢弃）", applied)
	}
}

func TestShouldRequestLyricsWaitsForLLM(t *testing.T) {
	metadata := &PlayerMetadata{Player: "chromium.instance1", Title: "网页标题", Artist: "-"}
	if !shouldRequestLyrics(metadata, nil, false, 0, time.Time{}) {
		t.Fatal("LLM 就绪时应发起歌词解析")
	}

	// LLM 结果没回来：先只显示「歌手 - 歌名」，不搜歌词。
	metadata.llmPending = true
	if shouldRequestLyrics(metadata, nil, false, 0, time.Time{}) {
		t.Fatal("等 LLM 结果时不应搜索歌词")
	}

	// 结果回来（或判定不是歌曲）后恢复搜索。
	metadata.llmPending = false
	metadata.Title, metadata.Artist = "爱言叶V", "初音未来"
	if !shouldRequestLyrics(metadata, nil, false, 0, time.Time{}) {
		t.Fatal("LLM 补全后应发起歌词解析")
	}

	// 已经有歌词时不再发起首次解析。
	if shouldRequestLyrics(metadata, &ParsedLyrics{Original: []TimedLyricLine{{TimeMs: 0, Text: "歌词"}}}, false, 0, time.Time{}) {
		t.Fatal("已有歌词时不应重复解析")
	}
}

func TestShouldRequestLyricsKeepsRetryingWhenEmpty(t *testing.T) {
	metadata := &PlayerMetadata{Player: "chromium.instance1", Title: "歌名"}
	lastResolve := time.Now().Add(-lyricRetryInterval - time.Second)
	if !shouldRequestLyrics(metadata, nil, false, 10*time.Second, lastResolve) {
		t.Fatal("上次解析落空后应在重试间隔后重试")
	}
	// 刚解析过，还没到重试间隔。
	if shouldRequestLyrics(metadata, nil, false, 10*time.Second, time.Now()) {
		t.Fatal("未到重试间隔时不应重试")
	}
}
