package api

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/chart"
	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
)

// 单次上传的数量与体积上限,防止误操作把服务打满。
const (
	maxChartFilesPerUpload = 100
	maxChartFileSize       = 1 << 30 // 1GB
	pushTimeout            = 10 * time.Minute
)

// ChartUploadHandler 处理 chart 包上传(浏览器文件 / 服务器路径)与上传记录查询。
type ChartUploadHandler struct {
	DB *gorm.DB
}

func NewChartUploadHandler(db *gorm.DB) *ChartUploadHandler { return &ChartUploadHandler{DB: db} }

// invalidFile 描述一个被跳过的非法文件(文件名 + 原因),随创建结果一起返回前端。
type invalidFile struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// preparedFile 是通过校验与解析、待创建记录的 chart 文件。
type preparedFile struct {
	path string
	name string
	meta *chart.Meta
	size int64
}

// UploadFiles POST /api/charts/upload-files (multipart: repo_id + files[])
//
// 浏览器选择的 tgz 文件:先落临时目录并解析 Chart.yaml,非法文件跳过并说明原因,
// 合法文件创建 pending 记录后异步推送。
func (h *ChartUploadHandler) UploadFiles(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		badReq(c, "读取上传表单失败: "+err.Error())
		return
	}
	repoID, _ := strconv.ParseUint(c.PostForm("repo_id"), 10, 64)
	files := form.File["files"]
	if repoID == 0 || len(files) == 0 {
		badReq(c, "请选择目标仓库与至少一个 chart 文件")
		return
	}
	if len(files) > maxChartFilesPerUpload {
		badReq(c, fmt.Sprintf("单次最多上传 %d 个文件,请分批提交", maxChartFilesPerUpload))
		return
	}
	repo, ok := h.loadRepo(c, uint(repoID))
	if !ok {
		return
	}

	tmpDir, err := os.MkdirTemp("", "irs-chart-")
	if err != nil {
		serverErr(c, "创建临时目录失败")
		return
	}

	var valid []preparedFile
	var invalid []invalidFile
	for _, fh := range files {
		if fh.Size > maxChartFileSize {
			invalid = append(invalid, invalidFile{fh.Filename, "文件超过 1GB 上限"})
			continue
		}
		// 文件名只留 base,防止伪造路径穿越临时目录。
		name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
		dst := filepath.Join(tmpDir, name)
		if err := saveMultipartFile(fh, dst); err != nil {
			invalid = append(invalid, invalidFile{fh.Filename, "保存文件失败: " + err.Error()})
			continue
		}
		meta, size, err := chart.ParseFile(dst)
		if err != nil {
			invalid = append(invalid, invalidFile{fh.Filename, err.Error()})
			_ = os.Remove(dst)
			continue
		}
		valid = append(valid, preparedFile{path: dst, name: name, meta: meta, size: size})
	}

	ids := h.createRecords(repo, valid, true)
	if len(ids) == 0 {
		_ = os.RemoveAll(tmpDir) // 全部非法,清掉刚建的目录
	}

	created := make([]gin.H, 0, len(ids))
	for _, id := range ids {
		created = append(created, gin.H{"id": id})
	}
	okWith(c, gin.H{"created": created, "invalid": invalid})
	if len(ids) > 0 {
		go h.runUploads(ids, tmpDir)
	}
}

// UploadPaths POST /api/charts/upload-paths (JSON: repo_id + paths[])
//
// 手动填写服务端路径:每条可以是 tgz 文件或目录(目录则扫描其中 *.tgz,不递归)。
func (h *ChartUploadHandler) UploadPaths(c *gin.Context) {
	var req struct {
		RepoID uint     `json:"repo_id" binding:"required"`
		Paths  []string `json:"paths" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badReq(c, "参数不完整")
		return
	}
	repo, ok := h.loadRepo(c, req.RepoID)
	if !ok {
		return
	}

	// 展开目录为具体 tgz 文件。
	var candidates []string
	var invalid []invalidFile
	for _, p := range req.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			invalid = append(invalid, invalidFile{p, "路径不存在或不可访问"})
			continue
		}
		if !st.IsDir() {
			candidates = append(candidates, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			invalid = append(invalid, invalidFile{p, "读取目录失败: " + err.Error()})
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".tgz") {
				continue
			}
			candidates = append(candidates, filepath.Join(p, e.Name()))
		}
	}
	sort.Strings(candidates)
	if len(candidates) > maxChartFilesPerUpload {
		badReq(c, fmt.Sprintf("共找到 %d 个 tgz,超过单次 %d 个上限,请分批提交", len(candidates), maxChartFilesPerUpload))
		return
	}

	var valid []preparedFile
	for _, p := range candidates {
		if !strings.HasSuffix(strings.ToLower(p), ".tgz") {
			invalid = append(invalid, invalidFile{p, "不是 .tgz 文件"})
			continue
		}
		meta, size, err := chart.ParseFile(p)
		if err != nil {
			invalid = append(invalid, invalidFile{p, err.Error()})
			continue
		}
		valid = append(valid, preparedFile{path: p, name: filepath.Base(p), meta: meta, size: size})
	}

	ids := h.createRecords(repo, valid, false)

	created := make([]gin.H, 0, len(ids))
	for _, id := range ids {
		created = append(created, gin.H{"id": id})
	}
	okWith(c, gin.H{"created": created, "invalid": invalid})
	if len(ids) > 0 {
		go h.runUploads(ids, "")
	}
}

// saveMultipartFile 把 multipart 文件头落到磁盘。
func saveMultipartFile(fh *multipart.FileHeader, dst string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// List GET /api/charts/uploads?status=&page=&size=
func (h *ChartUploadHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	status := c.Query("status")

	q := h.DB.Model(&model.ChartUpload{}).Order("id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var list []model.ChartUpload
	if err := q.Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		serverErr(c, "查询上传记录失败")
		return
	}
	vos := make([]gin.H, 0, len(list))
	for i := range list {
		vos = append(vos, uploadVO(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"data": vos, "total": total, "page": page, "size": size})
}

// Retry POST /api/charts/uploads/:id/retry
// 仅允许失败记录重试;源文件已消失(如容器重启清掉临时文件)时提示重新上传。
func (h *ChartUploadHandler) Retry(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var u model.ChartUpload
	if err := h.DB.First(&u, id).Error; err != nil {
		errResp(c, 404, "记录不存在")
		return
	}
	if u.Status != model.TaskStatusFailed {
		badReq(c, "只有失败的记录可以重试")
		return
	}
	if _, err := os.Stat(u.FilePath); err != nil {
		badReq(c, "源文件已不存在(临时文件可能已被清理),请重新上传")
		return
	}
	h.DB.Model(&u).Updates(map[string]any{"status": model.TaskStatusPending, "error": "", "finished_at": nil})
	go h.runUploads([]uint{u.ID}, "")
	okWith(c, gin.H{"message": "已重新入队"})
}

// loadRepo 校验并加载目标 chart 仓库,失败时已写响应,返回 ok=false。
func (h *ChartUploadHandler) loadRepo(c *gin.Context, id uint) (*model.ChartRepo, bool) {
	var repo model.ChartRepo
	if err := h.DB.First(&repo, id).Error; err != nil {
		errResp(c, 404, "chart 仓库不存在")
		return nil, false
	}
	return &repo, true
}

// createRecords 批量创建 pending 记录,返回记录 ID 列表。
// isTemp 标记浏览器上传的临时文件(成功后删除,失败保留以便重试)。
func (h *ChartUploadHandler) createRecords(repo *model.ChartRepo, prepared []preparedFile, isTemp bool) []uint {
	target := chartRepoTarget(repo)
	ids := make([]uint, 0, len(prepared))
	for _, p := range prepared {
		u := model.ChartUpload{
			ChartRepoID:  repo.ID,
			RepoName:     repo.Name,
			RepoType:     repo.Type,
			TargetRef:    target.Ref(p.meta.Name, p.meta.Version),
			FileName:     p.name,
			FilePath:     p.path,
			IsTemp:       isTemp,
			ChartName:    p.meta.Name,
			ChartVersion: p.meta.Version,
			Size:         p.size,
			Status:       model.TaskStatusPending,
		}
		if err := h.DB.Create(&u).Error; err != nil {
			continue
		}
		ids = append(ids, u.ID)
	}
	return ids
}

// runUploads 串行推送一批记录;tmpDir 非空时结束后尝试清理(仅剩空目录时成功)。
func (h *ChartUploadHandler) runUploads(ids []uint, tmpDir string) {
	for _, id := range ids {
		h.runOne(id)
	}
	if tmpDir != "" {
		_ = os.Remove(tmpDir)
	}
}

// runOne 执行单条上传记录:置 running → 读文件 → 推送 → 写终态。
func (h *ChartUploadHandler) runOne(id uint) {
	db := h.DB
	var u model.ChartUpload
	if err := db.First(&u, id).Error; err != nil {
		return
	}
	var repo model.ChartRepo
	if err := db.First(&repo, u.ChartRepoID).Error; err != nil {
		h.finish(id, model.TaskStatusFailed, "", "chart 仓库配置已被删除,无法推送")
		return
	}

	db.Model(&model.ChartUpload{}).Where("id = ?", id).
		Updates(map[string]any{"status": model.TaskStatusRunning, "error": ""})

	data, err := os.ReadFile(u.FilePath)
	if err != nil {
		h.finish(id, model.TaskStatusFailed, "", "读取文件失败(服务器路径不存在或临时文件已清理): "+err.Error())
		return
	}
	meta, err := chart.ParseBytes(data)
	if err != nil {
		h.finish(id, model.TaskStatusFailed, "", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	res, err := chart.Push(ctx, chartRepoTarget(&repo), meta, data, u.FileName)
	if err != nil {
		h.finish(id, model.TaskStatusFailed, "", err.Error())
		return
	}
	h.finish(id, model.TaskStatusSuccess, res.Digest, "")
	// 浏览器上传的临时文件推送成功后即删;失败的保留,便于用户修复仓库配置后重试。
	if u.IsTemp {
		_ = os.Remove(u.FilePath)
	}
}

// finish 写入记录终态。
func (h *ChartUploadHandler) finish(id uint, status, digest, errMsg string) {
	now := time.Now()
	updates := map[string]any{"status": status, "finished_at": now, "error": errMsg}
	if digest != "" {
		updates["digest"] = digest
	}
	if err := h.DB.Model(&model.ChartUpload{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		fmt.Printf("[chart] 更新上传记录 %d 终态失败: %v\n", id, err)
	}
}

// uploadVO 构造上传记录视图(不暴露服务端文件路径)。
func uploadVO(u *model.ChartUpload) gin.H {
	return gin.H{
		"id":            u.ID,
		"chart_repo_id": u.ChartRepoID,
		"repo_name":     u.RepoName,
		"repo_type":     u.RepoType,
		"target_ref":    u.TargetRef,
		"file_name":     u.FileName,
		"chart_name":    u.ChartName,
		"chart_version": u.ChartVersion,
		"size":          u.Size,
		"status":        u.Status,
		"error":         u.Error,
		"digest":        u.Digest,
		"created_at":    u.CreatedAt,
		"finished_at":   u.FinishedAt,
	}
}

// okWith 与 helpers.ok 相同;独立命名避免与函数内的 ok 布尔变量冲突。
func okWith(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// RegisterRoutes 注册受保护的 chart 上传路由。
func (h *ChartUploadHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/charts")
	g.Use(middleware.AuthRequired())
	g.POST("/upload-files", h.UploadFiles)
	g.POST("/upload-paths", h.UploadPaths)
	g.GET("/uploads", h.List)
	g.POST("/uploads/:id/retry", h.Retry)
}
