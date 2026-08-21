package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	cfg := &Config{Server: "http://localhost:8080", Token: "tok-1", Username: "admin"}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Server != cfg.Server || got.Token != cfg.Token || got.Username != cfg.Username {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// 保存的文件权限应为 0600(含 token)。Windows 不支持 Unix 权限位,跳过。
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("权限应为 0600,实际 %v", st.Mode().Perm())
		}
	}

	if err := Clear(path); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Clear 后文件应不存在")
	}
	// 重复 Clear 不报错。
	if err := Clear(path); err != nil {
		t.Fatalf("重复 Clear: %v", err)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatalf("文件不存在应返回空配置而非错误: %v", err)
	}
	if cfg.Server != "" || cfg.Token != "" {
		t.Fatalf("应为空配置: %+v", cfg)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
}
