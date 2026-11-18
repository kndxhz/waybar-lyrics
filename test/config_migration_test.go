package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func migrationFile(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigMigrationPreservesValuesAndComments(t *testing.T) {
	path := migrationFile(t, "# heading\nmode: splayer-next # mode comment\nsplayer:\n  latency_ms: 0 # zero comment\n  api:\n    address: 192.0.2.1\nextra:\n  custom: yes # unknown comment\n")
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.SPlayer.LatencyMS != 0 || got.SPlayer.Address != "192.0.2.1" || got.SPlayer.Port != 14558 {
		t.Fatalf("config: %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"# heading", "# mode comment", "# zero comment", "# unknown comment", "custom: yes", "latency_ms: 0", "song_id_mode:", "log:", "level:", "address: 192.0.2.1"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("missing %q in %s", s, data)
		}
	}
	if strings.Contains(string(data), "api:") || strings.Contains(string(data), "control_port") {
		t.Errorf("legacy fields not removed: %s", data)
	}
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("second load rewrote complete configuration")
	}
}

func TestConfigMigrationEmptyAndNull(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", "mode: mpris"},
		{"comment only", "# retained\n", "# retained"},
		{"nested null", "splayer: null\n", "splayer: null"},
		{"field null", "splayer:\n  latency_ms: null\n", "latency_ms: null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := migrationFile(t, tc.input)
			if _, err := loadConfig(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Fatalf("want %q in %s", tc.want, data)
			}
		})
	}
}

func TestConfigMigrationInvalidInputUnchanged(t *testing.T) {
	for _, input := range []string{"splayer: [broken\n", "splayer:\n  port: not-a-number\n", "mode: a\nmode: b\n"} {
		t.Run(input, func(t *testing.T) {
			path := migrationFile(t, input)
			if _, err := loadConfig(); err == nil {
				t.Fatal("expected error")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != input {
				t.Fatalf("file changed: %s", data)
			}
		})
	}
}

func TestConfigMigrationRemovesLegacyAPIFields(t *testing.T) {
	for _, tc := range []struct {
		name, input, address string
		port                 int
	}{
		{"空字段不覆盖外层", "mode: splayer-next\nsplayer:\n  address: 10.0.0.1\n  port: 15000\n  api:\n    address: \"\"\n    port: 0\n", "10.0.0.1", 15000},
		{"旧字段覆盖外层", "mode: splayer-next\nsplayer:\n  address: 10.0.0.1\n  port: 15000\n  api:\n    address: 192.0.2.1\n    port: 16666\n", "192.0.2.1", 16666},
		{"仅旧字段", "mode: splayer-next\nsplayer:\n  api:\n    address: 192.0.2.2\n    port: 17777\n", "192.0.2.2", 17777},
		{"空 api 空值", "mode: splayer-next\nsplayer:\n  api: null\n", "127.0.0.1", 14558},
		{"空 api 映射", "mode: splayer-next\nsplayer:\n  api: {}\n", "127.0.0.1", 14558},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := migrationFile(t, tc.input)
			config, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if config.SPlayer.Address != tc.address || config.SPlayer.Port != tc.port {
				t.Fatalf("config: %+v", config.SPlayer)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "api:") {
				t.Fatalf("legacy api block not removed: %s", data)
			}
			var parsed struct {
				SPlayer struct {
					Address string `yaml:"address"`
					Port    int    `yaml:"port"`
				} `yaml:"splayer"`
			}
			if err := yaml.Unmarshal(data, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.SPlayer.Address != tc.address || parsed.SPlayer.Port != tc.port {
				t.Fatalf("written config mismatch: %+v", parsed.SPlayer)
			}
			if _, err := loadConfig(); err != nil {
				t.Fatal(err)
			}
			again, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, again) {
				t.Fatal("second load rewrote migrated configuration")
			}
		})
	}
}

func TestConfigMigrationRemovesControlPort(t *testing.T) {
	path := migrationFile(t, "mode: splayer\nsplayer:\n  control_port: 26000 # old setting\n")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "control_port") {
		t.Fatalf("obsolete control_port not removed: %s", data)
	}
}

func TestConfigMigrationKeepsUnknownAPIFields(t *testing.T) {
	path := migrationFile(t, "mode: splayer-next\nsplayer:\n  api:\n    address: 192.0.2.3\n    extra: keep\n")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "extra: keep") || !strings.Contains(string(data), "api:") {
		t.Fatalf("unknown api fields lost: %s", data)
	}
}

func TestNewConfigOmitsLegacyAPIFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api:") {
		t.Fatalf("new config contains legacy api: %s", data)
	}
}

func TestConfigMigrationWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.yaml")
	err := completeConfigFile(path, []byte("mode: mpris\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigMPRISFallbackDefaultsToTrue(t *testing.T) {
	path := migrationFile(t, "mode: splayer-next\nlog:\n  path: /tmp/wy.log\n  level: INFO\n")
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !got.MPRISFallback {
		t.Fatalf("缺少 mpris-fallback 时应默认为 true，got %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mpris-fallback: true") {
		t.Errorf("配置升级应补写 mpris-fallback: true，got %s", data)
	}
	// 再次读取不应重复写入或改动取值
	again, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !again.MPRISFallback {
		t.Fatalf("二次读取的 mpris-fallback 应为 true，got %+v", again)
	}
}

func TestConfigMPRISFallbackKeepsExplicitFalse(t *testing.T) {
	path := migrationFile(t, "mode: splayer\nmpris-fallback: false\n")
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.MPRISFallback {
		t.Fatalf("显式关闭的 mpris-fallback 不应被改回 true，got %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mpris-fallback: false") {
		t.Errorf("应保留 mpris-fallback: false，got %s", data)
	}
}

func TestConfigLLMDefaultsMigrated(t *testing.T) {
	path := migrationFile(t, "mode: mpris\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LLM.Enable {
		t.Error("llm.enable 默认应为 false")
	}
	if config.LLM.BaseURL != "https://api.deepseek.com/v1" || config.LLM.ModelName != "deepseek-flash" {
		t.Errorf("llm 默认值不符：%+v", config.LLM)
	}
	if config.LLM.APIKey != "xxx" {
		t.Errorf("api_key 默认值应写为 xxx，got %q", config.LLM.APIKey)
	}
	if !strings.Contains(config.LLM.Prompt, "{metadata}") {
		t.Errorf("默认 Prompt 应包含占位符：%q", config.LLM.Prompt)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"llm:", "enable: false", "base_url:", "model_name: deepseek-flash", "api_key: xxx", "{metadata}"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("升级后的配置缺少 %q：\n%s", want, data)
		}
	}
}

func TestConfigLLMPreservesExplicitValues(t *testing.T) {
	path := migrationFile(t, "mode: mpris\nllm:\n  enable: true\n  api_key: sk-real\n  prompt: |\n    自定义指令\n\n    {metadata}\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.LLM.Enable || config.LLM.APIKey != "sk-real" {
		t.Fatalf("显式设置应保留：%+v", config.LLM)
	}
	if !strings.Contains(config.LLM.Prompt, "自定义指令") || !strings.Contains(config.LLM.Prompt, "{metadata}") {
		t.Fatalf("自定义 Prompt 应保留：%q", config.LLM.Prompt)
	}
	// 缺失的字段补齐默认值。
	if config.LLM.ModelName != "deepseek-flash" || config.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("缺失字段应补默认值：%+v", config.LLM)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "api_key: sk-real") || strings.Contains(string(data), "自定义指令") == false {
		t.Fatalf("写回的配置不符：\n%s", data)
	}
	if strings.Contains(string(data), "基于以下内容") {
		t.Fatalf("不应覆盖用户 Prompt：\n%s", data)
	}
}

func TestConfigMPRISMSDefaultsTo500(t *testing.T) {
	path := migrationFile(t, "mode: mpris\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.MPRISMS != 500 {
		t.Fatalf("mpris-ms 默认值应为 500，got %d", config.MPRISMS)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mpris-ms: 500") {
		t.Fatalf("升级后的配置应补写 mpris-ms：\n%s", data)
	}
}

func TestConfigMPRISMSKeepsExplicitValue(t *testing.T) {
	path := migrationFile(t, "mode: mpris\nmpris-ms: 0\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.MPRISMS != 0 {
		t.Fatalf("显式设置 mpris-ms: 0 应保留，got %d", config.MPRISMS)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mpris-ms: 0") {
		t.Fatalf("配置应保留显式取值：\n%s", data)
	}
}

func TestConfigLLMRetryDefaultsToThree(t *testing.T) {
	path := migrationFile(t, "mode: mpris\nllm:\n  enable: true\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LLM.Retry != 3 {
		t.Fatalf("llm.retry 默认值应为 3，got %d", config.LLM.Retry)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "retry: 3") {
		t.Fatalf("升级后的配置应补写 retry: 3：\n%s", data)
	}
}

func TestConfigLLMRetryKeepsExplicitValue(t *testing.T) {
	path := migrationFile(t, "mode: mpris\nllm:\n  enable: true\n  retry: 0\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LLM.Retry != 0 {
		t.Fatalf("显式 retry: 0 应保留，got %d", config.LLM.Retry)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "retry: 0") {
		t.Fatalf("不应覆盖显式 retry：\n%s", data)
	}
}

func TestConfigLLMRetryNegativeClampedToZero(t *testing.T) {
	migrationFile(t, "mode: mpris\nllm:\n  enable: true\n  retry: -3\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.LLM.Retry != 0 {
		t.Fatalf("负数重试次数应归一为 0，got %d", config.LLM.Retry)
	}
}

func TestConfigPauseMPRISFallbackDefaultsToTrue(t *testing.T) {
	path := migrationFile(t, "mode: splayer-next\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.SPlayer.PauseMPRISFallback {
		t.Fatalf("pause-mpris-fallback 应默认开启：%+v", config.SPlayer)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pause-mpris-fallback: true") {
		t.Errorf("配置文件缺少 pause-mpris-fallback：%s", data)
	}
}

func TestConfigPauseMPRISFallbackKeepsExplicitFalse(t *testing.T) {
	migrationFile(t, "mode: splayer-next\nsplayer:\n  pause-mpris-fallback: false\n")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.SPlayer.PauseMPRISFallback {
		t.Fatalf("显式关闭应保留：%+v", config.SPlayer)
	}
}
