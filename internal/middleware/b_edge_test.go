package middleware

// b_edge_test 是 B 类边界测试:AuthRequired 对 Authorization 头各种畸形形态的处理,
// 锁定当前正确行为,当前必须全部通过(GREEN)。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"images-repo-sync/internal/auth"
)

// TestAuthRequiredHeaderEdgeCases 畸形 Authorization 头一律 401。
func TestAuthRequiredHeaderEdgeCases(t *testing.T) {
	r := newTestRouter("mw-edge-secret")
	validToken, _, err := auth.GenerateToken(1, "alice", false)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	cases := []struct {
		name      string
		header    string // hasHeader 为 false 时不设置该头
		hasHeader bool
	}{
		{"无 Authorization 头", "", false},
		{"bearer 小写 + 无效 token", "bearer not-a-jwt", true},
		{"Bearer 小写 + 空 token", "bearer ", true},
		{"双空格 + 合法 token(前导空格污染 token)", "Bearer  " + validToken, true},
		{"双空格 + 无效 token", "Bearer  not-a-jwt", true},
		{"非 Bearer scheme(Basic)", "Basic " + validToken, true},
		{"非 Bearer scheme(Token)", "Token " + validToken, true},
		{"scheme 小写 basic", "basic " + validToken, true},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		if c.hasHeader {
			req.Header.Set("Authorization", c.header)
		}
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: 状态 = %d, want 401 (header=%q body=%s)", c.name, w.Code, c.header, w.Body.String())
		}
	}
}

// TestAuthRequiredSchemeCaseInsensitiveValidToken scheme 大小写不敏感(RFC 6750):
// 小写 `bearer` + 合法 token 现状为放行,锁定该行为。
func TestAuthRequiredSchemeCaseInsensitiveValidToken(t *testing.T) {
	r := newTestRouter("mw-edge-secret")
	token, _, err := auth.GenerateToken(9, "bob", false)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		req.Header.Set("Authorization", scheme+" "+token)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("scheme %q + 合法 token: 状态 = %d, want 200 (body=%s)", scheme, w.Code, w.Body.String())
		}
	}
}

// TestAuthRequiredInjectsIdentity 合法 token 通过后,userID/username 应正确注入上下文。
func TestAuthRequiredInjectsIdentity(t *testing.T) {
	r := newTestRouter("mw-edge-secret")
	const (
		wantID   = 4242
		wantName = "charlie"
	)
	token, _, err := auth.GenerateToken(wantID, wantName, false)
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("合法 token 应通过, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"id":4242`) {
		t.Errorf("上下文未注入正确 userID: %s", body)
	}
	if !contains(body, `"name":"charlie"`) {
		t.Errorf("上下文未注入正确 username: %s", body)
	}
}
