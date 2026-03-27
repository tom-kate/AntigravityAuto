package config

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Proxy        string `yaml:"proxy"          json:"proxy"`
	ProxyEnabled bool   `yaml:"proxy_enabled"  json:"proxy_enabled"`
	CPAToken     string `yaml:"cpa_token"      json:"cpa_token"`
	CPAAPIURL    string `yaml:"cpa_api_url"    json:"cpa_api_url"`
	SMSUsername  string `yaml:"sms_username"   json:"sms_username"`
	SMSPassword string `yaml:"sms_password"   json:"sms_password"`
	SMSChannelID  string   `yaml:"sms_channel_id,omitempty"  json:"sms_channel_id,omitempty"`  // deprecated: use sms_channel_ids
	SMSChannelIDs []string `yaml:"sms_channel_ids"           json:"sms_channel_ids"`
	Headless     bool   `yaml:"headless"       json:"headless"`
	Concurrency  int    `yaml:"concurrency"    json:"concurrency"`
	Port         int    `yaml:"port"           json:"port"`
}

const configFile = "data/config.yaml"

var (
	App     Config
	mu      sync.RWMutex
	modTime time.Time
)

// Get returns a thread-safe copy of the current config
func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	return App
}

// Load reads config.yaml
func Load() error {
	return reload()
}

// reload reads the yaml file into App
func reload() error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config.yaml failed: %v", err)
	}

	if cfg.Port == 0 {
		cfg.Port = 8080
	}

	// Migrate legacy sms_channel_id → sms_channel_ids
	if cfg.SMSChannelID != "" && len(cfg.SMSChannelIDs) == 0 {
		cfg.SMSChannelIDs = []string{cfg.SMSChannelID}
		cfg.SMSChannelID = ""
	}

	App = cfg

	// Update mod time
	if fi, err := os.Stat(configFile); err == nil {
		modTime = fi.ModTime()
	}

	return nil
}

// Save writes the current config to config.yaml
func Save(cfg Config) error {
	mu.Lock()
	defer mu.Unlock()

	if cfg.Port == 0 {
		cfg.Port = 8080
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return err
	}

	App = cfg

	if fi, err := os.Stat(configFile); err == nil {
		modTime = fi.ModTime()
	}

	return nil
}

// StartHotReload checks for config file changes every 5 seconds
func StartHotReload() {
	go func() {
		for {
			time.Sleep(5 * time.Second)

			fi, err := os.Stat(configFile)
			if err != nil {
				continue
			}

			mu.RLock()
			changed := fi.ModTime().After(modTime)
			mu.RUnlock()

			if changed {
				if err := reload(); err != nil {
					log.Printf("Hot reload config failed: %v", err)
				} else {
					log.Println("Config hot-reloaded successfully")
				}
			}
		}
	}()
}
