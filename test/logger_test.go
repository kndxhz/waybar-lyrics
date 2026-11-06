package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileTruncationAndLevels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte("old log"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { appLog.SetOutput(io.Discard); file.Close(); configureLogLevel("INFO") }()
	lyrics := &ParsedLyrics{Source: "API", Original: []TimedLyricLine{{TimeMs: 100, Text: "secret lyric"}}, Translation: []TimedLyricLine{{TimeMs: 100, Text: "secret translation"}}}
	configureLogLevel("INFO")
	failureLogged := false
	logLyricsResult("解析", lyrics, &failureLogged)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "old log") || strings.Contains(string(data), "secret") || !strings.Contains(string(data), "来源=API") {
		t.Fatalf("unexpected INFO log: %s", data)
	}
	configureLogLevel("DEBUG")
	logLyricsResult("解析", lyrics, &failureLogged)
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "secret lyric") || !strings.Contains(string(data), "secret translation") {
		t.Fatalf("missing DEBUG lyrics: %s", data)
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var output bytes.Buffer
	appLog.SetOutput(&output)
	defer func() { appLog.SetOutput(io.Discard); configureLogLevel("INFO") }()
	configureLogLevel("warn")
	logEvent("INFO", "hidden")
	logEvent("WARN", "visible")
	if strings.Contains(output.String(), "hidden") || !strings.Contains(output.String(), "visible") {
		t.Fatalf("unexpected log: %s", output.String())
	}
}

func TestLyricsFailureWarningDeduplication(t *testing.T) {
	var output bytes.Buffer
	appLog.SetOutput(&output)
	defer func() { appLog.SetOutput(io.Discard); configureLogLevel("INFO") }()
	configureLogLevel("INFO")
	failed := false
	logLyricsResult("解析", nil, &failed)
	logLyricsResult("解析", nil, &failed)
	if count := strings.Count(output.String(), "[WARN]"); count != 1 {
		t.Fatalf("expected one warning, got %d: %s", count, output.String())
	}
	logLyricsResult("解析", &ParsedLyrics{Source: "API"}, &failed)
	logLyricsResult("解析", nil, &failed)
	if count := strings.Count(output.String(), "[WARN]"); count != 2 {
		t.Fatalf("expected warning after recovery, got %d: %s", count, output.String())
	}
}

func TestLogRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openLogFile(link); err == nil {
		f.Close()
		t.Fatal("symlink accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("target changed: %q %v", data, err)
	}
}

func TestLogConfigDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Log.Path != "/tmp/waybar-lyrics.log" || config.Log.Level != "INFO" {
		t.Fatalf("defaults: %+v", config.Log)
	}
	path := filepath.Join(dir, "waybar-lyrics", "waybar-lyrics.yaml")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "level: INFO") {
		t.Fatalf("default file: %s %v", data, err)
	}
	if err := os.WriteFile(path, []byte("log:\n  path: /tmp/custom.log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err = loadConfig()
	if err != nil || config.Log.Path != "/tmp/custom.log" || config.Log.Level != "INFO" {
		t.Fatalf("partial config: %+v %v", config.Log, err)
	}
	config.Log.Level = "invalid"
	if validateConfig(config) == nil {
		t.Fatal("invalid log level accepted")
	}
}

func TestLegacyLogConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "waybar-lyrics", "waybar-lyrics.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("mode: splayer-next\nsplayer:\n  port: 15558\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Log.Path != "/tmp/waybar-lyrics.log" || config.Log.Level != "INFO" {
		t.Fatalf("log defaults: %+v", config.Log)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("level: INFO")) || !bytes.Contains(data, []byte("port: 15558")) {
		t.Fatalf("legacy config not upgraded correctly: %q %v", data, err)
	}
}
