package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 持有应用运行期配置。所有字段都来自环境变量,带合理默认值。
type Config struct {
	Port                  string
	DBPath                string
	JWTSecret             string
	AdminUsername         string
	AdminPassword         string
	EncryptKey            string
	SkopeoBin             string
	TaskConcurrency       int // 任务并发 worker 数(默认 1 串行)
	TaskRetentionDays     int // 已结束任务保留天数;0 = 永久保留
	LoginLogRetentionDays int // 登录日志保留天数;0 = 永久保留
}

// AppConfig 是全局配置单例,在 main 中通过 Load() 初始化。
var AppConfig = &Config{
	Port:          "8080",
	DBPath:        "/data/images-repo-sync.db",
	AdminUsername: "admin",
	AdminPassword: "admin123",
	SkopeoBin:     "skopeo",
}

// Load 从环境变量读取配置并填充 AppConfig。
//
// 行为说明:
//   - DBPath:默认 /data/images-repo-sync.db;若所在目录不存在或不可写,回退到可执行文件旁的 ./data/。
//   - JWTSecret:为空时生成一次性随机值(重启后旧 token 失效),并打印告警。
//   - AdminUsername/AdminPassword:仅在首次 users 表为空时用于 seed。
//   - EncryptKey:用于加密 registry 密码;为空表示明文存储(UI 会告警)。
func Load() (*Config, error) {
	c := AppConfig

	if v := os.Getenv("PORT"); v != "" {
		c.Port = v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		c.DBPath = v
	}
	// 确保数据库目录存在且真正可写,否则建库会失败。
	if dir := filepath.Dir(c.DBPath); dir != "" && dir != "." {
		if err := ensureDirWritable(dir); err != nil {
			// 目录不存在或不可写时回退到本地 ./data,保证开发体验。
			fallback := filepath.Join("data", filepath.Base(c.DBPath))
			if fbErr := os.MkdirAll(filepath.Dir(fallback), 0o755); fbErr != nil {
				log.Printf("[config] 创建回退数据库目录 %s 失败: %v", filepath.Dir(fallback), fbErr)
			}
			fmt.Printf("[config] 无法使用数据库目录 %s(%v),回退到 %s\n", dir, err, fallback)
			c.DBPath = fallback
		}
	}

	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.JWTSecret = v
	} else if c.JWTSecret == "" {
		c.JWTSecret = randomSecret()
		fmt.Println("[config] 警告: 未设置 JWT_SECRET,本次启动随机生成,重启后所有登录将失效。生产环境请显式设置 JWT_SECRET。")
	}

	if v := os.Getenv("ADMIN_USERNAME"); v != "" {
		c.AdminUsername = strings.TrimSpace(v)
	}
	if v := os.Getenv("ADMIN_PASSWORD"); v != "" {
		c.AdminPassword = v
	}
	c.EncryptKey = os.Getenv("ENCRYPT_KEY")
	if c.EncryptKey == "" {
		fmt.Println("[config] 警告: 未设置 ENCRYPT_KEY,registry 密码将以明文存储。生产环境请设置 ENCRYPT_KEY。")
	}

	if v := os.Getenv("SKOPEO_BIN"); v != "" {
		c.SkopeoBin = v
	}

	c.TaskConcurrency = envInt("TASK_CONCURRENCY", 1)
	if c.TaskConcurrency < 1 {
		c.TaskConcurrency = 1
	} else if c.TaskConcurrency > 8 {
		c.TaskConcurrency = 8
	}
	c.TaskRetentionDays = envInt("TASK_RETENTION_DAYS", 30)
	if c.TaskRetentionDays < 0 {
		c.TaskRetentionDays = 0
	}
	c.LoginLogRetentionDays = envInt("LOGIN_LOG_RETENTION_DAYS", 180)
	if c.LoginLogRetentionDays < 0 {
		c.LoginLogRetentionDays = 0
	}

	return c, nil
}

// envInt 读取整型环境变量;未设置或非法时返回默认值。
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
		fmt.Printf("[config] 忽略无效的 %s 值,使用默认值 %d\n", name, def)
	}
	return def
}

// ensureDirWritable 确保目录存在且真正可写:MkdirAll 成功后再写探测文件并删除。
// 防止目录存在但只读/权限受限时 MkdirAll 静默通过、拖到建库阶段才失败。
func ensureDirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".irs-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		return err
	}
	return os.Remove(probe)
}

// randomSecret 生成 32 字节随机十六进制字符串作为临时 JWT 密钥。
func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 熵源故障时实际不可达;绝不能静默退回公开常量签发 token,
		// 宁愿快速失败(fail-fast)让问题在启动阶段立刻暴露。
		panic(fmt.Sprintf("[config] 生成 JWT 随机密钥失败(系统熵源故障): %v", err))
	}
	return hex.EncodeToString(b)
}
