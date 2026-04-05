package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VastaiAPIKey string `yaml:"vastai_api_key"`
	HFToken      string `yaml:"hf_token"`
	BasePort     int    `yaml:"base_port"`
}

func Load() (*Config, error) {
	cfg := &Config{
		BasePort: 8000,
	}

	// Load config file from ~/.mycodeagent/config.yaml
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
	}

	// Env vars override config file
	if v := os.Getenv("VASTAI_API_KEY"); v != "" {
		cfg.VastaiAPIKey = v
	}
	if v := os.Getenv("HF_TOKEN"); v != "" {
		cfg.HFToken = v
	}

	return cfg, nil
}

// Save writes the config to ~/.mycodeagent/config.yaml.
func Save(cfg *Config) error {
	dataDir, err := DataDir()
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	configPath := filepath.Join(dataDir, "config.yaml")
	return os.WriteFile(configPath, data, 0o600)
}

func ConfigPath() (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "config.yaml"), nil
}

func DataDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(homeDir, ".mycodeagent")
	return dir, os.MkdirAll(dir, 0o700)
}
