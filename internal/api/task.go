package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
//
// SourceRegistryID 为 0 表示「自动匹配源」:按每条镜像地址的主机名到
// 「源镜像组」(InSourceGroup=true 的仓库)里匹配凭证;docker.io 无匹配时匿名拉取。
// 浏览目录模式必须显式指定 SourceRegistryID。
// TargetRegistryIDs 支持多目标(1..10),所有目标共用模式/project/架构。
type createTaskRequest struct {
	SourceRegistryID  uint     `json:"source_registry_id"`
	TargetRegistryIDs []uint   `json:"target_registry_ids" binding:"required"`
	Mode              string   `json:"mode" binding:"required"`
	TargetProject     string   `json:"target_project"`
	Arch              string   `json:"arch"` // amd64 / arm64 / all;空按 amd64
	Refs              []string `json:"refs" binding:"required"`
}

// maxRefsPerTask 限制单任务镜像条数,防止误粘贴超大清单把任务明细撑爆。
const maxRefsPerTask = 500

// 目标数量与「镜像 × 目标」展开后的明细总数上限。
const (
	maxTargetsPerTask = 10
	maxItemsPerTask   = 2000
)

// Create POST /api/tasks
//
// 校验源/目标仓库 → 按模式解析每条 ref(自动匹配模式下按主机名分配源凭证)
// → 展开为「镜像 × 目标」明细落库 → 入队执行。
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
	if len(req.Refs) > maxRefsPerTask {
		badReq(c, fmt.Sprintf("单次任务最多 %d 条镜像,请分批提交", maxRefsPerTask))
		return
	}

	// 校验目标仓库(去重 + 限量)。
	targetIDs := make([]uint, 0, len(req.TargetRegistryIDs))
	seenTarget := make(map[uint]bool)
	for _, id := range req.TargetRegistryIDs {
		if id == 0 || seenTarget[id] {
			continue
		}
		seenTarget[id] = true
		targetIDs = append(targetIDs, id)
	}
	if len(targetIDs) == 0 {
		badReq(c, "请至少选择一个目标仓库")
		return
	}
	if len(targetIDs) > maxTargetsPerTask {
		badReq(c, fmt.Sprintf("单次任务最多 %d 个目标仓库", maxTargetsPerTask))
		return
	}
	targets := make([]model.Registry, 0, len(targetIDs))
	for _, id := range targetIDs {
		var dst model.Registry
		if err := h.DB.First(&dst, id).Error; err != nil {
			badReq(c, fmt.Sprintf("目标仓库 #%d 不存在", id))
			return
		}
		targets = append(targets, dst)
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

	// 解析源:显式指定(浏览目录)或按主机名自动匹配源镜像组。
	type refSource struct {
		sourceID uint   // 匹配到的源仓库 ID;0 表示匿名
		host     string // 匿名时的仓库主机名(目前仅 docker.io)
	}

	// 先把 refs 规整为去重后的有序列表,源解析结果按 ref 缓存。
	// 规整化:剥掉 docker:// 与 http(s):// 前缀(http:// 仅用于标识明文 HTTP 仓库,
	// skopeo 对 --tls-verify=false 的 HTTP registry 会自动回退,引用本身不能带 scheme)。
	uniqueRefs := make([]string, 0, len(req.Refs))
	seenRef := make(map[string]bool, len(req.Refs))
	for _, ref := range req.Refs {
		ref = strings.TrimSpace(ref)
		ref = strings.TrimPrefix(ref, "docker://")
		ref = strings.TrimPrefix(ref, "https://")
		ref = strings.TrimPrefix(ref, "http://")
		if ref == "" || seenRef[ref] {
			continue
		}
		seenRef[ref] = true
		uniqueRefs = append(uniqueRefs, ref)
	}

	refSources := make(map[string]refSource, len(uniqueRefs))
	if req.SourceRegistryID > 0 {
		var src model.Registry
		if err := h.DB.First(&src, req.SourceRegistryID).Error; err != nil {
			badReq(c, "源仓库不存在")
			return
		}
		if seenTarget[req.SourceRegistryID] {
			badReq(c, "源和目标仓库不能相同")
			return
		}
		for _, ref := range uniqueRefs {
			refSources[ref] = refSource{sourceID: req.SourceRegistryID}
		}
	} else {
		srcMap, missing := h.matchSourceGroup(uniqueRefs)
		if len(missing) > 0 {
			badReq(c, "以下镜像仓库未加入源镜像组,请在「仓库管理」中开启: "+strings.Join(missing, "、"))
			return
		}
		for _, ref := range uniqueRefs {
			host, _ := skopeo.ParseSource(ref)
			rs := refSource{}
			if id, ok := srcMap[strings.ToLower(host)]; ok {
				if seenTarget[id] {
					badReq(c, fmt.Sprintf("镜像 %q 的源仓库与目标仓库相同,请调整", ref))
					return
				}
				rs.sourceID = id
			} else {
				// 匹配不到且是 docker.io → 匿名拉取公开镜像。
				rs.host = host
			}
			refSources[ref] = rs
		}
	}

	// 目标 project:多目标共用一套。模式①②必填(不再回退单个目标的默认值,避免歧义)。
	project := strings.TrimSpace(req.TargetProject)
	if req.Mode != model.ModeReplaceHost && project == "" {
		badReq(c, "该同步模式需要指定目标 project")
		return
	}

	// 展开「镜像 × 目标」明细(目标内层,同一镜像的各目标明细相邻;
	// runner 按目标分组串行执行,顺序无关紧要)。
	type parsedItem struct {
		sourceID uint
		host     string
		targetID uint
		src, dst string
	}
	items := make([]parsedItem, 0, len(uniqueRefs)*len(targets))
	for _, ref := range uniqueRefs {
		rs := refSources[ref]
		for ti := range targets {
			target, err := skopeo.ResolveTarget(ref, targets[ti].Host, req.Mode, project)
			if err != nil {
				badReq(c, fmt.Sprintf("解析镜像 %q 失败: %v", ref, err))
				return
			}
			items = append(items, parsedItem{sourceID: rs.sourceID, host: rs.host, targetID: targets[ti].ID, src: ref, dst: target})
		}
	}
	if len(items) == 0 {
		badReq(c, "没有有效的镜像")
		return
	}
	if len(items) > maxItemsPerTask {
		badReq(c, fmt.Sprintf("展开为 %d 条明细,超过单次 %d 条上限,请减少镜像或目标数量", len(items), maxItemsPerTask))
		return
	}

	// 落库。任务级 Source/Target 保留:浏览目录模式为真实值;
	// 自动匹配模式 Source=0,Target=第一个目标(旧明细回退与列表展示兼容)。
	t := &model.SyncTask{
		SourceRegistryID: req.SourceRegistryID,
		TargetRegistryID: targetIDs[0],
		Mode:             req.Mode,
		TargetProject:    project,
		Arch:             arch,
		Status:           model.TaskStatusPending,
		Total:            len(items),
		CreatedBy:        middleware.UserID(c),
	}
	for _, it := range items {
		t.Items = append(t.Items, model.SyncTaskItem{
			SourceRegistryID: it.sourceID,
			TargetRegistryID: it.targetID,
			SourceHost:       it.host,
			SourceRef:        it.src,
			TargetRef:        it.dst,
			Status:           model.ItemStatusPending,
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

// matchSourceGroup 提取 refs 中出现的主机名,与「源镜像组」仓库按 host 匹配。
// 返回 host(小写)-> registryID 映射,以及没有匹配配置且不是 docker.io 的主机名列表。
// 匹配键统一剥掉 http(s):// 前缀(仓库配置可能带 scheme 标识明文 HTTP)。
func (h *TaskHandler) matchSourceGroup(refs []string) (map[string]uint, []string) {
	hosts := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if host, _ := skopeo.ParseSource(ref); host != "" {
			hosts[strings.ToLower(host)] = true
		}
	}
	var group []model.Registry
	h.DB.Where("in_source_group = ?", true).Find(&group)

	matched := make(map[string]uint, len(group))
	for i := range group {
		matched[strings.ToLower(skopeo.StripScheme(group[i].Host))] = group[i].ID
	}

	var missing []string
	for host := range hosts {
		if _, ok := matched[host]; !ok && host != "docker.io" {
			missing = append(missing, host)
		}
	}
	sort.Strings(missing)
	return matched, missing
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
// 先订阅再查库:若任务恰好在「查快照」与「订阅」两步之间结束,结束事件已广播完,
// 后订阅的客户端将永远收不到 task_finished;先订阅则该窗口内的事件进缓冲,
// 快照也会反映终态,两条路径都能正确收尾。客户端断开时自动清理订阅。
//
// 运行中任务:SSE 重连(如页面刷新)时,先回放 Manager 内存中的事件历史
// (任务开始、逐条进度日志、已完成条目的结果),再转发实时事件——
// 否则刷新后实时日志清空,只能看到新产生的事件。
// 已结束任务:历史已随任务结束清除,改从数据库回放明细(携带真实时间)。
func (h *TaskHandler) Stream(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)

	// 1. 先订阅,消除与任务结束广播之间的竞态;同时取走历史事件快照。
	subID, ch, hist := task.Instance().Subscribe(uint(id))
	defer task.Instance().Unsubscribe(uint(id), subID)

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
	// 然后结束流。回放事件携带各自的真实执行时间,前端按事件时间显示而非页面打开时间。
	if t.Status == model.TaskStatusSuccess || t.Status == model.TaskStatusFailed || t.Status == model.TaskStatusCanceled {
		startedAt := time.Now()
		if t.StartedAt != nil {
			startedAt = *t.StartedAt
		}
		writeSSE(task.Event{Type: task.EventTaskStarted, TaskID: uint(id), Time: startedAt, Data: map[string]any{"total": len(t.Items)}})
		for _, it := range t.Items {
			evt := task.Event{
				ItemID:    it.ID,
				SourceRef: it.SourceRef,
				TargetRef: it.TargetRef,
				Time:      itemEventTime(it.StartedAt, it.FinishedAt, startedAt),
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
		finishedAt := time.Now()
		if t.FinishedAt != nil {
			finishedAt = *t.FinishedAt
		}
		writeSSE(task.Event{Type: task.EventTaskFinished, TaskID: uint(id), Time: finishedAt,
			Data: map[string]any{
				"status":    t.Status,
				"succeeded": t.Succeeded,
				"failed":    t.Failed,
				"skipped":   t.Skipped,
			}})
		return
	}

	// 2. 运行中任务:回放历史事件(任务开始以来已广播的全部事件,含真实时间),
	// 让刷新后重连的客户端续上实时日志,再进入实时转发。
	for _, ev := range hist {
		writeSSE(ev)
	}

	// 3. 转发实时事件直到任务结束。
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
		"id":                 t.ID,
		"source_registry_id": t.SourceRegistryID,
		"target_registry_id": t.TargetRegistryID,
		"mode":               t.Mode,
		"target_project":     t.TargetProject,
		"arch":               t.Arch,
		"total":              t.Total,
		"succeeded":          t.Succeeded,
		"failed":             t.Failed,
		"skipped":            t.Skipped,
		"status":             t.Status,
		"error":              t.Error,
		"created_at":         t.CreatedAt,
		"started_at":         t.StartedAt,
		"finished_at":        t.FinishedAt,
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
				"id":                 it.ID,
				"source_registry_id": it.SourceRegistryID,
				"target_registry_id": it.TargetRegistryID,
				"source_host":        it.SourceHost,
				"source_ref":         it.SourceRef,
				"target_ref":         it.TargetRef,
				"status":             it.Status,
				"error":              it.Error,
				"digest":             it.Digest,
				"started_at":         it.StartedAt,
				"finished_at":        it.FinishedAt,
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

// nowPtr 返回指向当前时间的指针(用于可空时间字段)。
func nowPtr() *time.Time {
	t := time.Now()
	return &t
}

// itemEventTime 取回放事件应显示的时间:优先明细的结束/开始时间,
// 均缺失时(未执行的明细)回退任务开始时间。
func itemEventTime(started, finished *time.Time, fallback time.Time) time.Time {
	if finished != nil {
		return *finished
	}
	if started != nil {
		return *started
	}
	return fallback
}
