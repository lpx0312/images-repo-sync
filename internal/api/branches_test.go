package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
)

// TestExtractHarborErr Harbor 错误体提取。
func TestExtractHarborErr(t *testing.T) {
	cases := []struct {
		body, want string
	}{
		{`{"errors":[{"code":"X","message":"a"},{"message":"b"}]}`, "a; b"},
		{`{"errors":[]}`, `{"errors":[]}`}, // 空 errors 不算 Harbor 错误体,返回原文
		{`not json`, "not json"},
		{strings.Repeat("x", 200), strings.Repeat("x", 150) + "..."},
	}
	for _, c := range cases {
		if got := extractHarborErr([]byte(c.body)); got != c.want {
			t.Errorf("extractHarborErr(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}

// TestMeUserDeleted token 有效但用户已删除。
func TestMeUserDeleted(t *testing.T) {
	r, db, token := newAuthed(t)
	db.Where("username = ?", "admin").Delete(&model.User{})
	if w := do(r, token, http.MethodGet, "/api/auth/me", nil); w.Code != http.StatusNotFound {
		t.Errorf("用户不存在应 404: %d", w.Code)
	}
}

// TestChangePasswordUserDeleted 用户被删后改密码 404。
func TestChangePasswordUserDeleted(t *testing.T) {
	r, db, token := newAuthed(t)
	db.Where("username = ?", "admin").Delete(&model.User{})
	w := do(r, token, http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "x", "new_password": "NewPass@123",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("应 404: %d", w.Code)
	}
}

// TestServerErrOnBrokenTable 查询失败走 serverErr(500)。
func TestServerErrOnBrokenTable(t *testing.T) {
	r, db, token := newAuthed(t)
	if err := db.Migrator().DropTable("registries"); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	if w := do(r, token, http.MethodGet, "/api/registries", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("查询失败应 500: %d", w.Code)
	}
	if err := db.Migrator().DropTable("chart_repos"); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	if w := do(r, token, http.MethodGet, "/api/chart-repos", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("chart 仓库查询失败应 500: %d", w.Code)
	}
	if err := db.Migrator().DropTable("chart_uploads"); err != nil {
		t.Fatalf("删表失败: %v", err)
	}
	if w := do(r, token, http.MethodGet, "/api/charts/uploads", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("上传记录查询失败应 500: %d", w.Code)
	}
}

// TestChartRepoUpdateValidation update 的分支补齐。
func TestChartRepoUpdateValidation(t *testing.T) {
	r, db, token := newAuthed(t)
	id := seedChartRepo(t, db, "up-me", "oci", "h.example.com", "proj")

	// body 非法 JSON。
	req := do(r, token, http.MethodPut, "/api/chart-repos/1", "not-json")
	_ = req // ShouldBindJSON 对 map body 不适用;改用缺 name 的 map:

	// 缺 name(binding required)。
	if w := do(r, token, http.MethodPut, "/api/chart-repos/"+fmt.Sprint(id), map[string]any{
		"type": "oci", "host": "h", "project": "p",
	}); w.Code != http.StatusBadRequest {
		t.Errorf("缺 name 应 400: %d", w.Code)
	}
	// host 清洗后为空。
	if w := do(r, token, http.MethodPut, "/api/chart-repos/"+fmt.Sprint(id), map[string]any{
		"name": "n", "type": "oci", "host": "  ", "project": "p",
	}); w.Code != http.StatusBadRequest {
		t.Errorf("空 host 应 400: %d", w.Code)
	}
	// OCI 缺 project。
	if w := do(r, token, http.MethodPut, "/api/chart-repos/"+fmt.Sprint(id), map[string]any{
		"name": "n", "type": "oci", "host": "h", "project": " ",
	}); w.Code != http.StatusBadRequest {
		t.Errorf("OCI 缺项目应 400: %d", w.Code)
	}
	// 重名冲突。
	seedChartRepo(t, db, "other", "oci", "h2.example.com", "p")
	if w := do(r, token, http.MethodPut, "/api/chart-repos/"+fmt.Sprint(id), map[string]any{
		"name": "other", "type": "oci", "host": "h", "project": "p",
	}); w.Code != http.StatusBadRequest {
		t.Errorf("重名应 400: %d", w.Code)
	}
}

// TestRegistryCatalogBadResponses 200 非 JSON / 500 状态码。
func TestRegistryCatalogBadResponses(t *testing.T) {
	host := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not-json-but-200")
	})
	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "badjson", Host: host, Type: "generic", Insecure: true}
	db.Create(&reg)

	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg.ID)+"/repos", nil), &res)
	if res.OK || !strings.Contains(res.Message, "解析响应失败") {
		t.Fatalf("非 JSON 应失败: %+v", res)
	}

	host500 := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	reg2 := model.Registry{Name: "s500", Host: host500, Type: "generic", Insecure: true}
	db.Create(&reg2)
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg2.ID)+"/repos", nil), &res)
	if res.OK || !strings.Contains(res.Message, "仓库返回 500") {
		t.Fatalf("500 应失败: %+v", res)
	}
}

// TestHarborListRawErrors Harbor 401 / 500(带错误体) / 缺 page 自动补全。
func TestHarborListRawErrors(t *testing.T) {
	host := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			// 不该发生(harborListRaw 会自动补 page=1)。
			w.WriteHeader(422)
		default:
			w.WriteHeader(401)
		}
	})
	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "hb-err", Host: host, Type: "harbor", Insecure: true}
	db.Create(&reg)

	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg.ID)+"/repos", nil), &res)
	if res.OK || !strings.Contains(res.Message, "认证失败") {
		t.Fatalf("Harbor 401 应认证失败: %+v", res)
	}

	// 500 带 Harbor 错误体。
	host500 := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"errors":[{"message":"internal boom"}]}`)
	})
	reg2 := model.Registry{Name: "hb-500", Host: host500, Type: "harbor", Insecure: true}
	db.Create(&reg2)
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg2.ID)+"/repos", nil), &res)
	if res.OK || !strings.Contains(res.Message, "internal boom") {
		t.Fatalf("500 应带错误详情: %+v", res)
	}

	// 200 非 JSON 数组。
	hostBad := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"not":"array"}`)
	})
	reg3 := model.Registry{Name: "hb-bad", Host: hostBad, Type: "harbor", Insecure: true}
	db.Create(&reg3)
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg3.ID)+"/repos", nil), &res)
	if res.OK || !strings.Contains(res.Message, "解析 Harbor 响应失败") {
		t.Fatalf("非数组应解析失败: %+v", res)
	}
}

// TestLoadRegDecryptFailure 加密模式下坏密文 → 404。
func TestLoadRegDecryptFailure(t *testing.T) {
	r, db, token := newAuthed(t)
	saved := config.AppConfig.EncryptKey
	config.AppConfig.EncryptKey = "some-key"
	t.Cleanup(func() { config.AppConfig.EncryptKey = saved })

	reg := model.Registry{Name: "enc-bad", Host: "h.example.com", Type: "generic", PasswordEnc: "!!!bad!!!"}
	db.Create(&reg)
	if w := do(r, token, http.MethodGet, "/api/catalog/"+fmt.Sprint(reg.ID)+"/repos", nil); w.Code != 404 {
		t.Errorf("解密失败应 404: %d %s", w.Code, w.Body.String())
	}
}

// TestChartRepoTargetDecrypt chartRepoTarget 的解密分支。
func TestChartRepoTargetDecrypt(t *testing.T) {
	newAuthed(t) // 初始化 DB 全局依赖
	saved := config.AppConfig.EncryptKey
	config.AppConfig.EncryptKey = ""
	t.Cleanup(func() { config.AppConfig.EncryptKey = saved })

	// 明文模式(EncryptKey 空):Decrypt 原样返回。
	repo := model.ChartRepo{Name: "plain", Type: "oci", Host: "http://h:5000", Project: "p", PasswordEnc: "stored-value"}
	tg := chartRepoTarget(&repo)
	if tg.Password != "stored-value" || tg.Scheme != "http" || tg.Host != "h:5000" {
		t.Fatalf("target = %+v", tg)
	}
}
