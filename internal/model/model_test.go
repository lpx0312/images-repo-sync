package model

import "testing"

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
