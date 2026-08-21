package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/crypto"
	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/registry"
)

// RegistryHandler 处理镜像仓库的 CRUD 与连接测试。
type RegistryHandler struct {
	DB *gorm.DB
}

func NewRegistryHandler(db *gorm.DB) *RegistryHandler { return &RegistryHandler{DB: db} }

// registryDTO 是创建/更新时的请求体。Password 明文传入,服务端加密存储。
type registryDTO struct {
	Name           string `json:"name" binding:"required"`
	Host           string `json:"host" binding:"required"`
	Type           string `json:"type"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	Insecure       bool   `json:"insecure"`
	DefaultProject string `json:"default_project"`
}

// toModel 把 DTO 转成模型并加密密码。Password 为空时(更新场景)保留原密码。
// Host 统一过 normalizeHost 清洗(去误填的 scheme 与首尾斜杠)。
func (d *registryDTO) toModel(existing *model.Registry) (*model.Registry, error) {
	m := &model.Registry{}
	if existing != nil {
		*m = *existing
	}
	m.Name = strings.TrimSpace(d.Name)
	m.Host = normalizeHost(d.Host)
	if d.Type != "" {
		m.Type = d.Type
	} else if m.Type == "" {
		m.Type = model.RegistryTypeGeneric
	}
	m.Username = strings.TrimSpace(d.Username)
	m.Insecure = d.Insecure
	m.DefaultProject = strings.TrimSpace(d.DefaultProject)

	// Password 为空:更新时保留旧密码;否则视为确实没填。
	if d.Password != "" {
		enc, err := crypto.Encrypt(d.Password)
		if err != nil {
			return nil, err
		}
		m.PasswordEnc = enc
	}
	return m, nil
}

// normalizeHost 清洗用户输入的仓库地址:去掉误填的 http(s):// 前缀与首尾空白/斜杠,
// 避免拼出 "https://https://host" 之类的非法 URL。仓库一律走 HTTPS(见 registry 包)。
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.Trim(h, "/")
}

// registryVO 是返回给前端的视图,不含密码哈希。
type registryVO struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	Type           string `json:"type"`
	Username       string `json:"username"`
	HasPassword    bool   `json:"has_password"`
	Insecure       bool   `json:"insecure"`
	DefaultProject string `json:"default_project"`
}

func toVO(m *model.Registry) registryVO {
	return registryVO{
		ID:             m.ID,
		Name:           m.Name,
		Host:           m.Host,
		Type:           m.Type,
		Username:       m.Username,
		HasPassword:    m.PasswordEnc != "",
		Insecure:       m.Insecure,
		DefaultProject: m.DefaultProject,
	}
}

// List GET /api/registries
// 仓库不分角色(源/目标在同步任务时选择),故不再接受 role 过滤参数。
func (h *RegistryHandler) List(c *gin.Context) {
	q := h.DB.Model(&model.Registry{}).Order("id DESC")
	var list []model.Registry
	if err := q.Find(&list).Error; err != nil {
		serverErr(c, "查询仓库失败")
		return
	}
	vos := make([]registryVO, 0, len(list))
	for i := range list {
		vos = append(vos, toVO(&list[i]))
	}
	ok(c, vos)
}

// Create POST /api/registries
func (h *RegistryHandler) Create(c *gin.Context) {
	var dto registryDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		badReq(c, "参数不完整:name/host 必填")
		return
	}
	m, err := dto.toModel(nil)
	if err != nil {
		serverErr(c, "加密密码失败")
		return
	}
	if m.Host == "" {
		badReq(c, "仓库地址无效")
		return
	}
	if err := h.DB.Create(m).Error; err != nil {
		badReq(c, "创建失败,名称或地址可能重复")
		return
	}
	ok(c, toVO(m))
}

// Update PUT /api/registries/:id
func (h *RegistryHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var existing model.Registry
	if err := h.DB.First(&existing, id).Error; err != nil {
		errResp(c, 404, "仓库不存在")
		return
	}
	var dto registryDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		badReq(c, "参数不完整")
		return
	}
	m, err := dto.toModel(&existing)
	if err != nil {
		serverErr(c, "加密密码失败")
		return
	}
	if m.Host == "" {
		badReq(c, "仓库地址无效")
		return
	}
	if err := h.DB.Save(m).Error; err != nil {
		badReq(c, "更新失败,名称或地址可能重复")
		return
	}
	ok(c, toVO(m))
}

// Delete DELETE /api/registries/:id
func (h *RegistryHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.DB.Delete(&model.Registry{}, id).Error; err != nil {
		serverErr(c, "删除失败")
		return
	}
	ok(c, gin.H{"message": "已删除"})
}

// Test POST /api/registries/:id/test
//
// 走标准 OCI/Docker Registry v2 流程(GET /v2/ + Bearer token)探测连通性与凭证,
// 对 Harbor / ACR / DockerHub / 通用 registry 全通用。
func (h *RegistryHandler) Test(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var m model.Registry
	if err := h.DB.First(&m, id).Error; err != nil {
		errResp(c, 404, "仓库不存在")
		return
	}
	pass := ""
	if m.PasswordEnc != "" {
		pass, _ = crypto.Decrypt(m.PasswordEnc)
	}
	result := registry.Probe(c.Request.Context(), m.Host, m.Username, pass, m.Insecure)
	ok(c, result) // 始终 200,由前端根据 ok 字段展示红/绿
}

// RegisterRoutes 注册受 AuthRequired 保护的 registry 路由。
func (h *RegistryHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/registries")
	g.Use(middleware.AuthRequired())
	g.GET("", h.List)
	g.POST("", h.Create)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.POST("/:id/test", h.Test)
}
