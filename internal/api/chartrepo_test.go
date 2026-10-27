package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
)

func TestChartRepoCRUD(t *testing.T) {
	r, db, token := newAuthed(t)

	// Create OCI。
	var created struct {
		ID          uint   `json:"id"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Host        string `json:"host"`
		Project     string `json:"project"`
		HasPassword bool   `json:"has_password"`
	}
	w := do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{
		"name": "charts-prod", "type": "oci", "host": "  harbor.example.com/  ",
		"project": " /datacenter-chart/ ", "username": "u", "password": "p",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &created)
	if created.Name != "charts-prod" || created.Type != "oci" ||
		created.Host != "harbor.example.com" || created.Project != "datacenter-chart" {
		t.Errorf("创建结果: %+v", created)
	}

	// OCI 缺 project 400。
	if w := do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{
		"name": "no-proj", "type": "oci", "host": "h",
	}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "chart 项目") {
		t.Errorf("OCI 缺项目应 400: %d %s", w.Code, w.Body.String())
	}
	// 非法 type 归 oci。
	w = do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{
		"name": "weird-type", "type": "ftp", "host": "h", "project": "p",
	})
	var weird struct {
		Type string `json:"type"`
	}
	decode(t, w, &weird)
	if weird.Type != model.ChartRepoTypeOCI {
		t.Errorf("非法 type 应归 oci: %+v", weird)
	}
	// chartmuseum 无 project 允许。
	if w := do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{
		"name": "cm", "type": "chartmuseum", "host": "cm.example.com",
	}); w.Code != http.StatusOK {
		t.Errorf("chartmuseum 创建失败: %d", w.Code)
	}
	// 缺字段 400。
	if w := do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{"name": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("缺字段应 400: %d", w.Code)
	}
	// 重名 400。
	if w := do(r, token, http.MethodPost, "/api/chart-repos", map[string]any{
		"name": "cm", "type": "chartmuseum", "host": "h2",
	}); w.Code != http.StatusBadRequest {
		t.Errorf("重名应 400: %d", w.Code)
	}

	// List。
	var list []map[string]any
	decode(t, do(r, token, http.MethodGet, "/api/chart-repos", nil), &list)
	if len(list) != 3 {
		t.Fatalf("list = %d", len(list))
	}

	// Update:改名保留密码。
	w = do(r, token, http.MethodPut, "/api/chart-repos/1", map[string]any{
		"name": "charts-prod-2", "type": "oci", "host": "harbor.example.com", "project": "p2",
	})
	var updated struct {
		Name        string `json:"name"`
		Project     string `json:"project"`
		HasPassword bool   `json:"has_password"`
	}
	decode(t, w, &updated)
	if updated.Name != "charts-prod-2" || updated.Project != "p2" || !updated.HasPassword {
		t.Errorf("update: %+v", updated)
	}
	// Update 不存在 404。
	if w := do(r, token, http.MethodPut, "/api/chart-repos/999", map[string]any{"name": "n", "type": "oci", "host": "h", "project": "p"}); w.Code != 404 {
		t.Errorf("更新不存在应 404: %d", w.Code)
	}

	// Delete。
	if w := do(r, token, http.MethodDelete, "/api/chart-repos/3", nil); w.Code != http.StatusOK {
		t.Errorf("删除失败: %d", w.Code)
	}
	var count int64
	db.Model(&model.ChartRepo{}).Count(&count)
	if count != 2 {
		t.Errorf("删除后 = %d", count)
	}

	// 无 token 401。
	if w := do(r, "", http.MethodGet, "/api/chart-repos", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("无 token 应 401: %d", w.Code)
	}
}

func TestChartRepoTestEndpoint(t *testing.T) {
	r, db, token := newAuthed(t)
	// host 指向 httptest 的 /v2/ 200 服务(ChartRepo host 保留 http:// 前缀)。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	repoID := seedChartRepo(t, db, "probe-target", "oci", ts.URL, "proj")

	var res struct {
		OK bool `json:"ok"`
	}
	w := do(r, token, http.MethodPost, "/api/chart-repos/"+jsonInt(int64(repoID))+"/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("test 应 200: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &res)
	if !res.OK {
		t.Fatalf("可达 OCI 仓库应 ok: %s", w.Body.String())
	}

	// 不存在 404。
	if w := do(r, token, http.MethodPost, "/api/chart-repos/999/test", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
}
