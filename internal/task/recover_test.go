package task

// recover_test 覆盖 RecoverStuckTasks 的两个分支(B 类边界,当前必须通过):
//   - running 任务:非终态明细(pending 与 running)统一置 failed,终态明细不动,
//     并按明细实际状态回填任务计数;
//   - pending 任务:重新入队,由真实 worker 消费(此处用必然失败的仓库引用验证出队执行)。

import (
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// TestRecoverStuckRunningTaskMarksItemsFailed running 任务的明细里
// pending 与 running 两种非终态都要置 failed(并写 error 与 finished_at),
// success 明细保持不变;任务自身置 failed 且计数被 refreshTaskCounts 回填。
func TestRecoverStuckRunningTaskMarksItemsFailed(t *testing.T) {
	db := testutil.NewTestDB(t)

	task := model.SyncTask{
		Status: model.TaskStatusRunning, Mode: "flat",
		Total: 3, Succeeded: 1,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("seed running 任务失败: %v", err)
	}
	past := time.Now().Add(-1 * time.Hour)
	items := []model.SyncTaskItem{
		{TaskID: task.ID, SourceRef: "a", TargetRef: "a", Status: model.ItemStatusSuccess, FinishedAt: &past},
		{TaskID: task.ID, SourceRef: "b", TargetRef: "b", Status: model.ItemStatusRunning, StartedAt: &past}, // 执行中被打断
		{TaskID: task.ID, SourceRef: "c", TargetRef: "c", Status: model.ItemStatusPending},                   // 尚未开始
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("seed 明细失败: %v", err)
	}

	RecoverStuckTasks()

	tk := reloadTask(t, db, task.ID)
	if tk.Status != model.TaskStatusFailed {
		t.Errorf("running 任务恢复后状态 = %s, want failed", tk.Status)
	}
	if tk.Error == "" || tk.FinishedAt == nil {
		t.Errorf("任务应写入 error 与 finished_at: %+v", tk)
	}
	if tk.Succeeded != 1 || tk.Failed != 2 || tk.Skipped != 0 {
		t.Errorf("计数回填不对: succeeded=%d failed=%d skipped=%d, want 1/2/0", tk.Succeeded, tk.Failed, tk.Skipped)
	}

	var after []model.SyncTaskItem
	db.Where("task_id = ?", task.ID).Order("id ASC").Find(&after)
	wantStatus := map[uint]string{items[0].ID: model.ItemStatusSuccess, items[1].ID: model.ItemStatusFailed, items[2].ID: model.ItemStatusFailed}
	for _, it := range after {
		if it.Status != wantStatus[it.ID] {
			t.Errorf("明细 %d 状态 = %s, want %s", it.ID, it.Status, wantStatus[it.ID])
		}
		if it.Status == model.ItemStatusFailed && (it.Error == "" || it.FinishedAt == nil) {
			t.Errorf("明细 %d 置 failed 应写入 error 与 finished_at", it.ID)
		}
	}
}

// TestRecoverStuckPendingTaskRequeued pending 任务在服务重启后应重新入队执行:
// 明细指向不存在的仓库(ID 999),worker 消费后必然 failed——以此证明
// 「重新入队 + worker 真实消费」链路生效,而不是任务永远停在 pending。
func TestRecoverStuckPendingTaskRequeued(t *testing.T) {
	db := testutil.NewTestDB(t)
	_ = Instance() // 必须先启动 worker,否则重新入队无人消费。

	task, _ := seedTask(t, db, model.SyncTask{
		Status:           model.TaskStatusPending,
		Mode:             "flat",
		SourceRegistryID: 999, // 不存在的仓库:加载凭证必败,任务将快速失败
		TargetRegistryID: 999,
	}, 1)

	RecoverStuckTasks()

	testutil.WaitFor(t, 15*time.Second, "pending 任务被重新执行并离开 pending", func() bool {
		st := reloadTask(t, db, task.ID).Status
		return st != model.TaskStatusPending
	})
	tk := reloadTask(t, db, task.ID)
	if tk.Status != model.TaskStatusFailed {
		t.Errorf("重入队任务终态 = %s, want failed(仓库 999 不存在)", tk.Status)
	}
	var items []model.SyncTaskItem
	db.Where("task_id = ?", task.ID).Find(&items)
	for _, it := range items {
		if it.Status != model.ItemStatusFailed {
			t.Errorf("明细 %d 状态 = %s, want failed", it.ID, it.Status)
		}
	}
}
