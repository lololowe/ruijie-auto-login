package ruijie

import (
	"encoding/json"
	"fmt"
	"os"
)

type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Config struct {
	PortalBase     string    `json:"portalBase"`
	Accounts       []Account `json:"accounts"`
	CheckInterval  int       `json:"checkInterval"`
	RetryInterval  int       `json:"retryInterval"`
	RequestTimeout int       `json:"requestTimeout"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config

	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if cfg.PortalBase == "" {
		return nil, fmt.Errorf("portalBase 不能为空")
	}

	if len(cfg.Accounts) == 0 {
		return nil, fmt.Errorf("至少需要配置一个账号")
	}

	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 10
	}

	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 5
	}

	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10
	}

	return &cfg, nil
}
