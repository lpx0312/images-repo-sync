package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestTableNames GORM 约定的表名(与 store.Init 的迁移及 SQL 语句强相关)。
func TestTableNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{User{}.TableName(), "users"},
		{Registry{}.TableName(), "registries"},
		{SyncTask{}.TableName(), "sync_tasks"},
		{SyncTaskItem{}.TableName(), "sync_task_items"},
		{ChartRepo{}.TableName(), "chart_repos"},
		{ChartUpload{}.TableName(), "chart_uploads"},
		{LoginLog{}.TableName(), "login_logs"},
		{SystemSetting{}.TableName(), "system_settings"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("TableName = %q, want %q", c.got, c.want)
		}
	}
}

// TestConstantValues 常量被 API/前端/CLI 共享,值变更即是破坏性改动,锁定校验。
func TestConstantValues(t *testing.T) {
	pairs := map[string]string{
		TaskStatusPending:        "pending",
		TaskStatusRunning:        "running",
		TaskStatusSuccess:        "success",
		TaskStatusFailed:         "failed",
		TaskStatusCanceled:       "canceled",
		ModeFlat:                 "flat",
		ModePreservePath:         "preserve_path",
		ModeReplaceHost:          "replace_host",
		RegistryTypeGeneric:      "generic",
		RegistryTypeHarbor:       "harbor",
		RegistryTypeDockerHub:    "dockerhub",
		RegistryTypeACR:          "acr",
		RegistryTypeSWR:          "swr",
		ChartRepoTypeOCI:         "oci",
		ChartRepoTypeChartMuseum: "chartmuseum",
		SettingKeyDefaultArch:    "default_arch",
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("常量值变化: got %q, want %q", got, want)
		}
	}
	if UserStatusActive != "active" || UserStatusDisabled != "disabled" {
		t.Error("用户状态常量异常")
	}
	if LoginStatusSuccess != "success" || LoginStatusFailed != "failed" {
		t.Error("登录状态常量异常")
	}
}

// newTestDB 打开 sqlite 内存库(与 store.Init 同款 glebarez 纯 Go 驱动),
// 供迁移/索引断言使用。:memory: 模式下每个连接是独立库,锁死单连接。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

// ---- A1 回归测试(断言修复后的期望行为,缺陷已修复) ----

// 回归锁定(已修复): A1 LoginLog.CreatedAt 缺少索引 idx_login_logs_created_at,
// 审计列表按时间倒序分页时全表扫描。补 `gorm:"index:idx_login_logs_created_at"` 后转 GREEN。
func TestLoginLogCreatedAtIndex(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&LoginLog{}); err != nil {
		t.Fatalf("AutoMigrate LoginLog 失败: %v", err)
	}
	if !db.Migrator().HasIndex(&LoginLog{}, "idx_login_logs_created_at") {
		t.Error("login_logs 应存在索引 idx_login_logs_created_at(CreatedAt),当前缺失")
	}
}

// 回归锁定(已修复): A1 SyncTask 缺少 (status, created_at) 复合索引 idx_sync_tasks_status_created_at,
// 任务列表"按状态过滤 + 时间倒序"查询走全表扫描。补复合索引后转 GREEN。
func TestSyncTaskStatusCreatedCompositeIndex(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&SyncTask{}); err != nil {
		t.Fatalf("AutoMigrate SyncTask 失败: %v", err)
	}
	if !db.Migrator().HasIndex(&SyncTask{}, "idx_sync_tasks_status_created_at") {
		t.Error("sync_tasks 应存在复合索引 idx_sync_tasks_status_created_at(status, created_at),当前缺失")
	}
}

// ---- B 类边界测试(当前必须通过,GREEN) ----

// TestAutoMigrateAllTables GREEN: 与 store.Init 相同的 8 张表 AutoMigrate 全部成功。
func TestAutoMigrateAllTables(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(
		&User{}, &Registry{}, &SyncTask{}, &SyncTaskItem{},
		&LoginLog{}, &SystemSetting{}, &ChartRepo{}, &ChartUpload{},
	); err != nil {
		t.Fatalf("AutoMigrate 应全部成功, got error: %v", err)
	}
	for _, tbl := range []string{
		"users", "registries", "sync_tasks", "sync_task_items",
		"login_logs", "system_settings", "chart_repos", "chart_uploads",
	} {
		if !db.Migrator().HasTable(tbl) {
			t.Errorf("表 %q 应已创建", tbl)
		}
	}
}

// TestItemStatusConstantValues GREEN: 补现有 TestConstantValues 未覆盖的
// 任务明细状态常量值锁定。
func TestItemStatusConstantValues(t *testing.T) {
	pairs := map[string]string{
		ItemStatusPending: "pending",
		ItemStatusRunning: "running",
		ItemStatusSuccess: "success",
		ItemStatusFailed:  "failed",
		ItemStatusSkipped: "skipped",
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("明细状态常量值变化: got %q, want %q", got, want)
		}
	}
}

// TestConstantsPersistRoundTrip GREEN: 常量写入数据库后原样读回,
// 锁定"常量字符串值 ↔ DB 实际存储"一致(varchar 长度不截断、无大小写漂移)。
func TestConstantsPersistRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&SyncTask{}, &SyncTaskItem{}, &Registry{}, &ChartRepo{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}

	// 任务状态全集 × 同步模式全集
	modes := []string{ModeFlat, ModePreservePath, ModeReplaceHost}
	for i, st := range []string{TaskStatusPending, TaskStatusRunning, TaskStatusSuccess, TaskStatusFailed, TaskStatusCanceled} {
		mode := modes[i%len(modes)]
		row := SyncTask{SourceRegistryID: 1, TargetRegistryID: 2, Mode: mode, Status: st}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("写入 SyncTask(%s/%s) 失败: %v", st, mode, err)
		}
		var got SyncTask
		if err := db.First(&got, row.ID).Error; err != nil {
			t.Fatalf("读回 SyncTask 失败: %v", err)
		}
		if got.Status != st {
			t.Errorf("任务状态 round trip: 写入 %q, 读回 %q", st, got.Status)
		}
		if got.Mode != mode {
			t.Errorf("同步模式 round trip: 写入 %q, 读回 %q", mode, got.Mode)
		}
	}

	// 明细状态全集
	for _, st := range []string{ItemStatusPending, ItemStatusRunning, ItemStatusSuccess, ItemStatusFailed, ItemStatusSkipped} {
		row := SyncTaskItem{TaskID: 1, SourceRef: "docker.io/library/nginx:1", TargetRef: "reg.local/nginx:1", Status: st}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("写入 SyncTaskItem(%s) 失败: %v", st, err)
		}
		var got SyncTaskItem
		if err := db.First(&got, row.ID).Error; err != nil {
			t.Fatalf("读回 SyncTaskItem 失败: %v", err)
		}
		if got.Status != st {
			t.Errorf("明细状态 round trip: 写入 %q, 读回 %q", st, got.Status)
		}
	}

	// 仓库类型全集(registries.type)
	for _, typ := range []string{RegistryTypeGeneric, RegistryTypeHarbor, RegistryTypeDockerHub, RegistryTypeACR, RegistryTypeSWR} {
		row := Registry{Name: "reg-" + typ, Host: "reg.local", Type: typ}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("写入 Registry(%s) 失败: %v", typ, err)
		}
		var got Registry
		if err := db.First(&got, row.ID).Error; err != nil {
			t.Fatalf("读回 Registry 失败: %v", err)
		}
		if got.Type != typ {
			t.Errorf("仓库类型 round trip: 写入 %q, 读回 %q", typ, got.Type)
		}
	}

	// Chart 仓库类型全集(chart_repos.type)
	for _, typ := range []string{ChartRepoTypeOCI, ChartRepoTypeChartMuseum} {
		row := ChartRepo{Name: "chart-" + typ, Type: typ, Host: "chart.local"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("写入 ChartRepo(%s) 失败: %v", typ, err)
		}
		var got ChartRepo
		if err := db.First(&got, row.ID).Error; err != nil {
			t.Fatalf("读回 ChartRepo 失败: %v", err)
		}
		if got.Type != typ {
			t.Errorf("Chart 仓库类型 round trip: 写入 %q, 读回 %q", typ, got.Type)
		}
	}
}
