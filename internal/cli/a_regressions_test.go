package cli

// a_regressions_test 是代码审查发现缺陷的 A 类回归测试:
// 断言"修复后的正确行为"。对应缺陷已在阶段 4 修复,当前应全部保持绿色;
// 注释中的编号对应 docs/review-2026-10-27.md 的发现编号。不加 t.Parallel。

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 回归锁定(已修复): A1 无任何全局配置时 `irs health --server URL` 应直接使用该地址成功(confirmClient 不应先拦截)。
func TestRunHealthServerFlagWithoutGlobalConfig(t *testing.T) {
	// 隔离宿主环境变量与 ~/.irs-cli.json。
	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	cfg := filepath.Join(t.TempDir(), "none.json")
	if code := Run([]string{"--config", cfg, "health", "--server", a.Server}); code != 0 {
		t.Errorf("health --server URL 无全局配置应退出 0,实际 %d", code)
	}
}

// 回归锁定(已修复): A2 Run(["--help"])/Run(["-h"]) 应输出帮助并退出 0,而非"无法识别的全局选项"退出 2。
func TestRunHelpShortFlags(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		if code := Run(args); code != 0 {
			t.Errorf("Run(%v) 应输出帮助并返回 0,实际 %d", args, code)
		}
	}
}

// 回归锁定(已修复): A3 --servername 不应被 --server 前缀误匹配吞掉下一个参数,应报无法识别的全局选项。
func TestParseGlobalsFlagPrefixMismatch(t *testing.T) {
	if _, _, err := parseGlobals([]string{"--servername", "v", "health"}); err == nil {
		t.Error("--servername v 应报错(无法识别的全局选项),不应被误当作 --server 的值")
	}
	// 对照:--server=URL 形式应可用(修复后必须保持)。
	app, rest, err := parseGlobals([]string{"--server=http://a:1", "health"})
	if err != nil || app.Server != "http://a:1" || len(rest) != 1 || rest[0] != "health" {
		t.Errorf("--server=URL 形式应可用: %+v %v %v", app, rest, err)
	}
}

// 回归锁定(已修复): A4 tasks create 位置参数之后跟 `--wait` 不应被吞进镜像列表,应报错提示。
func TestTasksCreateDashAfterPositional(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, json.RawMessage(taskJSON))
	})
	err := cmdTasksCreate(a, []string{
		"--source", "1", "--target", "2", "--mode", "flat", "nginx:1", "--wait",
	})
	if err == nil || !strings.Contains(err.Error(), "--wait") {
		t.Errorf("位置参数后的 --wait 应报错提示而非吞进 refs: %v", err)
	}
}

// 回归锁定(已修复): A5 多次 --refs 应累加为多条镜像,而非静默覆盖只留最后一个。
func TestCollectRefsRepeatedRefsFlag(t *testing.T) {
	fs := newFlagSet("test", "")
	var refs refsValue // 生产代码的可累积 --refs 注册方式(原 fs.String 不具备累积语义)
	fs.Var(&refs, "refs", "")
	fs.String("refs-file", "", "")
	if err := fs.Parse([]string{"--refs", "a:1", "--refs", "b:2"}); err != nil {
		t.Fatal(err)
	}
	got, err := collectRefs(fs, fs.Args())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Errorf("--refs a:1 --refs b:2 应累加为 [a:1 b:2],实际 %v", got)
	}
}

// 回归锁定(已修复): A5 生产路径——cmdTasksCreate 自身注册的可累积 --refs,多次提供应合并提交。
// 防止注册方式回退为 fs.String 后,仅靠手工 fs.Var 的上层用例仍绿。
func TestCmdTasksCreateRepeatedRefsEndToEnd(t *testing.T) {
	var body []byte
	srvApp, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":7}}`))
	})
	a := newCmdApp(t, srvApp.Server, "tok")
	if err := cmdTasksCreate(a, []string{
		"--source", "1", "--target", "2", "--mode", "flat",
		"--refs", "a:1", "--refs", "b:2",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a:1", "b:2"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("请求体应包含多次 --refs 提供的 %s: %s", want, body)
		}
	}
}

// 回归锁定(已修复): A6 迟到订阅者:snapshot 已含 failed=1 且 task_finished 报 failed=1 时应返回非 nil(退出码非 0)。
func TestFollowTaskLateSubscriberFailedSnapshot(t *testing.T) {
	snap := `{"id":1,"status":"running","mode":"flat","arch":"amd64","total":1,"succeeded":0,"failed":1,"skipped":0,"target_project":"p","error":"","created_at":"2026-01-02T15:04:05Z","items":[]}`
	url := newSSEServer(t, []string{
		"event: snapshot\ndata: {\"data\":" + snap + "}\n\n",
		sseFinished("failed", 0, 1, 0),
	})
	a := newCmdApp(t, url, "tok")
	if err := followTask(a, a.Client(), 1); err == nil {
		t.Error("快照与结束事件均含 failed=1 时应返回错误,不应只统计本会话 item_failed 事件")
	}
}

// 回归锁定(已修复): A7 服务端返回 size=0 时 tasks list 不应除零 panic。
func TestTasksListSizeZeroNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("size=0 触发除零 panic: %v", r)
		}
	}()
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[],"total":0,"page":1,"size":0}`))
	})
	if err := cmdTasksList(a, nil); err != nil {
		t.Fatalf("空列表 size=0 应正常成功: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "共 0 个任务") {
		t.Errorf("输出: %q", out)
	}
}

// 回归锁定(已修复): A8 refs 文件 UTF-8 BOM 首行应被剥离(Windows 记事本场景)。
func TestReadRefsFileUTF8BOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refs-bom.txt")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbfnginx:1\nredis:7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := readRefsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != "nginx:1" || refs[1] != "redis:7" {
		t.Errorf("BOM 首行应被剥离: %q", refs)
	}
}
