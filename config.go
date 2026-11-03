package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Mode    string        `yaml:"mode"`
	SPlayer SPlayerConfig `yaml:"splayer"`
}

type SPlayerConfig struct {
	Address    string           `yaml:"address"`
	Port       int              `yaml:"port"`
	SongIDMode string           `yaml:"song_id_mode"`
	API        SPlayerAPIConfig `yaml:"api"`
}

type SPlayerAPIConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

var appConfig = defaultConfig()

func defaultConfig() AppConfig {
	return AppConfig{
		Mode: "mpris",
		SPlayer: SPlayerConfig{
			Address:    "127.0.0.1",
			Port:       14558,
			SongIDMode: "auto",
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

func loadConfig() AppConfig {
	config := defaultConfig()
	path := configPath()
	if path == "" {
		return config
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return defaultConfig()
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
	return config
}

func validateConfig(config AppConfig) error {
	switch config.Mode {
	case "mpris", "splayer", "splayer-next":
		return nil
	default:
		return errors.New("mode 必须是 mpris、splayer 或 splayer-next")
	}
}
