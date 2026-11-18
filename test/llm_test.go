package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	keep, _ := applyLLMExtraction(metadata, text)
	return keep
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

func TestClassifyTrackChange(t *testing.T) {
	raw := &PlayerMetadata{Player: "chromium.instance1", Title: "爱言叶V feat. 初音未来", Artist: "DECO*27", TrackID: "TrackCE25"}
	filled := *raw
	filled.Title, filled.Artist = "爱言叶V", "初音未来"
	filled.RawTitle, filled.RawArtist, filled.llmFilled = raw.Title, raw.Artist, true

	if got := classifyTrackChange("", "", "", raw); got != trackSwitched {
		t.Fatalf("首轮应算切歌，got %v", got)
	}
	sameKey := buildIdentityKey(raw)
	if got := classifyTrackChange(sameKey, buildSongKey(raw), raw.TrackID, raw); got != trackSame {
		t.Fatalf("同一首歌应算没变化，got %v", got)
	}
	// 只有标题歌手被 LLM 补全，属于同一首歌，不能记成切歌。
	if got := classifyTrackChange(sameKey, buildSongKey(raw), raw.TrackID, &filled); got != trackRefilled {
		t.Fatalf("LLM 补全应算补全而非切歌，got %v", got)
	}
	other := *raw
	other.Title = "别的歌"
	if got := classifyTrackChange(sameKey, buildSongKey(raw), raw.TrackID, &other); got != trackSwitched {
		t.Fatalf("换歌应算切歌，got %v", got)
	}
	// splayer-next 的歌曲 ID 变了也是切歌。
	splayer := &PlayerMetadata{Player: "splayer-next", Title: "歌名", Artist: "歌手", TrackID: "2"}
	if got := classifyTrackChange(buildIdentityKey(splayer), buildSongKey(splayer), "1", splayer); got != trackSwitched {
		t.Fatalf("歌曲 ID 变化应算切歌，got %v", got)
	}
}

func TestBuildIdentityKeyIgnoresLLMFill(t *testing.T) {
	raw := &PlayerMetadata{Player: "chromium.instance1", Title: "爱言叶V feat. 初音未来", Artist: "DECO*27", TrackID: "TrackCE25"}
	filled := *raw
	filled.Title, filled.Artist = "爱言叶V", "初音未来"
	filled.RawTitle, filled.RawArtist, filled.llmFilled = raw.Title, raw.Artist, true

	if buildSongKey(&filled) == buildSongKey(raw) {
		t.Fatal("补全后搜索词应变化")
	}
	if buildIdentityKey(&filled) != buildIdentityKey(raw) {
		t.Fatalf("补全不应改变 identity key：%q vs %q", buildIdentityKey(&filled), buildIdentityKey(raw))
	}
}

func TestGetMPRISMetadataRecordsLLMFillIdentity(t *testing.T) {
	server, _ := newLLMServer(t, "", `{"title":"爱言叶V","singer":"初音未来"}`, http.StatusOK)
	defer server.Close()
	configureLLMTest(t, server.URL)
	fakeMPRIS(t, "org.mpris.MediaPlayer2.bilibili.instance1", "DECO*27 - 爱言叶V feat. 初音未来_哔哩哔哩_bilibili", "", "Playing")
	onlyMPRISPlayers(t, "org.mpris.MediaPlayer2.bilibili.instance1")

	first := getMPRISMetadata("")
	if first == nil {
		t.Fatal("首轮应读到元数据")
	}
	if first.Title != "爱言叶V feat. 初音未来" || first.Artist != "DECO*27" {
		t.Fatalf("首轮应先用清洗后的原始标题：%+v", first)
	}
	llmInFlight.Wait()

	second := getMPRISMetadata("")
	if second == nil {
		t.Fatal("补全后应读到元数据")
	}
	if second.Title != "爱言叶V" || second.Artist != "初音未来" {
		t.Fatalf("补全后应换成 LLM 结果：%+v", second)
	}
	if !second.llmFilled {
		t.Fatal("补全后应记住补全前的标题歌手")
	}
	if got := classifyTrackChange(buildIdentityKey(first), buildSongKey(first), first.TrackID, second); got != trackRefilled {
		t.Fatalf("补全前后应算同一首歌，got %v", got)
	}
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
		if !strings.Contains(payload.Messages[0].Content, defaultLLMPrompt[:20]) {
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
	if _, ready := applyLLMExtraction(rawMetadata(), text); !ready {
		t.Fatal("命中缓存时结果应当就绪")
	}
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

func TestApplyLLMExtractionJudgesBilibiliTitleVariantsOnce(t *testing.T) {
	server, calls := newLLMServer(t, "xesam:title 曾火遍全网的《溯》，你是否还知道？", `{"is_song":false}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	// chromium 播放 B 站时，xesam:title 会在带站点后缀与不带后缀之间来回切。
	variants := []string{
		"曾火遍全网的《溯》，你是否还知道？",
		"曾火遍全网的《溯》，你是否还知道？_哔哩哔哩_bilibili",
	}
	for _, title := range variants {
		metadata := &PlayerMetadata{Player: "chromium.instance100036", Title: title}
		if applyLLMWait(t, metadata, "xesam:title "+title) {
			t.Fatalf("标题 %q 判定为非歌曲时应跳过", title)
		}
	}
	llmInFlight.Wait()
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("接口请求次数 = %d，期望 1", got)
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
	if keep, ready := applyLLMExtraction(metadata, "xesam:title 原始标题"); !keep || ready {
		t.Fatalf("接口失败时应保持等待状态：keep=%t ready=%t", keep, ready)
	}

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
	if keep, ready := applyLLMExtraction(metadata, "   "); !keep || !ready {
		t.Fatalf("空元数据应立即就绪：keep=%t ready=%t", keep, ready)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("空元数据不应请求 LLM，got %d", got)
	}
}

func TestApplyLLMExtractionLogsResultAtInfo(t *testing.T) {
	server, _ := newLLMServer(t, "xesam:title 洛天依 - 譬如我.flac", `{"title":"譬如我","singer":"洛天依"}`, http.StatusOK)
	configureLLMTest(t, server.URL)

	var output bytes.Buffer
	appLog.SetOutput(&output)
	t.Cleanup(func() {
		appLog.SetOutput(io.Discard)
		configureLogLevel("INFO")
	})
	configureLogLevel("INFO")

	metadata := &PlayerMetadata{Player: "vlc.instance1", Title: "洛天依 - 譬如我.flac", Artist: "洛天依"}
	applyLLMWait(t, metadata, "xesam:title 洛天依 - 譬如我.flac")

	logged := output.String()
	for _, want := range []string{"[INFO]", "LLM 提取歌曲信息", "歌名=譬如我", "歌手=洛天依"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("INFO 级别日志里缺少 %q：%s", want, logged)
		}
	}
	// 请求与响应的原文只在 DEBUG 记录，INFO 下不要刷屏。
	for _, unwanted := range []string{"LLM 请求:", "LLM 响应:"} {
		if strings.Contains(logged, unwanted) {
			t.Fatalf("INFO 级别不应出现 %q：%s", unwanted, logged)
		}
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
	// 交给模型的 xesam:title 已经去掉文件名扩展名与站点后缀。
	server, _ := newLLMServer(t, "xesam:title 洛天依Official,姜洄 - 譬如我", `{"title":"譬如我","singer":"洛天依Official, 姜洄"}`, http.StatusOK)
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
		if keep, _ := applyLLMExtraction(rawMetadata(), "xesam:title 语音消息"); keep {
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
	if keep, _ := applyLLMExtraction(metadata, "xesam:title 洛天依 - 譬如我.flac"); !keep {
		t.Fatal("识别为歌曲时应保留元数据")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("首轮不应等待接口，耗时 %s", elapsed)
	}
	if metadata.Title != "洛天依 - 譬如我.flac" {
		t.Fatalf("首轮应保留原始标题：%+v", metadata)
	}

	llmInFlight.Wait()
	if keep, _ := applyLLMExtraction(metadata, "xesam:title 洛天依 - 譬如我.flac"); !keep {
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

func TestGetMPRISMetadataOnlyAsksLLMForChosenPlayer(t *testing.T) {
	var calls int32
	var prompts []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		if len(payload.Messages) > 0 {
			mu.Lock()
			prompts = append(prompts, payload.Messages[0].Content)
			mu.Unlock()
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"爱言叶V\",\"singer\":\"初音未来\"}"}}]}`)
	}))
	defer server.Close()
	configureLLMTest(t, server.URL)

	fakeMPRIS(t, "org.mpris.MediaPlayer2.playing.instance1", "DECO*27 - 爱言叶V feat. 初音未来_哔哩哔哩_bilibili", "", "Playing")
	fakeMPRIS(t, "org.mpris.MediaPlayer2.paused.instance1", "别的播放器里的视频标题", "", "Paused")
	onlyMPRISPlayers(t,
		"org.mpris.MediaPlayer2.playing.instance1",
		"org.mpris.MediaPlayer2.paused.instance1",
	)

	for i := 0; i < 3; i++ {
		metadata := getMPRISMetadata("")
		if metadata == nil || metadata.Player != "playing.instance1" {
			t.Fatalf("第 %d 轮选中的播放器 = %+v", i, metadata)
		}
		time.Sleep(150 * time.Millisecond)
	}
	llmInFlight.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("只应为选中的播放器请求一次 LLM，实际 %d 次", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, prompt := range prompts {
		if strings.Contains(prompt, "别的播放器里的视频标题") {
			t.Errorf("没选中的播放器也被拿去请求 LLM：%s", prompt)
		}
	}
}

func TestLLMTimeoutUsesConfiguredValue(t *testing.T) {
	configureLLMTest(t, "http://127.0.0.1:1")
	if got := llmTimeout(); got != 60*time.Second {
		t.Fatalf("默认超时应为 60s，got %s", got)
	}
	appConfig.LLM.Timeout = 15000
	if got := llmTimeout(); got != 15*time.Second {
		t.Fatalf("timeout: 15000 应为 15s，got %s", got)
	}
	appConfig.LLM.Timeout = 0
	if got := llmTimeout(); got != 0 {
		t.Fatalf("timeout: 0 表示不超时，got %s", got)
	}
	appConfig.LLM.Timeout = -1
	if got := llmTimeout(); got != 0 {
		t.Fatalf("负数应表示不超时，got %s", got)
	}
}

func TestApplyLLMExtractionTimesOutWhenConfigured(t *testing.T) {
	server, calls := newSlowLLMServer(t, 300*time.Millisecond, `{"title":"爱言叶V","singer":"初音未来"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Retry = 0
	appConfig.LLM.Timeout = 100

	metadata := &PlayerMetadata{Player: "chromium.instance1", Title: "原始标题", Artist: "原始歌手"}
	applyLLMWait(t, metadata, "xesam:title 原始标题")
	if metadata.Title != "原始标题" || metadata.Artist != "原始歌手" {
		t.Fatalf("超时后应保留原始标题：%+v", metadata)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("超时不应重试，请求次数 = %d, want 1", got)
	}
}

func TestApplyLLMExtractionWithoutTimeout(t *testing.T) {
	server, _ := newSlowLLMServer(t, 300*time.Millisecond, `{"title":"爱言叶V","singer":"初音未来"}`)
	configureLLMTest(t, server.URL)
	appConfig.LLM.Timeout = 0

	metadata := &PlayerMetadata{Player: "chromium.instance1", Title: "原始标题", Artist: "原始歌手"}
	applyLLMWait(t, metadata, "xesam:title 原始标题")
	if metadata.Title != "爱言叶V" || metadata.Artist != "初音未来" {
		t.Fatalf("不超时时应拿到提取结果：%+v", metadata)
	}
}
