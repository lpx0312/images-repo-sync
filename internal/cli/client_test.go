package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestDoJSONUnwrap 验证 {data}/{error} 包装的解包与错误提取。
func TestDoJSONUnwrap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ok":
			fmt.Fprint(w, `{"data": {"v": 42}}`)
		case "/api/list":
			fmt.Fprint(w, `{"data": [1,2,3], "total": 3, "page": 1, "size": 20}`)
		case "/api/bad":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "参数不完整"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error": "接口不存在"}`)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	ctx := context.Background()

	var v struct{ V int }
	env, err := c.DoJSON(ctx, "GET", "/api/ok", nil, &v)
	if err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if v.V != 42 {
		t.Fatalf("data 未解包: %+v", v)
	}
	if env.Total != 0 {
		t.Fatalf("ok 接口不应有 total")
	}

	var list []int
	env, err = c.DoJSON(ctx, "GET", "/api/list", nil, &list)
	if err != nil || len(list) != 3 || env.Total != 3 {
		t.Fatalf("列表解包失败: %v %+v", err, env)
	}

	_, err = c.DoJSON(ctx, "GET", "/api/bad", nil, nil)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 400 || apiErr.Message != "参数不完整" {
		t.Fatalf("错误解包失败: %v", err)
	}

	// 无 token 时也应发出请求(此处 404 属正常路径)。
	_, err = c.DoJSON(ctx, "GET", "/api/none", nil, nil)
	if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 404 {
		t.Fatalf("404 应为 APIError: %v", err)
	}
}

// TestClient401AutoRelogin 验证 token 失效时用账号密码自动重登并重试。
func TestClient401AutoRelogin(t *testing.T) {
	var mu sync.Mutex
	validToken := "new" // 登录后签发的新 token
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/auth/login":
			fmt.Fprint(w, `{"data": {"token": "new", "user": {"username": "admin"}}}`)
		default:
			mu.Lock()
			ok := auth == "Bearer "+validToken
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error": "token 无效"}`)
				return
			}
			fmt.Fprint(w, `{"data": {"ok": true}}`)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "expired")
	c.Username = "admin"
	c.Password = "pass"
	refreshed := ""
	c.OnTokenRefresh = func(tok, exp string) { refreshed = tok }

	if _, err := c.DoJSON(context.Background(), "GET", "/api/data", nil, nil); err != nil {
		t.Fatalf("应自动重登成功: %v", err)
	}
	if refreshed != "new" || c.Token != "new" {
		t.Fatalf("token 应更新为 new: %q", c.Token)
	}
}

// TestLoginWrongPassword 登录失败应返回 APIError 而非自动重试。
func TestLoginWrongPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": "用户名或密码错误"}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	_, err := c.Login(context.Background(), "admin", "wrong", false)
	if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 401 {
		t.Fatalf("应为 401 APIError: %v", err)
	}
}

// TestStreamSSE 验证 SSE 帧解析(事件边界、心跳注释、断流结束)。
// 载荷使用与服务端一致的 Event 信封格式:{type, ..., data:{...}}。
func TestStreamSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n") // 心跳注释
		fmt.Fprint(w, "event: snapshot\ndata: {\"type\":\"snapshot\",\"task_id\":7,\"data\":{\"id\":7}}\n\n")
		fmt.Fprint(w, "event: item_success\ndata: {\"source_ref\":\"nginx\",\"data\":{\"digest\":\"sha256:abc\"}}\n\n")
		fmt.Fprint(w, "event: task_finished\ndata: {\"data\":{\"status\":\"success\"}}\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	var events []string
	err := c.StreamSSE(context.Background(), "/api/tasks/7/stream", func(ev SSEEvent) bool {
		events = append(events, ev.Event+"|"+string(ev.Data))
		return true
	})
	if err != nil {
		t.Fatalf("StreamSSE: %v", err)
	}
	want := []string{
		`snapshot|{"type":"snapshot","task_id":7,"data":{"id":7}}`,
		`item_success|{"source_ref":"nginx","data":{"digest":"sha256:abc"}}`,
		`task_finished|{"data":{"status":"success"}}`,
	}
	if len(events) != len(want) {
		t.Fatalf("事件数 %d != %d: %v", len(events), len(want), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("事件 %d: got %q want %q", i, events[i], want[i])
		}
	}
}

// TestTaskEventEnvelope 信封二次解析:snapshot 的 data 应能解出 taskVO。
func TestTaskEventEnvelope(t *testing.T) {
	raw := []byte(`{"type":"snapshot","task_id":31,"data":{"id":31,"status":"running","total":2,"succeeded":1,"items":[{"id":1,"source_ref":"a","target_ref":"b"}]}}`)
	var env taskEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var tv taskVO
	if err := json.Unmarshal(env.Data, &tv); err != nil {
		t.Fatalf("snapshot data 应为 taskVO: %v", err)
	}
	if tv.ID != 31 || tv.Total != 2 || len(tv.Items) != 1 {
		t.Fatalf("taskVO 解析不符: %+v", tv)
	}
	// item_success 的 digest 二次解析。
	raw2 := []byte(`{"source_ref":"a","target_ref":"b","data":{"digest":"sha256:x"}}`)
	var env2 taskEvent
	_ = json.Unmarshal(raw2, &env2)
	if env2.digest() != "sha256:x" || env2.SourceRef != "a" {
		t.Fatalf("item 事件解析不符: %+v", env2)
	}
}

// TestStreamSSEStopCallback 回调返回 false 时应停止读取。
func TestStreamSSEStopCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 100; i++ {
			fmt.Fprintf(w, "event: e%d\ndata: {}\n\n", i)
		}
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond) // 让客户端有机会提前断开
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	count := 0
	_ = c.StreamSSE(context.Background(), "/stream", func(ev SSEEvent) bool {
		count++
		return count < 3
	})
	if count != 3 {
		t.Fatalf("应在第 3 个事件后停止,实际 %d", count)
	}
}

// TestUploadFiles 验证 multipart 上传与服务端响应解包。
func TestUploadFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "表单错误"}`)
			return
		}
		if r.FormValue("repo_id") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "缺 repo_id"}`)
			return
		}
		files := r.MultipartForm.File["files"]
		fmt.Fprintf(w, `{"data": {"created": [{"id": %d}]}}`, len(files))
	}))
	defer srv.Close()

	dir := t.TempDir()
	f1 := dir + "/a.tgz"
	f2 := dir + "/b.tgz"
	for _, f := range []string{f1, f2} {
		if err := writeBytes(f, "fake-tgz"); err != nil {
			t.Fatal(err)
		}
	}
	c := NewClient(srv.URL, "")
	env, err := c.UploadFiles(context.Background(), "/api/charts/upload-files", "files", []string{f1, f2}, map[string]string{"repo_id": "1"})
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}
	if got := string(env.Data); got != `{"created": [{"id": 2}]}` {
		t.Fatalf("上传响应不符: %s", got)
	}
}
