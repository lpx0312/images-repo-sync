package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/store"
)

// AuthHandler 处理登录/登出/当前用户/改密码。
type AuthHandler struct {
	DB *gorm.DB
}

func NewAuthHandler(db *gorm.DB) *AuthHandler { return &AuthHandler{DB: db} }

// loginRequest 登录请求体。
type loginRequest struct {
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
	RememberMe bool   `json:"remember_me"`
}

// Login POST /api/auth/login
//
// 校验账号密码 → 签发 JWT → 写登录日志。无论用户不存在还是密码错,
// 都返回统一的「用户名或密码错误」以避免用户名枚举;用户不存在时也跑一次
// 等耗时的 bcrypt 比较,消除时序侧信道。连续失败会触发临时锁定(见 login_limiter.go)。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badReq(c, "用户名和密码不能为空")
		return
	}
	ip := c.ClientIP()
	ua := c.GetHeader("User-Agent")

	// 失败限速:同一用户名或 IP 连续失败过多时临时拒绝。
	userKey := "user:" + strings.ToLower(strings.TrimSpace(req.Username))
	ipKey := "ip:" + ip
	if locked, wait := loginGuard.locked(userKey, ipKey); locked {
		store.RecordLoginLog(0, req.Username, ip, ua, model.LoginStatusFailed, "失败次数过多,临时锁定")
		mins := int(wait.Minutes())
		if mins < 1 {
			mins = 1
		}
		errResp(c, http.StatusTooManyRequests, fmt.Sprintf("登录失败次数过多,请约 %d 分钟后再试", mins))
		return
	}

	var user model.User
	if err := h.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		auth.CheckPassword(loginDummyHash, req.Password) // 等耗时比较,拉平与密码错误分支的时序
		loginGuard.recordFailure(userKey, ipKey)
		store.RecordLoginLog(0, req.Username, ip, ua, model.LoginStatusFailed, "用户不存在")
		errResp(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if user.Status == model.UserStatusDisabled {
		store.RecordLoginLog(user.ID, req.Username, ip, ua, model.LoginStatusFailed, "账号已禁用")
		errResp(c, http.StatusForbidden, "账号已禁用")
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		loginGuard.recordFailure(userKey, ipKey)
		store.RecordLoginLog(user.ID, req.Username, ip, ua, model.LoginStatusFailed, "密码错误")
		errResp(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	loginGuard.reset(userKey, ipKey)

	token, expiresAt, err := auth.GenerateToken(user.ID, user.Username, req.RememberMe)
	if err != nil {
		serverErr(c, "生成令牌失败")
		return
	}

	now := time.Now()
	h.DB.Model(&user).Update("last_login_at", now)
	store.RecordLoginLog(user.ID, user.Username, ip, ua, model.LoginStatusSuccess, "登录成功")

	ok(c, gin.H{
		"token":      token,
		"expires_at": expiresAt,
		"user":       gin.H{"id": user.ID, "username": user.Username, "status": user.Status},
	})
}

// Logout POST /api/auth/logout
//
// JWT 无状态,登出仅在服务端记日志;客户端丢弃 token。
func (h *AuthHandler) Logout(c *gin.Context) {
	store.RecordLoginLog(
		middleware.UserID(c),
		middleware.Username(c),
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		model.LoginStatusSuccess,
		"主动登出",
	)
	ok(c, gin.H{"message": "已登出"})
}

// Me GET /api/auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	uid := middleware.UserID(c)
	var user model.User
	if err := h.DB.First(&user, uid).Error; err != nil {
		errResp(c, http.StatusNotFound, "用户不存在")
		return
	}
	ok(c, gin.H{"id": user.ID, "username": user.Username, "status": user.Status})
}

// changePasswordRequest 改密码请求体。
type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangePassword PUT /api/auth/password
//
// 校验旧密码 + 新密码强度(≥8 位含大小写数字特殊字符)→ 更新。
// 改密码后强制重新登录(前端会清 token)。
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badReq(c, "参数不完整")
		return
	}
	if !auth.IsStrongPassword(req.NewPassword) {
		badReq(c, "新密码至少 8 位,需同时包含大写、小写、数字和特殊字符")
		return
	}

	uid := middleware.UserID(c)
	var user model.User
	if err := h.DB.First(&user, uid).Error; err != nil {
		errResp(c, http.StatusNotFound, "用户不存在")
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.OldPassword) {
		errResp(c, http.StatusUnauthorized, "原密码错误")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		serverErr(c, "密码哈希失败")
		return
	}
	if err := h.DB.Model(&user).Update("password_hash", hash).Error; err != nil {
		serverErr(c, "更新密码失败")
		return
	}
	ok(c, gin.H{"message": "密码已修改,请重新登录"})
}

// RegisterRoutes 注册公开与受保护的 auth 路由。
func (h *AuthHandler) RegisterRoutes(rg *gin.RouterGroup) {
	authGroup := rg.Group("/auth")
	authGroup.POST("/login", h.Login)

	protected := authGroup.Group("")
	protected.Use(middleware.AuthRequired())
	protected.POST("/logout", h.Logout)
	protected.GET("/me", h.Me)
	protected.PUT("/password", h.ChangePassword)
}
