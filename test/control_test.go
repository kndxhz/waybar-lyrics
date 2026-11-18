package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestParseCLIControlCommands(t *testing.T) {
	commands := []string{"play", "pause", "play-pause", "next", "previous", "stop"}
	for _, command := range commands {
		for _, mode := range []string{"", "splayer-next", "splayer", "mpris"} {
			for _, modeFlag := range []string{"--mode", "-m"} {
				t.Run(command+"/"+mode+"/"+modeFlag, func(t *testing.T) {
					args := []string{command}
					if mode != "" {
						args = []string{modeFlag, mode, command}
					}
					options, err := parseCLI(args, io.Discard)
					if err != nil || options.Mode != mode || options.Command != command {
						t.Fatalf("parseCLI(%q) = %#v, %v; want mode=%q command=%q", args, options, err, mode, command)
					}
				})
			}
		}
	}
}

func TestParseCLINoCommandKeepsLyricsMode(t *testing.T) {
	for _, mode := range []string{"", "splayer-next", "splayer", "mpris"} {
		t.Run(mode, func(t *testing.T) {
			var args []string
			if mode != "" {
				args = []string{"--mode", mode}
			}
			options, err := parseCLI(args, io.Discard)
			if err != nil || options.Mode != mode || options.Command != "" {
				t.Fatalf("parseCLI(%q) = %#v, %v; want mode=%q without command", args, options, err, mode)
			}
		})
	}
}

func TestParseCLIRejectsInvalidControlArguments(t *testing.T) {
	for _, args := range [][]string{
		{"toggle"},
		{"prev"},
		{"PLAY"},
		{"play", "pause"},
		{"stop", "extra"},
		{"--mode", "splayer-next", "unknown"},
		{"--mode", "mpris", "next", "previous"},
		{"play", "--mode", "splayer"},
		{"pause", "-m", "mpris"},
		{"--mode"},
		{"--invalid"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseCLI(args, io.Discard); err == nil || errors.Is(err, flag.ErrHelp) {
				t.Fatalf("parseCLI(%q) should return an argument error, got %v", args, err)
			}
		})
	}
}

func TestParseCLIControlHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--mode", "splayer-next", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			if _, err := parseCLI(args, &output); !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("parseCLI(%q) error = %v, want flag.ErrHelp", args, err)
			}
			if output.Len() == 0 {
				t.Fatal("help should be written to the supplied writer")
			}
		})
	}
}

func TestParseCLIVersion(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-V"}, {"--version", "--mode", "mpris"}, {"-m", "splayer", "-V"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			options, err := parseCLI(args, io.Discard)
			if err != nil {
				t.Fatalf("parseCLI(%q) error = %v，want nil", args, err)
			}
			if !options.ShowVersion {
				t.Fatalf("parseCLI(%q) ShowVersion = false，want true", args)
			}
		})
	}
	options, err := parseCLI([]string{"-m", "mpris"}, io.Discard)
	if err != nil || options.ShowVersion {
		t.Fatalf("默认情况下不应显示版本：options=%#v err=%v", options, err)
	}
}

func TestVersionString(t *testing.T) {
	cleaned := strings.ReplaceAll(version, ".", "")
	if strings.Count(version, ".") != 2 || strings.Trim(cleaned, "0123456789") != "" {
		t.Fatalf("version = %q，应为 MAJOR.MINOR.PATCH", version)
	}
}

func TestMatchesPlayerMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		wantSPlayer bool
	}{
		{"splayer", true},
		{"SPLAYER.instance123", true},
		{"org.mpris.MediaPlayer2.SPlayer", true},
		{"prefix-sPlAyEr-suffix", true},
		{"spotify.instance123", false},
		{"vlc", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesPlayerMode(test.name, "splayer"); got != test.wantSPlayer {
				t.Errorf("matchesPlayerMode(%q, splayer) = %v, want %v", test.name, got, test.wantSPlayer)
			}
			if got := matchesPlayerMode(test.name, "mpris"); got != !test.wantSPlayer {
				t.Errorf("matchesPlayerMode(%q, mpris) = %v, want %v", test.name, got, !test.wantSPlayer)
			}
		})
	}
}

// Tests intentionally do not run in parallel: appConfig and PATH are process globals.
func configureControlTest(t *testing.T, mode, address string) {
	t.Helper()
	oldConfig := appConfig
	t.Cleanup(func() { appConfig = oldConfig })
	appConfig = defaultConfig()
	appConfig.Mode = mode
	appConfig.SPlayer.Address = address
	appConfig.SPlayer.Port = 0
	// 暂停回退会让控制命令改投 playerctl，这里默认关掉，避免用例受真实播放器影响。
	oldTarget := pauseFallbackControlTarget
	pauseFallbackControlTarget = func() string { return "" }
	t.Cleanup(func() { pauseFallbackControlTarget = oldTarget })
}

// PATH contains only this fake, so no test can control a real desktop player.
// The script uses shell builtins only and logs arguments without changing case.
func installControlPlayerctl(t *testing.T, players, listExit, commandExit string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
printf 'call' >> "$CONTROL_TEST_LOG"
for arg do
    printf '\t%s' "$arg" >> "$CONTROL_TEST_LOG"
done
printf '\n' >> "$CONTROL_TEST_LOG"
if [ "$#" -eq 1 ] && [ "$1" = '--list-all' ]; then
    printf '%s' "$CONTROL_TEST_PLAYERS"
    exit "$CONTROL_TEST_LIST_EXIT"
fi
if [ "$#" -eq 3 ] && [ "$1" = '--player' ]; then
    exit "$CONTROL_TEST_COMMAND_EXIT"
fi
exit 97
`
	if err := os.WriteFile(filepath.Join(dir, "playerctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CONTROL_TEST_LOG", logPath)
	t.Setenv("CONTROL_TEST_PLAYERS", players)
	t.Setenv("CONTROL_TEST_LIST_EXIT", listExit)
	t.Setenv("CONTROL_TEST_COMMAND_EXIT", commandExit)
	return logPath
}

func assertControlPlayerctlCalls(t *testing.T, logPath string, want []string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var got []string
	if len(data) > 0 {
		got = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("playerctl calls = %q, want %q", got, want)
	}
}

type controlAPIRequest struct {
	method string
	path   string
	body   string
}

type controlAPIRecorder struct {
	mu       sync.Mutex
	requests []controlAPIRequest
}

func newControlAPIServer(t *testing.T, respond http.HandlerFunc) (*httptest.Server, *controlAPIRecorder) {
	t.Helper()
	recorder := &controlAPIRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, controlAPIRequest{r.Method, r.URL.Path, string(body)})
		recorder.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func (recorder *controlAPIRecorder) assert(t *testing.T, want ...string) {
	t.Helper()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var got []string
	for _, request := range recorder.requests {
		got = append(got, request.method+" "+request.path)
		if request.method == http.MethodPost {
			var body map[string]json.RawMessage
			if err := json.Unmarshal([]byte(request.body), &body); err != nil || body == nil || len(body) != 0 {
				t.Errorf("POST %s body = %q, want JSON {}", request.path, request.body)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HTTP requests = %q, want %q", got, want)
	}
}

// disableControlFallback 关闭 mpris-fallback，让下面的用例只验证 SPlayer-Next
// 与 MPRIS 自身的失败语义，不受 playerctl 回退影响。
func disableControlFallback(t *testing.T) {
	t.Helper()
	appConfig.MPRISFallback = false
}

func TestControlPlayerSPlayerNextCommands(t *testing.T) {
	for _, test := range []struct{ command, path string }{
		{"play", "play"}, {"pause", "pause"}, {"next", "next"},
		{"previous", "prev"}, {"stop", "stop"},
	} {
		t.Run(test.command, func(t *testing.T) {
			logPath := installControlPlayerctl(t, "splayer\nspotify\n", "0", "0")
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			configureControlTest(t, "splayer-next", server.URL)
			if err := controlPlayer(test.command); err != nil {
				t.Fatalf("controlPlayer(%q): %v", test.command, err)
			}
			recorder.assert(t, "POST /api/"+test.path)
			assertControlPlayerctlCalls(t, logPath, nil)
		})
	}
}

func TestControlPlayerSPlayerNextPlayPause(t *testing.T) {
	for _, test := range []struct{ state, action string }{
		{"playing", "pause"}, {"paused", "play"}, {"stopped", "play"},
		{"buffering", "play"}, {"unknown-state", "play"},
	} {
		t.Run(test.state, func(t *testing.T) {
			logPath := installControlPlayerctl(t, "splayer\n", "0", "0")
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/status" {
					_ = json.NewEncoder(w).Encode(map[string]string{"state": test.state})
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			configureControlTest(t, "splayer-next", server.URL)
			if err := controlPlayer("play-pause"); err != nil {
				t.Fatal(err)
			}
			recorder.assert(t, "GET /api/status", "POST /api/"+test.action)
			assertControlPlayerctlCalls(t, logPath, nil)
		})
	}
}

func TestControlPlayerSPlayerNextErrorsWhenFallbackDisabled(t *testing.T) {
	for _, test := range []struct {
		name, command, body string
		status              int
	}{
		{"POST HTTP error", "play", `{"ok":true}`, http.StatusInternalServerError},
		{"POST client error", "next", `{"ok":true}`, http.StatusBadRequest},
		{"POST malformed JSON", "pause", `{`, http.StatusOK},
		{"POST empty response", "stop", ``, http.StatusOK},
		{"POST rejected", "previous", `{"ok":false}`, http.StatusOK},
		{"POST missing ok", "play", `{}`, http.StatusOK},
		{"POST null response", "play", `null`, http.StatusOK},
		{"GET HTTP error", "play-pause", `{"state":"playing"}`, http.StatusServiceUnavailable},
		{"GET malformed JSON", "play-pause", `{`, http.StatusOK},
		{"GET empty response", "play-pause", ``, http.StatusOK},
		{"GET missing state", "play-pause", `{}`, http.StatusOK},
		{"GET empty state", "play-pause", `{"state":""}`, http.StatusOK},
		{"GET null state", "play-pause", `{"state":null}`, http.StatusOK},
		{"GET non-string state", "play-pause", `{"state":true}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			logPath := installControlPlayerctl(t, "splayer\nspotify\n", "0", "0")
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			configureControlTest(t, "splayer-next", server.URL)
			disableControlFallback(t)
			if err := controlPlayer(test.command); err == nil {
				t.Fatal("invalid API response should return an error")
			}
			path := test.command
			if path == "previous" {
				path = "prev"
			}
			want := "POST /api/" + path
			if test.command == "play-pause" {
				want = "GET /api/status"
			}
			recorder.assert(t, want)
			assertControlPlayerctlCalls(t, logPath, nil)
		})
	}
}

func TestControlPlayerSPlayerNextToggleActionFailure(t *testing.T) {
	logPath := installControlPlayerctl(t, "splayer\n", "0", "0")
	server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"state":"playing"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":false}`)
	})
	configureControlTest(t, "splayer-next", server.URL)
	disableControlFallback(t)
	if err := controlPlayer("play-pause"); err == nil {
		t.Fatal("failed toggle action should return an error")
	}
	recorder.assert(t, "GET /api/status", "POST /api/pause")
	assertControlPlayerctlCalls(t, logPath, nil)
}

func TestControlPlayerSPlayerNextTransportFailure(t *testing.T) {
	logPath := installControlPlayerctl(t, "splayer\nspotify\n", "0", "0")
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	configureControlTest(t, "splayer-next", server.URL)
	disableControlFallback(t)
	for _, command := range []string{"play", "play-pause"} {
		if err := controlPlayer(command); err == nil {
			t.Errorf("controlPlayer(%q) should report a connection error", command)
		}
	}
	assertControlPlayerctlCalls(t, logPath, nil)
}

func TestControlPlayerSPlayerNextWithoutPlayerctl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	configureControlTest(t, "splayer-next", server.URL)
	if err := controlPlayer("next"); err != nil {
		t.Fatalf("HTTP control must not require playerctl: %v", err)
	}
	recorder.assert(t, "POST /api/next")
}
func TestControlPlayerMPRISControlsSelectedPlayer(t *testing.T) {
	// 目标播放器由 MPRIS 侧按 PlaybackStatus 选出，playerctl 只负责控制它。
	for _, command := range []string{"play", "pause", "play-pause", "next", "previous", "stop"} {
		t.Run("mpris/"+command, func(t *testing.T) {
			logPath := installControlPlayerctl(t, "", "0", "0")
			fakeMPRIS(t, "org.mpris.MediaPlayer2.pausedcheck", "暂停的歌", "歌手", "Paused")
			fakeMPRIS(t, "org.mpris.MediaPlayer2.playingcheck", "在播的歌", "歌手", "Playing")
			onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.pausedcheck", "org.mpris.MediaPlayer2.playingcheck")
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			configureControlTest(t, "mpris", server.URL)
			if err := controlPlayer(command); err != nil {
				t.Fatalf("controlPlayer(%q): %v", command, err)
			}
			assertControlPlayerctlCalls(t, logPath, []string{"call\t--player\tplayingcheck\t" + command})
			recorder.assert(t)
		})
	}
}

func TestControlPlayerSPlayerModeControlsSPlayer(t *testing.T) {
	logPath := installControlPlayerctl(t, "", "0", "0")
	fakeMPRIS(t, "org.mpris.MediaPlayer2.splayer_next.instance1", "播放中的歌", "歌手", "Playing")
	fakeMPRIS(t, "org.mpris.MediaPlayer2.spotify.instance1", "别的歌", "别的歌手", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.splayer_next.instance1", "org.mpris.MediaPlayer2.spotify.instance1")
	server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	configureControlTest(t, "splayer", server.URL)

	if err := controlPlayer("play-pause"); err != nil {
		t.Fatalf("controlPlayer: %v", err)
	}
	assertControlPlayerctlCalls(t, logPath, []string{"call\t--player\tsplayer_next.instance1\tplay-pause"})
	recorder.assert(t)
}

func TestControlPlayerMPRISErrorsNeverUseHTTP(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        string
		players     []string
		commandExit string
		wantCalls   []string
	}{
		{"没有注册的播放器", "mpris", nil, "0", nil},
		{"splayer 模式排除其他播放器", "splayer", []string{"org.mpris.MediaPlayer2.spotify.instance1"}, "0", nil},
		{"mpris 模式排除 SPlayer", "mpris", []string{"org.mpris.MediaPlayer2.splayer_next.instance1"}, "0", nil},
		{"playerctl 命令失败", "mpris", []string{"org.mpris.MediaPlayer2.spotify.instance1"}, "3",
			[]string{"call\t--player\tspotify.instance1\tplay"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logPath := installControlPlayerctl(t, "", "0", test.commandExit)
			for index, name := range test.players {
				fakeMPRIS(t, name, "歌名"+string(rune('A'+index)), "歌手", "Playing")
			}
			onlyMPRISPlayers(t, test.players...)
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			configureControlTest(t, test.mode, server.URL)
			disableControlFallback(t)
			if err := controlPlayer("play"); err == nil {
				t.Fatal("没有可控制的播放器或子进程失败时应返回错误")
			}
			assertControlPlayerctlCalls(t, logPath, test.wantCalls)
			recorder.assert(t)
		})
	}
}

func TestControlPlayerMPRISMissingPlayerctl(t *testing.T) {
	for _, mode := range []string{"splayer", "mpris"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			name := "org.mpris.MediaPlayer2.missingcheck"
			if mode == "splayer" {
				name = "org.mpris.MediaPlayer2.splayer_next.instance1"
			}
			fakeMPRIS(t, name, "歌名", "歌手", "Playing")
			onlyMPRISPlayers(t, name)
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			configureControlTest(t, mode, server.URL)
			err := controlPlayer("play")
			if err == nil {
				t.Fatal("缺少 playerctl 时应返回错误")
			}
			if !strings.Contains(err.Error(), "playerctl") {
				t.Fatalf("错误应提示缺少 playerctl：%v", err)
			}
			recorder.assert(t)
		})
	}
}
