package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/skopeo"
)

// SettingsHandler 处理系统设置的读取与更新。
type SettingsHandler struct {
	DB *gorm.DB
}

func NewSettingsHandler(db *gorm.DB) *SettingsHandler { return &SettingsHandler{DB: db} }

// 默认值兜底:数据库里没有时返回这些。
var settingDefaults = map[string]string{
	model.SettingKeyDefaultArch: skopeo.ArchAMD64,
}

// validArchs 是合法的架构取值。
var validArchs = map[string]bool{
	skopeo.ArchAMD64: true,
	skopeo.ArchARM64: true,
	skopeo.ArchAll:   true,
}

// Get GET /api/settings
//
// 返回所有系统配置(密码等敏感项不会出现在这里,目前只有默认架构等)。
func (h *SettingsHandler) Get(c *gin.Context) {
	var rows []model.SystemSetting
	h.DB.Find(&rows)
	m := map[string]string{}
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	// 缺失的键用默认值补齐。
	for k, v := range settingDefaults {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	ok(c, m)
}

// updateRequest 更新设置请求体。key-value 形式,只更新 body 里出现的键。
type updateRequest struct {
	DefaultArch string `json:"default_arch"`
}

// Update PUT /api/settings
//
// 更新系统配置。目前支持 default_arch(amd64/arm64/all)。
func (h *SettingsHandler) Update(c *gin.Context) {
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badReq(c, "参数格式错误")
		return
	}

	updates := map[string]string{}
	if req.DefaultArch != "" {
		if !validArchs[req.DefaultArch] {
			badReq(c, "默认架构无效,可选: amd64 / arm64 / all")
			return
		}
		updates[model.SettingKeyDefaultArch] = req.DefaultArch
	}

	for k, v := range updates {
		h.DB.Save(&model.SystemSetting{Key: k, Value: v})
	}
	ok(c, gin.H{"message": "设置已保存"})
}

// RegisterRoutes 注册受保护的 settings 路由。
func (h *SettingsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/settings")
	g.Use(middleware.AuthRequired())
	g.GET("", h.Get)
	g.PUT("", h.Update)
}
