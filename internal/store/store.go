package store

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
)

// DB 是全局数据库句柄,在 Init 中初始化。
var DB *gorm.DB

// Init 打开 SQLite 数据库,执行 AutoMigrate 并在首次启动时 seed 默认 admin。
//
// 使用 glebarez/sqlite(modernc 纯 Go 驱动),无需 CGO,Docker 静态构建更干净。
func Init() error {
	cfg := config.AppConfig
	// _pragma: foreign_keys 开外键约束; journal_mode(WAL) 允许读写并发(SSE 长连接场景关键);
	// busy_timeout 提到 10s,避免 worker 写与 API 读争用导致 database is locked。
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)", cfg.DBPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 并发性能调优。WAL 模式下可允许多个读连接 + 单写。
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}
	// WAL 模式支持多读单写,适当放开读连接数;写仍由 SQLite 串行化(busy_timeout 兜底)。
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(0)

	if err := db.AutoMigrate(
		&model.User{},
		&model.Registry{},
		&model.SyncTask{},
		&model.SyncTaskItem{},
		&model.LoginLog{},
		&model.SystemSetting{},
	); err != nil {
		return fmt.Errorf("自动迁移失败: %w", err)
	}

	DB = db

	if err := seedDefaultSettings(db); err != nil {
		return fmt.Errorf("初始化系统设置失败: %w", err)
	}
	if err := seedDefaultAdmin(db); err != nil {
		return fmt.Errorf("初始化默认管理员失败: %w", err)
	}
	return nil
}

// seedDefaultSettings 给系统配置表写入默认值(仅当键不存在时)。
func seedDefaultSettings(db *gorm.DB) error {
	defaults := map[string]string{
		model.SettingKeyDefaultArch: "amd64", // 默认同步架构
	}
	for k, v := range defaults {
		var count int64
		db.Model(&model.SystemSetting{}).Where("`key` = ?", k).Count(&count)
		if count == 0 {
			db.Create(&model.SystemSetting{Key: k, Value: v})
		}
	}
	return nil
}

// seedDefaultAdmin 在 users 表为空时创建默认 admin 账号。
// 用户名/密码来自 config.AppConfig(可被环境变量覆盖)。
func seedDefaultAdmin(db *gorm.DB) error {
	var count int64
	if err := db.Model(&model.User{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(config.AppConfig.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	admin := model.User{
		Username:     config.AppConfig.AdminUsername,
		PasswordHash: string(hash),
		Status:       model.UserStatusActive,
	}
	if err := db.Create(&admin).Error; err != nil {
		return err
	}
	log.Printf("[store] 默认管理员账号已创建: %s (密码: %s)。请及时登录并修改默认密码!",
		admin.Username, maskPassword(config.AppConfig.AdminPassword))
	return nil
}

// maskPassword 仅用于日志脱敏,保留首尾字符。
func maskPassword(p string) string {
	if len(p) <= 2 {
		return strings.Repeat("*", len(p))
	}
	return string(p[0]) + strings.Repeat("*", len(p)-2) + string(p[len(p)-1])
}

// RecordLoginLog 写一条登录审计日志。失败仅打印,不影响登录主流程。
func RecordLoginLog(userID uint, username, ip, ua, status, message string) {
	if DB == nil {
		return
	}
	entry := model.LoginLog{
		UserID:    userID,
		Username:  username,
		IP:        ip,
		UserAgent: ua,
		Status:    status,
		Message:   message,
		CreatedAt: time.Now(),
	}
	if err := DB.Create(&entry).Error; err != nil {
		log.Printf("[store] 写登录日志失败: %v", err)
	}
}
