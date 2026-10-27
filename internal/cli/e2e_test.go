package cli

// e2e_test 用真实后端(api.SetupRouter + 真实 DB + task worker + fake skopeo)
// 验证 irs 命令行工具的完整用户路径:
// health → login → registries create → tasks create --wait → tasks get → logout。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"images-repo-sync/internal/api"
	"images-repo-sync/internal/task"
	"images-repo-sync/internal/testutil"
)

// jsonDecode 从 reader 解 JSON。
func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

// startPlatform 启动完整后端(HTTP 可达),返回 base URL。
func startPlatform(t *testing.T) string {
	t.Helper()
	db := testutil.NewTestDB(t)
	testutil.UseFakeSkopeo(t)
	_ = task.Instance() // 启动 worker

	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // /v2/ ping 200,源仓库探测通过
	}))
	t.Cleanup(regSrv.Close)
	_ = regSrv

	r := api.SetupRouter(db, nil)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestE2ECLIFullFlow(t *testing.T) {
	base := startPlatform(t)
	cfgPath := t.TempDir() + "/irs-e2e.json"

	// 隔离宿主环境变量与 ~/.irs-cli.json。
	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")
	t.Setenv("IRS_USER", "")
	t.Setenv("IRS_PASSWORD", "")

	run := func(args ...string) int {
		full := append([]string{"--config", cfgPath}, args...)
		return Run(full)
	}

	// 1. 未配置时提示。
	if code := run("whoami"); code != 1 {
		t.Errorf("未登录 whoami 退出码 = %d, want 1", code)
	}

	// 2. health(公开接口)。
	if code := run("--server", base, "health"); code != 0 {
		t.Fatalf("health 退出码 = %d", code)
	}

	// 3. 登录。
	if code := run("login", "--server", base, "--username", "admin", "--password", "Admin@123"); code != 0 {
		t.Fatalf("login 退出码 = %d", code)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil || cfg.Token == "" || cfg.Server != base {
		t.Fatalf("登录凭据未保存: %+v %v", cfg, err)
	}

	// 4. whoami 用配置文件的凭据。
	a := newCmdApp(t, "", "")
	a.ConfigPath = cfgPath
	if err := cmdWhoami(a, nil); err != nil {
		t.Fatalf("whoami 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "admin") {
		t.Errorf("whoami 输出: %q", out)
	}

	// 5. 建源/目标仓库。
	if code := run("registries", "create", "--name", "e2e-src",
		"--host", mockRegistryHost(t), "--type", "generic",
		"--username", "u", "--password", "p"); code != 0 {
		t.Fatalf("建源仓库退出码 = %d", code)
	}
	if code := run("registries", "create", "--name", "e2e-dst",
		"--host", "e2e-dst.example.com", "--type", "generic"); code != 0 {
		t.Fatalf("建目标仓库退出码 = %d", code)
	}

	// 6. registries list。
	b := newCmdApp(t, base, cfg.Token)
	if err := cmdRegistriesList(b, nil); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	if out := readOut(t, b); !strings.Contains(out, "e2e-src") || !strings.Contains(out, "e2e-dst") {
		t.Errorf("list 输出: %q", out)
	}

	// 7. 创建任务并等待(fake skopeo 复制成功)。
	if code := run("tasks", "create", "--source", "1", "--target", "2",
		"--mode", "flat", "--project", "mirror", "--refs", "docker.io/library/nginx:1.25",
		"--wait"); code != 0 {
		t.Fatalf("tasks create --wait 退出码 = %d", code)
	}

	// 8. tasks get 验证明细。
	c := newCmdApp(t, base, cfg.Token)
	if err := cmdTasksGet(c, []string{"1"}); err != nil {
		t.Fatalf("tasks get 失败: %v", err)
	}
	out := readOut(t, c)
	if !strings.Contains(out, "success ✓") || !strings.Contains(out, "e2e-dst.example.com/mirror/nginx:1.25") {
		t.Errorf("任务详情: %q", out)
	}

	// 9. settings 读写。
	if code := run("settings", "set", "--default-arch", "all"); code != 0 {
		t.Fatalf("settings set 退出码 = %d", code)
	}
	d := newCmdApp(t, base, cfg.Token)
	if err := cmdSettingsGet(d, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, d); !strings.Contains(out, "all") {
		t.Errorf("settings: %q", out)
	}

	// 10. logout 后凭据清空。
	if code := run("logout"); code != 0 {
		t.Fatalf("logout 退出码 = %d", code)
	}
	if _, err := LoadConfig(cfgPath); err != nil || !isEmptyConfig(cfgPath) {
		cfg2, _ := LoadConfig(cfgPath)
		if cfg2.Token != "" {
			t.Errorf("logout 后 token 应清空: %+v", cfg2)
		}
	}
}

// mockRegistryHost 源仓库探测用的 200 /v2/ 服务地址。
func mockRegistryHost(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// isEmptyConfig 判断配置文件已清空或删除。
func isEmptyConfig(path string) bool {
	cfg, err := LoadConfig(path)
	return err != nil || (cfg.Server == "" && cfg.Token == "")
}

// TestE2ECLITaskFailure 任务失败镜像时 CLI 退出码 1。
func TestE2ECLITaskFailure(t *testing.T) {
	base := startPlatform(t)
	cfgPath := t.TempDir() + "/irs-fail.json"
	t.Setenv("IRS_SERVER", "")
	t.Setenv("IRS_TOKEN", "")

	run := func(args ...string) int {
		return Run(append([]string{"--config", cfgPath, "--server", base, "--token", e2eAdminToken(t, base)}, args...))
	}

	if code := run("registries", "create", "--name", "src", "--host", mockRegistryHost(t)); code != 0 {
		t.Fatalf("建源仓库失败: %d", code)
	}
	if code := run("registries", "create", "--name", "dst", "--host", "dst.example.com"); code != 0 {
		t.Fatalf("建目标仓库失败: %d", code)
	}
	// failcopy 关键字让 fake skopeo 失败。
	if code := run("tasks", "create", "--source", "1", "--target", "2",
		"--mode", "flat", "--project", "m", "--refs", "docker.io/x/failcopy:1", "--wait"); code != 1 {
		t.Fatalf("失败任务退出码 = %d, want 1", code)
	}
}

// e2eAdminToken 通过 API 登录拿 token(失败路径测试用)。
func e2eAdminToken(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Post(base+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"Admin@123"}`))
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	defer resp.Body.Close()
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := jsonDecode(resp.Body, &env); err != nil || env.Data.Token == "" {
		t.Fatalf("token 解析失败: %v", err)
	}
	return env.Data.Token
}
