package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
)

func TestRegistryCRUD(t *testing.T) {
	r, db, token := newAuthed(t)

	// Create。
	var created struct {
		ID          uint   `json:"id"`
		Name        string `json:"name"`
		Host        string `json:"host"`
		Type        string `json:"type"`
		HasPassword bool   `json:"has_password"`
		Insecure    bool   `json:"insecure"`
	}
	w := do(r, token, http.MethodPost, "/api/registries", map[string]any{
		"name": "harbor-prod", "host": "  https://harbor.example.com/  ", "type": "harbor",
		"username": " alice ", "password": "secret", "insecure": true, "default_project": " lib ",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &created)
	if created.Name != "harbor-prod" || created.Host != "harbor.example.com" || created.Type != "harbor" {
		t.Errorf("创建结果: %+v", created)
	}
	if !created.HasPassword || !created.Insecure {
		t.Errorf("标志不对: %+v", created)
	}
	// username 应 trim(通过 List 验证)。

	// 缺 name/host 400。
	if w := do(r, token, http.MethodPost, "/api/registries", map[string]any{"name": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("缺 host 应 400: %d", w.Code)
	}
	// host 清洗后为空。
	if w := do(r, token, http.MethodPost, "/api/registries", map[string]any{"name": "x", "host": "https://"}); w.Code != http.StatusBadRequest {
		t.Errorf("空 host 应 400: %d", w.Code)
	}
	// 重名 400。
	if w := do(r, token, http.MethodPost, "/api/registries", map[string]any{"name": "harbor-prod", "host": "h2.example.com"}); w.Code != http.StatusBadRequest {
		t.Errorf("重名应 400: %d", w.Code)
	}

	// List。
	var list []map[string]any
	w = do(r, token, http.MethodGet, "/api/registries", nil)
	decode(t, w, &list)
	if len(list) != 1 || list[0]["username"] != "alice" || list[0]["default_project"] != "lib" {
		t.Fatalf("list = %v", list)
	}

	// Update:改名字,留空密码应保留旧密码。
	w = do(r, token, http.MethodPut, "/api/registries/1", map[string]any{
		"name": "harbor-prod-2", "host": "harbor.example.com", "type": "generic",
	})
	var updated struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		HasPassword bool   `json:"has_password"`
	}
	decode(t, w, &updated)
	if updated.Name != "harbor-prod-2" || updated.Type != "generic" {
		t.Errorf("update 结果: %+v", updated)
	}
	if !updated.HasPassword {
		t.Error("空密码更新应保留旧密码")
	}
	var reg model.Registry
	db.First(&reg, 1)
	if reg.PasswordEnc == "" {
		t.Error("密码被清空了")
	}

	// Update 不存在的 ID。
	if w := do(r, token, http.MethodPut, "/api/registries/999", map[string]any{"name": "n", "host": "h"}); w.Code != 404 {
		t.Errorf("更新不存在应 404: %d", w.Code)
	}

	// Delete。
	if w := do(r, token, http.MethodDelete, "/api/registries/1", nil); w.Code != http.StatusOK {
		t.Errorf("删除失败: %d", w.Code)
	}
	var count int64
	db.Model(&model.Registry{}).Count(&count)
	if count != 0 {
		t.Errorf("删除后 count = %d", count)
	}

	// 无 token 全部 401。
	if w := do(r, "", http.MethodGet, "/api/registries", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("无 token 应 401: %d", w.Code)
	}
}

func TestRegistryTestEndpoint(t *testing.T) {
	r, _, token := newAuthed(t)
	id := createRegistryViaAPI(t, r, token, "test-reg", "http://127.0.0.1:1", "generic")

	// 探测不可达端口:仍返回 200,由 ok 字段表示结果。
	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	w := do(r, token, http.MethodPost, "/api/registries/"+strconv.FormatUint(uint64(id), 10)+"/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("test 接口应 200: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &res)
	if res.OK {
		t.Fatal("不可达仓库 ok 应为 false")
	}
	if !strings.Contains(res.Message, "连接被拒绝") {
		t.Errorf("message = %q", res.Message)
	}

	// 不存在的仓库 404。
	if w := do(r, token, http.MethodPost, "/api/registries/999/test", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
}
