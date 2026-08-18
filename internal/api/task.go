package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/skopeo"
	"images-repo-sync/internal/task"
)

// TaskHandler 处理同步任务的创建、查询、SSE 流与取消。
type TaskHandler struct {
	DB *gorm.DB
}

func NewTaskHandler(db *gorm.DB) *TaskHandler { return &TaskHandler{DB: db} }

// createTaskRequest 创建任务的请求体。
type createTaskRequest struct {
	SourceRegistryID uint     `json:"source_registry_id" binding:"required"`
	TargetRegistryID uint     `json:"target_registry_id" binding:"required"`
	Mode             string   `json:"mode" binding:"required"`
	TargetProject    string   `json:"target_project"`
	Arch             string   `json:"arch"` // amd64 / arm64 / all;空按 amd64
	Refs             []string `json:"refs" binding:"required"`
}

// Create POST /api/tasks
//
// 校验源/目标仓库存在 → 按模式解析每条 ref → 落库任务和明细 → 入队执行。
func (h *TaskHandler) Create(c *gin.Context) {
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badReq(c, "参数不完整")
		return
	}
	if len(req.Refs) == 0 {
		badReq(c, "镜像列表不能为空")
		return
	}

	// 校验源/目标仓库存在,并取目标 host 用于解析。
	var src, dst model.Registry
	if err := h.DB.First(&src, req.SourceRegistryID).Error; err != nil {
		badReq(c, "源仓库不存在")
		return
	}
	if err := h.DB.First(&dst, req.TargetRegistryID).Error; err != nil {
		badReq(c, "目标仓库不存在")
		return
	}
	if req.SourceRegistryID == req.TargetRegistryID {
		badReq(c, "源和目标仓库不能相同")
		return
	}

	// 校验架构取值,空值默认 amd64。
	arch := req.Arch
	if arch == "" {
		arch = skopeo.ArchAMD64
	}
	switch arch {
	case skopeo.ArchAMD64, skopeo.ArchARM64, skopeo.ArchAll:
	default:
		badReq(c, "架构参数无效,可选: amd64 / arm64 / all")
		return
	}

	// 解析每条 ref 的目标引用。
	project := req.TargetProject
	if project == "" {
		project = dst.DefaultProject
	}
	type parsedItem struct {
		SourceRef string
		TargetRef string
	}
	items := make([]parsedItem, 0, len(req.Refs))
	seen := make(map[string]bool, len(req.Refs))
	for _, ref := range req.Refs {
		ref = trimSpaces(ref)
		if ref == "" {
			continue
		}
		target, err := skopeo.ResolveTarget(ref, dst.Host, req.Mode, project)
		if err != nil {
			badReq(c, fmt.Sprintf("解析镜像 %q 失败: %v", ref, err))
			return
		}
		if seen[target] {
			continue // 去重
		}
		seen[target] = true
		items = append(items, parsedItem{SourceRef: ref, TargetRef: target})
	}
	if len(items) == 0 {
		badReq(c, "没有有效的镜像")
		return
	}

	// 落库。
	t := &model.SyncTask{
		SourceRegistryID: req.SourceRegistryID,
		TargetRegistryID: req.TargetRegistryID,
		Mode:             req.Mode,
		TargetProject:    project,
		Arch:             arch,
		Status:           model.TaskStatusPending,
		Total:            len(items),
		CreatedBy:        middleware.UserID(c),
	}
	for _, it := range items {
		t.Items = append(t.Items, model.SyncTaskItem{
			SourceRef: it.SourceRef,
			TargetRef: it.TargetRef,
			Status:    model.ItemStatusPending,
		})
	}
	if err := h.DB.Create(t).Error; err != nil {
		serverErr(c, "创建任务失败")
		return
	}

	// 入队执行。
	task.Instance().Enqueue(t.ID)

	// 返回精简视图(含 items)。
	ok(c, h.buildTaskVO(t, true))
}

// List GET /api/tasks?status=&page=&size=
func (h *TaskHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	status := c.Query("status")

	q := h.DB.Model(&model.SyncTask{}).Order("id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var list []model.SyncTask
	if err := q.Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		serverErr(c, "查询任务失败")
		return
	}
	vos := make([]gin.H, 0, len(list))
	for i := range list {
		vos = append(vos, h.buildTaskVO(&list[i], false))
	}
	c.JSON(http.StatusOK, gin.H{"data": vos, "total": total, "page": page, "size": size})
}

// Get GET /api/tasks/:id
func (h *TaskHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var t model.SyncTask
	// Preload 的第二参数必须用函数形式才能加 Order;
	// 字符串形式会被当成 WHERE 条件拼成非法 SQL(AND ORDER BY ...)。
	if err := h.DB.Preload("Items", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("id ASC")
	}).First(&t, id).Error; err != nil {
		errResp(c, 404, "任务不存在")
		return
	}
	ok(c, h.buildTaskVO(&t, true))
}

// Cancel POST /api/tasks/:id/cancel
func (h *TaskHandler) Cancel(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var t model.SyncTask
	if err := h.DB.First(&t, id).Error; err != nil {
		errResp(c, 404, "任务不存在")
		return
	}
	if t.Status != model.TaskStatusRunning && t.Status != model.TaskStatusPending {
		badReq(c, "任务已结束,无法取消")
		return
	}
	if !task.Instance().Cancel(uint(id)) {
		// 可能还在队列里没开始;直接置 canceled。
		now := nowPtr()
		h.DB.Model(&t).Updates(map[string]any{"status": model.TaskStatusCanceled, "finished_at": now, "error": "取消"})
	}
	ok(c, gin.H{"message": "已请求取消"})
}

// Stream GET /api/tasks/:id/stream —— SSE 实时事件流。
//
// 建立连接后,先补发当前任务快照与已有 items 状态(避免漏掉已发生的事件),
// 然后订阅 manager 实时事件并转发。客户端断开时自动清理订阅。
func (h *TaskHandler) Stream(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var t model.SyncTask
	// Preload 用函数形式加 Order,避免字符串条件被误当 WHERE 拼成非法 SQL。
	if err := h.DB.Preload("Items", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("id ASC")
	}).First(&t, id).Error; err != nil {
		errResp(c, 404, "任务不存在")
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	flusher, _ := c.Writer.(http.Flusher)
	writeSSE := func(ev task.Event) {
		payload, _ := json.Marshal(ev)
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Type, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 1. 先补发快照。
	writeSSE(task.Event{
		Type:   "snapshot",
		TaskID: uint(id),
		Data:   h.buildTaskVO(&t, true),
	})

	// 已结束任务:回放每个 item 的结果作为日志(避免快任务跑完后详情页日志为空),
	// 然后结束流。
	if t.Status == model.TaskStatusSuccess || t.Status == model.TaskStatusFailed || t.Status == model.TaskStatusCanceled {
		writeSSE(task.Event{Type: task.EventTaskStarted, TaskID: uint(id), Data: map[string]any{"total": len(t.Items)}})
		for _, it := range t.Items {
			evt := task.Event{
				ItemID:    it.ID,
				SourceRef: it.SourceRef,
				TargetRef: it.TargetRef,
			}
			switch it.Status {
			case model.ItemStatusSuccess:
				evt.Type = task.EventItemSuccess
				evt.Data = map[string]any{"digest": it.Digest}
			case model.ItemStatusFailed:
				evt.Type = task.EventItemFailed
				evt.Message = it.Error
			case model.ItemStatusSkipped:
				evt.Type = task.EventItemFailed
				evt.Message = "跳过"
			default: // pending / running:任务已结束但 item 未执行完(任务级错误中断)
				evt.Type = task.EventItemFailed
				evt.Message = "未执行(任务中断)"
			}
			writeSSE(evt)
		}
		writeSSE(task.Event{Type: task.EventTaskFinished, TaskID: uint(id),
			Data: map[string]any{
				"status":    t.Status,
				"succeeded": t.Succeeded,
				"failed":    t.Failed,
				"skipped":   t.Skipped,
			}})
		return
	}

	// 2. 订阅实时事件。
	subID, ch := task.Instance().Subscribe(uint(id))
	defer task.Instance().Unsubscribe(uint(id), subID)

	// 心跳与事件共用同一 select,避免 goroutine 并发写 c.Writer 导致 SSE 帧损坏。
	// (Gin ResponseWriter 非并发安全,两个 goroutine 交错 Fprintf 会让客户端 JSON 解析失败。)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	notify := c.Request.Context().Done()
	for {
		select {
		case ev, alive := <-ch:
			if !alive {
				return
			}
			writeSSE(ev)
			if ev.Type == task.EventTaskFinished {
				return
			}
		case <-ticker.C:
			// SSE 心跳(注释行),防止反向代理因空闲超时断连。
			fmt.Fprintf(c.Writer, ": ping\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-notify:
			return
		}
	}
}

// buildTaskVO 构造返回前端的任务视图。
func (h *TaskHandler) buildTaskVO(t *model.SyncTask, withItems bool) gin.H {
	vo := gin.H{
		"id":                t.ID,
		"source_registry_id": t.SourceRegistryID,
		"target_registry_id": t.TargetRegistryID,
		"mode":              t.Mode,
		"target_project":    t.TargetProject,
		"arch":              t.Arch,
		"total":             t.Total,
		"succeeded":         t.Succeeded,
		"failed":            t.Failed,
		"skipped":           t.Skipped,
		"status":            t.Status,
		"error":             t.Error,
		"created_at":        t.CreatedAt,
		"started_at":        t.StartedAt,
		"finished_at":       t.FinishedAt,
	}
	if withItems {
		if len(t.Items) == 0 && t.ID > 0 {
			// Get 路径已 Preload;List 路径不需要 items。
			var items []model.SyncTaskItem
			h.DB.Where("task_id = ?", t.ID).Order("id ASC").Find(&items)
			t.Items = items
		}
		itemVOs := make([]gin.H, 0, len(t.Items))
		for _, it := range t.Items {
			itemVOs = append(itemVOs, gin.H{
				"id":          it.ID,
				"source_ref":  it.SourceRef,
				"target_ref":  it.TargetRef,
				"status":      it.Status,
				"error":       it.Error,
				"digest":      it.Digest,
				"started_at":  it.StartedAt,
				"finished_at": it.FinishedAt,
			})
		}
		vo["items"] = itemVOs
	}
	return vo
}

// RegisterRoutes 注册受保护的 task 路由。
func (h *TaskHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/tasks")
	g.Use(middleware.AuthRequired())
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.POST("/:id/cancel", h.Cancel)
	g.GET("/:id/stream", h.Stream)
}

// trimSpaces 去除首尾空白与可能的换行。
func trimSpaces(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

// nowPtr 返回指向当前时间的指针(用于可空时间字段)。
func nowPtr() *time.Time {
	t := time.Now()
	return &t
}
