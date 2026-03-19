package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type ProxmoxConfig struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	User       string `yaml:"user"`
	TokenName  string `yaml:"token_name"`
	TokenValue string `yaml:"token_value"`
	Node       string `yaml:"node"`
	VerifySSL  bool   `yaml:"verify_ssl"`
	Storage    string `yaml:"storage"`
	Template   string `yaml:"template"`
	Bridge     string `yaml:"bridge"`
	VMIDStart  int    `yaml:"vmid_start"`
	VMIDEnd    int    `yaml:"vmid_end"`
}

type GatewayConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	HostKeyPath    string `yaml:"host_key_path"`
	GatewayKeyPath string `yaml:"gateway_key_path"`
	DBPath         string `yaml:"db_path"`
}

type Config struct {
	Proxmox ProxmoxConfig `yaml:"proxmox"`
	Gateway GatewayConfig `yaml:"gateway"`
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("PROXBOX_CONFIG")
		if path == "" {
			path = "config.yaml"
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Proxmox.Port == 0 {
		cfg.Proxmox.Port = 8006
	}
	if cfg.Gateway.Port == 0 {
		cfg.Gateway.Port = 2222
	}
	if cfg.Proxmox.VMIDStart == 0 {
		cfg.Proxmox.VMIDStart = 5000
	}
	if cfg.Proxmox.VMIDEnd == 0 {
		cfg.Proxmox.VMIDEnd = 5999
	}

	return &cfg, nil
}
