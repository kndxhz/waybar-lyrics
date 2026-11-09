package main

import (
	"testing"
	"time"
)

func TestNextIDRetryTimingAndLimit(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	metadata := &PlayerMetadata{Player: "splayer-next", Source: "netease", Title: "song", Artist: "artist"}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var retry nextIDRetry
	if !retry.shouldWait(metadata, start) {
		t.Fatal("missing ID must start a retry window")
	}
	if retry.attempts != 0 {
		t.Fatalf("initial observation is not a retry: got %d", retry.attempts)
	}
	for i := 1; i <= 5; i++ {
		due := start.Add(time.Duration(i) * 5 * time.Second)
		if !retry.shouldWait(metadata, due.Add(-time.Nanosecond)) {
			t.Fatalf("retry %d: stopped before interval elapsed", i)
		}
		if retry.attempts != i-1 {
			t.Fatalf("retry %d: counted early, got %d", i, retry.attempts)
		}
		wait := retry.shouldWait(metadata, due)
		if wait != (i < 5) || retry.attempts != i {
			t.Fatalf("retry %d: wait=%v attempts=%d", i, wait, retry.attempts)
		}
	}
	if retry.shouldWait(metadata, start.Add(time.Minute)) || retry.attempts != 5 {
		t.Fatalf("retry limit must remain exhausted: %+v", retry)
	}
}

func TestNextIDRetryStopsWhenIDAppears(t *testing.T) {
	oldConfig := appConfig
	appConfig = defaultConfig()
	t.Cleanup(func() { appConfig = oldConfig })

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	metadata := &PlayerMetadata{Player: "splayer-next", Source: "netease"}
	var retry nextIDRetry
	if !retry.shouldWait(metadata, start) || !retry.shouldWait(metadata, start.Add(5*time.Second)) {
		t.Fatal("missing ID should keep waiting before retry limit")
	}
	metadata.TrackID = "12345"
	if retry.shouldWait(metadata, start.Add(5*time.Second+time.Millisecond)) {
		t.Fatal("available ID should immediately end waiting")
	}
}

func TestNextSongKeyHandlesLateIDAndSource(t *testing.T) {
	metadata := &PlayerMetadata{Player: "splayer-next", Title: "song", Artist: "artist", Source: "netease"}
	original := buildSongKey(metadata)
	metadata.TrackID = "123"
	if buildSongKey(metadata) != original {
		t.Fatal("late ID should not reset the song")
	}
	metadata.Source = "qq"
	if buildSongKey(metadata) == original {
		t.Fatal("a source change should reset the song")
	}
}

func TestNextIDRetryEligibility(t *testing.T) {
	oldConfig := appConfig
	t.Cleanup(func() { appConfig = oldConfig })
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		metadata PlayerMetadata
		mode     string
		want     bool
	}{
		{"missing ID", PlayerMetadata{Player: "splayer-next", Source: "netease"}, "auto", true},
		{"unknown source", PlayerMetadata{Player: "splayer-next"}, "auto", true},
		{"zero ID", PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "0"}, "auto", true},
		{"invalid ID", PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "local-id"}, "auto", true},
		{"valid ID", PlayerMetadata{Player: "splayer-next", Source: "netease", TrackID: "123"}, "auto", false},
		{"other player", PlayerMetadata{Player: "splayer"}, "auto", false},
		{"forced search", PlayerMetadata{Player: "splayer-next", Source: "netease"}, "search", false},
		{"other source missing ID", PlayerMetadata{Player: "splayer-next", Source: "qq"}, "auto", true},
		{"other source valid ID", PlayerMetadata{Player: "splayer-next", Source: "qq", TrackID: "123"}, "auto", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appConfig = defaultConfig()
			appConfig.SPlayer.SongIDMode = tc.mode
			var retry nextIDRetry
			if got := retry.shouldWait(&tc.metadata, now); got != tc.want {
				t.Fatalf("shouldWait() = %v, want %v", got, tc.want)
			}
		})
	}
}
