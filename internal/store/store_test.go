package store

import (
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
)

// closeDBAt 关闭指定连接池(Windows 下不关会导致 TempDir 清理失败;
// 必须捕获具体句柄,cleanup 时全局 DB 可能已被替换)。
func closeDBAt(db *gorm.DB) {
	if db != nil {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
}

// reinit 对当前 DBPath 重新 Init 并注册关闭(供需要二次 Init 的测试用)。
func reinit(t *testing.T) *gorm.DB {
	t.Helper()
	if err := Init(); err != nil {
		t.Fatalf("重新 Init 失败: %v", err)
	}
	db := DB
	t.Cleanup(func() { closeDBAt(db) })
	return db
}

// setupDB 在临时目录初始化全新数据库并返回句柄(store 包内部测试不能
// 依赖 testutil——那会形成 store→testutil→store 导入环)。
func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	config.AppConfig.DBPath = filepath.Join(t.TempDir(), "test.db")
	config.AppConfig.AdminUsername = "admin"
	config.AppConfig.AdminPassword = "Admin@123"
	if err := Init(); err != nil {
		t.Fatalf("初始化测试数据库失败: %v", err)
	}
	db := DB
	t.Cleanup(func() { closeDBAt(db) })
	return db
}

func TestInitSeedsAdminAndSettings(t *testing.T) {
	db := setupDB(t)

	// 默认 admin 已 seed。
	var users []model.User
	if err := db.Find(&users).Error; err != nil {
		t.Fatalf("查询 users 失败: %v", err)
	}
	if len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("默认 admin 未正确 seed: %+v", users)
	}
	if !auth.CheckPassword(users[0].PasswordHash, "Admin@123") {
		t.Fatal("seed 的 admin 密码应与配置一致")
	}

	// 默认系统设置已 seed。
	var setting model.SystemSetting
	if err := db.Where("`key` = ?", model.SettingKeyDefaultArch).First(&setting).Error; err != nil {
		t.Fatalf("默认设置未 seed: %v", err)
	}
	if setting.Value != "amd64" {
		t.Errorf("default_arch = %q, want amd64", setting.Value)
	}

	// 对同一库重复 Init:幂等,不重复 seed。
	reinit(t)
	var count int64
	db.Model(&model.User{}).Count(&count)
	if count != 1 {
		t.Errorf("重复 Init 后 users = %d, want 1", count)
	}
}

func TestInitCustomAdmin(t *testing.T) {
	setupDB(t) // 先建库 seed admin。
	// 换自定义账号密码重新初始化(库已有用户,不应重新 seed)。
	config.AppConfig.AdminUsername = "root2"
	config.AppConfig.AdminPassword = "Root2@456"
	reinit(t)
	var users []model.User
	DB.Find(&users)
	if len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("已有用户时不应重新 seed: %+v", users)
	}
}

func TestInitEmptyDBWithCustomAdmin(t *testing.T) {
	// 全新临时库:以自定义账号 seed(通过重设 DBPath 触发全新 Init)。
	config.AppConfig.DBPath = filepath.Join(t.TempDir(), "fresh.db")
	config.AppConfig.AdminUsername = "ops"
	config.AppConfig.AdminPassword = "Ops@12345"
	reinit(t)
	var users []model.User
	DB.Find(&users)
	if len(users) != 1 || users[0].Username != "ops" {
		t.Fatalf("自定义 admin 未 seed: %+v", users)
	}
	if !auth.CheckPassword(users[0].PasswordHash, "Ops@12345") {
		t.Fatal("自定义密码应生效")
	}
}

func TestMaskPassword(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a", "*"},
		{"ab", "**"},
		{"abc", "a*c"},
		{"Admin@123", "A*******3"},
	}
	for _, c := range cases {
		if got := maskPassword(c.in); got != c.want {
			t.Errorf("maskPassword(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRecordLoginLog(t *testing.T) {
	db := setupDB(t)
	RecordLoginLog(1, "admin", "127.0.0.1", "ua-test", model.LoginStatusSuccess, "登录成功")

	var logs []model.LoginLog
	if err := db.Order("id DESC").Find(&logs).Error; err != nil || len(logs) != 1 {
		t.Fatalf("登录日志未写入: %v n=%d", err, len(logs))
	}
	l := logs[0]
	if l.UserID != 1 || l.Username != "admin" || l.IP != "127.0.0.1" ||
		l.UserAgent != "ua-test" || l.Status != model.LoginStatusSuccess {
		t.Errorf("日志字段不对: %+v", l)
	}

	// DB 为 nil 时静默跳过(服务未初始化的防御分支)。
	saved := DB
	DB = nil
	RecordLoginLog(1, "x", "", "", "success", "") // 不应 panic
	DB = saved
}

func TestRecoverChartUploads(t *testing.T) {
	db := setupDB(t)
	seed := []model.ChartUpload{
		{Status: model.TaskStatusPending, FileName: "p.tgz"},
		{Status: model.TaskStatusRunning, FileName: "r.tgz"},
		{Status: model.TaskStatusSuccess, FileName: "s.tgz"},
		{Status: model.TaskStatusFailed, FileName: "f.tgz"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed 失败: %v", err)
		}
	}

	RecoverChartUploads()

	var after []model.ChartUpload
	db.Order("id ASC").Find(&after)
	want := map[string]string{"p.tgz": "failed", "r.tgz": "failed", "s.tgz": "success", "f.tgz": "failed"}
	for _, u := range after {
		if u.Status != want[u.FileName] {
			t.Errorf("%s 状态 = %s, want %s", u.FileName, u.Status, want[u.FileName])
		}
		if u.Status == "failed" && u.FileName != "f.tgz" {
			if u.Error == "" || u.FinishedAt == nil {
				t.Errorf("%s 应写入错误与完成时间", u.FileName)
			}
		}
	}

	// DB 为 nil 时直接返回。
	saved := DB
	DB = nil
	RecoverChartUploads() // 不应 panic
	DB = saved
}

// TestSeedBcryptCost 验证 seed 使用的哈希可被 bcrypt 校验(链路冒烟)。
func TestSeedBcryptCost(t *testing.T) {
	setupDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt 失败: %v", err)
	}
	if cost, err := bcrypt.Cost(hash); err != nil || cost < bcrypt.MinCost {
		t.Errorf("bcrypt cost 异常: %d %v", cost, err)
	}
}

// TestInitOpenFailure DBPath 指向目录时 Open 必然失败,应返回错误而非 panic。
func TestInitOpenFailure(t *testing.T) {
	dir := t.TempDir()
	config.AppConfig.DBPath = dir // 目录本身不是可打开的数据库文件
	if err := Init(); err == nil {
		t.Fatal("DBPath 为目录时 Init 应失败")
	}
}

// TestRecordLoginLogWriteFailure 表被删后写入失败仅打日志,不影响调用方。
func TestRecordLoginLogWriteFailure(t *testing.T) {
	db := setupDB(t)
	if err := db.Migrator().DropTable(&model.LoginLog{}); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	RecordLoginLog(1, "admin", "127.0.0.1", "ua", model.LoginStatusFailed, "密码错误") // 不应 panic
}
