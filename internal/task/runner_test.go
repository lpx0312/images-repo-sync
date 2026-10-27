package task

// runner_test 覆盖 runTask 的关键分支:
//   - A 类回归(RED,断言修复后行为):预执行失败时任务计数回填(A1)、
//     终态任务不得重跑(A2)。
//   - B 类边界(GREEN,当前必须通过):probe 阶段取消、updateItem DB 失败分支。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// seedRegistry 建一个 generic 仓库记录(明文密码为空,EncryptKey 未启用时原样兼容)。
func seedRegistry(t *testing.T, db *gorm.DB, name, host string) model.Registry {
	t.Helper()
	reg := model.Registry{Name: name, Host: host, Type: model.RegistryTypeGeneric}
	if err := db.Create(&reg).Error; err != nil {
		t.Fatalf("seed registry %s 失败: %v", name, err)
	}
	return reg
}

// seedTask 建任务与 N 条 pending 明细(item 的源/目标仓库 ID 留 0,沿用任务级)。
func seedTask(t *testing.T, db *gorm.DB, task model.SyncTask, itemCount int) (model.SyncTask, []model.SyncTaskItem) {
	t.Helper()
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("seed task 失败: %v", err)
	}
	items := make([]model.SyncTaskItem, itemCount)
	for i := range items {
		items[i] = model.SyncTaskItem{
			TaskID:    task.ID,
			SourceRef: "src.example.com/app:v1",
			TargetRef: "dst.example.com/proj/app:v1",
			Status:    model.ItemStatusPending,
		}
	}
	if len(items) > 0 {
		if err := db.Create(&items).Error; err != nil {
			t.Fatalf("seed items 失败: %v", err)
		}
	}
	return task, items
}

// reloadTask 重新加载任务行。
func reloadTask(t *testing.T, db *gorm.DB, id uint) model.SyncTask {
	t.Helper()
	var tk model.SyncTask
	if err := db.First(&tk, id).Error; err != nil {
		t.Fatalf("加载任务 %d 失败: %v", id, err)
	}
	return tk
}

// TestRunTaskSourceProbeFailure 源仓库 probe 失败(404,非有效 registry)时任务整体失败。
// 回归锁定(已修复): A1 —— runTask 预执行失败(probe/凭证/authfile)时,兜底只把明细置
// failed、任务置 failed,但不回填 task 的 succeeded/failed/skipped 计数,
// 导致「任务 failed 但 Failed=0」的计数与明细不一致。修复后应满足:
// 终态 failed 且 Failed=N、Total=N、Succeeded=0、Skipped=0。
func TestRunTaskSourceProbeFailure(t *testing.T) {
	db := testutil.NewTestDB(t)

	// 源仓库探测必然失败:/v2/ 返回 404 → 「不是有效的镜像仓库」。
	probeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer probeSrv.Close()

	src := seedRegistry(t, db, "a1-src", probeSrv.URL)
	dst := seedRegistry(t, db, "a1-dst", "dst.example.com")

	task, _ := seedTask(t, db, model.SyncTask{
		Status:           model.TaskStatusPending,
		Mode:             "flat",
		SourceRegistryID: src.ID,
		TargetRegistryID: dst.ID,
	}, 2)

	err := Instance().runTask(task.ID)
	if err == nil || !strings.Contains(err.Error(), "登录失败") {
		t.Fatalf("probe 失败应返回登录错误, got: %v", err)
	}

	tk := reloadTask(t, db, task.ID)
	if tk.Status != model.TaskStatusFailed {
		t.Errorf("任务终态 = %s, want failed", tk.Status)
	}
	if tk.Total != 2 {
		t.Errorf("Total = %d, want 2", tk.Total)
	}
	if tk.Failed != 2 { // 当前实现不回填计数,此断言失败(RED)
		t.Errorf("回归锁定(已修复): A1 Failed 计数 = %d, want 2(与明细 failed 数一致)", tk.Failed)
	}
	if tk.Succeeded != 0 {
		t.Errorf("Succeeded = %d, want 0", tk.Succeeded)
	}
	if tk.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", tk.Skipped)
	}

	var items []model.SyncTaskItem
	db.Where("task_id = ?", task.ID).Order("id ASC").Find(&items)
	if len(items) != 2 {
		t.Fatalf("明细数 = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.Status != model.ItemStatusFailed {
			t.Errorf("明细 %d 状态 = %s, want failed", it.ID, it.Status)
		}
		if it.Error == "" || it.FinishedAt == nil {
			t.Errorf("明细 %d 应写入 error 与 finished_at", it.ID)
		}
	}
}

// TestRunTaskTerminalStateNotRerun 终态(success)任务再次进入 runTask 不得重跑。
// 回归锁定(已修复): A2 —— runTask 只挡 canceled,对 success/failed 等终态任务无防御:
// seed 一个已 success 的任务(带明细),直接调 runTask,当前会重新置 running 并
// 真的执行 copy(明细 digest 被 fake inspect 改写、finished_at 被刷新)。
// 修复后应满足:状态仍 success、明细与 finished_at 不变、没有新的 copy 发生。
func TestRunTaskTerminalStateNotRerun(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.UseFakeSkopeo(t)

	// 源仓库探测成功(200 即公开 registry)。
	probeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer probeSrv.Close()

	src := seedRegistry(t, db, "a2-src", probeSrv.URL)
	dst := seedRegistry(t, db, "a2-dst", "dst.example.com")

	past := time.Now().Add(-2 * time.Hour)
	const seededDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	task := model.SyncTask{
		Status:           model.TaskStatusSuccess,
		Mode:             "flat",
		SourceRegistryID: src.ID,
		TargetRegistryID: dst.ID,
		Total:            1,
		Succeeded:        1,
		StartedAt:        &past,
		FinishedAt:       &past,
	}
	task, _ = seedTask(t, db, task, 0) // 明细单独 seed(需要终态字段),不走 seedTask 的 pending 模板
	item := model.SyncTaskItem{
		TaskID:     task.ID,
		SourceRef:  "src.example.com/nginx:1.25",
		TargetRef:  "dst.example.com/proj/nginx:1.25",
		Status:     model.ItemStatusSuccess,
		Digest:     seededDigest,
		StartedAt:  &past,
		FinishedAt: &past,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed 明细失败: %v", err)
	}

	// 直接调 runTask。若无终态防御,任务会被重新置 running 并真的跑一遍
	// fake copy(正常 ref 会成功)+ fake inspect(digest 被改写)。
	_ = Instance().runTask(task.ID)

	tk := reloadTask(t, db, task.ID)
	if tk.Status != model.TaskStatusSuccess {
		t.Errorf("终态任务状态被改写为 %s, want success", tk.Status)
	}
	if tk.FinishedAt == nil || tk.FinishedAt.After(past.Add(time.Hour)) {
		t.Errorf("回归锁定(已修复): A2 任务 finished_at 被刷新(重跑痕迹): %v, want 保持 %v", tk.FinishedAt, past)
	}
	if tk.Succeeded != 1 || tk.Failed != 0 || tk.Skipped != 0 {
		t.Errorf("计数被改写: succeeded=%d failed=%d skipped=%d, want 1/0/0", tk.Succeeded, tk.Failed, tk.Skipped)
	}

	var after model.SyncTaskItem
	if err := db.First(&after, item.ID).Error; err != nil {
		t.Fatalf("加载明细失败: %v", err)
	}
	if after.Digest != seededDigest {
		t.Errorf("回归锁定(已修复): A2 明细 digest = %s, want %s(digest 被改写说明发生了新的 copy/inspect)", after.Digest, seededDigest)
	}
	if after.Status != model.ItemStatusSuccess {
		t.Errorf("明细状态 = %s, want success", after.Status)
	}
	if after.FinishedAt == nil || after.FinishedAt.After(past.Add(time.Hour)) {
		t.Errorf("回归锁定(已修复): A2 明细 finished_at 被刷新: %v, want 保持 %v", after.FinishedAt, past)
	}
}

// TestRunTaskCancelDuringProbe 取消发生在 probe 阶段:
// 源仓库探测 HTTP 挂起(等客户端断开),订阅事件流等到「正在登录源仓库」后 Cancel,
// runTask 应快速返回(probe 请求随 ctx 取消中止),任务置 canceled、明细置 skipped。
func TestRunTaskCancelDuringProbe(t *testing.T) {
	db := testutil.NewTestDB(t)

	// 探测端点一直挂起,直到客户端取消请求(模拟慢源仓库)。
	probeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			return
		}
	}))
	defer probeSrv.Close()

	src := seedRegistry(t, db, "b-cancel-src", probeSrv.URL)
	dst := seedRegistry(t, db, "b-cancel-dst", "dst.example.com")

	task, _ := seedTask(t, db, model.SyncTask{
		Status:           model.TaskStatusPending,
		Mode:             "flat",
		SourceRegistryID: src.ID,
		TargetRegistryID: dst.ID,
	}, 2)

	m := Instance()
	subID, ch, _ := m.Subscribe(task.ID)
	defer m.Unsubscribe(task.ID, subID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.runTask(task.ID) // 返回值不断言(取消路径返回 probe 错误属预期)
	}()

	// 等待进入 probe 阶段的事件 marker(10s 内未出现即失败)。
	deadline := time.After(10 * time.Second)
	sawProbe := false
	for !sawProbe {
		select {
		case ev := <-ch:
			if strings.Contains(ev.Message, "正在登录源仓库") {
				sawProbe = true
			}
		case <-deadline:
			t.Fatal("10s 内未收到 probe 阶段事件(正在登录源仓库)")
		}
	}
	m.Cancel(task.ID)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("取消发生在 probe 阶段时 runTask 未快速返回(被慢 probe 卡住)")
	}

	testutil.WaitFor(t, 5*time.Second, "任务终态 canceled", func() bool {
		return reloadTask(t, db, task.ID).Status == model.TaskStatusCanceled
	})

	var items []model.SyncTaskItem
	db.Where("task_id = ?", task.ID).Order("id ASC").Find(&items)
	for _, it := range items {
		if it.Status != model.ItemStatusSkipped {
			t.Errorf("取消后明细 %d 状态 = %s, want skipped", it.ID, it.Status)
		}
	}
}

// TestUpdateItemDBFailure updateItem 的 DB 失败分支:传入已 Close 的连接,
// 只打日志、不 panic、不影响其他数据。updateItem 以参数接收 db,
// 无需改写全局 store.DB(避免 Windows 句柄残留与测试间互踩),直接注入失效句柄。
func TestUpdateItemDBFailure(t *testing.T) {
	db := testutil.NewTestDB(t)

	task, _ := seedTask(t, db, model.SyncTask{
		Status: model.TaskStatusPending, Mode: "flat",
	}, 1)
	var item model.SyncTaskItem
	if err := db.Where("task_id = ?", task.ID).First(&item).Error; err != nil {
		t.Fatalf("加载明细失败: %v", err)
	}

	// 独立临时库:打开后立刻关闭,得到必然失败的 gorm 句柄。
	dead, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "dead.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	if sqlDB, err := dead.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 不应 panic;错误被吞(仅打日志)。
	updateItem(dead, item.ID, map[string]any{"status": model.ItemStatusRunning, "started_at": time.Now()})

	// 原库数据不受影响。
	var after model.SyncTaskItem
	if err := db.First(&after, item.ID).Error; err != nil {
		t.Fatalf("重新加载明细失败: %v", err)
	}
	if after.Status != model.ItemStatusPending {
		t.Errorf("失效连接上的更新不应影响原库: 明细状态 = %s, want pending", after.Status)
	}
}
