package task

import (
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"images-repo-sync/internal/skopeo"
	"images-repo-sync/internal/store"
)

// worker 串行消费任务队列并执行。
// SQLite 写串行 + skopeo 受 IO 限制,串行执行更稳定;后续可改并发池。
func (m *Manager) worker() {
	for taskID := range m.queue {
		if err := m.runTask(taskID); err != nil {
			log.Printf("[task] 任务 %d 执行失败: %v", taskID, err)
		}
	}
}

// runTask 执行单个同步任务:加载任务和明细 → 逐条 skopeo copy → 更新状态。
func (m *Manager) runTask(taskID uint) error {
	db := store.DB

	ctx, cancel := context.WithCancel(context.Background())
	m.RegisterCancel(taskID, cancel)
	defer func() {
		cancel()
		m.ClearCancel(taskID)
	}()

	// 1. 加载任务及明细,标记 running。
	var items []syncTaskItemWithRefs
	var arch string // 从任务读取的目标架构,稍后传给 skopeo.Copy
	if err := db.Transaction(func(tx *gorm.DB) error {
		var t taskLoader
		if err := tx.Table("sync_tasks").Where("id = ?", taskID).First(&t).Error; err != nil {
			return err
		}
		arch = t.Arch
		if err := tx.Table("sync_task_items").
			Select("id, source_ref, target_ref").
			Where("task_id = ?", taskID).Order("id ASC").
			Scan(&items).Error; err != nil {
			return err
		}
		now := time.Now()
		return tx.Table("sync_tasks").Where("id = ?", taskID).Updates(map[string]any{
			"status":     "running",
			"started_at": now,
			"total":      len(items),
		}).Error
	}); err != nil {
		return err
	}

	// 加载源/目标 registry 凭证(两次简单查询,各自解密密码)。
	srcCreds, err := loadRegistryCreds(db, "sync_tasks", "source_registry_id", taskID)
	if err != nil {
		return fmt.Errorf("加载源仓库: %w", err)
	}
	dstCreds, err := loadRegistryCreds(db, "sync_tasks", "target_registry_id", taskID)
	if err != nil {
		return fmt.Errorf("加载目标仓库: %w", err)
	}

	// 生成源/目标临时 auth 文件。
	srcAuth, err := skopeo.WriteAuthFile(srcCreds.Host, srcCreds.User, srcCreds.Pass)
	if err != nil {
		return fmt.Errorf("生成源凭证文件: %w", err)
	}
	defer skopeo.CleanupAuthFiles(srcAuth)
	dstAuth, err := skopeo.WriteAuthFile(dstCreds.Host, dstCreds.User, dstCreds.Pass)
	if err != nil {
		return fmt.Errorf("生成目标凭证文件: %w", err)
	}
	defer skopeo.CleanupAuthFiles(dstAuth)

	m.emit(taskID, EventTaskStarted, Event{
		Data: map[string]any{"total": len(items)},
	})

	// 2. 逐条执行 copy。
	succeeded, failed, skipped := 0, 0, 0
	for _, it := range items {
		// 每条 item 用独立 ctx,但继承任务级 cancel。
		itemCtx := ctx
		_ = itemCtx

		m.emit(taskID, EventItemStarted, Event{
			ItemID:    it.ID,
			SourceRef: it.SourceRef,
			TargetRef: it.TargetRef,
		})
		updateItem(db, it.ID, map[string]any{"status": "running", "started_at": time.Now()})

		// 广播日志行。
		res := skopeo.Copy(ctx, skopeo.CopyOptions{
			SrcRef:          it.SourceRef,
			SrcAuthFile:     srcAuth,
			SrcInsecure:     srcCreds.Insecure,
			DstRef:          it.TargetRef,
			DstAuthFile:     dstAuth,
			DstInsecure:     dstCreds.Insecure,
			PreserveDigests: true,
			Arch:            arch,
		}, func(line string) {
			m.emit(taskID, EventItemProgress, Event{
				ItemID: it.ID, Message: line,
			})
		})

		finishedAt := time.Now()
		if res.OK {
			succeeded++
			digest := ""
			// 取目标 digest(失败不影响主流程)。
			if d, err := skopeo.InspectDigest(ctx, it.TargetRef, dstCreds.Host, dstCreds.User, dstCreds.Pass, dstCreds.Insecure); err == nil {
				digest = d
			}
			updateItem(db, it.ID, map[string]any{
				"status": "success", "digest": digest, "finished_at": finishedAt, "error": "",
			})
			m.emit(taskID, EventItemSuccess, Event{
				ItemID: it.ID, SourceRef: it.SourceRef, TargetRef: it.TargetRef, Data: map[string]any{"digest": digest},
			})
		} else if ctx.Err() != nil {
			// 任务被取消:当前 item 置 skipped,并跳出循环,剩余未执行 item 在循环外统一标记。
			skipped++
			updateItem(db, it.ID, map[string]any{"status": "skipped", "finished_at": finishedAt})
			m.emit(taskID, EventItemFailed, Event{ItemID: it.ID, Message: "已取消"})
			break
		} else {
			failed++
			updateItem(db, it.ID, map[string]any{
				"status": "failed", "error": res.StderrTail, "finished_at": finishedAt,
			})
			m.emit(taskID, EventItemFailed, Event{
				ItemID: it.ID, SourceRef: it.SourceRef, TargetRef: it.TargetRef, Message: res.StderrTail,
			})
		}
	}

	// 取消时,把 break 之后剩余未执行的 item 统一标记为 skipped。
	if ctx.Err() != nil {
		now := time.Now()
		if err := db.Table("sync_task_items").
			Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).
			Updates(map[string]any{"status": "skipped", "finished_at": now}).Error; err != nil {
			log.Printf("[task] 标记剩余 item 为 skipped 失败: %v", err)
		}
		// 统计被跳过的数量(查询而不是猜)。
		var skipCount int64
		db.Table("sync_task_items").Where("task_id = ? AND status = 'skipped'", taskID).Count(&skipCount)
		skipped = int(skipCount)
	}

	// 3. 更新任务终态。
	finalStatus := "success"
	taskErrMsg := ""
	if ctx.Err() != nil {
		finalStatus = "canceled"
	} else if failed > 0 {
		finalStatus = "failed"
		taskErrMsg = fmt.Sprintf("%d 条镜像同步失败", failed)
	}
	now := time.Now()
	if err := db.Table("sync_tasks").Where("id = ?", taskID).Updates(map[string]any{
		"status":      finalStatus,
		"succeeded":   succeeded,
		"failed":      failed,
		"skipped":     skipped,
		"error":       taskErrMsg,
		"finished_at": now,
	}).Error; err != nil {
		log.Printf("[task] 更新任务 %d 终态失败: %v", taskID, err)
	}

	m.emit(taskID, EventTaskFinished, Event{
		Data: map[string]any{
			"status":    finalStatus,
			"succeeded": succeeded,
			"failed":    failed,
			"skipped":   skipped,
		},
	})
	return nil
}

// updateItem 更新某条 item 的字段。
func updateItem(db *gorm.DB, itemID uint, fields map[string]any) {
	if err := db.Table("sync_task_items").Where("id = ?", itemID).Updates(fields).Error; err != nil {
		log.Printf("[task] 更新 item %d 失败: %v", itemID, err)
	}
}
