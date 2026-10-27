package api

// boundary_b_test 是 B 类边界测试:来自 2026-10-27 全仓审查的「测试盲区」清单,
// 断言当前实现的边界行为。B 类用例必须保持 GREEN(不因缺陷修复而改变断言方向,
// 若后续阶段调整对应生产行为,需同步更新)。
// 全局 DB / task worker 为包级共享,本文件不加 t.Parallel()。

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/task"
	"images-repo-sync/internal/testutil"
)

// ---------------- login_limiter ----------------

// TestLoginLimiterRelockAfterExpiry 锁定到期后必须能重新累计失败次数,
// 而不是一次锁定后永久锁死或计数残留。
// recordFailure 在锁定时已把 fails 清零,到期后应从 0 重新数到阈值再次锁定。
func TestLoginLimiterRelockAfterExpiry(t *testing.T) {
	l := newLoginLimiter()
	k := "user:alice"

	for i := 0; i < loginMaxUserFails; i++ {
		l.recordFailure(k)
	}
	if locked, wait := l.locked(k); !locked || wait <= 0 {
		t.Fatalf("达到阈值应锁定: locked=%v wait=%v", locked, wait)
	}

	// 模拟 10 分钟锁定期结束:包内测试可直接把内部记录的到期时间拨回过去。
	l.mu.Lock()
	l.records[k].lockedUntil = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if locked, _ := l.locked(k); locked {
		t.Fatal("锁定期已过不应再锁定")
	}

	// 到期后重新累计:阈值减一次失败不锁,再失败一次重新锁定。
	for i := 0; i < loginMaxUserFails-1; i++ {
		l.recordFailure(k)
	}
	if locked, _ := l.locked(k); locked {
		t.Fatal("到期后重新累计,未达阈值不应锁定")
	}
	l.recordFailure(k)
	if locked, wait := l.locked(k); !locked || wait <= 0 {
		t.Fatalf("重新达到阈值应再次锁定: locked=%v wait=%v", locked, wait)
	}
}

// TestLoginLimiterResetOnlyGivenKeys 成功登录的 reset 只清调用方传入的 key,
// 其它 key 的锁定状态不受影响(某一用户登录成功不得顺手解锁其他用户/IP)。
func TestLoginLimiterResetOnlyGivenKeys(t *testing.T) {
	l := newLoginLimiter()
	ua, ub, ip1 := "user:a", "user:b", "ip:10.0.0.8"

	for i := 0; i < loginMaxUserFails; i++ {
		l.recordFailure(ua)
		l.recordFailure(ub)
	}
	for i := 0; i < loginMaxIPFails; i++ {
		l.recordFailure(ip1)
	}
	for _, k := range []string{ua, ub, ip1} {
		if locked, _ := l.locked(k); !locked {
			t.Fatalf("%s 前置条件失败: 应处于锁定", k)
		}
	}

	l.reset(ua) // 成功登录只 reset 传入 key。
	if locked, _ := l.locked(ua); locked {
		t.Fatal("被 reset 的 key 不应再锁定")
	}
	for _, k := range []string{ub, ip1} {
		if locked, _ := l.locked(k); !locked {
			t.Errorf("未 reset 的 %s 仍应锁定", k)
		}
	}
}

// ---------------- task Create ----------------

// TestTaskCreateRefsCollapseToEmpty refs 规整(去空白/去 docker:// 与 http(s):// 前缀/
// 去重)后为空 → 400「没有有效的镜像」,不落库、不入队。
func TestTaskCreateRefsCollapseToEmpty(t *testing.T) {
	r, _, token := newAuthed(t)
	dst := createRegistryViaAPI(t, r, token, "collapse-dst", "dst.example.com", "generic")

	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dst},
		"mode":                "flat",
		"target_project":      "p",
		"refs":                []string{"", "   ", "docker://", "https://", "http://"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "没有有效的镜像") {
		t.Fatalf("规整后为空应 400: %d %s", w.Code, w.Body.String())
	}
}

// TestTaskCreateInvalidModeBoundary 非法 mode:未知取值在解析目标引用时被拒(「未知模式」),
// 空 mode 被 binding required 拦截(「参数不完整」)。
func TestTaskCreateInvalidModeBoundary(t *testing.T) {
	r, _, token := newAuthed(t)
	dst := createRegistryViaAPI(t, r, token, "mode-dst", "dst.example.com", "generic")

	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dst},
		"mode":                "bogus",
		"target_project":      "p",
		"refs":                []string{"nginx:1.25"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "未知模式") {
		t.Fatalf("未知 mode 应 400 且提示未知模式: %d %s", w.Code, w.Body.String())
	}

	body := map[string]any{
		"target_registry_ids": []uint{dst},
		"target_project":      "p",
		"refs":                []string{"nginx:1.25"},
	} // 不带 mode
	if w := do(r, token, http.MethodPost, "/api/tasks", body); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "参数不完整") {
		t.Fatalf("缺 mode 应 400 参数不完整: %d %s", w.Code, w.Body.String())
	}
}

// TestTaskCreateItemsBoundaryAt2000 明细数(镜像 × 目标)边界:
// 恰好 2000 条放行;超过 2000 条拒单。放行的任务立即取消,不真正执行 2000 次复制。
//
// 为避免与真实 worker 产生时序竞争,先用 slowcopy 引用占住唯一的 worker
// (fake skopeo 会睡 30s),使 2000 条明细的任务确定性地停留在 pending 队列里,
// 取消走「队列中取消→直接置 canceled」分支。
func TestTaskCreateItemsBoundaryAt2000(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)
	_ = task.Instance() // 启动真实 worker(测试配置并发为 1)

	targetIDs := []uint{createRegistryViaAPI(t, r, token, "b2k-d0", "b2k-d0.example.com", "generic")}
	for i := 1; i <= 4; i++ {
		targetIDs = append(targetIDs,
			createRegistryViaAPI(t, r, token, fmt.Sprintf("b2k-d%d", i), fmt.Sprintf("b2k-d%d.example.com", i), "generic"))
	}

	// 队头卡位任务:worker 卡在 slowcopy 的 30s 睡眠里。
	var slow taskVO
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{targetIDs[0]},
		"mode":                "flat",
		"target_project":      "p",
		"refs":                []string{"slowcopy:1"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建卡位任务失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &slow)
	testutil.WaitFor(t, 10*time.Second, "卡位任务进入 running", func() bool {
		var tk model.SyncTask
		db.First(&tk, slow.ID)
		return tk.Status == model.TaskStatusRunning
	})

	makeRefs := func(n int) []string {
		refs := make([]string, n)
		for i := range refs {
			refs[i] = fmt.Sprintf("app-%d:1.0", i)
		}
		return refs
	}

	// 401 × 5 = 2005 条 > 2000 → 拒单。
	w = do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": targetIDs,
		"mode":                "flat",
		"target_project":      "p",
		"refs":                makeRefs(401),
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "超过单次 2000") {
		t.Fatalf("2005 条明细应 400: %d %s", w.Code, w.Body.String())
	}

	// 400 × 5 = 恰好 2000 条 → 放行,明细完整落库。
	var out taskVO
	w = do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": targetIDs,
		"mode":                "flat",
		"target_project":      "p",
		"refs":                makeRefs(400),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("恰 2000 条应放行: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &out)
	if out.Total != 2000 || len(out.Items) != 2000 {
		t.Fatalf("明细数不对: total=%d items=%d", out.Total, len(out.Items))
	}

	// 该任务仍在队列(pending),取消走直接置 canceled 分支。
	if w := do(r, token, http.MethodPost, "/api/tasks/"+itoa(out.ID)+"/cancel", nil); w.Code != http.StatusOK {
		t.Fatalf("取消失败: %d %s", w.Code, w.Body.String())
	}
	var after model.SyncTask
	db.First(&after, out.ID)
	if after.Status != model.TaskStatusCanceled {
		t.Fatalf("队列中取消后状态 = %s, want canceled", after.Status)
	}

	// 释放卡位任务并等 worker 收尾,避免把在跑任务留给后续用例。
	if w := do(r, token, http.MethodPost, "/api/tasks/"+itoa(slow.ID)+"/cancel", nil); w.Code != http.StatusOK {
		t.Fatalf("取消卡位任务失败: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 15*time.Second, "卡位任务取消完成", func() bool {
		var tk model.SyncTask
		db.First(&tk, slow.ID)
		return tk.Status != model.TaskStatusPending && tk.Status != model.TaskStatusRunning
	})
}

// TestTaskCreateAutoMatchSourceEqualsTargetBoundary 自动匹配源模式下,
// 按主机名匹配到的源仓库恰为本次的目标仓库 → 整单拒绝。
func TestTaskCreateAutoMatchSourceEqualsTargetBoundary(t *testing.T) {
	r, db, token := newAuthed(t)
	dst := createRegistryViaAPI(t, r, token, "am-dst", "am.example.com", "generic")
	// 把目标仓库加入源镜像组,使 "am.example.com/..." 的镜像能匹配到它。
	if err := db.Model(&model.Registry{}).Where("id = ?", dst).Update("in_source_group", true).Error; err != nil {
		t.Fatal(err)
	}

	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dst},
		"mode":                "flat",
		"target_project":      "p",
		"refs":                []string{"am.example.com/x/y:1", "nginx:1.25"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "源仓库与目标仓库相同") {
		t.Fatalf("自动匹配源=目标应拒单: %d %s", w.Code, w.Body.String())
	}
	var cnt int64
	db.Model(&model.SyncTask{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("拒单后不应有任务落库: count=%d", cnt)
	}
}

// ---------------- task Stream ----------------

// TestStreamClientDisconnectNoPanic 客户端断开(直接关闭 response body)后:
// 服务不得 panic,任务可正常取消,HTTP 服务仍可继续响应。
// 取消/结束事件可能广播给尚未退出订阅循环的 handler,对已关闭连接的写入只是错误,不是 panic。
func TestStreamClientDisconnectNoPanic(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)
	_ = task.Instance()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	dst := createRegistryViaAPI(t, r, token, "sdc-dst", "dst.example.com", "generic")
	var slow taskVO
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dst},
		"mode":                "flat",
		"target_project":      "p",
		"refs":                []string{"slowcopy:1"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建任务失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &slow)
	testutil.WaitFor(t, 10*time.Second, "任务进入 running", func() bool {
		var tk model.SyncTask
		db.First(&tk, slow.ID)
		return tk.Status == model.TaskStatusRunning
	})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tasks/"+itoa(slow.ID)+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("建立 SSE 连接失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream 状态 = %d", resp.StatusCode)
	}
	// 首个 SSE 帧应为 snapshot(超时由 client.Timeout 兜底,读不到即失败)。
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "event: snapshot" {
		t.Fatalf("首个 SSE 帧应为 snapshot: %q err=%v", line, err)
	}

	// 客户端断开。
	resp.Body.Close()

	// 立即取消任务:事件广播可能命中已断开的订阅者。
	if w := do(r, token, http.MethodPost, "/api/tasks/"+itoa(slow.ID)+"/cancel", nil); w.Code != http.StatusOK {
		t.Fatalf("断开后取消任务失败: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 15*time.Second, "任务取消完成", func() bool {
		var tk model.SyncTask
		db.First(&tk, slow.ID)
		return tk.Status != model.TaskStatusPending && tk.Status != model.TaskStatusRunning
	})

	// 服务仍存活可用。
	hr, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatalf("断开后服务无响应: %v", err)
	}
	hr.Body.Close()
	if hr.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", hr.StatusCode)
	}
}

// TestStreamFinishedReplayFullOverHTTP 已结束任务:真实 HTTP SSE 连接上回放完整事件序列
// (snapshot → task_started → 每条明细一个事件 → task_finished),随后连接正常关闭(读到 EOF)。
// 明细覆盖 success/failed/skipped/pending 四种回放分支。
func TestStreamFinishedReplayFullOverHTTP(t *testing.T) {
	r, db, token := newAuthed(t)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	started := time.Now().Add(-2 * time.Minute)
	finished := started.Add(time.Minute)
	tk := model.SyncTask{
		Status: model.TaskStatusSuccess, Mode: "flat", Total: 4,
		Succeeded: 1, Failed: 1, Skipped: 1, StartedAt: &started, FinishedAt: &finished,
	}
	tk.Items = []model.SyncTaskItem{
		{SourceRef: "a:1", TargetRef: "b:1", Status: model.ItemStatusSuccess, Digest: "sha256:fullreplay", StartedAt: &started, FinishedAt: &finished},
		{SourceRef: "c:1", TargetRef: "d:1", Status: model.ItemStatusFailed, Error: "boom-reason", StartedAt: &started, FinishedAt: &finished},
		{SourceRef: "e:1", TargetRef: "f:1", Status: model.ItemStatusSkipped, StartedAt: &started, FinishedAt: &finished},
		{SourceRef: "g:1", TargetRef: "h:1", Status: model.ItemStatusPending},
	}
	if err := db.Create(&tk).Error; err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tasks/"+itoa(tk.ID)+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("建立 SSE 连接失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream 状态 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	// 回放完毕 handler 返回,连接关闭 → ReadAll 正常 EOF(否则 client.Timeout 报错)。
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取回放流失败(应正常 EOF): %v", err)
	}

	frames := collectSSEEvents(string(body))
	want := []string{
		"snapshot", "task_started",
		"item_success", "item_failed", "item_failed", "item_failed", // 4 条明细,后 3 条均为 item_failed 帧
		"task_finished",
	}
	if len(frames) != len(want) {
		t.Fatalf("回放帧数 = %d, want %d\nframes=%v", len(frames), len(want), frames)
	}
	for i, wt := range want {
		if !strings.HasPrefix(frames[i], "event: "+wt) {
			t.Errorf("第 %d 帧应为 %s: %q", i, wt, frames[i])
		}
	}
	for _, wantIn := range []string{"sha256:fullreplay", "boom-reason", "跳过", "未执行", `"success"`} {
		if !strings.Contains(string(body), wantIn) {
			t.Errorf("回放内容缺少 %q", wantIn)
		}
	}
}

// ---------------- settings ----------------

// TestSettingsSaveFailureBranch Save/Find 失败分支(表损坏注入,参考 branches_test.go):
// 表不存在时 Get 静默回退默认值,Update 的 Save 失败返回 500(已修复吞错行为),
// 且值确实没有落库;全程不 panic。
func TestSettingsSaveFailureBranch(t *testing.T) {
	r, db, token := newAuthed(t)

	if err := db.Migrator().DropTable("system_settings"); err != nil {
		t.Fatalf("删表失败: %v", err)
	}

	// Get:Find 报错被忽略,返回默认值兜底。
	var m map[string]string
	w := do(r, token, http.MethodGet, "/api/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("坏表 Get 应 200(吞错+默认值): %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &m)
	if m["default_arch"] != "amd64" {
		t.Fatalf("坏表 Get 应回退默认值: %v", m)
	}

	// Update:Save 到不存在的表必然失败,应返回 500 而非静默吞错。
	w = do(r, token, http.MethodPut, "/api/settings", map[string]any{"default_arch": "arm64"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("坏表 Put 应 500(保存失败): %d %s", w.Code, w.Body.String())
	}

	// 恢复表后确认值没有被保存进去(证明 Save 确实失败)。
	if err := db.Migrator().CreateTable(&model.SystemSetting{}); err != nil {
		t.Fatalf("重建表失败: %v", err)
	}
	w = do(r, token, http.MethodGet, "/api/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("恢复后 Get 失败: %d", w.Code)
	}
	decode(t, w, &m)
	if m["default_arch"] != "amd64" {
		t.Fatalf("坏表期间的保存不应生效: %v", m)
	}
}
