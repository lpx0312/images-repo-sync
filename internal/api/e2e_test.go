package api

// e2e_test 走完整链路:真实 store/task worker + fake skopeo + mock registry,
// 覆盖「登录 → 配置仓库 → 创建任务 → worker 执行 → SSE 回放」的用户主路径。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/task"
	"images-repo-sync/internal/testutil"
)

// TestE2ETaskLifecycle 端到端主链路:登录→建源/目标仓库→创建同步任务→
// worker 用 fake skopeo 复制→任务 success→SSE 回放明细。
func TestE2ETaskLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	testutil.UseFakeSkopeo(t)

	// mock registry:/v2/ 200(源仓库预登录探测)。
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer regSrv.Close()

	// 启动真实任务 worker(task.Instance 单例)。
	_ = task.Instance()

	r := SetupRouter(db, nil)

	// 1. 登录拿 token。
	w := do(r, "", http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": "Admin@123",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	decode(t, w, &login)
	token := login.Token

	// 2. 建源/目标仓库。
	srcID := createRegistryViaAPI(t, r, token, "e2e-src", regSrv.URL, "generic")
	dstID := createRegistryViaAPI(t, r, token, "e2e-dst", "dst.example.com", "generic")

	// 3. 创建任务(fake skopeo 会成功复制)。
	w = do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"source_registry_id":  srcID,
		"target_registry_ids": []uint{dstID},
		"mode":                "flat",
		"target_project":      "e2e-mirror",
		"refs":                []string{"docker.io/library/nginx:1.25", "bitnami/redis:7.0"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建任务失败: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID    uint `json:"id"`
		Total int  `json:"total"`
	}
	decode(t, w, &created)
	if created.Total != 2 {
		t.Fatalf("total = %d", created.Total)
	}

	// 4. 轮询等待 worker 执行完成。
	testutil.WaitFor(t, 30*time.Second, "任务完成", func() bool {
		var tk model.SyncTask
		db.First(&tk, created.ID)
		return tk.Status == model.TaskStatusSuccess || tk.Status == model.TaskStatusFailed
	})
	var tk model.SyncTask
	db.Preload("Items").First(&tk, created.ID)
	if tk.Status != model.TaskStatusSuccess {
		body := ""
		for _, it := range tk.Items {
			body += fmt.Sprintf("[%s] %s", it.Status, it.Error)
		}
		t.Fatalf("e2e 任务应成功: %s items: %s", tk.Status, body)
	}
	if tk.Succeeded != 2 {
		t.Fatalf("succeeded = %d", tk.Succeeded)
	}
	for _, it := range tk.Items {
		if !strings.HasPrefix(it.TargetRef, "dst.example.com/e2e-mirror/") {
			t.Errorf("目标引用不对: %s", it.TargetRef)
		}
	}

	// 5. 列表与详情。
	w = do(r, token, http.MethodGet, "/api/tasks?status=success", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total":1`) {
		t.Errorf("列表: %s", w.Body.String())
	}
	w = do(r, token, http.MethodGet, fmt.Sprintf("/api/tasks/%d", created.ID), nil)
	if !strings.Contains(w.Body.String(), "e2e-mirror") {
		t.Errorf("详情: %s", w.Body.String())
	}

	// 6. SSE:已结束任务回放明细与终态。
	streamResp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks/%d/stream", created.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(streamResp, req)
	events := collectSSEEvents(streamResp.Body.String())
	hasSnapshot, hasFinished := false, false
	for _, ev := range events {
		if strings.HasPrefix(ev, "event: snapshot") {
			hasSnapshot = true
		}
		if strings.HasPrefix(ev, "event: task_finished") && strings.Contains(ev, `"success"`) {
			hasFinished = true
		}
	}
	if !hasSnapshot || !hasFinished {
		t.Fatalf("SSE 回放缺事件: snapshot=%v finished=%v, events=%v", hasSnapshot, hasFinished, events)
	}

	// 7. settings 全链路:读改读。
	w = do(r, token, http.MethodPut, "/api/settings", map[string]any{"default_arch": "arm64"})
	if w.Code != http.StatusOK {
		t.Fatalf("设置更新失败: %d", w.Code)
	}
	var m map[string]string
	decode(t, do(r, token, http.MethodGet, "/api/settings", nil), &m)
	if m["default_arch"] != "arm64" {
		t.Fatalf("设置未生效: %v", m)
	}
}

// collectSSEEvents 按空行切分 SSE 帧。
func collectSSEEvents(raw string) []string {
	var events []string
	var cur strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			if cur.Len() > 0 {
				events = append(events, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		events = append(events, cur.String())
	}
	return events
}
