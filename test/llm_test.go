package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godbus/dbus/v5"
)

func resetLLMCache() {
	llmCache.mu.Lock()
	llmCache.entries = map[string]llmCacheEntry{}
	llmCache.mu.Unlock()
}

func configureLLMTest(t *testing.T, baseURL string) {
	t.Helper()
	oldConfig := appConfig
	t.Cleanup(func() { appConfig = oldConfig })
	appConfig = defaultConfig()
	appConfig.LLM.Enable = true
	appConfig.LLM.BaseURL = baseURL
	appConfig.LLM.APIKey = "test-key"
	resetLLMCache()
	t.Cleanup(resetLLMCache)
}

func TestCleanLLMOutput(t *testing.T) {
	for _, test := range []struct{ name, raw, want string }{
		{
			"带属性的思考标签",
			"<think type=\"reasoning\">先想想\n{\"title\":\"错误\"}</think>\n{\"title\":\"譬如我\",\"singer\":\"洛天依\"}",
			`{"title":"譬如我","singer":"洛天依"}`,
		},
		{"markdown 代码块", "```json\n{\"title\":\"歌名\"}\n```", `{"title":"歌名"}`},
		{"代码块带多余文字", "结果如下：\n```JSON\n{\"is_song\":false}\n```\n以上。", `{"is_song":false}`},
		{"未闭合思考标签", "<think>只剩思考过程 {\"title\":\"歌名\"}", ""},
		{"只有结束标签", "思考完毕</think>{\"title\":\"歌名\"}", `{"title":"歌名"}`},
		{"没有 JSON", "抱歉，我无法判断。", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanLLMOutput(test.raw); got != test.want {
				t.Fatalf("cleanLLMOutput = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseLLMExtraction(t *testing.T) {
	for _, test := range []struct {
		name       string
		raw        string
		wantIsSong bool
		wantTitle  string
		wantSinger string
		wantError  bool
	}{
		{"歌曲", `{"title":"譬如我","singer":"洛天依"}`, true, "譬如我", "洛天依", false},
		{"歌手为 null", `{"title":"譬如我","singer":null}`, true, "譬如我", "", false},
		{"歌手为空串", `{"title":"譬如我","singer":""}`, true, "譬如我", "", false},
		{"非歌曲", `{"is_song":false}`, false, "", "", false},
		{"非法 JSON", `{"title":`, false, "", "", true},
		{"缺少歌名", `{"singer":"洛天依"}`, false, "", "", true},
		{"被包裹的 JSON", "<think>x</think>```json\n{\"title\":\"譬如我\"}\n```", true, "譬如我", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLLMExtraction(test.raw)
			if test.wantError {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.IsSong != test.wantIsSong || got.Title != test.wantTitle || got.Singer != test.wantSinger {
				t.Fatalf("got %+v, want song=%t title=%q singer=%q", got, test.wantIsSong, test.wantTitle, test.wantSinger)
			}
		})
	}
}

func newLLMServer(t *testing.T, wantMetadata, content string, status int) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("request body: %v", err)
		}
		if payload.Model != defaultConfig().LLM.ModelName {
			t.Errorf("model = %q", payload.Model)
		}
		if len(payload.Messages) == 0 {
			t.Error("请求缺少 messages")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if wantMetadata != "" && !strings.Contains(payload.Messages[0].Content, wantMetadata) {
			t.Errorf("prompt 未填入元数据：%s", payload.Messages[0].Content)
		}
		if !strings.Contains(payload.Messages[0].Content, "提取这个媒体的歌名") {
			t.Errorf("prompt 丢失默认指令：%s", payload.Messages[0].Content)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = io.WriteString(w, `{"error":"boom"}`)
			return
		}
		response, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": content},
			}},
		})
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestApplyLLMExtractionOverridesTitle(t *testing.T) {
	server, calls := newLLMServer(t, "xesam:title 洛天依Official,姜洄 - 譬如我.flac", "<think>先想想</think>```json\n{\"title\":\"譬如我\",\"singer\":\"洛天依Official, 姜洄\"}\n```", http.StatusOK)
	configureLLMTest(t, server.URL)

	// 每轮刷新都会重新读到原始标题，因此用同样的原始元数据模拟两轮循环。
	rawMetadata := func() *PlayerMetadata {
		return &PlayerMetadata{Player: "firefox.instance1", Title: "洛天依Official,姜洄 - 譬如我.flac"}
	}
	text := "xesam:title 洛天依Official,姜洄 - 譬如我.flac\nmpris:length 153000000"

	metadata := rawMetadata()
	applyLLMExtraction(metadata, text)
	if metadata.Title != "譬如我" || metadata.Artist != "洛天依Official, 姜洄" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if got := strings.TrimSpace(metadata.Artist + " " + metadata.Title); got != "洛天依Official, 姜洄 譬如我" {
		t.Fatalf("搜索关键词 = %q", got)
	}

	// 同一首歌重复调用只请求一次。
	applyLLMExtraction(rawMetadata(), text)
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("LLM 请求次数 = %d, want 1", got)
	}
}

func TestApplyLLMExtractionSingerNullSearchesTitleOnly(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 洛天依Official,姜洄 - 譬如我.flac", `{"title":"譬如我","singer":null}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "vlc.instance1", Title: "洛天依Official,姜洄 - 譬如我.flac", Artist: "洛天依Official, 姜洄"}
	applyLLMExtraction(metadata, "xesam:title 洛天依Official,姜洄 - 譬如我.flac")

	if metadata.Title != "譬如我" || metadata.Artist != "" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if got := strings.TrimSpace(metadata.Artist + " " + metadata.Title); got != "譬如我" {
		t.Fatalf("搜索关键词 = %q", got)
	}
}

func TestApplyLLMExtractionKeepsTitleOnFailure(t *testing.T) {
	server, calls := newLLMServer(t, "xesam:title 原始标题", "", http.StatusInternalServerError)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "firefox.instance1", Title: "原始标题", Artist: "原始歌手"}
	applyLLMExtraction(metadata, "xesam:title 原始标题")
	applyLLMExtraction(metadata, "xesam:title 原始标题")

	if metadata.Title != "原始标题" || metadata.Artist != "原始歌手" {
		t.Fatalf("失败时应保留原始标题：%+v", metadata)
	}
	// 失败结果短期缓存，避免每轮刷新都重试。
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("LLM 请求次数 = %d, want 1", got)
	}
}

func TestApplyLLMExtractionKeepsTitleForNonSong(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 语音消息", `{"is_song":false}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "telegram.instance1", Title: "语音消息", Artist: "张三"}
	applyLLMExtraction(metadata, "xesam:title 语音消息")
	if metadata.Title != "语音消息" || metadata.Artist != "张三" {
		t.Fatalf("非歌曲应走原有逻辑：%+v", metadata)
	}
}

func TestApplyLLMExtractionSkipsEmptyMetadata(t *testing.T) {
	server, calls := newLLMServer(t, "", `{"title":"歌名"}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "vlc.instance1", Title: "歌名"}
	applyLLMExtraction(metadata, "   ")
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("空元数据不应请求 LLM，got %d", got)
	}
}

func TestLLMEnabled(t *testing.T) {
	config := defaultConfig()
	if llmEnabled(config) {
		t.Fatal("默认配置不应启用 LLM")
	}
	config.LLM.Enable = true
	if !llmEnabled(config) {
		t.Fatal("启用后应生效")
	}
	config.LLM.Prompt = "  "
	if llmEnabled(config) {
		t.Fatal("Prompt 为空时不应启用")
	}
	config = defaultConfig()
	config.LLM.Enable = true
	config.LLM.ModelName = ""
	if llmEnabled(config) {
		t.Fatal("模型为空时不应启用")
	}
}

func TestMPRISMetadataText(t *testing.T) {
	props := map[string]dbus.Variant{
		"Metadata": dbus.MakeVariant(map[string]dbus.Variant{
			"xesam:title":   dbus.MakeVariant("歌名"),
			"xesam:artist":  dbus.MakeVariant([]string{"甲", "乙"}),
			"mpris:length":  dbus.MakeVariant(int64(153000000)),
			"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/mpris/MediaPlayer2/Track/1")),
			"xesam:album":   dbus.MakeVariant(""),
		}),
		"PlaybackStatus": dbus.MakeVariant("Playing"),
		"Position":       dbus.MakeVariant(int64(54000000)),
	}
	want := strings.Join([]string{
		"mpris:length 153000000",
		"mpris:trackid /org/mpris/MediaPlayer2/Track/1",
		"xesam:artist 甲, 乙",
		"xesam:title 歌名",
		"PlaybackStatus Playing",
		"Position 54000000",
	}, "\n")
	if got := mprisMetadataText(props); got != want {
		t.Fatalf("mprisMetadataText =\n%s\nwant\n%s", got, want)
	}
}

func TestGetMPRISMetadataAppliesLLM(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 洛天依Official,姜洄 - 譬如我.flac", `{"title":"譬如我","singer":"洛天依Official, 姜洄"}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	conn := fakeMPRIS(t, "org.mpris.MediaPlayer2.llmtest", "洛天依Official,姜洄 - 譬如我.flac", "人工歌手", "Playing")

	// 直接按总线名读取假播放器，避免机器上真实播放器影响结果。
	metadata, status, ok := readMPRISPlayer(conn, "org.mpris.MediaPlayer2.llmtest", "llmtest")
	if !ok || metadata == nil {
		t.Fatalf("应读到假播放器元数据，ok=%t", ok)
	}
	if status != "Playing" {
		t.Fatalf("status = %q", status)
	}
	if metadata.Title != "譬如我" || metadata.Artist != "洛天依Official, 姜洄" {
		t.Fatalf("LLM 结果未覆盖元数据：%+v", metadata)
	}
}
