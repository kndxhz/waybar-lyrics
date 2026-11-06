package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"syscall"
)

var appLog = log.New(io.Discard, "", log.LstdFlags)
var appLogLevel = "INFO"

var logLevels = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

func logEvent(level, format string, args ...any) {
	if logLevels[level] >= logLevels[appLogLevel] {
		appLog.Printf("[%s] %s", level, fmt.Sprintf(format, args...))
	}
}

// openLogFile refuses symlinks and non-regular files before truncating the log.
func openLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("日志路径不是普通文件: %s", path)
	}
	if err == nil {
		err = file.Truncate(0)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	appLog.SetOutput(file)
	return file, nil
}

func logLyricsResult(action string, lyrics *ParsedLyrics, failureLogged *bool) {
	if lyrics == nil {
		if !*failureLogged {
			logEvent("WARN", "%s: 未找到歌词，将重试", action)
			*failureLogged = true
		}
		return
	}
	*failureLogged = false
	logEvent("INFO", "%s: 来源=%s 原文行数=%d 翻译行数=%d", action, lyrics.Source, len(lyrics.Original), len(lyrics.Translation))
	if appLogLevel == "DEBUG" {
		for _, line := range lyrics.Original {
			logEvent("DEBUG", "原文 [%dms] %s", line.TimeMs, line.Text)
		}
		for _, line := range lyrics.Translation {
			logEvent("DEBUG", "翻译 [%dms] %s", line.TimeMs, line.Text)
		}
	}
}

func configureLogLevel(level string) {
	appLogLevel = strings.ToUpper(strings.TrimSpace(level))
}
