package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"images-repo-sync/internal/testutil"
)

func TestRouterHealthz(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := SetupRouter(db, nil)
	w := do(r, "", http.MethodGet, "/api/healthz", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
}

// TestRouterStaticFS 前端静态资源:assets 缓存头、精确文件、SPA 回退、API 404。
func TestRouterStaticFS(t *testing.T) {
	db := testutil.NewTestDB(t)
	dist := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>app</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"favicon.svg":   &fstest.MapFile{Data: []byte("<svg/>")},
	}
	r := SetupRouter(db, dist)

	// SPA 路由回退 index.html。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks/123", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "app") {
		t.Fatalf("SPA 回退失败: %d %s", w.Code, w.Body.String())
	}
	if cache := w.Header().Get("Cache-Control"); cache != "no-cache" {
		t.Errorf("index.html Cache-Control = %q", cache)
	}

	// assets 长缓存。
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if w2.Code != http.StatusOK || w2.Body.String() != "console.log(1)" {
		t.Fatalf("assets: %d %s", w2.Code, w2.Body.String())
	}
	if cache := w2.Header().Get("Cache-Control"); !strings.Contains(cache, "max-age=31536000") {
		t.Errorf("assets Cache-Control = %q", cache)
	}
	if ct := w2.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("assets Content-Type = %q", ct)
	}

	// 精确文件(favicon):按扩展名给 Content-Type。
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if w3.Code != http.StatusOK || w3.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("favicon: %d %s %q", w3.Code, w3.Body.String(), w3.Header().Get("Content-Type"))
	}

	// API 404 不回退 HTML。
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, httptest.NewRequest(http.MethodGet, "/api/no-such", nil))
	if w4.Code != http.StatusNotFound || !strings.Contains(w4.Body.String(), "接口不存在") {
		t.Fatalf("API 404: %d %s", w4.Code, w4.Body.String())
	}
}

// TestRouterNoStaticFS 未嵌前端时不注册回退路由。
func TestRouterNoStaticFS(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := SetupRouter(db, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whatever", nil))
	// 无 staticFS 时 NoRoute 未注册 → gin 默认 404 空响应。
	if w.Code != http.StatusNotFound {
		t.Fatalf("应 404, got %d", w.Code)
	}
}

// TestRouterCORS 仅放行本地开发前端。
func TestRouterCORS(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := SetupRouter(db, nil)

	allowed := []string{
		"http://localhost:3000", "http://127.0.0.1:3000",
		"http://localhost:5173", "http://127.0.0.1:5173",
	}
	for _, origin := range allowed {
		req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("origin %s 未放行", origin)
		}
	}

	// 外部 origin 拒绝。
	req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") == "http://evil.example.com" {
		t.Error("外部 origin 不应放行")
	}

	// OPTIONS 预检。
	req2 := httptest.NewRequest(http.MethodOptions, "/api/healthz", nil)
	req2.Header.Set("Origin", "http://localhost:3000")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNoContent {
		t.Errorf("预检应 204, got %d", w2.Code)
	}
}
