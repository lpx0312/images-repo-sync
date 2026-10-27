package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withDefaults 保存并恢复全局 AppConfig,避免测试间互相污染。
func withDefaults(t *testing.T, mut func()) {
	t.Helper()
	saved := *AppConfig
	defer func() { *AppConfig = saved }()
	mut()
}

func TestLoadDefaults(t *testing.T) {
	withDefaults(t, func() {
		// 清空可能来自宿主环境的干扰变量。
		for _, k := range []string{"PORT", "JWT_SECRET", "ADMIN_USERNAME", "ADMIN_PASSWORD",
			"ENCRYPT_KEY", "SKOPEO_BIN", "TASK_CONCURRENCY", "TASK_RETENTION_DAYS", "LOGIN_LOG_RETENTION_DAYS"} {
			t.Setenv(k, "")
		}
		dbDir := t.TempDir()
		t.Setenv("DB_PATH", filepath.Join(dbDir, "app.db"))

		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.Port != "8080" {
			t.Errorf("默认端口 = %q, want 8080", c.Port)
		}
		if c.DBPath != filepath.Join(dbDir, "app.db") {
			t.Errorf("DBPath = %q", c.DBPath)
		}
		if c.JWTSecret == "" {
			t.Error("未设 JWT_SECRET 时应自动生成随机密钥")
		}
		if len(c.JWTSecret) != 64 { // 32 字节 hex
			t.Errorf("随机密钥长度 = %d, want 64(hex)", len(c.JWTSecret))
		}
		if c.EncryptKey != "" {
			t.Errorf("默认 EncryptKey 应为空,得到 %q", c.EncryptKey)
		}
		if c.SkopeoBin != "skopeo" {
			t.Errorf("默认 SkopeoBin = %q", c.SkopeoBin)
		}
		if c.TaskConcurrency != 1 {
			t.Errorf("默认并发 = %d, want 1", c.TaskConcurrency)
		}
		if c.TaskRetentionDays != 30 {
			t.Errorf("默认任务保留 = %d, want 30", c.TaskRetentionDays)
		}
		if c.LoginLogRetentionDays != 180 {
			t.Errorf("默认日志保留 = %d, want 180", c.LoginLogRetentionDays)
		}
		// 目录应被创建。
		if _, err := os.Stat(dbDir); err != nil {
			t.Errorf("数据库目录未被创建: %v", err)
		}
	})
}

func TestLoadOverrides(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
		t.Setenv("PORT", "9090")
		t.Setenv("JWT_SECRET", "my-secret")
		t.Setenv("ADMIN_USERNAME", " root ") // 应被 TrimSpace
		t.Setenv("ADMIN_PASSWORD", "p@ss")
		t.Setenv("ENCRYPT_KEY", "enc-key")
		t.Setenv("SKOPEO_BIN", "/usr/local/bin/skopeo2")
		t.Setenv("TASK_CONCURRENCY", "4")
		t.Setenv("TASK_RETENTION_DAYS", "0")
		t.Setenv("LOGIN_LOG_RETENTION_DAYS", "-5") // 负数归 0(永久保留)

		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.Port != "9090" || c.JWTSecret != "my-secret" || c.AdminUsername != "root" ||
			c.AdminPassword != "p@ss" || c.EncryptKey != "enc-key" || c.SkopeoBin != "/usr/local/bin/skopeo2" {
			t.Errorf("环境变量覆盖结果不对: %+v", c)
		}
		if c.TaskConcurrency != 4 {
			t.Errorf("并发 = %d, want 4", c.TaskConcurrency)
		}
		if c.TaskRetentionDays != 0 || c.LoginLogRetentionDays != 0 {
			t.Errorf("保留天数归零失败: %d/%d", c.TaskRetentionDays, c.LoginLogRetentionDays)
		}
	})
}

func TestLoadConcurrencyClamp(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
		t.Setenv("TASK_CONCURRENCY", "0") // <1 归 1
		c, _ := Load()
		if c.TaskConcurrency != 1 {
			t.Errorf("并发 0 应归 1, got %d", c.TaskConcurrency)
		}

		t.Setenv("TASK_CONCURRENCY", "99") // >8 封顶 8
		c, _ = Load()
		if c.TaskConcurrency != 8 {
			t.Errorf("并发 99 应封顶 8, got %d", c.TaskConcurrency)
		}
	})
}

func TestLoadDBDirFallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("依赖 Windows 非法路径字符")
	}
	withDefaults(t, func() {
		// 用含 Windows 非法字符 '|' 的目录名触发 MkdirAll 失败。
		t.Setenv("DB_PATH", `C:\can|not\exist\a.db`)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.DBPath == "" || c.DBPath == `C:\can|not\exist\a.db` {
			t.Errorf("目录不可写时应回退到本地 ./data, got %q", c.DBPath)
		}
		if !strings.HasPrefix(filepath.ToSlash(c.DBPath), "data/") {
			t.Errorf("回退路径应在 ./data 下, got %q", c.DBPath)
		}
		// 清理回退产生的目录。
		_ = os.RemoveAll("data")
	})
}

func TestEnvIntInvalid(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
		t.Setenv("TASK_CONCURRENCY", "not-a-number") // 非法 → 默认 1
		t.Setenv("TASK_RETENTION_DAYS", " 7 ")       // 带空白 → TrimSpace 后合法
		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.TaskConcurrency != 1 {
			t.Errorf("非法并发应回退默认 1, got %d", c.TaskConcurrency)
		}
		if c.TaskRetentionDays != 7 {
			t.Errorf("带空白数字应解析为 7, got %d", c.TaskRetentionDays)
		}
	})
}

func TestRandomSecretUnique(t *testing.T) {
	// 连续两次生成的密钥应不同(随机性冒烟)。
	if randomSecret() == randomSecret() {
		t.Fatal("两次随机密钥不应相同")
	}
}
