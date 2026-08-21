package task

import (
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/skopeo"
	"images-repo-sync/internal/store"
)

// worker 消费任务队列并执行(实例数由 TASK_CONCURRENCY 决定,默认 1)。
// skopeo 受 IO 限制,少量并发即可;单个任务内的镜像始终串行复制。
func (m *Manager) worker() {
	for taskID := range m.queue {
		if err := m.runTask(taskID); err != nil {
			log.Printf("[task] 任务 %d 执行失败: %v", taskID, err)
		}
	}
}

// runTask 执行单个同步任务:加载任务和明细 → 逐条 skopeo copy → 更新状态。
func (m *Manager) runTask(taskID uint) (retErr error) {
	db := store.DB

	ctx, cancel := context.WithCancel(context.Background())
	m.RegisterCancel(taskID, cancel)
	defer func() {
		cancel()
		m.ClearCancel(taskID)
	}()

	// 兜底:任何提前 return 的错误路径都把任务置为 failed,避免卡在 pending/running。
	// (正常结束时 retErr 为 nil,且终态已在下方更新,这里不会覆盖。)
	defer func() {
		if retErr == nil {
			return
		}
		now := time.Now()
		// 若任务已被取消(ctx 取消),置 canceled 而非 failed。
		status := model.TaskStatusFailed
		if ctx.Err() != nil {
			status = model.TaskStatusCanceled
		}
		// 任务级失败时,把尚未执行完的 item(pending/running)统一标记为 failed/skipped,
		// 否则前端回放会因 item 停在 pending 而显示"跳过",造成"重试被跳过"的误解。
		itemStatus := model.ItemStatusFailed
		if status == model.TaskStatusCanceled {
			itemStatus = model.ItemStatusSkipped
		}
		if err := db.Table("sync_task_items").
			Where("task_id = ? AND status IN ?", taskID, []string{model.ItemStatusPending, model.ItemStatusRunning}).
			Updates(map[string]any{"status": itemStatus, "finished_at": now, "error": retErr.Error()}).Error; err != nil {
			log.Printf("[task] 兜底标记剩余 item 为 %s 失败(任务 %d): %v", itemStatus, taskID, err)
		}
		if err := db.Table("sync_tasks").Where("id = ?", taskID).Updates(map[string]any{
			"status":      status,
			"error":       retErr.Error(),
			"finished_at": now,
		}).Error; err != nil {
			log.Printf("[task] 兜底置 %s 失败(任务 %d): %v", status, taskID, err)
		}
		m.emit(taskID, EventTaskFinished, Event{
			Data: map[string]any{"status": status, "error": retErr.Error()},
		})
	}()

	// 1. 加载任务及明细。先检查是否已被取消(用户可能在 worker 取到前点取消)。
	var items []syncTaskItemWithRefs
	var arch string // 从任务读取的目标架构,稍后传给 skopeo.Copy
	if err := db.Transaction(func(tx *gorm.DB) error {
		var t taskLoader
		if err := tx.Table("sync_tasks").Where("id = ?", taskID).First(&t).Error; err != nil {
			return err
		}
		// P0-1 修复:若任务已被取消,直接退出,不置 running。
		if t.Status == model.TaskStatusCanceled {
			return errAlreadyCanceled
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
			"status":     model.TaskStatusRunning,
			"started_at": now,
			"total":      len(items),
		}).Error
	}); err != nil {
		if err == errAlreadyCanceled {
			return nil // 已取消,不算错误
		}
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

	// 华为 SWR 基础版拒收顶层 OCI image index;带 --preserve-digests 时 skopeo 无法把
	// OCI index 转成 Docker manifest list,推送必然失败。目标为 SWR 时关闭 digest 严格保留。
	// skopeo 在无需格式转换时本就保持原 manifest,因此实际变化的只有被转换的顶层 digest。
	preserveDigests := dstCreds.Type != model.RegistryTypeSWR
	if !preserveDigests {
		m.emit(taskID, EventItemProgress, Event{
			Message: "目标仓库类型为华为云 SWR:已关闭 digest 严格保留,OCI 多架构镜像将自动转换为 Docker manifest list(仅顶层 digest 变化)",
		})
	}

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
		updateItem(db, it.ID, map[string]any{"status": model.ItemStatusRunning, "started_at": time.Now()})

		// 广播日志行。
		logHandler := func(line string) {
			m.emit(taskID, EventItemProgress, Event{
				ItemID: it.ID, Message: line,
			})
		}
		copyOpt := skopeo.CopyOptions{
			SrcRef:          it.SourceRef,
			SrcAuthFile:     srcAuth,
			SrcInsecure:     srcCreds.Insecure,
			DstRef:          it.TargetRef,
			DstAuthFile:     dstAuth,
			DstInsecure:     dstCreds.Insecure,
			PreserveDigests: preserveDigests,
			Arch:            arch,
		}
		res := skopeo.Copy(ctx, copyOpt, logHandler)

		// 兜底:目标仓库没配成 SWR 类型、但同样拒收当前 manifest 格式时(如 SWR 被配成
		// generic),按报错特征识别后去掉 --preserve-digests 转换格式重试一次,避免整条失败。
		if !res.OK && ctx.Err() == nil && copyOpt.PreserveDigests && skopeo.IsPreserveDigestsConflict(res.StderrTail) {
			m.emit(taskID, EventItemProgress, Event{
				ItemID:  it.ID,
				Message: "目标仓库拒绝当前 manifest 格式,关闭 digest 严格保留并转换格式后重试...",
			})
			copyOpt.PreserveDigests = false
			res = skopeo.Copy(ctx, copyOpt, logHandler)
		}

		finishedAt := time.Now()
		if res.OK {
			succeeded++
			digest := ""
			// 取目标 digest(失败不影响主流程),复用任务的目标 auth 文件。
			if d, err := skopeo.InspectDigest(ctx, it.TargetRef, dstAuth, dstCreds.Insecure); err == nil {
				digest = d
			}
			updateItem(db, it.ID, map[string]any{
				"status": model.ItemStatusSuccess, "digest": digest, "finished_at": finishedAt, "error": "",
			})
			m.emit(taskID, EventItemSuccess, Event{
				ItemID: it.ID, SourceRef: it.SourceRef, TargetRef: it.TargetRef, Data: map[string]any{"digest": digest},
			})
		} else if ctx.Err() != nil {
			// 任务被取消:当前 item 置 skipped,并跳出循环,剩余未执行 item 在循环外统一标记。
			skipped++
			updateItem(db, it.ID, map[string]any{"status": model.ItemStatusSkipped, "finished_at": finishedAt})
			m.emit(taskID, EventItemFailed, Event{ItemID: it.ID, Message: "已取消"})
			break
		} else {
			failed++
			updateItem(db, it.ID, map[string]any{
				"status": model.ItemStatusFailed, "error": res.StderrTail, "finished_at": finishedAt,
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
			Where("task_id = ? AND status IN ?", taskID, []string{model.ItemStatusPending, model.ItemStatusRunning}).
			Updates(map[string]any{"status": model.ItemStatusSkipped, "finished_at": now}).Error; err != nil {
			log.Printf("[task] 标记剩余 item 为 skipped 失败: %v", err)
		}
		// 统计被跳过的数量(查询而不是猜)。
		var skipCount int64
		db.Table("sync_task_items").Where("task_id = ? AND status = ?", taskID, model.ItemStatusSkipped).Count(&skipCount)
		skipped = int(skipCount)
	}

	// 3. 更新任务终态。
	finalStatus := model.TaskStatusSuccess
	taskErrMsg := ""
	if ctx.Err() != nil {
		finalStatus = model.TaskStatusCanceled
	} else if failed > 0 {
		finalStatus = model.TaskStatusFailed
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
