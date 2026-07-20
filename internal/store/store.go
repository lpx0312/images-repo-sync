package store

import (
	"fmt"
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
	// _pragma 配置开启外键约束,并使用 BUSY_TIMEOUT 避免并发写冲突。
	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", cfg.DBPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 并发性能调优。
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // SQLite 写串行,单连接避免锁冲突
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := db.AutoMigrate(
		&model.User{},
		&model.Registry{},
		&model.SyncTask{},
		&model.SyncTaskItem{},
		&model.LoginLog{},
	); err != nil {
		return fmt.Errorf("自动迁移失败: %w", err)
	}

	DB = db

	if err := seedDefaultAdmin(db); err != nil {
		return fmt.Errorf("初始化默认管理员失败: %w", err)
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
	fmt.Printf("[store] 默认管理员账号已创建: %s (密码: %s)。请及时登录并修改默认密码!\n",
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
		fmt.Printf("[store] 写登录日志失败: %v\n", err)
	}
}
