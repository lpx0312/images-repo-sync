package cli

import (
	"fmt"
	"os"
	"strings"
)

// command 是一个子命令的定义。
type command struct {
	name  string // 完整命令名,如 "registries list";顶层命令如 "login"
	usage string // 位置参数说明,如 "<id> [--yes]"
	desc  string // 一行说明
	run   func(a *App, args []string) error
}

// allCommands 注册全部子命令(分组的命令按 "组 子命令" 两段匹配)。
var allCommands = func() []command {
	var cs []command
	cs = append(cs, authCommands...)
	cs = append(cs, registryCommands...)
	cs = append(cs, catalogCommands...)
	cs = append(cs, taskCommands...)
	cs = append(cs, chartCommands...)
	cs = append(cs, settingsCommands...)
	return cs
}()

// Run 是 CLI 入口:解析全局参数、分发子命令,返回进程退出码。
func Run(args []string) int {
	app, rest, err := parseGlobals(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 2
	}

	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printHelp(app)
		return 0
	}

	// 先按两段(组+子命令)匹配,再按一段匹配。
	var cmd *command
	for i := range allCommands {
		if allCommands[i].name == strings.Join(rest[:min(2, len(rest))], " ") {
			cmd = &allCommands[i]
			break
		}
	}
	if cmd == nil {
		for i := range allCommands {
			if allCommands[i].name == rest[0] {
				cmd = &allCommands[i]
				break
			}
		}
	}
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "错误: 未知命令 %q(输入 irs help 查看用法)\n", rest[0])
		return 2
	}

	// 子命令自己的参数从其名称段之后开始。
	cmdArgs := rest[strings.Count(cmd.name, " ")+1:]
	if err := cmd.run(app, cmdArgs); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	return 0
}

// parseGlobals 从参数头部剥离全局选项(--server/--token/--json/--config),
// 遇到第一个非选项参数(即命令名)即停——命令自己的参数(如 login --server)
// 不受影响。支持 --key=value 与 --key value 两种形式。
func parseGlobals(args []string) (*App, []string, error) {
	app := &App{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, args[i:]...)
			break
		}
		takeValue := func(flag string) (string, error) {
			if strings.HasPrefix(a, flag+"=") {
				return strings.TrimPrefix(a, flag+"="), nil
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", fmt.Errorf("%s 需要一个值", flag)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--json" || a == "-j":
			app.JSONOut = true
		case strings.HasPrefix(a, "--server"):
			v, err := takeValue("--server")
			if err != nil {
				return nil, nil, err
			}
			app.Server = v
		case strings.HasPrefix(a, "--token"):
			v, err := takeValue("--token")
			if err != nil {
				return nil, nil, err
			}
			app.Token = v
		case strings.HasPrefix(a, "--config"):
			v, err := takeValue("--config")
			if err != nil {
				return nil, nil, err
			}
			app.ConfigPath = v
		case a == "--version" || a == "-v":
			rest = append(rest, "version")
		default:
			return nil, nil, fmt.Errorf("无法识别的全局选项 %s(全局选项需放在命令之前)", a)
		}
	}
	return app, rest, nil
}

// printHelp 输出整体帮助。
func printHelp(a *App) {
	a.Printf("irs — images-repo-sync 平台命令行工具 v%s\n\n", Version)
	a.Printf("用法: irs [全局选项] <命令> [参数]\n\n")
	a.Printf("全局选项:\n")
	a.Printf("  --server URL    平台地址(默认取配置文件或 IRS_SERVER 环境变量)\n")
	a.Printf("  --token TOKEN   Bearer token(默认取配置文件或 IRS_TOKEN 环境变量)\n")
	a.Printf("  --json          以 JSON 输出结果(适合脚本与 AI 解析)\n")
	a.Printf("  --config PATH   配置文件路径(默认 ~/.irs-cli.json)\n\n")
	a.Printf("命令:\n")
	PrintTable(a.Out(), []string{"命令", "参数", "说明"}, commandRows())
	a.Printf("\n首次使用: irs login --server http://<host>:<port> --username admin --password <密码>\n")
	a.Printf("完整文档: docs/cli.md\n")
}

func commandRows() [][]string {
	rows := make([][]string, 0, len(allCommands))
	for _, c := range allCommands {
		rows = append(rows, []string{"irs " + c.name, c.usage, c.desc})
	}
	return rows
}
