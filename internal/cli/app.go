package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

// Version 是 CLI 版本号,发布时通过 ldflags 注入。
var Version = "0.1.0"

// newFlagSet 构造绑定 App 的 FlagSet,并注册 --json 输出开关,
// 使 --json 既可放在命令之前(全局选项)也可放在命令之后。
func (a *App) newFlagSet(name, usage string) *flag.FlagSet {
	fs := newFlagSet(name, usage)
	fs.BoolVar(&a.JSONOut, "json", a.JSONOut, "以 JSON 输出结果(适合脚本与 AI 解析)")
	return fs
}

// App 汇集一次 CLI 调用的全局选项与登录态,供各命令共享。
type App struct {
	Server     string // --server 或 IRS_SERVER 或配置文件
	Token      string // --token 或 IRS_TOKEN 或配置文件
	JSONOut    bool   // --json:原样输出 JSON(便于脚本/AI 解析)
	ConfigPath string // --config:配置文件路径(默认 ~/.irs-cli.json)

	cfg *Config
	out *os.File // 输出流(测试时可替换)
}

// Out 返回输出目标(默认 stdout)。
func (a *App) Out() *os.File {
	if a.out != nil {
		return a.out
	}
	return os.Stdout
}

// Printf 是 fmt.Printf 到 App 输出流的快捷方式。
func (a *App) Printf(format string, args ...any) {
	fmt.Fprintf(a.Out(), format, args...)
}

// LoadConfig 加载配置文件(已加载则直接返回)。
func (a *App) LoadConfig() (*Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := LoadConfig(a.ConfigPath)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// Client 按优先级合并 flag > 环境变量 > 配置文件,构造 API 客户端。
// 环境变量提供的账号密码(IRS_USER/IRS_PASSWORD)同时用于 token 过期自动重登。
func (a *App) Client() *Client {
	server := firstNonEmpty(a.Server, os.Getenv("IRS_SERVER"))
	token := firstNonEmpty(a.Token, os.Getenv("IRS_TOKEN"))
	if server == "" || token == "" {
		cfg, err := a.LoadConfig()
		if err == nil {
			if server == "" {
				server = cfg.Server
			}
			if token == "" {
				token = cfg.Token
			}
		}
	}
	c := NewClient(server, token)
	c.Username = firstNonEmpty(os.Getenv("IRS_USER"), os.Getenv("IRS_USERNAME"))
	c.Password = firstNonEmpty(os.Getenv("IRS_PASSWORD"), os.Getenv("IRS_PASS"))
	cfgPath := a.ConfigPath
	c.OnTokenRefresh = func(token, expiresAt string) {
		// 自动重登拿到新 token 时静默持久化,后续命令可继续复用。
		if cfg, err := a.LoadConfig(); err == nil {
			cfg.Token = token
			cfg.TokenExpiresAt = expiresAt
			_ = cfg.Save(cfgPath)
		}
	}
	return c
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// confirmServer 确保已配置服务端地址,返回客户端;未配置时给出可操作的错误。
func (a *App) confirmClient() (*Client, error) {
	c := a.Client()
	if c.Server == "" {
		return nil, fmt.Errorf("未配置服务端地址:请用 --server http://<host>:<port>、设置 IRS_SERVER 环境变量,或先执行 irs login")
	}
	return c, nil
}

// ctx 为命令构造带默认超时的 context。
func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultTimeout)
}

// defaultTimeout 单个 API 调用的默认超时(SSE 流与等待循环各自另行控制)。
const defaultTimeout = 120 * time.Second
