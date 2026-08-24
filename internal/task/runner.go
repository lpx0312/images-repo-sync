package task

import (
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/registry"
	"images-repo-sync/internal/skopeo"
	"images-repo-sync/internal/store"
)

// worker 消费任务队列并执行(实例数由 TASK_CONCURRENCY 决定,默认 1)。
// skopeo 受 IO 限制,少量并发即可;目标之间与镜像之间均串行复制。
func (m *Manager) worker() {
	for taskID := range m.queue {
		if err := m.runTask(taskID); err != nil {
			log.Printf("[task] 任务 %d 执行失败: %v", taskID, err)
		}
	}
}

// resolvedCreds 是一条明细最终生效的源/目标凭证组合。
// srcAuthFile 为空表示匿名源(如 docker.io 公开镜像,不传 authfile)。
type resolvedCreds struct {
	src         registryCreds
	srcAuth     string
	srcInsecure bool
	dst         registryCreds
	dstAuth     string
}

// runTask 执行单个同步任务:加载任务和明细 → 预登录源仓库 → 逐条 skopeo copy → 更新状态。
//
// 明细按「目标仓库」分组串行执行(同一时刻只推一个目标,推完 A 的全部镜像再推 B);
// 源凭证按明细各自的 source_registry_id 取(自动匹配源的任务每条镜像可能来自不同仓库)。
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
	var taskInfo taskLoader
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("sync_tasks").Where("id = ?", taskID).First(&taskInfo).Error; err != nil {
			return err
		}
		// P0-1 修复:若任务已被取消,直接退出,不置 running。
		if taskInfo.Status == model.TaskStatusCanceled {
			return errAlreadyCanceled
		}
		if err := tx.Table("sync_task_items").
			Select("id, source_registry_id, target_registry_id, source_host, source_ref, target_ref").
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

	// 2. 解析每条明细实际生效的源/目标仓库 ID(0 回退任务级,旧数据兼容)。
	itemSourceID := make(map[uint]uint, len(items))      // itemID -> source registry ID(0=匿名)
	itemTargetID := make(map[uint]uint, len(items))      // itemID -> target registry ID
	anonSourceHosts := make(map[uint]string, len(items)) // itemID -> 匿名源主机名
	involved := map[uint]bool{}
	for _, it := range items {
		srcID := it.SourceRegistryID
		if srcID == 0 && it.SourceHost == "" {
			srcID = taskInfo.SourceRegistryID
		}
		dstID := it.TargetRegistryID
		if dstID == 0 {
			dstID = taskInfo.TargetRegistryID
		}
		itemSourceID[it.ID] = srcID
		itemTargetID[it.ID] = dstID
		if it.SourceHost != "" {
			anonSourceHosts[it.ID] = it.SourceHost
		}
		if srcID != 0 {
			involved[srcID] = true
		}
		if dstID != 0 {
			involved[dstID] = true
		}
	}

	// 3. 加载涉及的仓库凭证(每个 ID 一次)。
	credsCache := make(map[uint]registryCreds, len(involved))
	for id := range involved {
		c, err := loadRegistryCredsByID(db, id)
		if err != nil {
			return fmt.Errorf("加载仓库 #%d 凭证失败: %w", id, err)
		}
		credsCache[id] = c
	}

	// 4. 预登录所有涉及的源仓库(用户要求:同步前先把源仓库都登录/校验一遍,
	//    凭证问题提前暴露,避免推一半才发现)。匿名源(docker.io)跳过。
	sourceIDs := make(map[uint]bool, len(items))
	for _, srcID := range itemSourceID {
		if srcID != 0 {
			sourceIDs[srcID] = true
		}
	}
	probeClient := ctx
	for srcID := range sourceIDs {
		c := credsCache[srcID]
		m.emit(taskID, EventItemProgress, Event{
			Message: fmt.Sprintf("正在登录源仓库 %s(%s)...", c.Name, c.Host),
		})
		res := registry.Probe(probeClient, c.Host, c.User, c.Pass, c.Insecure)
		if !res.OK {
			return fmt.Errorf("源仓库 %s(%s)登录失败: %s", c.Name, c.Host, res.Message)
		}
		m.emit(taskID, EventItemProgress, Event{
			Message: fmt.Sprintf("源仓库 %s 登录成功", c.Name),
		})
	}

	// 5. 为每个涉及的仓库生成 auth 文件(源/目标共用一份,按 ID 缓存)。
	authFiles := make(map[uint]string, len(involved))
	defer func() {
		for _, p := range authFiles {
			skopeo.CleanupAuthFiles(p)
		}
	}()
	for id, c := range credsCache {
		af, err := skopeo.WriteAuthFile(c.Host, c.User, c.Pass)
		if err != nil {
			return fmt.Errorf("生成仓库 %s 凭证文件失败: %w", c.Name, err)
		}
		authFiles[id] = af
	}

	m.emit(taskID, EventTaskStarted, Event{
		Data: map[string]any{"total": len(items)},
	})

	// 6. 按目标分组串行执行:同一时刻只推一个目标,该目标的全部镜像完成后进入下一目标。
	succeeded, failed, skipped := 0, 0, 0
	byTarget := make(map[uint][]syncTaskItemWithRefs)
	var targetOrder []uint
	for _, it := range items {
		tid := itemTargetID[it.ID]
		if _, ok := byTarget[tid]; !ok {
			targetOrder = append(targetOrder, tid)
		}
		byTarget[tid] = append(byTarget[tid], it)
	}

	for _, tid := range targetOrder {
		dstCreds := credsCache[tid]
		// 华为 SWR 基础版拒收顶层 OCI image index;带 --preserve-digests 时 skopeo 无法把
		// OCI index 转成 Docker manifest list,推送必然失败。目标为 SWR 时关闭 digest 严格保留。
		preserveDigests := dstCreds.Type != model.RegistryTypeSWR
		if !preserveDigests && len(byTarget[tid]) > 0 {
			m.emit(taskID, EventItemProgress, Event{
				Message: fmt.Sprintf("目标仓库 %s 为华为云 SWR:已关闭 digest 严格保留,OCI 多架构镜像将自动转换为 Docker manifest list(仅顶层 digest 变化)", dstCreds.Name),
			})
		}

		for _, it := range byTarget[tid] {
			// 每条 item 用独立 ctx,但继承任务级 cancel。
			itemCtx := ctx
			_ = itemCtx

			srcCreds := registryCreds{}
			srcAuth := ""
			srcInsecure := false
			if sid := itemSourceID[it.ID]; sid != 0 {
				srcCreds = credsCache[sid]
				srcAuth = authFiles[sid]
				srcInsecure = srcCreds.Insecure
			} else if host := anonSourceHosts[it.ID]; host != "" {
				srcCreds = registryCreds{Host: host} // 匿名源:无凭证,不传 authfile
			}

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
				SrcInsecure:     srcInsecure,
				DstRef:          it.TargetRef,
				DstAuthFile:     authFiles[tid],
				DstInsecure:     dstCreds.Insecure,
				PreserveDigests: preserveDigests,
				Arch:            taskInfo.Arch,
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
				// 取目标 digest(失败不影响主流程),复用该目标的 auth 文件。
				if d, err := skopeo.InspectDigest(ctx, it.TargetRef, authFiles[tid], dstCreds.Insecure); err == nil {
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
				errText := skopeo.FriendlyCopyError(res.StderrTail)
				updateItem(db, it.ID, map[string]any{
					"status": model.ItemStatusFailed, "error": errText, "finished_at": finishedAt,
				})
				m.emit(taskID, EventItemFailed, Event{
					ItemID: it.ID, SourceRef: it.SourceRef, TargetRef: it.TargetRef, Message: errText,
				})
			}
		}
		// 目标内被取消时跳出目标循环。
		if ctx.Err() != nil {
			break
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

	// 7. 更新任务终态。
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
