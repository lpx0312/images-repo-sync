package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

func TestTaskCreateValidation(t *testing.T) {
	r, _, token := newAuthed(t)
	src := createRegistryViaAPI(t, r, token, "src", "src.example.com", "generic")
	dst := createRegistryViaAPI(t, r, token, "dst", "dst.example.com", "generic")

	cases := []struct {
		name string
		body map[string]any
		code int
		want string
	}{
		{"缺 refs", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat"}, 400, "参数不完整"},
		{"空 refs", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat", "refs": []string{}}, 400, "镜像列表不能为空"},
		{"refs 全空白", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat", "target_project": "p", "refs": []string{" "}}, 400, "没有有效的镜像"},
		{"无目标", map[string]any{"target_registry_ids": []uint{}, "mode": "flat", "refs": []string{"nginx"}}, 400, "至少选择一个目标仓库"},
		{"目标全 0", map[string]any{"target_registry_ids": []uint{0}, "mode": "flat", "refs": []string{"nginx"}}, 400, "至少选择一个目标仓库"},
		{"目标不存在", map[string]any{"target_registry_ids": []uint{999}, "mode": "flat", "refs": []string{"nginx"}}, 400, "不存在"},
		{"非法架构", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat", "refs": []string{"nginx"}, "arch": "i386"}, 400, "架构参数无效"},
		{"源不存在", map[string]any{"source_registry_id": 999, "target_registry_ids": []uint{dst}, "mode": "flat", "refs": []string{"nginx"}}, 400, "源仓库不存在"},
		{"源=目标", map[string]any{"source_registry_id": src, "target_registry_ids": []uint{src}, "mode": "flat", "refs": []string{"nginx"}}, 400, "源和目标仓库不能相同"},
		{"模式①缺 project", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat", "refs": []string{"nginx"}}, 400, "project"},
		{"模式②缺 project", map[string]any{"target_registry_ids": []uint{dst}, "mode": "preserve_path", "refs": []string{"nginx"}}, 400, "project"},
		{"非法模式", map[string]any{"target_registry_ids": []uint{dst}, "mode": "mirror", "target_project": "p", "refs": []string{"nginx"}}, 400, "解析镜像"},
		{"自动匹配源未配置", map[string]any{"target_registry_ids": []uint{dst}, "mode": "flat", "target_project": "p", "refs": []string{"gcr.io/x/y:1"}}, 400, "源镜像组"},
	}
	for _, c := range cases {
		w := do(r, token, http.MethodPost, "/api/tasks", c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s (want %d 含 %q)", c.name, w.Code, w.Body.String(), c.code, c.want)
		}
	}
}

func TestTaskCreateTooMany(t *testing.T) {
	r, _, token := newAuthed(t)
	dst := createRegistryViaAPI(t, r, token, "dst", "dst.example.com", "generic")

	refs := make([]string, 501)
	for i := range refs {
		refs[i] = "nginx:" + jsonInt(int64(i))
	}
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dst}, "mode": "flat", "target_project": "p", "refs": refs,
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "500 条") {
		t.Fatalf("超量应 400: %d %s", w.Code, w.Body.String())
	}

	// 目标数量超限。
	ids := make([]uint, 11)
	for i := range ids {
		ids[i] = dst
	}
	w = do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": ids, "mode": "flat", "target_project": "p", "refs": []string{"nginx"},
	})
	// 同一目标会被去重,先建 11 个不同目标再测。
	if w.Code == 200 {
		t.Log("同一目标被去重;11 目标超限场景见 TestTaskCreateMaxTargets")
	}
}

func TestTaskCreateMaxTargets(t *testing.T) {
	r, _, token := newAuthed(t)
	ids := make([]uint, 0, 11)
	for i := 0; i < 11; i++ {
		ids = append(ids, createRegistryViaAPI(t, r, token, "t"+jsonInt(int64(i)), "t"+jsonInt(int64(i))+".example.com", "generic"))
	}
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": ids, "mode": "flat", "target_project": "p", "refs": []string{"nginx"},
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "10 个目标") {
		t.Fatalf("目标超限应 400: %d %s", w.Code, w.Body.String())
	}
}

func TestTaskCreateSuccessExplicitSource(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, _, token := newAuthed(t)
	src := createRegistryViaAPI(t, r, token, "src", "src.example.com", "generic")
	dst := createRegistryViaAPI(t, r, token, "dst", "dst.example.com", "generic")

	var out taskVO
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"source_registry_id":  src,
		"target_registry_ids": []uint{dst, dst}, // 重复目标应去重
		"mode":                "flat",
		"target_project":      "mirror",
		"refs":                []string{"nginx:1.25", "nginx:1.25", " docker://redis:7 ", ""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &out)
	if out.Total != 2 { // 2 个有效 ref(去重+去空白) × 1 个目标(去重)
		t.Fatalf("total = %d, want 2 (body=%+v)", out.Total, out)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d", len(out.Items))
	}
	// 目标引用按 flat 模式展开。
	if out.Items[0].TargetRef != "dst.example.com/mirror/nginx:1.25" {
		t.Errorf("target_ref = %q", out.Items[0].TargetRef)
	}
	if out.Items[1].SourceRef != "redis:7" || out.Items[1].TargetRef != "dst.example.com/mirror/redis:7" {
		t.Errorf("item[1] = %+v", out.Items[1])
	}
}

func TestTaskCreateAutoMatchSource(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)

	// 源镜像组:StripScheme 匹配(host 带 http:// 前缀也应匹配)。
	groupReg := model.Registry{Name: "group-src", Host: "http://docker.src.example.com", Type: "generic", InSourceGroup: true}
	db.Create(&groupReg)
	dstID := createRegistryViaAPI(t, r, token, "dst", "dst.example.com", "generic")

	var out taskVO
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dstID},
		"mode":                "preserve_path",
		"target_project":      "mirror",
		"refs":                []string{"docker.src.example.com/team/app:1.0", "nginx:1.25"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("自动匹配创建失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &out)
	if len(out.Items) != 2 {
		t.Fatalf("items = %d", len(out.Items))
	}
	// 匿名 docker.io 源记录 SourceHost;匹配源记录 registry ID。
	foundAnon, foundGroup := false, false
	for _, it := range out.Items {
		if it.SourceHost == "docker.io" {
			foundAnon = true
		}
		if it.SourceRegistryID == groupReg.ID {
			foundGroup = true
		}
	}
	if !foundAnon || !foundGroup {
		t.Errorf("源分配不对: anon=%v group=%v items=%+v", foundAnon, foundGroup, out.Items)
	}

	// 自动匹配且源=目标 → 报错。
	db.Model(&model.Registry{}).Where("id = ?", dstID).Update("in_source_group", true)
	w = do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"target_registry_ids": []uint{dstID},
		"mode":                "flat", "target_project": "p",
		"refs": []string{"dst.example.com/x/y:1"},
	})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "源仓库与目标仓库相同") {
		t.Fatalf("源=目标应报错: %d %s", w.Code, w.Body.String())
	}
}

// taskVO/it 是 API 任务视图的本地镜像(只取断言需要的字段)。
type taskVO struct {
	ID     uint   `json:"id"`
	Total  int    `json:"total"`
	Status string `json:"status"`
	Items  []struct {
		SourceRegistryID uint   `json:"source_registry_id"`
		SourceHost       string `json:"source_host"`
		SourceRef        string `json:"source_ref"`
		TargetRef        string `json:"target_ref"`
		Status           string `json:"status"`
	} `json:"items"`
}

func TestTaskListGetCancel(t *testing.T) {
	r, db, token := newAuthed(t)

	seed := func(status string) uint {
		tk := model.SyncTask{Status: status, Mode: "flat", Total: 1}
		if err := db.Create(&tk).Error; err != nil {
			t.Fatalf("seed 失败: %v", err)
		}
		return tk.ID
	}
	idSuccess := seed(model.TaskStatusSuccess)
	seed(model.TaskStatusFailed) // 供状态过滤用例使用
	idPending := seed(model.TaskStatusPending)

	// List 全量。
	var list []taskVO
	var env struct {
		Data  json.RawMessage `json:"data"`
		Total int64           `json:"total"`
	}
	w := do(r, token, http.MethodGet, "/api/tasks?page=1&size=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list 失败: %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	json.Unmarshal(env.Data, &list)
	if env.Total != 3 || len(list) != 2 { // 分页生效,总数 3
		t.Fatalf("list = %d 条, total=%d", len(list), env.Total)
	}
	// DESC 排序:最新在前。
	if list[0].ID != idPending {
		t.Errorf("排序不对: 首条 = %d", list[0].ID)
	}

	// 状态过滤。
	w = do(r, token, http.MethodGet, "/api/tasks?status=failed", nil)
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Total != 1 {
		t.Fatalf("状态过滤 total = %d", env.Total)
	}

	// 非法分页参数回退默认。
	if w = do(r, token, http.MethodGet, "/api/tasks?page=-1&size=9999", nil); w.Code != http.StatusOK {
		t.Errorf("非法分页应 200: %d", w.Code)
	}

	// Get(含明细)。
	tk := model.SyncTask{Status: model.TaskStatusSuccess, Mode: "flat"}
	tk.Items = []model.SyncTaskItem{{SourceRef: "a:1", TargetRef: "b:1", Status: model.ItemStatusSuccess}}
	db.Create(&tk)
	var got taskVO
	w = do(r, token, http.MethodGet, "/api/tasks/"+itoa(tk.ID), nil)
	decode(t, w, &got)
	if len(got.Items) != 1 || got.Items[0].SourceRef != "a:1" {
		t.Fatalf("get items = %+v", got.Items)
	}
	if w = do(r, token, http.MethodGet, "/api/tasks/99999", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
	_ = idSuccess

	// Cancel:已结束 400。
	if w = do(r, token, http.MethodPost, "/api/tasks/"+itoa(idSuccess)+"/cancel", nil); w.Code != 400 {
		t.Errorf("已结束取消应 400: %d", w.Code)
	}
	// Cancel:pending(不在运行)直接置 canceled。
	if w = do(r, token, http.MethodPost, "/api/tasks/"+itoa(idPending)+"/cancel", nil); w.Code != 200 {
		t.Errorf("pending 取消失败: %d %s", w.Code, w.Body.String())
	}
	var after model.SyncTask
	db.First(&after, idPending)
	if after.Status != model.TaskStatusCanceled {
		t.Errorf("pending 取消后 = %s", after.Status)
	}
	// Cancel:不存在 404。
	if w = do(r, token, http.MethodPost, "/api/tasks/99999/cancel", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
}

func TestTaskStreamFinishedReplay(t *testing.T) {
	r, db, token := newAuthed(t)

	started := time.Now().Add(-time.Minute)
	finished := started.Add(10 * time.Second)
	tk := model.SyncTask{
		Status: model.TaskStatusSuccess, Mode: "flat", Total: 2,
		Succeeded: 2, StartedAt: &started, FinishedAt: &finished,
	}
	tk.Items = []model.SyncTaskItem{
		{SourceRef: "a:1", TargetRef: "b:1", Status: model.ItemStatusSuccess, Digest: "sha256:x", StartedAt: &started, FinishedAt: &finished},
		{SourceRef: "c:1", TargetRef: "d:1", Status: model.ItemStatusFailed, Error: "boom", StartedAt: &started, FinishedAt: &finished},
	}
	db.Create(&tk)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+itoa(tk.ID)+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("stream 失败: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"event: snapshot", "event: task_started", "event: item_success", "event: item_failed", "event: task_finished"} {
		if !strings.Contains(body, want) {
			t.Errorf("流缺少 %s:\n%s", want, body)
		}
	}
	if !strings.Contains(w.Body.String(), "sha256:x") || !strings.Contains(body, "boom") {
		t.Errorf("回放内容缺明细: %s", body)
	}

	// 不存在的任务 404。
	if w := do(r, token, http.MethodGet, "/api/tasks/99999/stream", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
}

func TestTaskStreamRunning(t *testing.T) {
	r, db, token := newAuthed(t)

	tk := model.SyncTask{Status: model.TaskStatusRunning, Mode: "flat", Total: 1}
	db.Create(&tk)

	// 请求带可取消 ctx:测试结束 cancel 让 handler goroutine 退出。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+itoa(tk.ID)+"/stream", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	// 运行中任务:发出 snapshot 后流保持打开(无 task_finished)。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.Body.String(), "event: snapshot") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(w.Body.String(), "event: snapshot") {
		t.Fatalf("应有快照: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "task_finished") {
		t.Fatalf("运行中任务不应发 finished: %q", w.Body.String())
	}
	// 断开客户端:handler 应退出,避免 goroutine 泄漏。
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后 stream handler 未退出")
	}
}

// TestItemEventTime 回放事件时间选择。
func TestItemEventTime(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fin := time.Now().Add(-1 * time.Hour)
	if got := itemEventTime(&started, &fin, time.Now()); !got.Equal(fin) {
		t.Error("优先 finished")
	}
	if got := itemEventTime(&started, nil, time.Now()); !got.Equal(started) {
		t.Error("次选 started")
	}
	if got := itemEventTime(nil, nil, started); !got.Equal(started) {
		t.Error("兜底 fallback")
	}
}

// TestMatchSourceGroup 单元级验证源镜像组匹配。
func TestMatchSourceGroup(t *testing.T) {
	_, db, _ := newAuthed(t)
	h := NewTaskHandler(db)
	db.Create(&model.Registry{Name: "g1", Host: "HTTPS://Up.Example.com/", Type: "generic", InSourceGroup: true})
	db.Create(&model.Registry{Name: "g2", Host: "plain.example.com", Type: "generic", InSourceGroup: true})
	db.Create(&model.Registry{Name: "not-in-group", Host: "other.example.com", Type: "generic"})

	matched, missing := h.matchSourceGroup([]string{
		"up.example.com/a:1", // 大小写与 scheme 不敏感
		"plain.example.com/b:1",
		"unknown.example.com/c:1", // 无匹配 → missing
		"nginx:1",                 // docker.io → 匿名,不算 missing
	})
	if _, ok := matched["up.example.com"]; !ok {
		t.Errorf("scheme/大小写匹配失败: %v", matched)
	}
	if _, ok := matched["plain.example.com"]; !ok {
		t.Errorf("裸 host 匹配失败: %v", matched)
	}
	if len(missing) != 1 || missing[0] != "unknown.example.com" {
		t.Errorf("missing = %v", missing)
	}
}

func itoa(n uint) string {
	return jsonInt(int64(n))
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
