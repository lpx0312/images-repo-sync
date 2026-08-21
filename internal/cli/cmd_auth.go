package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// authCommands 登录态与服务信息相关命令。
var authCommands = []command{
	{name: "login", usage: "[--server URL] [--username U] [--password P] [--token T]",
		desc: "登录平台并保存凭据到配置文件", run: cmdLogin},
	{name: "logout", usage: "", desc: "清除本地保存的登录凭据", run: cmdLogout},
	{name: "whoami", usage: "", desc: "显示当前登录用户", run: cmdWhoami},
	{name: "health", usage: "[--server URL]", desc: "平台健康检查(无需登录)", run: cmdHealth},
	{name: "version", usage: "", desc: "显示 CLI 版本", run: cmdVersion},
}

// cmdLogin 两种方式:
//   - --token:直接采用已有 JWT(如从环境或其他会话取得),会用 /auth/me 校验;
//   - 账号密码:用户名默认 admin,密码来自 --password 或 IRS_PASSWORD 环境变量。
//
// 成功后把 server/token/username 写入配置文件,后续命令免传。
func cmdLogin(a *App, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "平台地址,如 http://localhost:8080")
	username := fs.String("username", "", "用户名(默认 admin)")
	password := fs.String("password", "", "密码(也可用 IRS_PASSWORD 环境变量)")
	token := fs.String("token", "", "直接使用已有 token,跳过账号密码登录")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("参数错误,用法: irs login %s", "[--server URL] [--username U] [--password P] [--token T]")
	}

	serverV := firstNonEmpty(*server, os.Getenv("IRS_SERVER"))
	if serverV == "" {
		if cfg, err := a.LoadConfig(); err == nil {
			serverV = cfg.Server
		}
	}
	if serverV == "" {
		return fmt.Errorf("缺少 --server(或 IRS_SERVER 环境变量),例: irs login --server http://localhost:8080")
	}

	c := NewClient(serverV, "")
	cCtx, cancel := ctx()
	defer cancel()
	if err := c.Ping(cCtx); err != nil {
		return fmt.Errorf("平台不可达(%s): %w", serverV, err)
	}

	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Server = c.Server

	switch {
	case *token != "":
		c.Token = *token
		var me struct {
			Username string `json:"username"`
			Status   string `json:"status"`
		}
		if _, err := c.DoJSON(cCtx, "GET", "/api/auth/me", nil, &me); err != nil {
			return fmt.Errorf("token 校验失败: %w", err)
		}
		cfg.Token = *token
		cfg.Username = me.Username
		a.Printf("登录成功: %s (用户 %s, token 模式)\n", c.Server, me.Username)

	default:
		user := firstNonEmpty(*username, os.Getenv("IRS_USER"), os.Getenv("IRS_USERNAME"), "admin")
		pass := firstNonEmpty(*password, os.Getenv("IRS_PASSWORD"), os.Getenv("IRS_PASS"))
		if pass == "" {
			return fmt.Errorf("缺少密码:请用 --password 或设置 IRS_PASSWORD 环境变量")
		}
		res, err := c.Login(cCtx, user, pass, false)
		if err != nil {
			return fmt.Errorf("登录失败: %w", err)
		}
		cfg.Token = res.Token
		cfg.TokenExpiresAt = res.ExpiresAt
		cfg.Username = res.Username
		exp := FmtTime(res.ExpiresAt)
		a.Printf("登录成功: %s (用户 %s,token 有效期至 %s)\n", c.Server, res.Username, exp)
	}

	if err := cfg.Save(a.ConfigPath); err != nil {
		return fmt.Errorf("登录成功但保存配置失败: %w", err)
	}
	path, _ := DefaultConfigPath()
	if a.ConfigPath != "" {
		path = a.ConfigPath
	}
	a.Printf("凭据已保存到 %s\n", path)
	return nil
}

// cmdLogout 删除配置文件(服务端 JWT 无状态,无需通知)。
func cmdLogout(a *App, args []string) error {
	if err := Clear(a.ConfigPath); err != nil {
		return err
	}
	a.Printf("已登出,本地凭据已清除\n")
	return nil
}

// cmdWhoami GET /api/auth/me。
func cmdWhoami(a *App, args []string) error {
	fs := a.newFlagSet("whoami", "[--json]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	var me struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Status   string `json:"status"`
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/auth/me", nil, &me); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), me)
	}
	a.Printf("用户: %s (ID %d, 状态 %s)\n", me.Username, me.ID, me.Status)
	return nil
}

// cmdHealth GET /api/healthz(公开接口)。
func cmdHealth(a *App, args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "平台地址")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("参数错误")
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	if *server != "" {
		c = NewClient(*server, "")
	}
	cCtx, cancel := ctx()
	defer cancel()
	if err := c.Ping(cCtx); err != nil {
		return err
	}
	if a.JSONOut {
		a.Printf(`{"server": %q, "status": "ok"}`+"\n", c.Server)
		return nil
	}
	a.Printf("平台正常: %s\n", c.Server)
	return nil
}

// cmdVersion 打印版本(顺带显示生效的服务端地址,便于排查)。
func cmdVersion(a *App, args []string) error {
	a.Printf("irs CLI v%s (images-repo-sync)\n", Version)
	if cfg, err := a.LoadConfig(); err == nil && cfg.Server != "" {
		a.Printf("已配置服务端: %s\n", cfg.Server)
	}
	return nil
}

// newFlagSet 构造统一的 FlagSet(ContinueOnError + 简短用法错误)。
func newFlagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: irs %s %s\n", name, usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseIDArg 解析并校验十进制正整数 ID 参数。
func parseIDArg(name, s string) (uint, error) {
	var id uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("%s 需要是数字 ID,收到 %q", name, s)
		}
		id = id*10 + uint64(ch-'0')
		if id > 1<<32 {
			return 0, fmt.Errorf("%s 数值过大", name)
		}
	}
	if id == 0 {
		return 0, fmt.Errorf("缺少 %s 参数", name)
	}
	return uint(id), nil
}

// requireArgs 校验位置参数个数。
func requireArgs(args []string, n int, usage string) error {
	if len(args) < n {
		return fmt.Errorf("参数不足,用法: %s", usage)
	}
	return nil
}

// yesFlag --yes 确认标志(删除等破坏性操作强制要求)。
func yesFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("yes", false, "确认执行(删除操作必须)")
}

// ensureYes 检查确认标志。
func ensureYes(yes bool, what string) error {
	if !yes {
		return fmt.Errorf("%s 是破坏性操作,请追加 --yes 确认", what)
	}
	return nil
}

// hostOr 文件读取辅助:refs 文件每行一条,# 开头为注释。
func readRefsFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 --refs-file 失败: %w", err)
	}
	var refs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		refs = append(refs, line)
	}
	return refs, nil
}
