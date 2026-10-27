package api

// auth_revocation_test 是 A 类回归测试的跨包 e2e 入口(唯一允许新增的 api 测试文件):
// 验证「改密码后旧 token 必须立即失效」这一安全属性。
// 走完整 HTTP 链路:login 拿 token → PUT /api/auth/password 改密 → 旧 token 访问 /me。

import (
	"net/http"
	"testing"

	"images-repo-sync/internal/testutil"
)

// TestChangePasswordRevokesOldToken 改密后旧 token 应被拒绝。
// 回归锁定(已修复): A1 —— 改密后旧 token 仍有效:现在旧 token 访问 /api/auth/me 返回 200,
// 期望 401(服务端应使改密前签发的 token 失效,例如按用户记录密码版本/签发时间校验)。
func TestChangePasswordRevokesOldToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := SetupRouter(db, nil)

	// loginGuard 是包级全局限速器:先清零,避免此前测试的失败计数干扰;结束再清防污染。
	loginGuard.reset("user:admin", "ip:192.0.2.1")
	defer loginGuard.reset("user:admin", "ip:192.0.2.1")

	// 1. 登录拿 token。
	w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "Admin@123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	decode(t, w, &login)
	oldToken := login.Token
	if oldToken == "" {
		t.Fatal("登录响应缺少 token")
	}

	// 2. 前置确认:改密前旧 token 可访问 /me(保证后续 401 归因于改密,而非 token 本身无效)。
	if w := do(r, oldToken, http.MethodGet, "/api/auth/me", nil); w.Code != http.StatusOK {
		t.Fatalf("改密前旧 token 应可访问 /me: %d %s", w.Code, w.Body.String())
	}

	// 3. 修改密码。
	w = do(r, oldToken, http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "Admin@123", "new_password": "NewPass@123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("修改密码失败: %d %s", w.Code, w.Body.String())
	}

	// 4. 回归锁定(已修复): A1 —— 用改密前的旧 token 访问 /me,应 401。
	w = do(r, oldToken, http.MethodGet, "/api/auth/me", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("回归锁定(已修复) A1: 改密后旧 token 应被拒绝(401), got %d (body=%s)", w.Code, w.Body.String())
	}
}
