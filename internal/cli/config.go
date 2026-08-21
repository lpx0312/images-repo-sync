package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是 CLI 本地配置文件(~/.irs-cli.json)的内容:
// 保存服务端地址与登录 token,让后续命令免重复传参。
// 出于安全考虑不落盘密码;token 过期后重新 irs login 即可。
type Config struct {
	Server         string `json:"server,omitempty"`
	Token          string `json:"token,omitempty"`
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
	Username       string `json:"username,omitempty"`
}

// DefaultConfigPath 返回默认配置文件路径 $HOME/.irs-cli.json。
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户目录: %w", err)
	}
	return filepath.Join(home, ".irs-cli.json"), nil
}

// LoadConfig 读取配置文件;文件不存在时返回空配置(视为未登录,非错误)。
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		p, err := DefaultConfigPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件 %s 格式非法: %w", path, err)
	}
	return cfg, nil
}

// Save 把配置写回文件。文件含 token 属敏感信息,权限取 0600。
func (c *Config) Save(path string) error {
	if path == "" {
		p, err := DefaultConfigPath()
		if err != nil {
			return err
		}
		path = p
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Clear 删除配置文件(登出)。
func Clear(path string) error {
	if path == "" {
		p, err := DefaultConfigPath()
		if err != nil {
			return err
		}
		path = p
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除配置文件失败: %w", err)
	}
	return nil
}
