package auth

import (
	"testing"
	"time"

	"images-repo-sync/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func withSecret(t *testing.T, secret string) {
	t.Helper()
	saved := config.AppConfig.JWTSecret
	config.AppConfig.JWTSecret = secret
	t.Cleanup(func() { config.AppConfig.JWTSecret = saved })
}

func TestGenerateAndValidateToken(t *testing.T) {
	withSecret(t, "unit-test-secret")
	token, expiresAt, err := GenerateToken(7, "admin", false)
	if err != nil {
		t.Fatalf("GenerateToken 失败: %v", err)
	}
	// 24h 有效期。
	if d := time.Until(expiresAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("短期 token 有效期 = %v, want ~24h", d)
	}

	claims, err := ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken 失败: %v", err)
	}
	if claims.UserID != 7 || claims.Username != "admin" {
		t.Errorf("claims 不对: %+v", claims)
	}
	if claims.Issuer != "images-repo-sync" {
		t.Errorf("issuer = %q", claims.Issuer)
	}
}

func TestRememberMeExpiry(t *testing.T) {
	withSecret(t, "s")
	_, expiresAt, err := GenerateToken(1, "admin", true)
	if err != nil {
		t.Fatalf("GenerateToken 失败: %v", err)
	}
	if d := time.Until(expiresAt); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("rememberMe 有效期 = %v, want ~7 天", d)
	}
}

func TestValidateTokenRejects(t *testing.T) {
	withSecret(t, "unit-test-secret")
	good, _, _ := GenerateToken(1, "admin", false)

	cases := []struct {
		name, token string
	}{
		{"空 token", ""},
		{"乱字符串", "not-a-jwt"},
		{"篡改签名", good + "x"},
	}
	for _, c := range cases {
		if _, err := ValidateToken(c.token); err == nil {
			t.Errorf("%s: 应校验失败", c.name)
		}
	}
}

func TestValidateTokenWrongSecret(t *testing.T) {
	withSecret(t, "secret-A")
	token, _, _ := GenerateToken(1, "admin", false)
	config.AppConfig.JWTSecret = "secret-B"
	if _, err := ValidateToken(token); err == nil {
		t.Fatal("换密钥后旧 token 应失效")
	}
}

func TestValidateTokenRejectsAlgNone(t *testing.T) {
	// 构造 alg=none 的恶意 token,验证中间件强制 HMAC。
	withSecret(t, "s")
	claims := &Claims{UserID: 1, Username: "attacker"}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("构造 none token 失败: %v", err)
	}
	if _, err := ValidateToken(signed); err == nil {
		t.Fatal("alg=none 的 token 应被拒绝")
	}
}

func TestValidateExpiredToken(t *testing.T) {
	withSecret(t, "s")
	claims := &Claims{UserID: 1, Username: "admin"}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte("s"))
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if _, err := ValidateToken(signed); err == nil {
		t.Fatal("过期 token 应被拒绝")
	}
}
