package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// captureLog 把日志重定向到缓冲区，用例结束时恢复。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	oldLevel := appLogLevel
	appLog.SetOutput(&output)
	configureLogLevel("INFO")
	t.Cleanup(func() {
		appLog.SetOutput(io.Discard)
		configureLogLevel(oldLevel)
	})
	return &output
}

func TestLogSPlayerNextUnavailableLevelsByFallback(t *testing.T) {
	output := captureLog(t)
	oldConfig := appConfig
	t.Cleanup(func() {
		appConfig = oldConfig
		lastSPlayerNextError = ""
	})

	appConfig = defaultConfig()
	appConfig.Mode = "splayer-next"
	appConfig.MPRISFallback = true
	logSPlayerNextUnavailable(errors.New("连接被拒绝"))
	if got := output.String(); !strings.Contains(got, "[WARN]") || strings.Contains(got, "[ERROR]") {
		t.Fatalf("有 MPRIS 回退属于已处理的错误，应记 WARN：%s", got)
	}

	// 同一个错误只记一次，避免每 250ms 刷一条。
	output.Reset()
	logSPlayerNextUnavailable(errors.New("连接被拒绝"))
	if output.String() != "" {
		t.Fatalf("重复的错误不应重复记录：%s", output.String())
	}

	// 关掉回退后没有任何兜底，属于没人处理的错误。
	appConfig.MPRISFallback = false
	lastSPlayerNextError = ""
	output.Reset()
	logSPlayerNextUnavailable(errors.New("连接被拒绝"))
	if got := output.String(); !strings.Contains(got, "[ERROR]") {
		t.Fatalf("没有回退时应记 ERROR：%s", got)
	}
}

func TestLogSPlayerLyricsFailureFallsBackToSearch(t *testing.T) {
	output := captureLog(t)
	oldConfig := appConfig
	t.Cleanup(func() {
		appConfig = oldConfig
		lastSPlayerLyricsError = ""
	})

	appConfig = defaultConfig()
	appConfig.SPlayer.Address = "127.0.0.1"
	appConfig.SPlayer.Port = 1
	appConfig.SPlayer.API.Address = ""
	appConfig.SPlayer.API.Port = 0

	if lyrics := getSPlayerNextLyrics(); lyrics != nil {
		t.Fatalf("接口不可用时应返回 nil，got %+v", lyrics)
	}
	if got := output.String(); !strings.Contains(got, "[WARN]") || !strings.Contains(got, "改为按歌名搜索") {
		t.Fatalf("取不到播放器歌词属于已处理的错误，应记 WARN：%s", got)
	}
}

func TestLogMPRISFailureLogsErrorOnce(t *testing.T) {
	output := captureLog(t)
	t.Cleanup(func() { lastMPRISFailure = "" })

	logMPRISFailure("连接 D-Bus 会话总线失败", errors.New("没有会话总线"))
	logMPRISFailure("连接 D-Bus 会话总线失败", errors.New("没有会话总线"))
	if got := output.String(); strings.Count(got, "[ERROR]") != 1 {
		t.Fatalf("同一条 D-Bus 错误应只记一条 ERROR：%s", got)
	}
}

func TestLogLyricsFailureIsWarn(t *testing.T) {
	output := captureLog(t)
	failed := false
	logLyricsResult("解析", nil, &failed)
	got := output.String()
	if !strings.Contains(got, "[WARN]") || strings.Contains(got, "[ERROR]") {
		t.Fatalf("歌词没找到会重试，属于已处理的错误，应记 WARN：%s", got)
	}
}
