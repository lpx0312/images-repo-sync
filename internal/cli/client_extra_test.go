package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIErrorError(t *testing.T) {
	e := &APIError{Status: 500, Message: "boom"}
	if got := e.Error(); got != "HTTP 500: boom" {
		t.Errorf("Error() = %q", got)
	}
	e2 := &APIError{Status: 502}
	if got := e2.Error(); got != "HTTP 502" {
		t.Errorf("Error() = %q", got)
	}
}

func TestDoOnceNoServer(t *testing.T) {
	c := NewClient("", "")
	_, err := c.doOnce(context.Background(), http.MethodGet, "/x", nil, "")
	if err == nil || !strings.Contains(err.Error(), "未配置服务端地址") {
		t.Errorf("应报未配置: %v", err)
	}
}

func TestDoJSONBadResponses(t *testing.T) {
	// 顶层非 JSON。
	ts := newRawServer(t, "not json at all")
	c := NewClient(ts, "tok")
	if _, err := c.DoJSON(context.Background(), "GET", "/api/x", nil, nil); err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Errorf("应报非法 JSON: %v", err)
	}

	// data 类型与 out 不符。
	ts2 := newRawServer(t, `{"data":{"a":1},"total":0,"page":1,"size":1}`)
	c2 := NewClient(ts2, "tok")
	var list []string
	if _, err := c2.DoJSON(context.Background(), "GET", "/api/x", nil, &list); err == nil || !strings.Contains(err.Error(), "解析响应数据失败") {
		t.Errorf("应报解析失败: %v", err)
	}

	// 非 2xx 带 {error}。
	ts3 := newRawErrServer(t, 503, `{"error":"维护中"}`)
	c3 := NewClient(ts3, "tok")
	var out map[string]any
	_, err := c3.DoJSON(context.Background(), "GET", "/api/x", nil, &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "维护中" {
		t.Errorf("APIError 透传: %v", err)
	}

	// 非 2xx 且非 JSON 错误体:截断为 message。
	ts4 := newRawErrServer(t, 500, strings.Repeat("x", 400))
	c4 := NewClient(ts4, "tok")
	_, err = c4.DoJSON(context.Background(), "GET", "/api/x", nil, nil)
	if !errors.As(err, &apiErr) || len(apiErr.Message) > 303 {
		t.Errorf("超长错误应截断: %d", len(apiErr.Message))
	}
}

func newRawServer(t *testing.T, body string) string {
	return newServerURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
}

func newRawErrServer(t *testing.T, code int, body string) string {
	return newServerURL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	})
}

func newServerURL(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	return apiServerURL(t, h)
}

func TestUploadFilesClientErrors(t *testing.T) {
	// 文件不存在。
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {})
	c := a.Client()
	if _, err := c.UploadFiles(context.Background(), "/api/charts/upload-files", "files",
		[]string{"Z:/definitely/missing.tgz"}, nil); err == nil || !strings.Contains(err.Error(), "打开文件") {
		t.Errorf("应报打开失败: %v", err)
	}

	// 服务端 401。
	b, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		errResp(w, 401, "token 过期")
	})
	c2 := b.Client()
	p := filepath.Join(t.TempDir(), "a.tgz")
	os.WriteFile(p, []byte("x"), 0o644)
	// Username/Password 未配置 → 不自动重登,直接 401。
	_, err := c2.UploadFiles(context.Background(), "/api/charts/upload-files", "files", []string{p}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Errorf("应 401: %v", err)
	}

	// 成功路径 envelope 解析。
	d, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"created": []any{}})
	})
	env, err := d.Client().UploadFiles(context.Background(), "/api/charts/upload-files", "files", []string{p},
		map[string]string{"repo_id": "1"})
	if err != nil || env == nil {
		t.Fatalf("上传失败: %v", err)
	}
}

func TestRequestBadURL(t *testing.T) {
	// Server 为非法 URL:NewRequest 成功但 Do 失败。
	c := NewClient("http://127.0.0.1:1", "")
	if _, err := c.doOnce(context.Background(), "GET", "/x", nil, ""); err == nil {
		t.Error("不可达应报错")
	}
	// 方法非法 → NewRequest 失败。
	c2 := NewClient("http://127.0.0.1:1", "")
	if _, err := c2.doOnce(context.Background(), "BAD METHOD", "/x", nil, ""); err == nil {
		t.Error("非法方法应报错")
	}
}

func TestBodyBytesPassthrough(t *testing.T) {
	// body 为 []byte 时原样发送。
	var got string
	url := newServerURL(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		got = string(buf[:n])
		dataResp(w, map[string]any{})
	})
	c := NewClient(url, "")
	if _, err := c.request(context.Background(), "POST", "/x", []byte("raw-bytes"), "application/octet-stream", false); err != nil {
		t.Fatal(err)
	}
	if got != "raw-bytes" {
		t.Errorf("body = %q", got)
	}
}

func TestClientPriorityAndTokenRefresh(t *testing.T) {
	// flag > 环境变量 > 配置文件。
	cfgPath := t.TempDir() + "/cfg.json"
	(&Config{Server: "http://from-config", Token: "cfg-token"}).Save(cfgPath)

	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")
	a := &App{ConfigPath: cfgPath}
	c := a.Client()
	if c.Server != "http://from-config" || c.Token != "cfg-token" {
		t.Fatalf("config 优先级: %s %s", c.Server, c.Token)
	}

	// flag 覆盖。
	a2 := &App{Server: "http://flag-server", Token: "flag-token", ConfigPath: cfgPath}
	c2 := a2.Client()
	if c2.Server != "http://flag-server" || c2.Token != "flag-token" {
		t.Fatalf("flag 优先级: %s %s", c2.Server, c2.Token)
	}

	// OnTokenRefresh:自动重登后写回配置文件。
	a3 := &App{Server: "http://flag-server", ConfigPath: cfgPath}
	c3 := a3.Client()
	if c3.OnTokenRefresh == nil {
		t.Fatal("Client 应注册 OnTokenRefresh 回调")
	}
	c3.OnTokenRefresh("new-token", "2027-01-01T00:00:00Z")
	cfg, _ := LoadConfig(cfgPath)
	if cfg.Token != "new-token" || cfg.TokenExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("token 未持久化: %+v", cfg)
	}
}

func TestChartCmdsJSONMode(t *testing.T) {
	payload := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/test"):
			dataResp(w, map[string]any{"ok": true, "message": "连接成功"})
		case strings.Contains(r.URL.Path, "/chart-repos"):
			dataResp(w, []map[string]any{{"id": 1, "name": "n", "type": "oci", "host": "h", "project": "p"}})
		case strings.Contains(r.URL.Path, "/uploads"):
			w.Write([]byte(`{"data":[{"id":1,"chart_name":"c","chart_version":"1","status":"success"}],"total":1,"page":1,"size":20}`))
		default:
			errResp(w, 404, "no")
		}
	}
	a, _ := apiServer(t, payload)
	a.JSONOut = true
	if err := cmdChartReposList(a, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, `"name": "n"`) {
		t.Errorf("json: %q", out)
	}
	if err := cmdChartReposTest(a, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdChartsUploads(a, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, `"chart_name": "c"`) {
		t.Errorf("json: %q", out)
	}
}

func TestChartsUploadWithWait(t *testing.T) {
	// 上传后轮询,立即 success。
	tgz := filepath.Join(t.TempDir(), "demo-0.1.0.tgz")
	os.WriteFile(tgz, buildChartTgzBytes(t, "demo", "0.1.0"), 0o644)

	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/upload-files") {
			dataResp(w, map[string]any{
				"created": []map[string]any{{"id": 21}},
				"invalid": []map[string]any{},
			})
			return
		}
		w.Write([]byte(`{"data":[{"id":21,"status":"success","chart_name":"demo","chart_version":"0.1.0",
			"target_ref":"oci://h/p/demo:0.1.0","file_name":"demo.tgz","size":10,"error":"","digest":"sha256:x",
			"created_at":"2026-01-02T15:04:05Z","finished_at":null}],"total":1,"page":1,"size":20}`))
	})
	if err := cmdChartsUpload(a, []string{"9", tgz}); err != nil { // 默认 --wait
		t.Fatalf("upload+wait 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "全部 1 个 chart 上传成功") {
		t.Errorf("输出: %q", out)
	}
}

func TestChartsUploadPathWithWait(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/upload-paths") {
			dataResp(w, map[string]any{
				"created": []map[string]any{{"id": 22}},
				"invalid": []map[string]any{{"name": "x.tgz", "reason": "坏包"}},
			})
			return
		}
		w.Write([]byte(`{"data":[{"id":22,"status":"failed","chart_name":"c","chart_version":"1",
			"target_ref":"oci://h/p/c:1","file_name":"c.tgz","size":10,"error":"仓库拒绝","digest":"",
			"created_at":"2026-01-02T15:04:05Z","finished_at":null}],"total":1,"page":1,"size":20}`))
	})
	err := cmdChartsUploadPath(a, []string{"9", "/srv/charts/a.tgz"})
	if err == nil || !strings.Contains(err.Error(), "1 个 chart 上传失败") {
		t.Fatalf("失败应报错: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "[跳过] x.tgz") {
		t.Errorf("输出: %q", out)
	}
}

func TestChartsUploadTooMany(t *testing.T) {
	// 超过 100 个文件被拒。
	dir := t.TempDir()
	var args []string
	for i := 0; i < 101; i++ {
		p := filepath.Join(dir, string(rune('a'+i%26))+"-"+string(rune('0'+i%10))+".tgz")
		os.WriteFile(p, []byte("x"), 0o644)
		args = append(args, p)
	}
	// 文件名需唯一,重新用编号命名。
	args = nil
	for i := 0; i < 101; i++ {
		p := filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+".tgz")
		os.WriteFile(p, []byte("x"), 0o644)
		args = append(args, p)
	}
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := cmdChartsUpload(a, append([]string{"9"}, args...)); err == nil || !strings.Contains(err.Error(), "最多上传 100 个") {
		t.Errorf("超量应报错: %v", err)
	}
}

func TestRegistriesTestJSONMode(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": true, "message": "连接成功"})
	})
	a.JSONOut = true
	if err := cmdRegistriesTest(a, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, `"ok": true`) {
		t.Errorf("json: %q", out)
	}
}

func TestLogoutError(t *testing.T) {
	// ConfigPath 指向非空目录:os.Remove 失败。
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x"), []byte("y"), 0o644)
	a := &App{ConfigPath: dir}
	if err := cmdLogout(a, nil); err == nil {
		t.Error("删除非空目录应报错")
	}
}

func TestCmdTaskLogsFollow(t *testing.T) {
	// logs --follow 走 followTask。
	url := newSSEServer(t, []string{
		sseSnapshot(),
		sseFinished("success", 0, 0, 0),
	})
	a := newCmdApp(t, url, "tok")
	if err := cmdTasksLogs(a, []string{"1", "--follow"}); err != nil {
		t.Fatalf("logs --follow 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "任务结束") {
		t.Errorf("输出: %q", out)
	}
}

func TestPingErrors(t *testing.T) {
	// 非 200。
	url := newServerURL(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	c := NewClient(url, "")
	if err := c.Ping(context.Background()); err == nil {
		t.Error("503 应报错")
	}
	// 不可达。
	c2 := NewClient("http://127.0.0.1:1", "")
	if err := c2.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "服务不可达") {
		t.Errorf("应报不可达: %v", err)
	}
}

// apiServerURL 供 newServerURL 复用的 server 启动。
func apiServerURL(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	_, ts := apiServer(t, h)
	return ts.URL
}
