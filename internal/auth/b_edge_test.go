package auth

// b_edge_test 是 B 类边界测试:锁定当前正确行为,当前必须全部通过(GREEN)。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/golang-jwt/jwt/v5"
)

// --- HashPassword / CheckPassword 边界 ---

// TestHashPasswordRejectsOver72Bytes 超过 72 字节的密码 HashPassword 应报错。
// 这是 A3 中「强度校验应拒绝 >72 字节密码」的前提(bcrypt 硬限制,库层面必须报错)。
func TestHashPasswordRejectsOver72Bytes(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("A", 73)); err == nil {
		t.Fatal("73 字节密码 HashPassword 应报错(bcrypt 72 字节上限)")
	} else if !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Errorf("错误类型应为 ErrPasswordTooLong, got %v", err)
	}
	// 恰好 72 字节仍可正常哈希(边界内)。
	hash, err := HashPassword(strings.Repeat("A", 72))
	if err != nil {
		t.Fatalf("72 字节密码应可哈希: %v", err)
	}
	if !CheckPassword(hash, strings.Repeat("A", 72)) {
		t.Error("72 字节密码往返校验失败")
	}
}

// TestCheckPasswordRejectsWrongPassword 错误密码必须校验失败。
func TestCheckPasswordRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("Admin@123")
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	for _, wrong := range []string{"wrong", "Admin@12", "admin@123", "Admin@123 ", ""} {
		if CheckPassword(hash, wrong) {
			t.Errorf("CheckPassword(hash, %q) = true, want false", wrong)
		}
	}
	if !CheckPassword(hash, "Admin@123") {
		t.Error("正确密码应校验通过")
	}
}

// --- jwt 边界 ---

// TestExpiredTokenIsRejected 过期 token 必须被拒绝。
func TestExpiredTokenIsRejected(t *testing.T) {
	withSecret(t, "b-secret")
	claims := &Claims{UserID: 1, Username: "admin"}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("b-secret"))
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if _, err := ValidateToken(signed); err == nil {
		t.Fatal("过期 token 应被拒绝")
	}
}

// TestTamperedSignatureIsRejected 篡改签名(payload/签名改动)必须被拒绝。
func TestTamperedSignatureIsRejected(t *testing.T) {
	withSecret(t, "b-secret")
	good, _, err := GenerateToken(3, "bob", false)
	if err != nil {
		t.Fatalf("GenerateToken 失败: %v", err)
	}
	parts := strings.Split(good, ".")
	if len(parts) != 3 {
		t.Fatalf("token 结构异常: %q", good)
	}
	tampered := []string{
		good + "x", // 签名尾部追加
		parts[0] + "." + parts[1] + "." + parts[2] + "x", // 同上(显式)
		parts[0] + "." + parts[1] + "x." + parts[2],      // payload 被改、签名未跟随
	}
	for i, tok := range tampered {
		if _, err := ValidateToken(tok); err == nil {
			t.Errorf("篡改 token #%d 应被拒绝", i)
		}
	}
	// 原 token 仍有效(对照)。
	if _, err := ValidateToken(good); err != nil {
		t.Errorf("未篡改 token 应通过: %v", err)
	}
}

// TestRememberMeExpiryLongerThanDefault rememberMe=true 的过期时间必须长于默认(24h)。
func TestRememberMeExpiryLongerThanDefault(t *testing.T) {
	withSecret(t, "b-secret")
	_, defExp, err := GenerateToken(1, "admin", false)
	if err != nil {
		t.Fatalf("GenerateToken(默认) 失败: %v", err)
	}
	_, remExp, err := GenerateToken(1, "admin", true)
	if err != nil {
		t.Fatalf("GenerateToken(rememberMe) 失败: %v", err)
	}
	if !remExp.After(defExp) {
		t.Errorf("rememberMe 有效期 %v 应晚于默认 %v", remExp, defExp)
	}
	// 绝对量级:rememberMe ~7 天,默认 ~24h。
	if d := time.Until(remExp); d < 6*24*time.Hour {
		t.Errorf("rememberMe 有效期 = %v, 应不少于 6 天", d)
	}
	if d := time.Until(defExp); d >= 48*time.Hour {
		t.Errorf("默认有效期 = %v, 不应达到 48h", d)
	}
}
