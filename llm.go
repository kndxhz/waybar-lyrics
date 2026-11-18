package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// llmRequestTimeout 限制单次请求耗时，避免网络卡住歌词刷新循环。
	llmRequestTimeout = 10 * time.Second
	// llmFailureTTL 让失败的提取在短时间内不再重试，避免每轮循环都打一遍接口。
	llmFailureTTL = 60 * time.Second
	// llmMaxCacheEntries 限制缓存条目数，键随歌曲变化，超出后整体清空。
	llmMaxCacheEntries = 256
)

// llmExtraction 是 LLM 对一段媒体元数据的判断结果。
type llmExtraction struct {
	IsSong bool
	Title  string
	Singer string
}

type llmCacheEntry struct {
	extraction llmExtraction
	err        error
	expires    time.Time // 零值表示成功结果不主动过期
}

var llmCache = struct {
	mu      sync.Mutex
	entries map[string]llmCacheEntry
}{entries: map[string]llmCacheEntry{}}

var (
	// <think ...>…</think> 需要兼容带属性的写法，大小写与换行都不敏感。
	llmThinkBlockPattern = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think\s*>`)
	// 未闭合的思考标签（流式输出被截断）直接丢到结尾。
	llmOpenThinkPattern = regexp.MustCompile(`(?is)<think\b[^>]*>.*$`)
	llmOrphanThinkClose = regexp.MustCompile(`(?is)^.*?</think\s*>`)
	// ```json 之类的代码块标记，只删标记本身，保留块内 JSON。
	llmCodeFencePattern = regexp.MustCompile("`{3,}[a-zA-Z0-9_+-]*")
)

// llmEnabled 判断是否启用 LLM 提取；缺模型或接口地址时视为未启用。
func llmEnabled(config AppConfig) bool {
	if !config.LLM.Enable {
		return false
	}
	return strings.TrimSpace(config.LLM.BaseURL) != "" &&
		strings.TrimSpace(config.LLM.ModelName) != "" &&
		strings.TrimSpace(config.LLM.Prompt) != ""
}

// cleanLLMOutput 清洗模型原始输出：去思考标签、去代码块标记，
// 再截取首个 { 到最后一个 } 之间的内容，交给 JSON 解析。
func cleanLLMOutput(raw string) string {
	cleaned := llmThinkBlockPattern.ReplaceAllString(raw, "")
	cleaned = llmOrphanThinkClose.ReplaceAllString(cleaned, "")
	cleaned = llmOpenThinkPattern.ReplaceAllString(cleaned, "")
	cleaned = llmCodeFencePattern.ReplaceAllString(cleaned, "")
	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(cleaned[start : end+1])
}

// parseLLMExtraction 解析清洗后的 JSON。只有拿到歌名才算成功识别的歌曲；
// is_song 为 false 或歌名为空时返回解析错误之外的明确结果，由调用方决定兜底。
func parseLLMExtraction(raw string) (llmExtraction, error) {
	cleaned := cleanLLMOutput(raw)
	if cleaned == "" {
		return llmExtraction{}, errors.New("LLM 输出里没有 JSON 对象")
	}
	var payload struct {
		Title  *string `json:"title"`
		Singer *string `json:"singer"`
		IsSong *bool   `json:"is_song"`
	}
	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		return llmExtraction{}, fmt.Errorf("解析 LLM 输出失败：%w", err)
	}
	if payload.IsSong != nil && !*payload.IsSong {
		return llmExtraction{IsSong: false}, nil
	}
	title := ""
	if payload.Title != nil {
		title = strings.TrimSpace(*payload.Title)
	}
	if title == "" {
		return llmExtraction{}, errors.New("LLM 输出里没有歌名")
	}
	singer := ""
	if payload.Singer != nil {
		singer = strings.TrimSpace(*payload.Singer)
		if strings.EqualFold(singer, "null") {
			singer = ""
		}
	}
	return llmExtraction{IsSong: true, Title: title, Singer: singer}, nil
}

// llmRetryBackoff 是两次请求之间的等待时间，避免接口刚抖动就立刻重发。
const llmRetryBackoff = 200 * time.Millisecond

// requestLLMExtraction 把元数据文本填进 Prompt 占位符后请求 OpenAI 兼容接口。
// 请求失败按 llm.retry 重试，模型输出无法解析时不重试。
func requestLLMExtraction(metadataText string) (llmExtraction, error) {
	prompt := strings.ReplaceAll(appConfig.LLM.Prompt, "{metadata}", metadataText)
	content, err := callLLMChatWithRetry(prompt)
	if err != nil {
		return llmExtraction{}, err
	}
	return parseLLMExtraction(content)
}

// callLLMChatWithRetry 在请求失败时最多重试 llm.retry 次：0 表示只请求一次。
func callLLMChatWithRetry(prompt string) (string, error) {
	retries := appConfig.LLM.Retry
	if retries < 0 {
		retries = 0
	}
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), llmRequestTimeout)
		content, err := callLLMChat(ctx, prompt)
		cancel()
		if err == nil {
			if attempt > 0 {
				logEvent("INFO", "LLM 接口第 %d 次重试成功", attempt)
			}
			return content, nil
		}
		if attempt >= retries {
			return "", err
		}
		logEvent("WARN", "LLM 接口第 %d/%d 次请求失败，%v；%s 后重试", attempt+1, retries+1, err, llmRetryBackoff)
		time.Sleep(llmRetryBackoff)
	}
}

// callLLMChat 调用 {base_url}/chat/completions 并返回首条回复内容。
func callLLMChat(ctx context.Context, prompt string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(appConfig.LLM.BaseURL), "/")
	if baseURL == "" {
		return "", errors.New("LLM 接口地址为空")
	}
	payload, err := json.Marshal(map[string]any{
		"model":       appConfig.LLM.ModelName,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		return "", fmt.Errorf("构造 LLM 请求失败：%w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("构造 LLM 请求失败：%w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(appConfig.LLM.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: llmRequestTimeout}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("请求 LLM 接口失败：%w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取 LLM 响应失败：%w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("LLM 接口返回 %d：%s", response.StatusCode, snippet)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("解析 LLM 响应失败：%w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("LLM 响应里没有 choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// llmExtractionFor 带缓存的提取入口，键为播放器与原始标题歌手，同一首歌只请求一次。
// 第二个返回值表示结果是否直接来自缓存，调用方据此避免每轮刷新都打日志。
func llmExtractionFor(key, metadataText string) (llmExtraction, error, bool) {
	llmCache.mu.Lock()
	if entry, ok := llmCache.entries[key]; ok {
		if entry.expires.IsZero() || time.Now().Before(entry.expires) {
			llmCache.mu.Unlock()
			return entry.extraction, entry.err, true
		}
		delete(llmCache.entries, key)
	}
	llmCache.mu.Unlock()

	extraction, err := requestLLMExtraction(metadataText)

	llmCache.mu.Lock()
	if len(llmCache.entries) >= llmMaxCacheEntries {
		llmCache.entries = map[string]llmCacheEntry{}
	}
	entry := llmCacheEntry{extraction: extraction, err: err}
	if err != nil {
		entry.expires = time.Now().Add(llmFailureTTL)
	}
	llmCache.entries[key] = entry
	llmCache.mu.Unlock()
	return extraction, err, false
}

// applyLLMExtraction 用 LLM 从元数据文本里提取歌名与歌手。
// 返回 false 表示 LLM 判定这不是歌曲，调用方应按「没识别到歌曲」处理，
// 不再用原始标题去搜索歌词；请求失败或输出无法解析时保留原始标题继续搜索。
func applyLLMExtraction(metadata *PlayerMetadata, metadataText string) bool {
	if metadata == nil {
		return true
	}
	if strings.TrimSpace(metadataText) == "" {
		return true
	}
	key := metadata.Player + "\x00" + metadata.Title + "\x00" + metadata.Artist
	extraction, err, cached := llmExtractionFor(key, metadataText)
	// 同一首歌每轮刷新都会走到这里，结果命中缓存时不再重复记日志。
	if err != nil {
		if !cached {
			logEvent("WARN", "LLM 提取歌曲信息失败，改用原始标题「%s」搜索：%v", metadata.Title, err)
		}
		return true
	}
	if !extraction.IsSong {
		if !cached {
			logEvent("INFO", "LLM 判定该媒体不是歌曲，按未识别到歌曲处理：「%s」", metadata.Title)
		}
		return false
	}
	if !cached {
		logEvent("DEBUG", "LLM 提取歌曲信息：歌名=%s 歌手=%s", extraction.Title, extraction.Singer)
	}
	metadata.Title = extraction.Title
	metadata.Artist = extraction.Singer
	return true
}

// mprisMetadataText 把已读到的 D-Bus 属性拼成文本，内容与 playerctl metadata
// 的输出一致（每行一个「键 值」），无需额外调用 playerctl。
func mprisMetadataText(props map[string]dbus.Variant) string {
	lines := []string{}
	appendLine := func(key string, variant dbus.Variant) {
		value := strings.TrimSpace(mprisTextValue(variant))
		if value == "" {
			return
		}
		lines = append(lines, key+" "+value)
	}
	for _, key := range sortedVariantKeys(props) {
		switch value := props[key].Value().(type) {
		case map[string]dbus.Variant:
			for _, subKey := range sortedVariantKeys(value) {
				appendLine(subKey, value[subKey])
			}
		default:
			appendLine(key, props[key])
		}
	}
	return strings.Join(lines, "\n")
}

func sortedVariantKeys(props map[string]dbus.Variant) []string {
	keys := make([]string, 0, len(props))
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// mprisTextValue 把变体值格式化成一行文本，列表用逗号连接。
func mprisTextValue(variant dbus.Variant) string {
	switch value := variant.Value().(type) {
	case nil:
		return ""
	case string:
		return value
	case dbus.ObjectPath:
		return string(value)
	case []string:
		return strings.Join(value, ", ")
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, ", ")
	case bool:
		return strconv.FormatBool(value)
	default:
		return fmt.Sprintf("%v", value)
	}
}
