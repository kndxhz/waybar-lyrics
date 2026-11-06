package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	build := exec.Command("go", "build", "-o", binary, ".")
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
