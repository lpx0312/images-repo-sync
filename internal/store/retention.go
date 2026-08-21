package store

import (
	"context"
	"log"
	"time"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
)

// retentionBatch 每批删除的行数上限,避免单条 DELETE 的 IN 列表超出 SQLite 参数限制。
const retentionBatch = 500

// StartRetention 启动时立即清理一次过期数据,之后每 24 小时一次;ctx 取消时退出。
// 清理范围:超过保留天数的已结束任务(含明细)与登录日志;保留天数配 0 表示不清理。
func StartRetention(ctx context.Context) {
	cleanupExpiredData()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cleanupExpiredData()
		}
	}
}

// cleanupExpiredData 按配置的保留天数清理历史数据。失败仅打印,不影响主流程。
func cleanupExpiredData() {
	if DB == nil {
		return
	}
	cfg := config.AppConfig
	if cfg.TaskRetentionDays > 0 {
		if n := deleteOldTasks(cfg.TaskRetentionDays); n > 0 {
			log.Printf("[store] 已清理 %d 个 %d 天前的历史任务(含明细)", n, cfg.TaskRetentionDays)
		}
		// chart 上传记录与任务共用保留天数。
		if n := deleteOldChartUploads(cfg.TaskRetentionDays); n > 0 {
			log.Printf("[store] 已清理 %d 条 %d 天前的 chart 上传记录", n, cfg.TaskRetentionDays)
		}
	}
	if cfg.LoginLogRetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.LoginLogRetentionDays)
		if n := deleteBatch(func(ids []uint) int64 {
			res := DB.Where("id IN ?", ids).Delete(&model.LoginLog{})
			return res.RowsAffected
		}, func() ([]uint, error) {
			var ids []uint
			err := DB.Model(&model.LoginLog{}).
				Where("created_at < ?", cutoff).
				Limit(retentionBatch).Pluck("id", &ids).Error
			return ids, err
		}); n > 0 {
			log.Printf("[store] 已清理 %d 条 %d 天前的登录日志", n, cfg.LoginLogRetentionDays)
		}
	}
}

// deleteOldTasks 删除超过保留天数的已结束任务(先删明细再删任务,满足外键约束),
// 返回删除的任务数。
func deleteOldTasks(days int) int64 {
	cutoff := time.Now().AddDate(0, 0, -days)
	terminal := []string{model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusCanceled}
	return deleteBatch(
		func(ids []uint) int64 {
			DB.Where("task_id IN ?", ids).Delete(&model.SyncTaskItem{})
			res := DB.Where("id IN ?", ids).Delete(&model.SyncTask{})
			return res.RowsAffected
		},
		func() ([]uint, error) {
			var ids []uint
			err := DB.Model(&model.SyncTask{}).
				Where("status IN ? AND created_at < ?", terminal, cutoff).
				Limit(retentionBatch).Pluck("id", &ids).Error
			return ids, err
		})
}

// deleteOldChartUploads 删除超过保留天数的已结束 chart 上传记录,返回删除条数。
func deleteOldChartUploads(days int) int64 {
	cutoff := time.Now().AddDate(0, 0, -days)
	terminal := []string{model.TaskStatusSuccess, model.TaskStatusFailed}
	return deleteBatch(
		func(ids []uint) int64 {
			res := DB.Where("id IN ?", ids).Delete(&model.ChartUpload{})
			return res.RowsAffected
		},
		func() ([]uint, error) {
			var ids []uint
			err := DB.Model(&model.ChartUpload{}).
				Where("status IN ? AND created_at < ?", terminal, cutoff).
				Limit(retentionBatch).Pluck("id", &ids).Error
			return ids, err
		})
}

// deleteBatch 分批删除:selectFn 取一批待删 id,deleteFn 删除该批并返回删除行数,
// 循环直到取不到新批次。select/delete 出错时停止并返回已完成数。
func deleteBatch(deleteFn func(ids []uint) int64, selectFn func() ([]uint, error)) int64 {
	var total int64
	for {
		ids, err := selectFn()
		if err != nil {
			log.Printf("[store] 清理历史数据查询失败: %v", err)
			break
		}
		if len(ids) == 0 {
			break
		}
		total += deleteFn(ids)
		if len(ids) < retentionBatch {
			break
		}
	}
	return total
}
