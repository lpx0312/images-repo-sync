package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"images-repo-sync/internal/auth"
)

// Context keys。
const (
	CtxUserID   = "userID"
	CtxUsername = "username"
)

// AuthRequired 校验 Authorization: Bearer <token>,通过则把 userID/username 注入上下文。
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未提供认证信息"})
			return
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "认证格式错误"})
			return
		}
		claims, err := auth.ValidateToken(parts[1])
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "认证已过期，请重新登录"})
			return
		}
		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Next()
	}
}

// UserID 从上下文取出当前登录用户 ID。
func UserID(c *gin.Context) uint {
	if v, ok := c.Get(CtxUserID); ok {
		if id, ok := v.(uint); ok {
			return id
		}
	}
	return 0
}

// Username 从上下文取出当前登录用户名。
func Username(c *gin.Context) string {
	if v, ok := c.Get(CtxUsername); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
