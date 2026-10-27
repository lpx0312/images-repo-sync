package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/store"
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
		// 验签通过后再查一次用户:用户被删/禁用、或改密后签发的旧 token,一律拒绝。
		// 每个请求多一次单行查询,内网工具可接受;DB 未初始化(单测场景)时跳过。
		if store.DB != nil {
			var user model.User
			if err := store.DB.First(&user, claims.UserID).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "认证已过期，请重新登录"})
				return
			}
			if user.Status == model.UserStatusDisabled {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "账号已禁用"})
				return
			}
			// 改密时间晚于 token 签发时间(或 token 未携带签发时间)→ 旧 token 已被吊销。
			// 注:严格小于意味着与改密同秒签发的旧 token 仍可用(最长至其自然过期);
			// 收紧为 !After 会误杀改密后同秒重登录的新 token,当前权衡取宽松侧。
			if !user.PwdChangedAt.IsZero() &&
				(claims.IssuedAt == nil || claims.IssuedAt.Time.Before(user.PwdChangedAt)) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "认证已过期，请重新登录"})
				return
			}
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
