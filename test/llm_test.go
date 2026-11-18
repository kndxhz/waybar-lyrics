package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// applyLLMWait 触发一次提取并等待后台请求结束，返回后台结果的判定：
// false 表示 LLM 判定不是歌曲。
func applyLLMWait(t *testing.T, metadata *PlayerMetadata, text string) bool {
	t.Helper()
	applyLLMExtraction(metadata, text)
	llmInFlight.Wait()
	return applyLLMExtraction(metadata, text)
}

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
	// 先等后台请求结束，再恢复配置，避免用例提前返回时请求跑到别的用例里。
	t.Cleanup(func() {
		llmInFlight.Wait()
		resetLLMCache()
	})
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
	if !applyLLMWait(t, metadata, text) {
		t.Fatal("识别为歌曲时应保留元数据")
	}
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
	if !applyLLMWait(t, metadata, "xesam:title 洛天依Official,姜洄 - 譬如我.flac") {
		t.Fatal("识别为歌曲时应保留元数据")
	}

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
	// 这里只验证失败后的缓存行为，关闭重试以免请求次数受 llm.retry 影响。
	appConfig.LLM.Retry = 0

	metadata := &PlayerMetadata{Player: "firefox.instance1", Title: "原始标题", Artist: "原始歌手"}
	if !applyLLMWait(t, metadata, "xesam:title 原始标题") {
		t.Fatal("接口失败时不应丢弃元数据")
	}
	applyLLMExtraction(metadata, "xesam:title 原始标题")

	if metadata.Title != "原始标题" || metadata.Artist != "原始歌手" {
		t.Fatalf("失败时应保留原始标题：%+v", metadata)
	}
	// 失败结果短期缓存，避免每轮刷新都重试。
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("LLM 请求次数 = %d, want 1", got)
	}
}

func TestApplyLLMExtractionRejectsNonSong(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 语音消息", `{"is_song":false}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "telegram.instance1", Title: "语音消息", Artist: "张三"}
	if applyLLMWait(t, metadata, "xesam:title 语音消息") {
		t.Fatal("非歌曲应返回 false，按没识别到歌曲处理")
	}
	if metadata.Title != "语音消息" || metadata.Artist != "张三" {
		t.Fatalf("非歌曲时不应改动元数据：%+v", metadata)
	}
}

func TestApplyLLMExtractionSkipsEmptyMetadata(t *testing.T) {
	server, calls := newLLMServer(t, "", `{"title":"歌名"}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "vlc.instance1", Title: "歌名"}
	if !applyLLMExtraction(metadata, "   ") {
		t.Fatal("空元数据不应丢弃元数据")
	}
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
	// 首次读取时后台请求还没返回，只保留规范化后的原始标题（去掉扩展名）。
	if metadata.Title != "洛天依Official,姜洄 - 譬如我" {
		t.Fatalf("首次读取应先保留原始标题：%+v", metadata)
	}
	llmInFlight.Wait()

	metadata, status, ok = readMPRISPlayer(conn, "org.mpris.MediaPlayer2.llmtest", "llmtest")
	if !ok || metadata == nil {
		t.Fatalf("第二次读取应读到元数据，ok=%t", ok)
	}
	if status != "Playing" {
		t.Fatalf("status = %q", status)
	}
	if metadata.Title != "譬如我" || metadata.Artist != "洛天依Official, 姜洄" {
		t.Fatalf("LLM 结果未覆盖元数据：%+v", metadata)
	}
}

func TestApplyLLMExtractionLogsOncePerTrack(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 语音消息", `{"is_song":false}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	var buf bytes.Buffer
	oldOutput := appLog.Writer()
	appLog.SetOutput(&buf)
	t.Cleanup(func() { appLog.SetOutput(oldOutput) })

	rawMetadata := func() *PlayerMetadata {
		return &PlayerMetadata{Player: "telegram.instance1", Title: "语音消息"}
	}
	if applyLLMWait(t, rawMetadata(), "xesam:title 语音消息") {
		t.Fatal("非歌曲应返回 false")
	}
	// 之后命中缓存，每轮刷新仍然得到同一结论，但不再重复请求与记日志。
	for i := 0; i < 2; i++ {
		if applyLLMExtraction(rawMetadata(), "xesam:title 语音消息") {
			t.Fatal("非歌曲应返回 false")
		}
	}
	if got := strings.Count(buf.String(), "不是歌曲"); got != 1 {
		t.Fatalf("同一首歌只应记一次日志，got %d 次：%s", got, buf.String())
	}
}

func TestReadMPRISPlayerSkipsNonSong(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 例会录音", `{"is_song":false}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	// 标题不能用占位词（例如“语音消息”），否则读元数据阶段就会当作没有歌名。
	conn := fakeMPRIS(t, "org.mpris.MediaPlayer2.llmnonsong", "例会录音", "张三", "Playing")
	if _, _, ok := readMPRISPlayer(conn, "org.mpris.MediaPlayer2.llmnonsong", "llmnonsong"); !ok {
		t.Fatal("首次读取时后台请求未返回，应先保留元数据")
	}
	llmInFlight.Wait()
	if _, _, ok := readMPRISPlayer(conn, "org.mpris.MediaPlayer2.llmnonsong", "llmnonsong"); ok {
		t.Fatal("LLM 判定非歌曲时应按没识别到歌曲处理")
	}
}

// newFlakyLLMServer 前 failTimes 次返回 500，之后返回 content。
func newFlakyLLMServer(t *testing.T, failTimes int32, content string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= failTimes {
			w.WriteHeader(http.StatusInternalServerError)
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

func TestRequestLLMExtractionRetriesRequestFailures(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 2, `{"title":"譬如我","singer":"洛天依"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 3

	extraction, err := requestLLMExtraction("xesam:title 譬如我")
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if extraction.Title != "譬如我" || extraction.Singer != "洛天依" {
		t.Fatalf("提取结果不符：%+v", extraction)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("应请求 3 次（失败 2 次 + 成功 1 次），got %d", got)
	}
}

func TestRequestLLMExtractionStopsAfterRetries(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 10, `{"title":"譬如我"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 3

	if _, err := requestLLMExtraction("xesam:title 譬如我"); err == nil {
		t.Fatal("全部失败时应返回错误")
	}
	if got := atomic.LoadInt32(calls); got != 4 {
		t.Fatalf("重试 3 次即共 4 次请求，got %d", got)
	}
}

func TestRequestLLMExtractionWithoutRetry(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 10, `{"title":"譬如我"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 0

	if _, err := requestLLMExtraction("xesam:title 譬如我"); err == nil {
		t.Fatal("retry 为 0 时失败应直接返回错误")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("retry 为 0 时只请求 1 次，got %d", got)
	}
}

func TestRequestLLMExtractionDoesNotRetryUnparsableOutput(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 0, "我觉得这个不是歌曲")
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 3

	if _, err := requestLLMExtraction("xesam:title 譬如我"); err == nil {
		t.Fatal("输出无法解析时应返回错误")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("输出无法解析不应重试，got %d 次请求", got)
	}
}

func TestLLMRetryNegativeTreatedAsZero(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 10, `{"title":"譬如我"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = -5

	if _, err := requestLLMExtraction("xesam:title 譬如我"); err == nil {
		t.Fatal("负数重试次数应按 0 处理并返回错误")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("负数应视为不重试，got %d 次请求", got)
	}
}

// 慢接口不能让歌词刷新卡住：首轮立刻返回原始标题，结果就绪后下一次调用再生效。
func TestApplyLLMExtractionDoesNotBlock(t *testing.T) {
	server, _ := newSlowLLMServer(t, 400*time.Millisecond, `{"title":"譬如我","singer":"洛天依"}`)
	configureLLMTest(t, server.URL)

	metadata := &PlayerMetadata{Player: "firefox.instance1", Title: "洛天依 - 譬如我.flac"}
	started := time.Now()
	if !applyLLMExtraction(metadata, "xesam:title 洛天依 - 譬如我.flac") {
		t.Fatal("识别为歌曲时应保留元数据")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("首轮不应等待接口，耗时 %s", elapsed)
	}
	if metadata.Title != "洛天依 - 譬如我.flac" {
		t.Fatalf("首轮应保留原始标题：%+v", metadata)
	}

	llmInFlight.Wait()
	if !applyLLMExtraction(metadata, "xesam:title 洛天依 - 譬如我.flac") {
		t.Fatal("识别为歌曲时应保留元数据")
	}
	if metadata.Title != "譬如我" || metadata.Artist != "洛天依" {
		t.Fatalf("结果就绪后应覆盖标题歌手：%+v", metadata)
	}
}

// 后台请求同样要按 llm.retry 重试。
func TestApplyLLMExtractionRetriesInBackground(t *testing.T) {
	server, calls := newFlakyLLMServer(t, 1, `{"title":"譬如我","singer":"洛天依"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 3

	metadata := &PlayerMetadata{Player: "vlc.instance1", Title: "洛天依 - 譬如我.flac"}
	if !applyLLMWait(t, metadata, "xesam:title 洛天依 - 譬如我.flac") {
		t.Fatal("识别为歌曲时应保留元数据")
	}
	if metadata.Title != "譬如我" || metadata.Artist != "洛天依" {
		t.Fatalf("重试成功后应覆盖标题歌手：%+v", metadata)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("LLM 请求次数 = %d, want 2", got)
	}
}

// newSlowLLMServer 每次响应前等待 delay，模拟推理类模型。
func newSlowLLMServer(t *testing.T, delay time.Duration, content string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(delay)
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
