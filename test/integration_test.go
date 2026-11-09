package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMainLoggingDoesNotPolluteStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/now-playing":
			fmt.Fprint(w, `{"track":{"title":"title","artists":[{"name":"artist"}]},"position":1000}`)
		case "/api/lyrics":
			fmt.Fprint(w, `{"lyric":[{"startTime":0,"words":[{"word":"secret lyric"}]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "test-binary")
	build := exec.Command("go", "build", "-o", binary, "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	configDir := filepath.Join(dir, "waybar-lyrics")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "log")
	config := fmt.Sprintf("mode: splayer-next\nsplayer:\n  address: %q\nlog:\n  path: %q\n  level: DEBUG\n", server.URL, logPath)
	if err := os.WriteFile(filepath.Join(configDir, "waybar-lyrics.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir)
	stdout, err := cmd.Output()
	if err != nil && ctx.Err() == nil {
		t.Fatalf("run: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(stdout)))
	count := 0
	for scanner.Scan() {
		var value WaybarOutput
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatalf("stdout is not JSON: %q: %v", scanner.Text(), err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no Waybar output")
	}
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), "secret lyric") {
		t.Fatalf("missing debug log: %q %v", data, err)
	}
}

func TestMainPlaybackControlIsolated(t *testing.T) {
	buildDir := t.TempDir()
	binary := filepath.Join(buildDir, "control-test-binary")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build control binary: %v: %s", err, output)
	}

	for _, test := range []struct {
		name      string
		mode      string
		args      []string
		apiStatus int
		wantExit  int
		wantCalls []string
	}{
		{
			name: "CLI override toggles playing API once",
			mode: "mpris", args: []string{"--mode", "splayer-next", "play-pause"},
			apiStatus: http.StatusOK, wantExit: 0,
			wantCalls: []string{"GET /api/status", "POST /api/pause"},
		},
		{
			name: "configured API mode advances once",
			mode: "splayer-next", args: []string{"next"},
			apiStatus: http.StatusOK, wantExit: 0,
			wantCalls: []string{"POST /api/next"},
		},
		{
			name: "API failure reports runtime exit",
			mode: "splayer-next", args: []string{"next"},
			apiStatus: http.StatusServiceUnavailable, wantExit: 1,
			wantCalls: []string{"POST /api/next"},
		},
		{
			name: "invalid command reports usage without API",
			mode: "splayer-next", args: []string{"invalid-command"},
			apiStatus: http.StatusOK, wantExit: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, recorder := newControlAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if test.apiStatus != http.StatusOK {
					w.WriteHeader(test.apiStatus)
					fmt.Fprint(w, `{"ok":false,"error":"test failure"}`)
					return
				}
				switch r.URL.Path {
				case "/api/status":
					fmt.Fprint(w, `{"state":"playing"}`)
				case "/api/pause", "/api/next":
					fmt.Fprint(w, `{"ok":true}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			dir := t.TempDir()
			configDir := filepath.Join(dir, "waybar-lyrics")
			emptyPath := filepath.Join(dir, "empty-bin")
			for _, path := range []string{configDir, emptyPath} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			logPath := filepath.Join(dir, "existing.log")
			const originalLog = "existing lyric process log must survive control commands\n"
			if err := os.WriteFile(logPath, []byte(originalLog), 0600); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf("mode: %s\nsplayer:\n  address: %q\nlog:\n  path: %q\n  level: DEBUG\n", test.mode, server.URL, logPath)
			if err := os.WriteFile(filepath.Join(configDir, "waybar-lyrics.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, test.args...)
			// Do not inherit a real playerctl or the user's configuration directory.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+emptyPath, "XDG_CONFIG_HOME="+dir)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("control command did not exit promptly: %v; stdout=%q stderr=%q", ctx.Err(), stdout.String(), stderr.String())
			}
			exitCode := 0
			if err != nil {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Fatalf("run control binary: %v", err)
				}
				exitCode = exitError.ExitCode()
			}
			if exitCode != test.wantExit {
				t.Errorf("exit code = %d, want %d; stderr=%q", exitCode, test.wantExit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("control command must not emit lyric output: %q", stdout.String())
			}
			if test.wantExit != 0 && strings.TrimSpace(stderr.String()) == "" {
				t.Error("failed command should report an error on stderr")
			}
			if test.wantExit == 0 && stderr.Len() != 0 {
				t.Errorf("successful command should not emit errors: %q", stderr.String())
			}
			recorder.assert(t, test.wantCalls...)
			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read preexisting log: %v", err)
			}
			if string(logData) != originalLog {
				t.Errorf("control command changed preexisting log: got %q, want %q", logData, originalLog)
			}
		})
	}
}
