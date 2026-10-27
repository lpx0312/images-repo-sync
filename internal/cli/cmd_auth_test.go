package cli

// cmd 层测试的共享设施:mock 平台 + 输出捕获。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newCmdApp 构造输出到临时文件的 App(捕获命令输出)。
func newCmdApp(t *testing.T, server, token string) *App {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out-*.txt")
	if err != nil {
		t.Fatalf("创建输出文件失败: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return &App{Server: server, Token: token, out: f}
}

// readOut 读取 App 当前全部输出。
func readOut(t *testing.T, a *App) string {
	t.Helper()
	if _, err := a.out.Seek(0, 0); err != nil {
		t.Fatalf("seek 失败: %v", err)
	}
	data, err := io.ReadAll(a.out)
	if err != nil {
		t.Fatalf("读输出失败: %v", err)
	}
	return string(data)
}

// apiServer 起 mock 平台并构造连到它的 App。
func apiServer(t *testing.T, handler http.HandlerFunc) (*App, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return newCmdApp(t, ts.URL, "test-token"), ts
}

// dataResp 输出 {data:...} 包装。
func dataResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": v})
}

// errResp 输出 {error:...}。
func errResp(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func TestCmdVersionAndHelp(t *testing.T) {
	a := newCmdApp(t, "", "")
	if err := cmdVersion(a, nil); err != nil {
		t.Fatalf("version 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "irs CLI v") {
		t.Errorf("输出: %q", out)
	}
}

func TestCmdVersionWithConfig(t *testing.T) {
	a := newCmdApp(t, "", "")
	cfgPath := t.TempDir() + "/cfg.json"
	a.ConfigPath = cfgPath
	if err := (&Config{Server: "http://x:8080"}).Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := cmdVersion(a, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, "已配置服务端") {
		t.Errorf("应显示服务端: %q", out)
	}
}

func TestCmdLoginPasswordMode(t *testing.T) {
	var gotUser, gotPass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/healthz":
			w.WriteHeader(200)
		case "/api/auth/login":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotUser, _ = body["username"].(string)
			gotPass, _ = body["password"].(string)
			dataResp(w, map[string]any{
				"token": "tok-1", "expires_at": "2026-01-02T15:04:05Z",
				"user": map[string]any{"username": "admin"},
			})
		default:
			errResp(w, 404, "no")
		}
	}))
	defer ts.Close()

	cfgPath := t.TempDir() + "/cfg.json"
	a := newCmdApp(t, "", "")
	a.ConfigPath = cfgPath
	if err := cmdLogin(a, []string{"--server", ts.URL, "--username", "admin", "--password", "Admin@123"}); err != nil {
		t.Fatalf("login 失败: %v", err)
	}
	if gotUser != "admin" || gotPass != "Admin@123" {
		t.Errorf("提交的凭证: %s/%s", gotUser, gotPass)
	}
	// 配置已保存。
	cfg, err := LoadConfig(cfgPath)
	if err != nil || cfg.Token != "tok-1" || cfg.Server != ts.URL || cfg.Username != "admin" {
		t.Fatalf("配置保存不对: %+v %v", cfg, err)
	}
	if out := readOut(t, a); !strings.Contains(out, "登录成功") {
		t.Errorf("输出: %q", out)
	}
}

func TestCmdLoginTokenMode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/me" {
			dataResp(w, map[string]any{"username": "admin", "status": "active"})
			return
		}
		if r.URL.Path == "/api/healthz" {
			w.WriteHeader(200)
			return
		}
		errResp(w, 404, "no")
	}))
	defer ts.Close()

	a := newCmdApp(t, "", "")
	a.ConfigPath = t.TempDir() + "/cfg.json"
	if err := cmdLogin(a, []string{"--server", ts.URL, "--token", "existing-token"}); err != nil {
		t.Fatalf("token 登录失败: %v", err)
	}
	cfg, _ := LoadConfig(a.ConfigPath)
	if cfg.Token != "existing-token" || cfg.Username != "admin" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestCmdLoginErrors(t *testing.T) {
	a := newCmdApp(t, "", "")
	a.ConfigPath = t.TempDir() + "/cfg.json"

	// 缺 server。
	if err := cmdLogin(a, []string{"--password", "x"}); err == nil || !strings.Contains(err.Error(), "缺少 --server") {
		t.Errorf("缺 server 应报错: %v", err)
	}
	// 平台不可达。
	if err := cmdLogin(a, []string{"--server", "http://127.0.0.1:1", "--password", "x"}); err == nil || !strings.Contains(err.Error(), "不可达") {
		t.Errorf("不可达应报错: %v", err)
	}
	// 缺密码。
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer okSrv.Close()
	if err := cmdLogin(a, []string{"--server", okSrv.URL}); err == nil || !strings.Contains(err.Error(), "缺少密码") {
		t.Errorf("缺密码应报错: %v", err)
	}
	// 登录被拒。
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			errResp(w, 401, "用户名或密码错误")
			return
		}
		w.WriteHeader(200)
	}))
	defer deny.Close()
	if err := cmdLogin(a, []string{"--server", deny.URL, "--password", "bad"}); err == nil || !strings.Contains(err.Error(), "登录失败") {
		t.Errorf("登录失败应报错: %v", err)
	}
	// token 模式校验失败。
	badToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/me" {
			errResp(w, 401, "无效")
			return
		}
		w.WriteHeader(200)
	}))
	defer badToken.Close()
	if err := cmdLogin(a, []string{"--server", badToken.URL, "--token", "x"}); err == nil || !strings.Contains(err.Error(), "token 校验失败") {
		t.Errorf("token 校验失败应报错: %v", err)
	}
	// flag 解析错误。
	if err := cmdLogin(a, []string{"--no-such-flag"}); err == nil {
		t.Error("未知 flag 应报错")
	}
}

func TestCmdLoginEnvFallback(t *testing.T) {
	// IRS_SERVER 环境变量提供地址;IRS_PASSWORD 提供密码。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/healthz":
			w.WriteHeader(200)
		case "/api/auth/login":
			dataResp(w, map[string]any{"token": "t", "expires_at": "", "user": map[string]any{"username": "admin"}})
		default:
			errResp(w, 404, "no")
		}
	}))
	defer ts.Close()
	t.Setenv("IRS_SERVER", ts.URL)
	t.Setenv("IRS_PASSWORD", "Admin@123")

	a := newCmdApp(t, "", "")
	a.ConfigPath = t.TempDir() + "/cfg.json"
	if err := cmdLogin(a, nil); err != nil {
		t.Fatalf("env 登录失败: %v", err)
	}
}

func TestCmdLogoutAndWhoami(t *testing.T) {
	cfgPath := t.TempDir() + "/cfg.json"
	(&Config{Server: "http://x", Token: "t"}).Save(cfgPath)

	a := newCmdApp(t, "", "")
	a.ConfigPath = cfgPath
	if err := cmdLogout(a, nil); err != nil {
		t.Fatalf("logout 失败: %v", err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Error("配置文件应被删除")
	}

	// whoami。
	b, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"id": 1, "username": "admin", "status": "active"})
	})
	if err := cmdWhoami(b, nil); err != nil {
		t.Fatalf("whoami 失败: %v", err)
	}
	if out := readOut(t, b); !strings.Contains(out, "admin") {
		t.Errorf("输出: %q", out)
	}

	// whoami --json。
	c, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"id": 1, "username": "admin", "status": "active"})
	})
	c.JSONOut = true
	if err := cmdWhoami(c, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, c); !strings.Contains(out, `"username": "admin"`) {
		t.Errorf("json 输出: %q", out)
	}
}

func TestCmdHealth(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if err := cmdHealth(a, nil); err != nil {
		t.Fatalf("health 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "平台正常") {
		t.Errorf("输出: %q", out)
	}

	// 显式 --server。
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if err := cmdHealth(a2, []string{"--server", a2.Server}); err == nil {
		t.Error("500 应报错")
	}
	// 未配置 server(隔离宿主 IRS_SERVER 环境变量与 ~/.irs-cli.json)。
	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")
	a3 := newCmdApp(t, "", "")
	a3.ConfigPath = t.TempDir() + "/not-exist.json"
	if err := cmdHealth(a3, nil); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Errorf("未配置且未给 --server 应报错: %v", err)
	}
	// 回归锁定(已修复): A1 无任何全局配置时 `health --server URL` 应直接成功(confirmClient 不应先于 --server 解析拦截)。
	okTS, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if err := cmdHealth(a3, []string{"--server", okTS.Server}); err != nil {
		t.Errorf("health --server <URL> 在无全局配置时应成功: %v", err)
	}
	// JSON 模式。
	a4, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	a4.JSONOut = true
	if err := cmdHealth(a4, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a4); !strings.Contains(out, `"status": "ok"`) {
		t.Errorf("json: %q", out)
	}
}

func TestConfirmClientNoServer(t *testing.T) {
	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")
	a := &App{ConfigPath: t.TempDir() + "/not-exist.json"}
	if _, err := a.confirmClient(); err == nil || !strings.Contains(err.Error(), "未配置服务端地址") {
		t.Errorf("应提示配置服务端: %v", err)
	}
	// 配置文件提供 server。
	cfgPath := t.TempDir() + "/cfg.json"
	(&Config{Server: "http://from-config:8080"}).Save(cfgPath)
	b := &App{ConfigPath: cfgPath}
	c, err := b.confirmClient()
	if err != nil || c.Server != "http://from-config:8080" {
		t.Fatalf("config server: %v %+v", err, c)
	}
}

func TestSmallHelpers(t *testing.T) {
	if boolMark(true) != "是" || boolMark(false) != "-" {
		t.Error("boolMark")
	}
	if emptyDash("") != "-" || emptyDash("x") != "x" {
		t.Error("emptyDash")
	}
	if joinNonEmpty("a", "", "b") != "a / b" {
		t.Error("joinNonEmpty")
	}
	if orDefault(nil, func() string { return "x" }) != "" {
		t.Error("orDefault nil 应空串")
	}
	if orDefault(&registryVO{Name: "n"}, func() string { return "n" }) != "n" {
		t.Error("orDefault")
	}
	if ensureYes(false, "删除") == nil {
		t.Error("未确认应报错")
	}
	if ensureYes(true, "删除") != nil {
		t.Error("已确认应通过")
	}
	if requireArgs([]string{}, 1, "usage") == nil {
		t.Error("参数不足应报错")
	}
	if requireArgs([]string{"a"}, 1, "usage") != nil {
		t.Error("参数足够应通过")
	}
	if taskFinished("success") != true || taskFinished("running") != false {
		t.Error("taskFinished")
	}
	if queryEscape("a b") != "a+b" {
		t.Error("queryEscape")
	}
	fs := newFlagSet("x", "usage")
	if fs == nil {
		t.Error("newFlagSet")
	}
	if yesFlag(fs) == nil {
		t.Error("yesFlag")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "c") != "c" || firstNonEmpty() != "" {
		t.Error("firstNonEmpty")
	}
}

// TestRunHelpAndVersion Run 分发的帮助/版本路径。
func TestRunHelpAndVersion(t *testing.T) {
	// Run 内部新建 App,输出到 stdout;这里只验证返回码(不 panic)。
	if code := Run([]string{"help"}); code != 0 {
		t.Errorf("help 退出码 = %d", code)
	}
	if code := Run([]string{"--version"}); code != 0 {
		t.Errorf("version 退出码 = %d", code)
	}
	if code := Run([]string{"no-such-cmd"}); code != 2 {
		t.Errorf("未知命令退出码 = %d", code)
	}
	if code := Run([]string{"--bad-global"}); code != 2 {
		t.Errorf("未知全局选项退出码 = %d", code)
	}
	// 命令执行失败退出码 1。
	if code := Run([]string{"--server", "http://127.0.0.1:1", "health"}); code != 1 {
		t.Errorf("失败命令退出码 = %d", code)
	}
}
