package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"images-repo-sync/internal/testutil"
)

// writeFile 简写 os.WriteFile。
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestChartReposListCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, []map[string]any{
			{"id": 2, "name": "oci-repo", "type": "oci", "host": "h.example.com",
				"project": "charts", "username": "u", "has_password": true, "insecure": false},
		})
	})
	if err := cmdChartReposList(a, nil); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"共 1 个", "oci-repo", "charts"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
}

func TestChartReposCreateCmd(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"id": 4, "name": "n", "type": "oci", "host": "h", "project": "p"})
	})
	if err := cmdChartReposCreate(a, []string{
		"--name", "n", "--type", "oci", "--host", "h.example.com", "--project", "p",
	}); err != nil {
		t.Fatalf("create 失败: %v", err)
	}
	if gotBody["type"] != "oci" || gotBody["project"] != "p" {
		t.Errorf("body = %v", gotBody)
	}

	// 校验分支。
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--host", "h"}, "--name 与 --host 必填"},
		{[]string{"--name", "n", "--host", "h"}, "--type 必填"},
		{[]string{"--name", "n", "--host", "h", "--type", "ftp"}, "--type 无效"},
		{[]string{"--name", "n", "--host", "h", "--type", "oci"}, "必须提供 --project"},
	}
	for _, c := range cases {
		if err := cmdChartReposCreate(a, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (want %q)", c.args, err, c.want)
		}
	}
}

func TestChartReposUpdateCmd(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			dataResp(w, []map[string]any{
				{"id": 5, "name": "cur", "type": "oci", "host": "old", "project": "pp",
					"username": "u", "has_password": false, "insecure": false},
			})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"id": 5, "name": "cur", "type": "oci", "host": "new", "project": "pp"})
	})
	if err := cmdChartReposUpdate(a, []string{"5", "--host", "new.example.com"}); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	if gotBody["name"] != "cur" || gotBody["host"] != "new.example.com" || gotBody["project"] != "pp" {
		t.Errorf("预填不对: %v", gotBody)
	}
	// OCI 缺 project。
	if err := cmdChartReposUpdate(a, []string{"5", "--project="}); err == nil {
		t.Error("OCI 缺项目应报错")
	}
	// ID 不存在。
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, []map[string]any{})
	})
	if err := cmdChartReposUpdate(a2, []string{"5"}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("应报不存在: %v", err)
	}
}

func TestChartReposDeleteAndTestCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"message": "ok"})
	})
	if err := cmdChartReposDelete(a, []string{"5"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("应要求确认: %v", err)
	}
	if err := cmdChartReposDelete(a, []string{"5", "--yes"}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	b, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": false, "message": "连接失败", "detail": "refused"})
	})
	if err := cmdChartReposTest(b, []string{"5"}); err != nil {
		t.Fatalf("test 命令失败: %v", err)
	}
	if out := readOut(t, b); !strings.Contains(out, "连接失败") || !strings.Contains(out, "refused") {
		t.Errorf("输出: %q", out)
	}
}

func TestChartRepoDesc(t *testing.T) {
	if got := chartRepoDesc(&chartRepoVO{Host: "h", Project: "p"}); !strings.Contains(got, "项目 p") {
		t.Errorf("desc = %q", got)
	}
	if got := chartRepoDesc(&chartRepoVO{Host: "h"}); got != "h" {
		t.Errorf("desc = %q", got)
	}
}

func TestChartsUploadCmd(t *testing.T) {
	// 构造一个合法 chart tgz(tar+gzip 的 Chart.yaml)。
	dir := t.TempDir()
	tgz := filepath.Join(dir, "demo-0.1.0.tgz")
	if err := os.WriteFile(tgz, buildChartTgzBytes(t, "demo", "0.1.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotFormRepo string
	var gotFileCount int
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			gotFormRepo = r.FormValue("repo_id")
			gotFileCount = len(r.MultipartForm.File["files"])
		}
		dataResp(w, map[string]any{
			"created": []map[string]any{{"id": 11}},
			"invalid": []map[string]any{{"name": "bad.tgz", "reason": "不是有效的 gzip"}},
		})
	})
	if err := cmdChartsUpload(a, []string{"--no-wait", "9", tgz}); err != nil {
		t.Fatalf("upload 失败: %v", err)
	}
	if gotFormRepo != "9" || gotFileCount != 1 {
		t.Errorf("multipart: repo=%s files=%d", gotFormRepo, gotFileCount)
	}
	out := readOut(t, a)
	if !strings.Contains(out, "[跳过] bad.tgz") || !strings.Contains(out, "已创建 1 条上传记录") {
		t.Errorf("输出: %q", out)
	}

	// 参数不足 / 路径不存在。
	if err := cmdChartsUpload(a, []string{"9"}); err == nil {
		t.Error("缺文件应报错")
	}
	if err := cmdChartsUpload(a, []string{"9", filepath.Join(dir, "missing.tgz")}); err == nil || !strings.Contains(err.Error(), "路径不存在") {
		t.Errorf("缺失路径应报错: %v", err)
	}
	// 空目录。
	empty := t.TempDir()
	if err := cmdChartsUpload(a, []string{"9", empty}); err == nil || !strings.Contains(err.Error(), "没有找到") {
		t.Errorf("空目录应报错: %v", err)
	}
}

func TestChartsUploadPathCmd(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"created": []map[string]any{{"id": 12}}, "invalid": []map[string]any{}})
	})
	if err := cmdChartsUploadPath(a, []string{"--no-wait", "9", "/data/charts/a.tgz", "/data/charts"}); err != nil {
		t.Fatalf("upload-path 失败: %v", err)
	}
	paths, _ := gotBody["paths"].([]any)
	if len(paths) != 2 || gotBody["repo_id"] != float64(9) {
		t.Errorf("body = %v", gotBody)
	}
}

func TestChartsUploadsCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":12,"chart_repo_id":9,"repo_name":"r","repo_type":"oci",
			"target_ref":"oci://h/p/c:1.0","file_name":"c-1.0.tgz","chart_name":"c","chart_version":"1.0",
			"size":1048576,"status":"success","error":"","digest":"sha256:x",
			"created_at":"2026-01-02T15:04:05Z","finished_at":"2026-01-02T15:05:05Z"}],
			"total":1,"page":1,"size":20}`))
	})
	if err := cmdChartsUploads(a, []string{"--status", "success"}); err != nil {
		t.Fatalf("uploads 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"共 1 条上传记录", "c", "1.0MB", "success ✓"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}
}

func TestChartsRetryCmd(t *testing.T) {
	// retry 后轮询:第一次 pending,第二次 success → 全部成功。
	var poll int32
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/retry") {
			dataResp(w, map[string]any{"message": "已重新入队"})
			return
		}
		n := atomic.AddInt32(&poll, 1)
		status := "running"
		if n > 1 {
			status = "success"
		}
		w.Write([]byte(`{"data":[{"id":12,"status":"` + status + `","chart_name":"c","chart_version":"1.0",
			"target_ref":"oci://h/p/c:1.0","file_name":"c.tgz","size":10,"error":"","digest":"sha256:x",
			"created_at":"2026-01-02T15:04:05Z","finished_at":null}],"total":1,"page":1,"size":20}`))
	})
	if err := cmdChartsRetry(a, []string{"12"}); err != nil {
		t.Fatalf("retry 失败: %v", err)
	}
	if !strings.Contains(readOut(t, a), "全部 1 个 chart 上传成功") {
		t.Errorf("输出: %q", readOut(t, a))
	}
	// 缺 ID。
	if err := cmdChartsRetry(a, nil); err == nil {
		t.Error("缺 ID 应报错")
	}
}

func TestWaitUploadsFailure(t *testing.T) {
	// 轮询发现 failed → 返回错误。
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":12,"status":"failed","chart_name":"c","chart_version":"1.0",
			"target_ref":"oci://h/p/c:1.0","file_name":"c.tgz","size":10,"error":"boom","digest":"",
			"created_at":"2026-01-02T15:04:05Z","finished_at":null}],"total":1,"page":1,"size":20}`))
	})
	if err := a.waitUploads(a.Client(), []uint{12}, true); err == nil || !strings.Contains(err.Error(), "1 个 chart 上传失败") {
		t.Fatalf("失败应报错: %v", err)
	}
	if !strings.Contains(readOut(t, a), "[失败] c.tgz") {
		t.Errorf("输出: %q", readOut(t, a))
	}
}

func TestWaitUploadsNoWait(t *testing.T) {
	a := newCmdApp(t, "", "")
	if err := a.waitUploads(nil, []uint{1}, false); err != nil {
		t.Fatalf("不等待应直接成功: %v", err)
	}
}

func TestPrintUploadResult(t *testing.T) {
	a := newCmdApp(t, "", "")
	printUploadResult(a, &chartUploadVO{Status: "success", ChartName: "c", ChartVersion: "1",
		TargetRef: "oci://h/p/c:1", Digest: "sha256:x"})
	if out := readOut(t, a); !strings.Contains(out, "sha256:x") {
		t.Errorf("成功输出: %q", out)
	}
	printUploadResult(a, &chartUploadVO{Status: "failed", FileName: "f.tgz"})
	if out := readOut(t, a); !strings.Contains(out, "f.tgz") {
		t.Errorf("失败输出: %q", out)
	}
}

// buildChartTgzBytes 构造最小 chart tgz(复用 testutil 的构造器)。
func buildChartTgzBytes(t *testing.T, name, version string) []byte {
	t.Helper()
	return testutil.BuildChartTgz(t, name, version, name)
}
