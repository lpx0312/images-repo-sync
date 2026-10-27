package api

import (
	"net/http"
	"testing"

	"images-repo-sync/internal/model"
)

func TestSettingsGetAndUpdate(t *testing.T) {
	r, db, token := newAuthed(t)

	// Get:默认值兜底(store.Init 已 seed default_arch=amd64)。
	var m map[string]string
	w := do(r, token, http.MethodGet, "/api/settings", nil)
	decode(t, w, &m)
	if m["default_arch"] != "amd64" {
		t.Fatalf("default_arch = %q", m["default_arch"])
	}

	// 删掉后再 Get:仍回退默认值。
	db.Where("`key` = ?", model.SettingKeyDefaultArch).Delete(&model.SystemSetting{})
	w = do(r, token, http.MethodGet, "/api/settings", nil)
	decode(t, w, &m)
	if m["default_arch"] != "amd64" {
		t.Fatalf("缺失键应回退默认: %v", m)
	}

	// Update 合法值。
	w = do(r, token, http.MethodPut, "/api/settings", map[string]any{"default_arch": "arm64"})
	if w.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", w.Code, w.Body.String())
	}
	w = do(r, token, http.MethodGet, "/api/settings", nil)
	decode(t, w, &m)
	if m["default_arch"] != "arm64" {
		t.Fatalf("更新未生效: %v", m)
	}

	// 非法值 400;空 body(不更新任何键)也 200。
	if w := do(r, token, http.MethodPut, "/api/settings", map[string]any{"default_arch": "i386"}); w.Code != http.StatusBadRequest {
		t.Errorf("非法架构应 400: %d", w.Code)
	}
	if w := do(r, token, http.MethodPut, "/api/settings", map[string]any{}); w.Code != http.StatusOK {
		t.Errorf("空更新应 200: %d", w.Code)
	}

	// 无 token 401。
	if w := do(r, "", http.MethodGet, "/api/settings", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("无 token 应 401: %d", w.Code)
	}
}
