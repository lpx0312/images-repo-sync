package config

import (
	"fmt"
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

// ---- 以下为 B 类边界测试(当前必须通过,GREEN) ----
// 注:A2「DB 目录存在但只读」的回退测试在 Windows 上无法用只读目录阻止建文件
// (平台差异,需生产代码引入可注入的探测函数后才可测),按任务约定跳过 A 测试。

// TestLoadDBPathUnderExistingFile GREEN: DB_PATH 指向"已存在普通文件"下的子路径,
// MkdirAll 必然失败,应回退到 ./data/<basename> 而不是返回错误。
func TestLoadDBPathUnderExistingFile(t *testing.T) {
	withDefaults(t, func() {
		existing := filepath.Join(t.TempDir(), "occupied")
		if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
			t.Fatalf("构造已存在的普通文件失败: %v", err)
		}
		t.Setenv("DB_PATH", filepath.Join(existing, "app.db"))

		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		want := filepath.Join("data", "app.db")
		if c.DBPath != want {
			t.Errorf("目录创建失败时应回退到 %q, got %q", want, c.DBPath)
		}
		if fi, err := os.Stat("data"); err != nil || !fi.IsDir() {
			t.Errorf("回退目录 ./data 应被创建: %v", err)
		}
		// 清理回退在测试 cwd(internal/config)下产生的目录。
		_ = os.RemoveAll("data")
	})
}

// TestLoadDBPathBareFilename GREEN: DB_PATH 为裸文件名(Dir=".")时不触发建目录逻辑,
// DBPath 原样保留,且 Load 不应越权创建数据库文件本身(建库是 store.Init 的职责)。
func TestLoadDBPathBareFilename(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", "app.db")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.DBPath != "app.db" {
			t.Errorf("裸文件名应原样保留, got %q", c.DBPath)
		}
		if _, err := os.Stat("app.db"); !os.IsNotExist(err) {
			t.Errorf("Load 不应创建数据库文件本身: stat err = %v", err)
		}
		_ = os.RemoveAll("app.db")
	})
}

// TestLoadJWTSecretRandomPerLoad GREEN: JWT_SECRET 为空串时每次"冷启动"都生成
// 新的非空随机密钥;子测试各自先清空单例 JWTSecret,模拟两次独立进程启动。
func TestLoadJWTSecretRandomPerLoad(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("JWT_SECRET", "")
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "a.db"))

		secrets := make([]string, 2)
		for i := range secrets {
			t.Run(fmt.Sprintf("cold_start_%d", i+1), func(t *testing.T) {
				AppConfig.JWTSecret = "" // 模拟冷启动:单例尚未持有密钥
				c, err := Load()
				if err != nil {
					t.Fatalf("Load 失败: %v", err)
				}
				if c.JWTSecret == "" {
					t.Fatal("未设 JWT_SECRET 时应生成非空随机密钥")
				}
				if len(c.JWTSecret) != 64 { // 32 字节 hex
					t.Errorf("随机密钥长度 = %d, want 64", len(c.JWTSecret))
				}
				secrets[i] = c.JWTSecret
			})
		}
		if secrets[0] == secrets[1] {
			t.Errorf("两次冷启动生成的密钥不应相同: %q", secrets[0])
		}
	})
}

// TestLoadPortEmptyDefaults8080 GREEN: PORT 为空串时不覆盖,保持默认 8080。
func TestLoadPortEmptyDefaults8080(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
		t.Setenv("PORT", "")
		AppConfig.Port = "8080" // 单例先回到默认值,验证空串不会改动它

		c, err := Load()
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if c.Port != "8080" {
			t.Errorf("PORT 空串应保持默认 8080, got %q", c.Port)
		}
	})
}

// TestEnvIntEdgeCases GREEN: envInt 解析边界。现有测试只覆盖 0/99,
// 这里补全符号前缀、负数、临界值、非法值与空串的回退行为。
func TestEnvIntEdgeCases(t *testing.T) {
	const def = 7 // 任意默认值,区分"解析成功"与"回退"
	cases := []struct {
		name string
		val  string
		want int
	}{
		{"plus_sign", "+2", 2},  // strconv.Atoi 接受 '+'
		{"negative", "-3", -3},  // 负数原样返回(clamp 在 Load 层做)
		{"boundary_8", "8", 8},  // 并发上限边界值
		{"over_8", "9", 9},      // 超上限,envInt 本身不封顶
		{"invalid", "abc", def}, // 非法 → 回退默认
		{"empty", "", def},      // 空串 → 回退默认
		{"spaces", " 9 ", 9},    // 首尾空白 TrimSpace 后合法
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_ENV_INT_EDGE", tc.val)
			if got := envInt("TEST_ENV_INT_EDGE", def); got != tc.want {
				t.Errorf("envInt(%q) = %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}

// TestLoadTaskConcurrencyBoundary GREEN: TASK_CONCURRENCY 0 → 默认 1;9 → clamp 8。
func TestLoadTaskConcurrencyBoundary(t *testing.T) {
	withDefaults(t, func() {
		t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
		for _, tc := range []struct {
			val  string
			want int
		}{
			{"0", 1},
			{"9", 8},
		} {
			t.Run("concurrency_"+tc.val, func(t *testing.T) {
				t.Setenv("TASK_CONCURRENCY", tc.val)
				c, err := Load()
				if err != nil {
					t.Fatalf("Load 失败: %v", err)
				}
				if c.TaskConcurrency != tc.want {
					t.Errorf("TASK_CONCURRENCY=%s 应得到 %d, got %d", tc.val, tc.want, c.TaskConcurrency)
				}
			})
		}
	})
}
