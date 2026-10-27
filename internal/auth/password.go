package auth

import (
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// maxPasswordBytes 是 bcrypt 的 72 字节硬上限,超过在校验阶段直接拒绝,
// 避免强度校验放行后 HashPassword 才报错。
const maxPasswordBytes = 72

// HashPassword 用 bcrypt(DefaultCost=10)哈希密码。
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword 校验明文密码是否匹配哈希。
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// IsStrongPassword 校验密码强度:至少 8 个字符(按 rune 计,非字节),
// 且同时包含大写、小写、数字和特殊字符,总字节数不超过 bcrypt 的 72 字节上限。
// 与参考项目保持一致的规则。
func IsStrongPassword(p string) bool {
	if len([]rune(p)) < 8 {
		return false
	}
	if len(p) > maxPasswordBytes {
		return false
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case unicode.IsSpace(r):
			// 空格/制表等空白不算特殊字符,也不贡献任何强度。
		case unicode.IsPrint(r):
			hasSpecial = true
		}
	}
	return hasUpper && hasLower && hasDigit && hasSpecial
}
