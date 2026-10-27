package api

// 本文件是 api 包测试的共享设施:临时数据库 + 带认证的 httptest 客户端。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

func init() { gin.SetMode(gin.TestMode) }

// newAuthed 初始化测试数据库与路由,返回可带 Bearer 头的请求工厂。
func newAuthed(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	r := SetupRouter(db, nil)

	// 取 seed 的 admin 并签发 token。
	var user model.User
	if err := db.Where("username = ?", "admin").First(&user).Error; err != nil {
		t.Fatalf("查询 admin 失败: %v", err)
	}
	token, _, err := auth.GenerateToken(user.ID, user.Username, false)
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	return r, db, token
}

// do 发送一次 JSON 请求,带认证头。
func do(r *gin.Engine, token, method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decode 解包 {data:...} 响应到 out。
func decode(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是 JSON 包装: %d %s", w.Code, w.Body.String())
	}
	if env.Error != "" {
		t.Fatalf("响应报错(状态 %d): %s", w.Code, env.Error)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("data 解析失败: %v (%s)", err, env.Data)
		}
	}
}

// createRegistryViaAPI 通过 API 建一条仓库配置,返回 ID。
func createRegistryViaAPI(t *testing.T, r *gin.Engine, token, name, host, typ string) uint {
	t.Helper()
	w := do(r, token, http.MethodPost, "/api/registries", map[string]any{
		"name": name, "host": host, "type": typ, "username": "u", "password": "p",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建 registry 失败: %d %s", w.Code, w.Body.String())
	}
	var vo struct {
		ID uint `json:"id"`
	}
	decode(t, w, &vo)
	return vo.ID
}

// seedChartRepo 直接落库建 chart 仓库。
func seedChartRepo(t *testing.T, db *gorm.DB, name, typ, host, project string) uint {
	t.Helper()
	repo := model.ChartRepo{Name: name, Type: typ, Host: host, Project: project}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("创建 chart repo 失败: %v", err)
	}
	return repo.ID
}
