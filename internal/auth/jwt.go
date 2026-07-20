package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"images-repo-sync/internal/config"
)

// Claims 是 JWT 中携带的用户信息。
type Claims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// GenerateToken 为指定用户签发 HS256 JWT。
// rememberMe 为 true 时有效期延长到 7 天,否则 24 小时。
func GenerateToken(userID uint, username string, rememberMe bool) (string, time.Time, error) {
	expiry := 24 * time.Hour
	if rememberMe {
		expiry = 7 * 24 * time.Hour
	}
	expiresAt := time.Now().Add(expiry)

	claims := &Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "images-repo-sync",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	secret := config.AppConfig.JWTSecret
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

// ValidateToken 校验并解析 JWT。签名方法必须是 HMAC。
func ValidateToken(tokenString string) (*Claims, error) {
	secret := config.AppConfig.JWTSecret
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("不支持的签名方法")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("token 无效")
	}
	return claims, nil
}
