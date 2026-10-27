package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const taskJSON = `{"id":1,"source_registry_id":1,"target_registry_id":2,"mode":"flat","target_project":"p","arch":"amd64","total":2,"succeeded":1,"failed":1,"skipped":0,"status":"success","error":"","created_at":"2026-01-02T15:04:05Z","items":[{"id":1,"source_ref":"nginx:1","target_ref":"t/nginx:1","status":"success","error":"","digest":"sha256:a"},{"id":2,"source_ref":"redis:7","target_ref":"t/redis:7","status":"failed","error":"unauthorized","digest":""}]}`

const snapshotJSON = `{"id":1,"status":"running","mode":"flat","arch":"amd64","total":1,"succeeded":0,"failed":0,"skipped":0,"target_project":"p","error":"","created_at":"2026-01-02T15:04:05Z","items":[]}`

// newSSEServer 起一个顺序下发 SSE 帧的服务,返回 URL。
func newSSEServer(t *testing.T, frames []string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, f := range frames {
			fmt.Fprint(w, f)
			flusher.Flush()
		}
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// sseSnapshot 构造 snapshot 帧(与真实 Event 信封一致:taskVO 在 data 键内)。
func sseSnapshot() string {
	return "event: snapshot\ndata: {\"data\":" + snapshotJSON + "}\n\n"
}

// sseFinished 构造 task_finished 帧。
func sseFinished(status string, succ, fail, skip int) string {
	return fmt.Sprintf("event: task_finished\ndata: {\"data\":{\"status\":\"%s\",\"succeeded\":%d,\"failed\":%d,\"skipped\":%d}}\n\n",
		status, succ, fail, skip)
}

func TestTasksListCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/tasks") {
			errResp(w, 404, "no")
			return
		}
		// 列表接口返回带分页的 envelope。
		w.Write([]byte(`{"data":[` + taskJSON + `],"total":5,"page":1,"size":20}`))
	})
	if err := cmdTasksList(a, []string{"--status", "success"}); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"共 5 个任务", "success ✓", "flat"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
	// JSON 模式。
	a.JSONOut = true
	if err := cmdTasksList(a, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, `"total": 5`) {
		t.Errorf("json: %q", out)
	}
}

func TestTasksGetCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, json.RawMessage(taskJSON))
	})
	if err := cmdTasksGet(a, []string{"1"}); err != nil {
		t.Fatalf("get 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"任务 #1", "成功: 1", "unauthorized", "nginx:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
	// 参数校验。
	if err := cmdTasksGet(a, nil); err == nil {
		t.Error("缺 ID 应报错")
	}
}

func TestTasksCreateCmdValidation(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { errResp(w, 500, "no") })

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"缺 source/target", []string{"--mode", "flat", "--refs", "x"}, "必填"},
		{"缺 mode", []string{"--source", "1", "--target", "2", "--refs", "x"}, "--mode 必填"},
		{"非法 mode", []string{"--source", "1", "--target", "2", "--mode", "mirror", "--refs", "x"}, "--mode 无效"},
		{"非法 arch", []string{"--source", "1", "--target", "2", "--mode", "flat", "--arch", "riscv", "--refs", "x"}, "--arch 无效"},
		{"空 refs", []string{"--source", "1", "--target", "2", "--mode", "flat"}, "镜像列表为空"},
	}
	for _, c := range cases {
		if err := cmdTasksCreate(a, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want 含 %q)", c.name, err, c.want)
		}
	}
}

func TestTasksCreateCmdSuccess(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, json.RawMessage(taskJSON))
	})
	err := cmdTasksCreate(a, []string{
		"--source", "1", "--target", "2", "--mode", "flat", "--project", "p",
		"--arch", "arm64", "--refs", "nginx:1, redis:7", "busybox:1",
	})
	if err != nil {
		t.Fatalf("create 失败: %v", err)
	}
	if gotBody["mode"] != "flat" || gotBody["target_project"] != "p" || gotBody["arch"] != "arm64" {
		t.Errorf("body = %v", gotBody)
	}
	refs, _ := gotBody["refs"].([]any)
	if len(refs) != 3 || refs[2] != "busybox:1" { // 逗号分隔 + 位置参数合并
		t.Errorf("refs = %v", refs)
	}
	if out := readOut(t, a); !strings.Contains(out, "任务 #1 已创建") {
		t.Errorf("输出: %q", out)
	}
}

func TestTasksCreateCmdRefsFile(t *testing.T) {
	// --refs-file 读取(# 注释与空行跳过)。
	dir := t.TempDir()
	path := dir + "/refs.txt"
	if err := writeFile(path, "# comment\nnginx:1\n\nredis:7\n"); err != nil {
		t.Fatal(err)
	}
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, json.RawMessage(taskJSON))
	})
	if err := cmdTasksCreate(a, []string{
		"--source", "1", "--target", "2", "--mode", "replace_host", "--refs-file", path,
	}); err != nil {
		t.Fatalf("create 失败: %v", err)
	}
	refs, _ := gotBody["refs"].([]any)
	if len(refs) != 2 || refs[0] != "nginx:1" || refs[1] != "redis:7" {
		t.Errorf("refs = %v", refs)
	}

	// refs 文件不存在。
	if err := cmdTasksCreate(a, []string{
		"--source", "1", "--target", "2", "--mode", "flat", "--refs-file", dir + "/missing.txt",
	}); err == nil || !strings.Contains(err.Error(), "读取 --refs-file 失败") {
		t.Errorf("缺失文件应报错: %v", err)
	}
}

func TestTasksCancelCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"message": "已请求取消"})
	})
	if err := cmdTasksCancel(a, []string{"3"}); err != nil {
		t.Fatalf("cancel 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "已请求取消任务 #3") {
		t.Errorf("输出: %q", out)
	}
	if err := cmdTasksCancel(a, nil); err == nil {
		t.Error("缺 ID 应报错")
	}
}

func TestTasksLogsCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, json.RawMessage(taskJSON))
	})
	// 非 follow:打印当前快照。
	if err := cmdTasksLogs(a, []string{"1"}); err != nil {
		t.Fatalf("logs 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "任务 #1") {
		t.Errorf("输出: %q", out)
	}
}

func TestFollowTaskSuccess(t *testing.T) {
	// SSE 服务端:snapshot → item_started → item_success → task_finished。
	url := newSSEServer(t, []string{
		sseSnapshot(),
		"event: item_started\ndata: {\"item_id\":1,\"source_ref\":\"nginx:1\",\"target_ref\":\"t/nginx:1\"}\n\n",
		"event: item_success\ndata: {\"item_id\":1,\"source_ref\":\"nginx:1\",\"target_ref\":\"t/nginx:1\",\"data\":{\"digest\":\"sha256:x\"}}\n\n",
		sseFinished("success", 1, 0, 0),
	})
	a := newCmdApp(t, url, "tok")
	if err := followTask(a, a.Client(), 1); err != nil {
		t.Fatalf("followTask 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"任务 #1", "[同步] nginx:1 -> t/nginx:1", "[成功] nginx:1 -> t/nginx:1", "任务结束: 状态 success"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
}

func TestFollowTaskFailure(t *testing.T) {
	// 有失败镜像 → 返回错误(进程退出码 1 语义)。
	url := newSSEServer(t, []string{
		sseSnapshot(),
		"event: item_failed\ndata: {\"item_id\":2,\"source_ref\":\"redis:7\",\"target_ref\":\"t/redis:7\",\"message\":\"unauthorized\"}\n\n",
		sseFinished("failed", 0, 1, 0),
	})
	a := newCmdApp(t, url, "tok")
	err := followTask(a, a.Client(), 1)
	if err == nil || !strings.Contains(err.Error(), "1 个镜像同步失败") {
		t.Fatalf("失败任务应报错: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "[失败] redis:7") || !strings.Contains(out, "unauthorized") {
		t.Errorf("输出: %q", out)
	}
}

func TestFollowTaskStreamDropped(t *testing.T) {
	// 流断开且无 task_finished → 提示任务可能仍在执行。
	url := newSSEServer(t, []string{
		sseSnapshot(),
	})
	a := newCmdApp(t, url, "tok")
	err := followTask(a, a.Client(), 1)
	if err == nil || !strings.Contains(err.Error(), "未收到任务结束事件") {
		t.Fatalf("断流应报错: %v", err)
	}
}

func TestFollowTaskJSONMode(t *testing.T) {
	url := newSSEServer(t, []string{
		sseSnapshot(),
		"event: item_failed\ndata: {\"item_id\":2,\"message\":\"\"}\n\n",
		sseFinished("failed", 0, 1, 0),
	})
	a := newCmdApp(t, url, "tok")
	a.JSONOut = true
	if err := followTask(a, a.Client(), 1); err == nil {
		t.Fatal("有失败应报错")
	}
	out := readOut(t, a)
	if !strings.Contains(out, `"status":"running"`) { // snapshot 原样输出(含 Event 信封)
		t.Errorf("json 输出: %q", out)
	}
	// message 为空的失败事件在文本模式下显示"未知错误"(JSON 模式不打印文本)。
}

func TestFollowTaskUnknownMessageFallback(t *testing.T) {
	url := newSSEServer(t, []string{
		sseSnapshot(),
		"event: item_failed\ndata: {\"item_id\":2,\"source_ref\":\"a:1\",\"target_ref\":\"b:1\",\"message\":\"\"}\n\n",
		sseFinished("failed", 0, 1, 0),
	})
	a := newCmdApp(t, url, "tok")
	_ = followTask(a, a.Client(), 1)
	if out := readOut(t, a); !strings.Contains(out, "未知错误") {
		t.Errorf("空 message 应显示未知错误: %q", out)
	}
}

func TestTaskEventDigest(t *testing.T) {
	ev := &taskEvent{Data: json.RawMessage(`{"digest":"sha256:abc"}`)}
	if ev.digest() != "sha256:abc" {
		t.Errorf("digest = %q", ev.digest())
	}
	bad := &taskEvent{Data: json.RawMessage(`not-json`)}
	if bad.digest() != "" {
		t.Errorf("坏数据应空串: %q", bad.digest())
	}
}
