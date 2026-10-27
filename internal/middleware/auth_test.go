package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/config"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestRouter(secret string) *gin.Engine {
	config.AppConfig.JWTSecret = secret
	r := gin.New()
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	protected := r.Group("/", AuthRequired())
	protected.GET("/who", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": UserID(c), "name": Username(c)})
	})
	return r
}

func TestAuthRequiredAcceptsValidToken(t *testing.T) {
	r := newTestRouter("mw-secret")
	token, _, err := auth.GenerateToken(42, "alice", false)
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
	if !contains(w.Body.String(), `"id":42`) || !contains(w.Body.String(), `"name":"alice"`) {
		t.Errorf("上下文注入不对: %s", w.Body.String())
	}
}

func TestAuthRequiredRejects(t *testing.T) {
	r := newTestRouter("mw-secret")
	token, _, _ := auth.GenerateToken(1, "a", false)

	cases := []struct {
		name, header string
		code         int
	}{
		{"无 Authorization", "", http.StatusUnauthorized},
		{"非 Bearer 前缀", "Basic abc", http.StatusUnauthorized},
		{"Bearer 但 token 为空", "Bearer ", http.StatusUnauthorized},
		{"Bearer 篡改 token", "Bearer " + token + "x", http.StatusUnauthorized},
		{"只有一段", "Bearer", http.StatusUnauthorized},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		r.ServeHTTP(w, req)
		if w.Code != c.code {
			t.Errorf("%s: 状态 = %d, want %d (body=%s)", c.name, w.Code, c.code, w.Body.String())
		}
	}
}

func TestAuthRequiredAbortsChain(t *testing.T) {
	r := newTestRouter("s")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应 401, got %d", w.Code)
	}
}

func TestContextHelpersDefaults(t *testing.T) {
	// 上下文没有注入时 UserID/Username 返回零值(防御分支)。
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		c.Set(CtxUserID, "not-a-uint") // 类型错误分支
		c.Set(CtxUsername, 123)
		c.JSON(http.StatusOK, gin.H{"id": UserID(c), "name": Username(c)})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	body := w.Body.String()
	if !contains(body, `"id":0`) || !contains(body, `"name":""`) {
		t.Errorf("类型不符时应返回零值: %s", body)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
