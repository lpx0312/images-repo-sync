package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/chart"
	"images-repo-sync/internal/crypto"
	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
)

// ChartRepoHandler 处理 chart 仓库配置的 CRUD 与连接测试。
type ChartRepoHandler struct {
	DB *gorm.DB
}

func NewChartRepoHandler(db *gorm.DB) *ChartRepoHandler { return &ChartRepoHandler{DB: db} }

// chartRepoDTO 是创建/更新时的请求体。Password 明文传入,服务端加密存储。
type chartRepoDTO struct {
	Name     string `json:"name" binding:"required"`
	Type     string `json:"type" binding:"required"`
	Host     string `json:"host" binding:"required"`
	Project  string `json:"project"`
	Username string `json:"username"`
	Password string `json:"password"`
	Insecure bool   `json:"insecure"`
}

// toModel 把 DTO 转成模型并加密密码。Password 为空时(更新场景)保留原密码。
// Host 统一过 normalizeChartHost 清洗(去首尾空白与斜杠;http(s):// 前缀保留以区分协议)。
func (d *chartRepoDTO) toModel(existing *model.ChartRepo) (*model.ChartRepo, error) {
	m := &model.ChartRepo{}
	if existing != nil {
		*m = *existing
	}
	m.Name = strings.TrimSpace(d.Name)
	m.Host = normalizeChartHost(d.Host)
	m.Project = strings.Trim(strings.TrimSpace(d.Project), "/")
	m.Username = strings.TrimSpace(d.Username)
	m.Insecure = d.Insecure

	switch d.Type {
	case model.ChartRepoTypeOCI, model.ChartRepoTypeChartMuseum:
		m.Type = d.Type
	default:
		m.Type = model.ChartRepoTypeOCI
	}

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

// normalizeChartHost 清洗用户输入的仓库地址:去掉首尾空白与斜杠。
// 与镜像仓库不同,这里保留 http:// 前缀以支持明文 HTTP 的 ChartMuseum/registry;
// 无前缀或 https:// 一律按 HTTPS。
func normalizeChartHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.Trim(h, "/")
	return h
}

// chartRepoVO 是返回给前端的视图,不含密码。
type chartRepoVO struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Project     string `json:"project"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
	Insecure    bool   `json:"insecure"`
}

func chartRepoToVO(m *model.ChartRepo) chartRepoVO {
	return chartRepoVO{
		ID:          m.ID,
		Name:        m.Name,
		Type:        m.Type,
		Host:        m.Host,
		Project:     m.Project,
		Username:    m.Username,
		HasPassword: m.PasswordEnc != "",
		Insecure:    m.Insecure,
	}
}

// chartRepoTarget 把 ChartRepo 模型解密并转换成 chart.Target。
func chartRepoTarget(m *model.ChartRepo) chart.Target {
	scheme, host := chart.SplitHost(m.Host)
	t := chart.Target{
		Type:     m.Type,
		Scheme:   scheme,
		Host:     host,
		Project:  m.Project,
		Username: m.Username,
		Insecure: m.Insecure,
	}
	if m.PasswordEnc != "" {
		t.Password, _ = crypto.Decrypt(m.PasswordEnc)
	}
	return t
}

// List GET /api/chart-repos
func (h *ChartRepoHandler) List(c *gin.Context) {
	var list []model.ChartRepo
	if err := h.DB.Order("id DESC").Find(&list).Error; err != nil {
		serverErr(c, "查询 chart 仓库失败")
		return
	}
	vos := make([]chartRepoVO, 0, len(list))
	for i := range list {
		vos = append(vos, chartRepoToVO(&list[i]))
	}
	ok(c, vos)
}

// Create POST /api/chart-repos
func (h *ChartRepoHandler) Create(c *gin.Context) {
	var dto chartRepoDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		badReq(c, "参数不完整:name/type/host 必填")
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
	if m.Type == model.ChartRepoTypeOCI && m.Project == "" {
		badReq(c, "OCI 仓库必须填写 chart 项目地址(如 datacenter-test-chart)")
		return
	}
	if err := h.DB.Create(m).Error; err != nil {
		badReq(c, "创建失败,名称可能重复")
		return
	}
	ok(c, chartRepoToVO(m))
}

// Update PUT /api/chart-repos/:id
func (h *ChartRepoHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var existing model.ChartRepo
	if err := h.DB.First(&existing, id).Error; err != nil {
		errResp(c, 404, "chart 仓库不存在")
		return
	}
	var dto chartRepoDTO
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
	if m.Type == model.ChartRepoTypeOCI && m.Project == "" {
		badReq(c, "OCI 仓库必须填写 chart 项目地址(如 datacenter-test-chart)")
		return
	}
	if err := h.DB.Save(m).Error; err != nil {
		badReq(c, "更新失败,名称可能重复")
		return
	}
	ok(c, chartRepoToVO(m))
}

// Delete DELETE /api/chart-repos/:id
// 历史上传记录通过快照字段展示,不受仓库删除影响。
func (h *ChartRepoHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.DB.Delete(&model.ChartRepo{}, id).Error; err != nil {
		serverErr(c, "删除失败")
		return
	}
	ok(c, gin.H{"message": "已删除"})
}

// Test POST /api/chart-repos/:id/test
// OCI 走 /v2/ ping(与推送一致的认证流程),ChartMuseum 走 /health。
func (h *ChartRepoHandler) Test(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var m model.ChartRepo
	if err := h.DB.First(&m, id).Error; err != nil {
		errResp(c, 404, "chart 仓库不存在")
		return
	}
	result := chart.Probe(c.Request.Context(), chartRepoTarget(&m))
	ok(c, result) // 始终 200,由前端根据 ok 字段展示红/绿
}

// RegisterRoutes 注册受 AuthRequired 保护的 chart 仓库路由。
func (h *ChartRepoHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/chart-repos")
	g.Use(middleware.AuthRequired())
	g.GET("", h.List)
	g.POST("", h.Create)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.POST("/:id/test", h.Test)
}
