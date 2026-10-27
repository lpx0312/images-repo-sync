package api

import (
	"net/http"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
)

func TestLoginSuccess(t *testing.T) {
	r, db, _ := newAuthed(t)

	var out struct {
		Token string `json:"token"`
		User  struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Status   string `json:"status"`
		} `json:"user"`
	}
	w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "Admin@123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &out)
	if out.Token == "" || out.User.Username != "admin" {
		t.Fatalf("登录结果不对: %+v", out)
	}
	// last_login_at 应更新。
	var user model.User
	db.Where("username = ?", "admin").First(&user)
	if user.LastLoginAt == nil {
		t.Error("last_login_at 未更新")
	}
}

func TestLoginFailures(t *testing.T) {
	r, _, _ := newAuthed(t)

	cases := []struct {
		name   string
		body   map[string]any
		code   int
		wantIn string
	}{
		{"缺用户名", map[string]any{"password": "x"}, http.StatusBadRequest, "用户名和密码"},
		{"缺密码", map[string]any{"username": "admin"}, http.StatusBadRequest, "用户名和密码"},
		{"用户不存在", map[string]any{"username": "nobody", "password": "x"}, http.StatusUnauthorized, "用户名或密码错误"},
		{"密码错误", map[string]any{"username": "admin", "password": "wrong"}, http.StatusUnauthorized, "用户名或密码错误"},
		{"空 body", nil, http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		w := do(r, "", http.MethodPost, "/api/auth/login", c.body)
		if w.Code != c.code {
			t.Errorf("%s: 状态 = %d, want %d (%s)", c.name, w.Code, c.code, w.Body.String())
			continue
		}
		if c.wantIn != "" && !strings.Contains(w.Body.String(), c.wantIn) {
			t.Errorf("%s: 响应不含 %q: %s", c.name, c.wantIn, w.Body.String())
		}
	}
}

func TestLoginDisabledUser(t *testing.T) {
	r, db, _ := newAuthed(t)
	db.Model(&model.User{}).Where("username = ?", "admin").Update("status", model.UserStatusDisabled)

	w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "Admin@123",
	})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "账号已禁用") {
		t.Fatalf("禁用账号应 403: %d %s", w.Code, w.Body.String())
	}
}

func TestLoginRateLimited(t *testing.T) {
	r, _, _ := newAuthed(t)
	// loginGuard 是包级全局:开头清零(此前测试的失败已计数),结束再清一次防污染。
	loginGuard.reset("user:admin", "ip:192.0.2.1")
	defer loginGuard.reset("user:admin", "ip:192.0.2.1")

	// 连续失败 5 次(用户维度阈值)触发临时锁定。
	for i := 0; i < 5; i++ {
		w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
			"username": "admin", "password": "wrong",
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应 401: %d", i+1, w.Code)
		}
	}
	w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "Admin@123", // 即使密码正确,锁定期间也拒绝
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间应 429: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "登录失败次数过多") {
		t.Errorf("提示不对: %s", w.Body.String())
	}
}

func TestMeAndLogout(t *testing.T) {
	r, _, token := newAuthed(t)

	var me struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Status   string `json:"status"`
	}
	w := do(r, token, http.MethodGet, "/api/auth/me", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/me 失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &me)
	if me.Username != "admin" || me.Status != model.UserStatusActive {
		t.Errorf("me = %+v", me)
	}

	// 无 token 401。
	if w := do(r, "", http.MethodGet, "/api/auth/me", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("无 token 应 401, got %d", w.Code)
	}
	// 假 token 401。
	if w := do(r, "bogus", http.MethodGet, "/api/auth/me", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("假 token 应 401, got %d", w.Code)
	}

	// logout。
	if w := do(r, token, http.MethodPost, "/api/auth/logout", nil); w.Code != http.StatusOK {
		t.Errorf("logout 失败: %d", w.Code)
	}
}

func TestChangePassword(t *testing.T) {
	r, db, token := newAuthed(t)

	// 弱密码拒绝。
	w := do(r, token, http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "Admin@123", "new_password": "weak",
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "至少 8 位") {
		t.Fatalf("弱密码应 400: %d %s", w.Code, w.Body.String())
	}

	// 旧密码错误。
	w = do(r, token, http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "Wrong@123", "new_password": "NewPass@123",
	})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "原密码错误") {
		t.Fatalf("旧密码错应 401: %d %s", w.Code, w.Body.String())
	}

	// 成功修改。
	w = do(r, token, http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "Admin@123", "new_password": "NewPass@123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("修改失败: %d %s", w.Code, w.Body.String())
	}
	// 新密码可登录,旧密码不可。
	if w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{"username": "admin", "password": "NewPass@123"}); w.Code != http.StatusOK {
		t.Errorf("新密码登录失败: %d", w.Code)
	}
	if w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{"username": "admin", "password": "Admin@123"}); w.Code != http.StatusUnauthorized {
		t.Errorf("旧密码应失效: %d", w.Code)
	}
	_ = db
}

func TestChangePasswordEdgeCases(t *testing.T) {
	r, _, token := newAuthed(t)

	// 缺字段。
	if w := do(r, token, http.MethodPut, "/api/auth/password", map[string]any{"old_password": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("缺字段应 400: %d", w.Code)
	}
	// 假 token(user 不存在场景难以直接构造,这里校验 401)。
	if w := do(r, "bogus-token", http.MethodPut, "/api/auth/password", map[string]any{
		"old_password": "a", "new_password": "NewPass@123",
	}); w.Code != http.StatusUnauthorized {
		t.Errorf("无效 token 应 401: %d", w.Code)
	}
}
