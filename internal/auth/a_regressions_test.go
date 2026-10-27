package auth

// a_regressions_test 是代码审查缺陷的 A 类回归测试(A3/A4)。
// 这些测试断言「修复后」的行为,对应缺陷已修复,应保持 GREEN。

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// --- A3 IsStrongPassword ---

// TestIsStrongPasswordCountsRunesNotBytes 密码长度应按字符(rune)计数,而非字节数。
// 回归锁定(已修复): A3 —— "中aA1中" 只有 5 个字符(9 字节),现按 len()(字节)判定 ≥8 通过,
// 且中文被当作特殊字符,导致强度校验误放行;应拒绝。
func TestIsStrongPasswordCountsRunesNotBytes(t *testing.T) {
	pw := "中aA1中" // 9 字节,但仅 5 个字符
	if got := len([]rune(pw)); got != 5 {
		t.Fatalf("测试前提不成立: 字符数 = %d, want 5", got)
	}
	if IsStrongPassword(pw) {
		t.Errorf("回归锁定(已修复) A3: IsStrongPassword(%q) = true, want false(5 个字符不足 8,不能按字节数放行)", pw)
	}
}

// TestIsStrongPasswordSpaceIsNotSpecial 空格不应被视为特殊字符。
// 回归锁定(已修复): A3 —— "Aa1 bcde" 中空格落入 default 分支被当作特殊字符,
// 整体被误判为强密码;空格既不算特殊字符,该密码也应因缺特殊字符被拒绝。
func TestIsStrongPasswordSpaceIsNotSpecial(t *testing.T) {
	pw := "Aa1 bcde" // 大写/小写/数字齐备,唯一的"特殊字符"是空格
	if IsStrongPassword(pw) {
		t.Errorf("回归锁定(已修复) A3: IsStrongPassword(%q) = true, want false(空格不应算特殊字符)", pw)
	}
}

// TestIsStrongPasswordRejectsOver72Bytes 超过 72 字节的密码应拒绝(bcrypt 上限)。
// 回归锁定(已修复): A3 —— 现在强度校验不设上限,>72 字节的密码能通过校验,
// 但 HashPassword 会因 bcrypt 72 字节限制报错;应在校验阶段就拒绝。
func TestIsStrongPasswordRejectsOver72Bytes(t *testing.T) {
	pw := "Aa1!" + strings.Repeat("x", 70) // 74 字节,大小写/数字/特殊字符齐备
	if len(pw) != 74 {
		t.Fatalf("测试前提不成立: 字节数 = %d, want 74", len(pw))
	}
	if IsStrongPassword(pw) {
		t.Errorf("回归锁定(已修复) A3: IsStrongPassword(74 字节) = true, want false(超过 bcrypt 72 字节上限)")
	}
}

// --- A4 ValidateToken ---

// TestValidateTokenRequiresExpClaim 无 exp 的 HS256 token 应被拒绝。
// 回归锁定(已修复): A4 —— jwt/v5 默认不强制 exp,无过期的 token 现在被放行,
// 意味着可以签发永不过期的凭证;应要求必须携带 exp。
func TestValidateTokenRequiresExpClaim(t *testing.T) {
	withSecret(t, "a4-secret")
	claims := &Claims{
		UserID:   1,
		Username: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt: jwt.NewNumericDate(time.Now()),
			Issuer:   "images-repo-sync",
			// 故意不设置 ExpiresAt
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("a4-secret"))
	if err != nil {
		t.Fatalf("构造无 exp token 失败: %v", err)
	}
	if _, err := ValidateToken(signed); err == nil {
		t.Error("回归锁定(已修复) A4: 无 exp 的 token 应被拒绝,但校验通过了")
	}
}

// TestValidateTokenOnlyHS256 签名算法应收紧为仅 HS256。
// 回归锁定(已修复): A4 —— 现在 keyfunc 接受整个 HMAC 家族(HS256/384/512),
// 用同一 secret 签发的 HS384/HS512 token 也能通过校验;应仅允许 HS256。
func TestValidateTokenOnlyHS256(t *testing.T) {
	withSecret(t, "a4-secret")

	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
	}{
		{"HS384", jwt.SigningMethodHS384},
		{"HS512", jwt.SigningMethodHS512},
	} {
		claims := &Claims{
			UserID:   1,
			Username: "admin",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
		}
		signed, err := jwt.NewWithClaims(tc.method, claims).SignedString([]byte("a4-secret"))
		if err != nil {
			t.Fatalf("构造 %s token 失败: %v", tc.name, err)
		}
		if _, err := ValidateToken(signed); err == nil {
			t.Errorf("回归锁定(已修复) A4: 用同一 secret 签发的 %s token 应被拒绝(仅允许 HS256)", tc.name)
		}
	}
}
