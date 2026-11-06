package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Address     string `yaml:"address"`
	Port        int    `yaml:"port"`
	ControlPort int    `yaml:"control_port"`
	SongIDMode  string `yaml:"song_id_mode"`
	// LatencyMS SPlayer-Next 播放进度经 HTTP API 返回时的网络延迟补偿（毫秒）。
	LatencyMS int              `yaml:"latency_ms"`
	API       SPlayerAPIConfig `yaml:"api"`
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
			Address:     "127.0.0.1",
			Port:        14558,
			ControlPort: 25884,
			SongIDMode:  "auto",
			LatencyMS:   defaultLyricLatencyMS,
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
	if config.SPlayer.ControlPort <= 0 {
		config.SPlayer.ControlPort = 25884
	}
	if config.SPlayer.SongIDMode == "" {
		config.SPlayer.SongIDMode = "auto"
	}
	return config, nil
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
