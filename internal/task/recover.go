package task

import (
	"log"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/store"
)

// RecoverStuckTasks 服务启动时恢复上次异常中断的任务:
//   - running:原 worker 已随进程消失,置 failed 并把未完成明细标记为 failed。
//   - pending:任务已创建但未执行(队列在内存中,重启即丢),重新入队。
//
// 必须在 Instance() 之后调用,否则重新入队的任务没有 worker 消费。
func RecoverStuckTasks() {
	db := store.DB
	now := time.Now()

	var runningIDs []uint
	db.Table("sync_tasks").Where("status = ?", model.TaskStatusRunning).Pluck("id", &runningIDs)
	for _, id := range runningIDs {
		if err := db.Table("sync_task_items").
			Where("task_id = ? AND status IN ?", id, []string{model.ItemStatusPending, model.ItemStatusRunning}).
			Updates(map[string]any{"status": model.ItemStatusFailed, "finished_at": now, "error": "服务重启,任务中断"}).Error; err != nil {
			log.Printf("[task] 恢复任务 %d 时标记明细失败: %v", id, err)
		}
		if err := db.Table("sync_tasks").Where("id = ?", id).Updates(map[string]any{
			"status":      model.TaskStatusFailed,
			"error":       "服务重启,任务中断",
			"finished_at": now,
		}).Error; err != nil {
			log.Printf("[task] 恢复任务 %d 时更新终态失败: %v", id, err)
			continue
		}
		refreshTaskCounts(id)
	}
	if len(runningIDs) > 0 {
		log.Printf("[task] 已把 %d 个运行中任务标记为失败(服务重启中断)", len(runningIDs))
	}

	var pendingIDs []uint
	db.Table("sync_tasks").
		Where("status = ?", model.TaskStatusPending).
		Order("id ASC").Pluck("id", &pendingIDs)
	for _, id := range pendingIDs {
		Instance().Enqueue(id)
	}
	if len(pendingIDs) > 0 {
		log.Printf("[task] 已重新入队 %d 个待执行任务", len(pendingIDs))
	}
}

// refreshTaskCounts 按明细实际状态回填任务的 succeeded/failed/skipped 计数。
func refreshTaskCounts(taskID uint) {
	var rows []struct {
		Status string
		N      int64
	}
	if err := store.DB.Table("sync_task_items").
		Select("status, COUNT(*) AS n").
		Where("task_id = ?", taskID).
		Group("status").Scan(&rows).Error; err != nil {
		log.Printf("[task] 统计任务 %d 明细失败: %v", taskID, err)
		return
	}
	counts := map[string]int64{}
	for _, r := range rows {
		counts[r.Status] = r.N
	}
	store.DB.Table("sync_tasks").Where("id = ?", taskID).Updates(map[string]any{
		"succeeded": counts[model.ItemStatusSuccess],
		"failed":    counts[model.ItemStatusFailed],
		"skipped":   counts[model.ItemStatusSkipped],
	})
}
