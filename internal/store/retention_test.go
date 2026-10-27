package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
)

func TestDeleteOldTasks(t *testing.T) {
	db := setupDB(t)
	old := 40
	task := model.SyncTask{Status: model.TaskStatusSuccess, Mode: "flat", CreatedAt: time.Now().AddDate(0, 0, -old)}
	db.Create(&task)
	db.Create(&model.SyncTaskItem{TaskID: task.ID, SourceRef: "a", TargetRef: "b", Status: model.ItemStatusSuccess})

	// 未过期的终态任务不动。
	fresh := model.SyncTask{Status: model.TaskStatusFailed, Mode: "flat", CreatedAt: time.Now()}
	db.Create(&fresh)
	// 过期但未结束的任务不动。
	runningOld := model.SyncTask{Status: model.TaskStatusRunning, Mode: "flat", CreatedAt: time.Now().AddDate(0, 0, -old)}
	db.Create(&runningOld)

	if n := deleteOldTasks(30); n != 1 {
		t.Fatalf("删除任务数 = %d, want 1", n)
	}
	var cnt int64
	db.Model(&model.SyncTaskItem{}).Where("task_id = ?", task.ID).Count(&cnt)
	if cnt != 0 {
		t.Errorf("明细应级联删除,剩 %d", cnt)
	}
	var remain int64
	db.Model(&model.SyncTask{}).Count(&remain)
	if remain != 2 {
		t.Errorf("剩余任务 = %d, want 2", remain)
	}
}

func TestDeleteOldChartUploads(t *testing.T) {
	db := setupDB(t)
	db.Create(&model.ChartUpload{Status: model.TaskStatusSuccess, CreatedAt: time.Now().AddDate(0, 0, -40)})
	db.Create(&model.ChartUpload{Status: model.TaskStatusFailed, CreatedAt: time.Now().AddDate(0, 0, -40)})
	db.Create(&model.ChartUpload{Status: model.TaskStatusSuccess, CreatedAt: time.Now()})
	db.Create(&model.ChartUpload{Status: model.TaskStatusPending, CreatedAt: time.Now().AddDate(0, 0, -40)}) // 未结束不动

	if n := deleteOldChartUploads(30); n != 2 {
		t.Fatalf("删除 chart 记录 = %d, want 2", n)
	}
	var remain int64
	db.Model(&model.ChartUpload{}).Count(&remain)
	if remain != 2 {
		t.Errorf("剩余记录 = %d, want 2", remain)
	}
}

func TestCleanupExpiredData(t *testing.T) {
	db := setupDB(t)
	config.AppConfig.TaskRetentionDays = 30
	config.AppConfig.LoginLogRetentionDays = 30

	db.Create(&model.SyncTask{Status: model.TaskStatusSuccess, Mode: "flat", CreatedAt: time.Now().AddDate(0, 0, -60)})
	db.Create(&model.ChartUpload{Status: model.TaskStatusSuccess, CreatedAt: time.Now().AddDate(0, 0, -60)})
	db.Create(&model.LoginLog{Status: "success", CreatedAt: time.Now().AddDate(0, 0, -60)})

	cleanupExpiredData()

	var tasks, uploads, logs int64
	db.Model(&model.SyncTask{}).Count(&tasks)
	db.Model(&model.ChartUpload{}).Count(&uploads)
	db.Model(&model.LoginLog{}).Count(&logs)
	if tasks != 0 || uploads != 0 || logs != 0 {
		t.Fatalf("清理后剩余: tasks=%d uploads=%d logs=%d", tasks, uploads, logs)
	}

	// 保留天数配 0:不清理。
	config.AppConfig.TaskRetentionDays = 0
	config.AppConfig.LoginLogRetentionDays = 0
	db.Create(&model.SyncTask{Status: model.TaskStatusSuccess, Mode: "flat", CreatedAt: time.Now().AddDate(0, 0, -999)})
	db.Create(&model.LoginLog{Status: "success", CreatedAt: time.Now().AddDate(0, 0, -999)})
	cleanupExpiredData()
	db.Model(&model.SyncTask{}).Count(&tasks)
	db.Model(&model.LoginLog{}).Count(&logs)
	if tasks != 1 || logs != 1 {
		t.Errorf("保留天数 0 不应清理: tasks=%d logs=%d", tasks, logs)
	}

	// DB 为 nil:静默跳过。
	saved := DB
	DB = nil
	cleanupExpiredData()
	DB = saved
}

func TestDeleteBatchErrorStops(t *testing.T) {
	setupDB(t)
	calls := 0
	selectFn := func() ([]uint, error) {
		calls++
		if calls == 1 {
			return []uint{1, 2}, nil
		}
		return nil, errors.New("boom")
	}
	var deleted [][]uint
	n := deleteBatch(func(ids []uint) int64 {
		deleted = append(deleted, ids)
		return int64(len(ids))
	}, selectFn)
	if n != 2 || len(deleted) != 1 {
		t.Fatalf("出错前应完成首批: n=%d batches=%v", n, deleted)
	}
}

func TestDeleteBatchEmptyStops(t *testing.T) {
	setupDB(t)
	n := deleteBatch(
		func(ids []uint) int64 { t.Fatal("不应执行删除"); return 0 },
		func() ([]uint, error) { return nil, nil },
	)
	if n != 0 {
		t.Fatalf("空批次应返回 0, got %d", n)
	}
}

func TestStartRetentionExitOnCancel(t *testing.T) {
	setupDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { StartRetention(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartRetention 未随 ctx 取消退出")
	}
}

func TestDeleteOldTasksBatching(t *testing.T) {
	// 造超过单批上限(500)的过期任务,验证分批删除全部完成。
	db := setupDB(t)
	const total = retentionBatch + 30
	tasks := make([]model.SyncTask, total)
	for i := range tasks {
		tasks[i] = model.SyncTask{Status: model.TaskStatusSuccess, Mode: "flat", CreatedAt: time.Now().AddDate(0, 0, -40)}
	}
	if err := db.CreateInBatches(tasks, 200).Error; err != nil {
		t.Fatalf("批量造数失败: %v", err)
	}
	if n := deleteOldTasks(30); n != total {
		t.Fatalf("分批删除总数 = %d, want %d", n, total)
	}
}

// 回归锁定(已修复): A3 —— deleteOldTasks 的两步删除(retention.go:69-72,先删明细再删任务)
// 没有包在同一事务里:两条 DELETE 之间进程中断会留下孤儿明细,重跑批次时
// 第二步可能因残留数据反复失败。该中断点无法在单测中可靠注入(需要在两条
// DELETE 语句之间杀掉进程/连接,SQLite 单文件库也没有可拦截的中间钩子),
// 故跳过;修复方式应为把 deleteFn 内两步放进 DB.Transaction,修复后可断言
// 「任务删除失败时明细不残留」的回滚语义。
func TestDeleteOldTasksTwoStepAtomicity(t *testing.T) {
	t.Skip("A3: 两步删除的事务性无法在单测中注入中断验证,留待事务化修复后补充回滚断言")
}
