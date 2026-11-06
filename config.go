package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Mode    string        `yaml:"mode"`
	SPlayer SPlayerConfig `yaml:"splayer"`
	Log     LogConfig     `yaml:"log"`
}

type LogConfig struct {
	Path  string `yaml:"path"`
	Level string `yaml:"level"`
}

type SPlayerConfig struct {
	Address    string `yaml:"address"`
	Port       int    `yaml:"port"`
	SongIDMode string `yaml:"song_id_mode"`
	// LatencyMS SPlayer-Next 播放进度经 HTTP API 返回时的网络延迟补偿（毫秒）。
	LatencyMS int              `yaml:"latency_ms"`
	API       SPlayerAPIConfig `yaml:"api,omitempty"`
}

type SPlayerAPIConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

// defaultLyricLatencyMS 歌词默认提前量，用于抵消 SPlayer-Next API 的网络延迟。
const defaultLyricLatencyMS = 100

var appConfig = defaultConfig()

func defaultConfig() AppConfig {
	return AppConfig{
		Mode: "mpris",
		Log:  LogConfig{Path: "/tmp/waybar-lyrics.log", Level: "INFO"},
		SPlayer: SPlayerConfig{
			Address:    "127.0.0.1",
			Port:       14558,
			SongIDMode: "auto",
			LatencyMS:  defaultLyricLatencyMS,
		},
	}
}

func configPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "waybar-lyrics", "waybar-lyrics.yaml")
}

func loadConfig() (AppConfig, error) {
	config := defaultConfig()
	path := configPath()
	if path == "" {
		return config, errors.New("无法确定配置文件路径，请检查 HOME 或 XDG_CONFIG_HOME")
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = yaml.Marshal(config)
		if err != nil {
			return config, fmt.Errorf("生成默认配置失败：%w", err)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return config, fmt.Errorf("创建配置目录失败：%w", err)
		}
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(createErr, os.ErrExist) {
			data, err = os.ReadFile(path)
		} else if createErr != nil {
			return config, fmt.Errorf("创建配置文件失败：%w", createErr)
		} else {
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil {
				return config, fmt.Errorf("写入默认配置失败：%w", writeErr)
			}
			if closeErr != nil {
				return config, fmt.Errorf("关闭配置文件失败：%w", closeErr)
			}
			err = nil
		}
	}
	if err != nil {
		return config, fmt.Errorf("读取配置文件失败：%w", err)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return defaultConfig(), fmt.Errorf("解析配置文件失败：%w", err)
	}
	if err := completeConfigFile(path, data); err != nil {
		return defaultConfig(), err
	}
	config.Mode = strings.ToLower(strings.TrimSpace(config.Mode))
	if config.Mode == "" {
		config.Mode = "mpris"
	}
	if config.SPlayer.API.Address != "" {
		config.SPlayer.Address = config.SPlayer.API.Address
	}
	if config.SPlayer.API.Port != 0 {
		config.SPlayer.Port = config.SPlayer.API.Port
	}
	if config.SPlayer.Address == "" {
		config.SPlayer.Address = "127.0.0.1"
	}
	if config.SPlayer.Port == 0 {
		config.SPlayer.Port = 14558
	}
	if config.SPlayer.SongIDMode == "" {
		config.SPlayer.SongIDMode = "auto"
	}
	return config, nil
}

// completeConfigFile adds missing defaults without replacing existing YAML nodes.
func completeConfigFile(path string, data []byte) error {
	var document, defaults yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("解析配置文件失败：%w", err)
	}
	defaultData, err := yaml.Marshal(defaultConfig())
	if err != nil {
		return fmt.Errorf("生成默认配置失败：%w", err)
	}
	if err := yaml.Unmarshal(defaultData, &defaults); err != nil {
		return fmt.Errorf("解析默认配置失败：%w", err)
	}
	empty := len(document.Content) == 0
	if empty {
		document = defaults
	} else if document.Content[0].Kind != yaml.MappingNode {
		// Explicit null and other existing values are not missing keys.
		return nil
	} else {
		migrated, err := migrateLegacyAPI(document.Content[0])
		if err != nil {
			return err
		}
		if removeObsoleteSPlayerFields(document.Content[0]) {
			migrated = true
		}
		if mergeConfigDefaults(document.Content[0], defaults.Content[0]) {
			migrated = true
		}
		if !migrated {
			return nil
		}
	}
	updated, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("生成补全配置失败：%w", err)
	}
	// yaml.v3 does not retain comments in a document without a value.
	if empty && len(strings.TrimSpace(string(data))) > 0 {
		updated = append(append(append([]byte{}, data...), '\n'), updated...)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("确定配置文件路径失败：%w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("读取配置文件权限失败：%w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(resolved), ".waybar-lyrics-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败：%w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(updated)
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("写入补全配置失败：%w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭补全配置失败：%w", closeErr)
	}
	if err = os.Rename(name, resolved); err != nil {
		return fmt.Errorf("替换配置文件失败：%w", err)
	}
	return nil
}

// migrateLegacyAPI 将旧版 splayer.api 的地址和端口迁移到外层字段并删除该旧配置项。
// 若旧配置块含有无法迁移的自定义子项，则保持原样，避免静默丢失用户数据。
func migrateLegacyAPI(root *yaml.Node) (bool, error) {
	splayer, _ := mappingEntry(root, "splayer")
	if splayer == nil || splayer.Kind != yaml.MappingNode {
		return false, nil
	}
	api, apiIndex := mappingEntry(splayer, "api")
	if api == nil {
		return false, nil
	}
	if api.Kind == yaml.ScalarNode {
		if api.Tag != "!!null" {
			return false, errors.New("配置 splayer.api 必须是映射")
		}
		splayer.Content = append(splayer.Content[:apiIndex], splayer.Content[apiIndex+2:]...)
		return true, nil
	}
	var fields map[string]interface{}
	if err := api.Decode(&fields); err != nil {
		return false, fmt.Errorf("解析旧配置 splayer.api 失败：%w", err)
	}
	for key := range fields {
		if key != "address" && key != "port" {
			// 未知子项无法安全迁移，保留旧配置块。
			return false, nil
		}
	}
	var legacy SPlayerAPIConfig
	if err := api.Decode(&legacy); err != nil {
		return false, fmt.Errorf("解析旧配置 splayer.api 失败：%w", err)
	}
	if legacy.Address != "" {
		setMappingValue(splayer, "address", "!!str", legacy.Address)
	}
	if legacy.Port != 0 {
		setMappingValue(splayer, "port", "!!int", strconv.Itoa(legacy.Port))
	}
	splayer.Content = append(splayer.Content[:apiIndex], splayer.Content[apiIndex+2:]...)
	return true, nil
}

// removeObsoleteSPlayerFields 删除已不再使用的配置项，避免旧配置残留造成误解。
func removeObsoleteSPlayerFields(root *yaml.Node) bool {
	splayer, _ := mappingEntry(root, "splayer")
	if splayer == nil || splayer.Kind != yaml.MappingNode {
		return false
	}
	_, controlPortIndex := mappingEntry(splayer, "control_port")
	if controlPortIndex < 0 {
		return false
	}
	splayer.Content = append(splayer.Content[:controlPortIndex], splayer.Content[controlPortIndex+2:]...)
	return true
}

// mappingEntry 返回映射中指定键的值节点及其在 Content 中的下标。
func mappingEntry(mapping *yaml.Node, key string) (*yaml.Node, int) {
	if mapping.Kind != yaml.MappingNode {
		return nil, -1
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], i
		}
	}
	return nil, -1
}

// setMappingValue 覆盖已有键的值，不存在时追加到映射末尾。
func setMappingValue(mapping *yaml.Node, key, tag, value string) {
	if node, _ := mappingEntry(mapping, key); node != nil {
		node.Kind = yaml.ScalarNode
		node.Tag = tag
		node.Value = value
		node.Style = 0
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

func mergeConfigDefaults(existing, defaults *yaml.Node) bool {
	if existing.Kind != yaml.MappingNode || defaults.Kind != yaml.MappingNode {
		return false
	}
	// Decode merged mappings too, so YAML merge keys do not get overridden.
	var effective map[string]interface{}
	if err := existing.Decode(&effective); err != nil {
		return false
	}
	changed := false
	for i := 0; i < len(defaults.Content); i += 2 {
		key, value := defaults.Content[i], defaults.Content[i+1]
		found := false
		for j := 0; j < len(existing.Content); j += 2 {
			if existing.Content[j].Value == key.Value {
				found = true
				if mergeConfigDefaults(existing.Content[j+1], value) {
					changed = true
				}
				break
			}
		}
		if !found {
			if _, inherited := effective[key.Value]; inherited {
				continue
			}
			existing.Content = append(existing.Content, key, value)
			changed = true
		}
	}
	return changed
}

func parseCLI(args []string, stdout io.Writer) (string, error) {
	flags := flag.NewFlagSet("waybar-lyrics", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		fmt.Fprintln(stdout, "用法：waybar-lyrics [选项]")
		fmt.Fprintln(stdout, "Waybar 同步歌词模块。命令行模式优先级高于 YAML 配置。")
		fmt.Fprintln(stdout, "选项：")
		fmt.Fprintln(stdout, "  -mode string  播放器模式：mpris、splayer 或 splayer-next（覆盖配置文件）")
		fmt.Fprintln(stdout, "  -m string     播放器模式简写")
		fmt.Fprintln(stdout, "  -h, --help    显示帮助")
	}
	mode := flags.String("mode", "", "播放器模式：mpris、splayer 或 splayer-next（覆盖配置文件）")
	flags.StringVar(mode, "m", "", "播放器模式的简写")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("不支持的位置参数：%s", strings.Join(flags.Args(), " "))
	}
	return *mode, nil
}

func applyModeOverride(config AppConfig, mode string) (AppConfig, error) {
	if mode != "" {
		config.Mode = strings.ToLower(strings.TrimSpace(mode))
	}
	return config, validateConfig(config)
}

func validateConfig(config AppConfig) error {
	switch config.Mode {
	case "mpris", "splayer", "splayer-next":
	default:
		return errors.New("mode 必须是 mpris、splayer 或 splayer-next")
	}
	if config.SPlayer.LatencyMS < 0 {
		return errors.New("splayer.latency_ms 不能为负数")
	}
	if strings.TrimSpace(config.Log.Path) == "" {
		return errors.New("log.path 不能为空")
	}
	switch strings.ToUpper(strings.TrimSpace(config.Log.Level)) {
	case "DEBUG", "INFO", "WARN", "ERROR":
	default:
		return errors.New("log.level 必须是 DEBUG、INFO、WARN 或 ERROR")
	}
	return nil
}
