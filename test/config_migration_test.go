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
	for _, s := range []string{"# heading", "# mode comment", "# zero comment", "# unknown comment", "custom: yes", "latency_ms: 0", "control_port:", "song_id_mode:", "log:", "level:", "address: 192.0.2.1"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("missing %q in %s", s, data)
		}
	}
	if strings.Contains(string(data), "api:") {
		t.Errorf("legacy api block not removed: %s", data)
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
