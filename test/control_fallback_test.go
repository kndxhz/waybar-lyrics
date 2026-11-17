package main

import (
	"io"
	"net/http"
	"testing"
)

func TestControlPlayerFallsBackToPlayerctl(t *testing.T) {
	for _, test := range []struct {
		name, mode, players, selected string
		apiFails                      bool
	}{
		{
			name:     "splayer-next API 失败回退其他播放器",
			mode:     "splayer-next",
			players:  "splayer.instance1\nvlc.instance1\n",
			selected: "vlc.instance1",
			apiFails: true,
		},
		{
			name:     "splayer 没有 SPlayer 时回退其他播放器",
			mode:     "splayer",
			players:  "spotify.instance1\n",
			selected: "spotify.instance1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			logPath := installControlPlayerctl(t, test.players, "0", "0")
			server, _ := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				if !test.apiFails {
					_, _ = io.WriteString(w, `{"ok":true}`)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"ok":false}`)
			})
			configureControlTest(t, test.mode, server.URL)

			if err := controlPlayer("next"); err != nil {
				t.Fatalf("回退后应成功，got %v", err)
			}
			// splayer 模式先按 splayer 筛选一次，回退时再按 mpris 筛选一次。
			want := []string{"call\t--list-all"}
			if test.mode == "splayer" {
				want = append(want, "call\t--list-all")
			}
			want = append(want, "call\t--player\t"+test.selected+"\tnext")
			assertControlPlayerctlCalls(t, logPath, want)
		})
	}
}

func TestControlPlayerFallbackKeepsSPlayerPriority(t *testing.T) {
	// 能找到 SPlayer 的 splayer 模式不应回退，直接控制 SPlayer。
	logPath := installControlPlayerctl(t, "splayer.instance1\nspotify.instance1\n", "0", "0")
	server, _ := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	configureControlTest(t, "splayer", server.URL)

	if err := controlPlayer("play-pause"); err != nil {
		t.Fatalf("controlPlayer: %v", err)
	}
	assertControlPlayerctlCalls(t, logPath, []string{
		"call\t--list-all",
		"call\t--player\tsplayer.instance1\tplay-pause",
	})
}

func TestControlPlayerFallbackWithoutOtherPlayers(t *testing.T) {
	// 只有 SPlayer 注册时没有可回退的播放器，仍返回 SPlayer-Next 的原始错误。
	logPath := installControlPlayerctl(t, "splayer.instance1\n", "0", "0")
	server, _ := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	configureControlTest(t, "splayer-next", server.URL)

	if err := controlPlayer("play"); err == nil {
		t.Fatal("没有可回退的播放器时应返回错误")
	}
	assertControlPlayerctlCalls(t, logPath, []string{"call\t--list-all"})
}

func TestControlPlayerFallbackDisabledByConfig(t *testing.T) {
	logPath := installControlPlayerctl(t, "spotify.instance1\n", "0", "0")
	server, _ := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	configureControlTest(t, "splayer-next", server.URL)
	appConfig.MPRISFallback = false

	if err := controlPlayer("next"); err == nil {
		t.Fatal("关闭 mpris-fallback 后不应回退")
	}
	assertControlPlayerctlCalls(t, logPath, nil)
}

func TestControlPlayerMPRISIgnoresFallbackConfig(t *testing.T) {
	// mpris 模式本身就用 playerctl，与 mpris-fallback 无关。
	logPath := installControlPlayerctl(t, "spotify.instance1\n", "0", "0")
	server, _ := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	configureControlTest(t, "mpris", server.URL)
	appConfig.MPRISFallback = false

	if err := controlPlayer("next"); err != nil {
		t.Fatalf("controlPlayer: %v", err)
	}
	assertControlPlayerctlCalls(t, logPath, []string{
		"call\t--list-all",
		"call\t--player\tspotify.instance1\tnext",
	})
}
