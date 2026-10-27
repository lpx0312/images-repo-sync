package cli

// b_edge_test 是 B 类边界测试:断言当前实现即应满足的行为,必须 GREEN。
// 不加 t.Parallel;临时文件一律 t.TempDir()。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamSSEUnauthorizedBranch 非 200(401)分支应返回携带状态码的 APIError。
func TestStreamSSEUnauthorizedBranch(t *testing.T) {
	url := newServerURL(t, func(w http.ResponseWriter, r *http.Request) {
		errResp(w, 401, "token 无效")
	})
	c := NewClient(url, "tok")
	err := c.StreamSSE(context.Background(), "/api/tasks/1/stream", func(ev SSEEvent) bool { return true })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("401 应返回 APIError{401},实际 %v", err)
	}
}

// TestStreamSSEBodyReadError Body 读取中途连接被重置(RST,非干净 EOF)应返回错误。
func TestStreamSSEBodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		// 先下发一帧完整事件,再以 RST 方式断开连接。
		rw.WriteString("event: snapshot\ndata: {}\n\n")
		rw.Flush()
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0) // Close 时发 RST,客户端读取得到错误而非 EOF
		}
		conn.Close()
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	err := c.StreamSSE(context.Background(), "/stream", func(ev SSEEvent) bool { return true })
	if err == nil {
		t.Fatal("连接中途被重置应返回读错误,而非 nil")
	}
}

// TestCollectRefsMixedSeparators 空格/逗号混排、空串过滤与顺序保持。
func TestCollectRefsMixedSeparators(t *testing.T) {
	fs := newFlagSet("test", "")
	fs.String("refs", "", "")
	fs.String("refs-file", "", "")
	if err := fs.Parse([]string{"--refs", " a:1, , b:2 ,,c:3 ", " d:4 "}); err != nil {
		t.Fatal(err)
	}
	got, err := collectRefs(fs, fs.Args())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a:1", "b:2", "c:3", "d:4"}
	if len(got) != len(want) {
		t.Fatalf("混合分隔应得 %v,实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序/内容不符: %v", got)
		}
	}
}

// TestPrintTableCellWithTab 单元格含 \t 不应 panic,内容仍完整输出。
func TestPrintTableCellWithTab(t *testing.T) {
	var buf bytes.Buffer
	PrintTable(&buf, []string{"A", "B"}, [][]string{
		{"x\ty", "z"},
		{"p", "q"},
	})
	out := buf.String()
	for _, want := range []string{"A", "B", "x", "y", "z", "p", "q"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
}

// TestWaitUploadsTargetOnSecondPage 目标上传记录被挤到第 2 页时应翻页命中。
func TestWaitUploadsTargetOnSecondPage(t *testing.T) {
	row := func(id int) string {
		return fmt.Sprintf(`{"id":%d,"status":"success","chart_name":"c","chart_version":"1",`+
			`"target_ref":"oci://h/p/c:1","file_name":"c.tgz","size":10,"error":"","digest":"sha256:x",`+
			`"created_at":"2026-01-02T15:04:05Z","finished_at":null}`, id)
	}
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		var rows []string
		if r.URL.Query().Get("page") == "1" {
			for i := 1; i <= 100; i++ { // 第 1 页 100 条无关记录
				rows = append(rows, row(i))
			}
		} else { // 第 2 页含目标记录 999
			rows = append(rows, row(999))
		}
		fmt.Fprintf(w, `{"data":[%s],"total":150,"page":2,"size":100}`, strings.Join(rows, ","))
	})
	if err := a.waitUploads(a.Client(), []uint{999}, true); err != nil {
		t.Fatalf("目标在第 2 页应翻页命中并成功: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "全部 1 个 chart 上传成功") {
		t.Errorf("输出: %q", out)
	}
}

// TestSettingsGetJSONOutput settings get --json 输出应为合法 JSON 且字段正确。
func TestSettingsGetJSONOutput(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"default_arch": "amd64", "sync_interval": "300"})
	})
	a.JSONOut = true
	if err := cmdSettingsGet(a, nil); err != nil {
		t.Fatalf("settings get 失败: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(readOut(t, a)), &m); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if m["default_arch"] != "amd64" {
		t.Errorf("default_arch = %q", m["default_arch"])
	}
}
