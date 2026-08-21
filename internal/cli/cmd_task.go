package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// taskCommands 同步任务相关命令。
var taskCommands = []command{
	{name: "tasks list", usage: "[--status S] [--page N] [--size N]", desc: "列出同步任务", run: cmdTasksList},
	{name: "tasks get", usage: "<id>", desc: "查看任务详情(含逐镜像明细)", run: cmdTasksGet},
	{name: "tasks create", usage: "--source ID --target ID --mode M --refs R1[,R2...] [--wait]", desc: "创建并执行同步任务", run: cmdTasksCreate},
	{name: "tasks cancel", usage: "<id>", desc: "取消运行中/排队中的任务", run: cmdTasksCancel},
	{name: "tasks logs", usage: "<id> [--follow]", desc: "查看任务日志;--follow 实时跟踪直到结束", run: cmdTasksLogs},
}

// taskVO 与服务端任务视图对应。
type taskVO struct {
	ID               uint         `json:"id"`
	SourceRegistryID uint         `json:"source_registry_id"`
	TargetRegistryID uint         `json:"target_registry_id"`
	Mode             string       `json:"mode"`
	TargetProject    string       `json:"target_project"`
	Arch             string       `json:"arch"`
	Total            int          `json:"total"`
	Succeeded        int          `json:"succeeded"`
	Failed           int          `json:"failed"`
	Skipped          int          `json:"skipped"`
	Status           string       `json:"status"`
	Error            string       `json:"error"`
	CreatedAt        string       `json:"created_at"`
	StartedAt        any          `json:"started_at"`
	FinishedAt       any          `json:"finished_at"`
	Items            []taskItemVO `json:"items"`
}

type taskItemVO struct {
	ID        uint   `json:"id"`
	SourceRef string `json:"source_ref"`
	TargetRef string `json:"target_ref"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Digest    string `json:"digest"`
}

// taskFinished 任务是否处于终态。
func taskFinished(status string) bool {
	switch status {
	case "success", "failed", "canceled":
		return true
	}
	return false
}

func cmdTasksList(a *App, args []string) error {
	fs := a.newFlagSet("tasks list", "[--status S] [--page N] [--size N]")
	status := fs.String("status", "", "按状态过滤: pending/running/success/failed/canceled")
	page := fs.Int("page", 1, "页码")
	size := fs.Int("size", 20, "每页条数")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var list []taskVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	path := fmt.Sprintf("/api/tasks?page=%d&size=%d", *page, *size)
	if *status != "" {
		path += "&status=" + queryEscape(*status)
	}
	env, err := c.DoJSON(cCtx, "GET", path, nil, &list)
	if err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), map[string]any{"data": list, "total": env.Total, "page": env.Page, "size": env.Size})
	}
	rows := make([][]string, 0, len(list))
	for _, t := range list {
		rows = append(rows, []string{
			fmt.Sprint(t.ID), StatusMark(t.Status), t.Mode, t.Arch,
			fmt.Sprintf("%d/%d", t.Succeeded+t.Failed+t.Skipped, t.Total),
			fmt.Sprint(t.Succeeded), fmt.Sprint(t.Failed), fmt.Sprint(t.Skipped),
			FmtTime(t.CreatedAt),
		})
	}
	a.Printf("共 %d 个任务(第 %d/%d 页):\n", env.Total, env.Page, (env.Total+int64(env.Size)-1)/int64(env.Size))
	PrintTable(a.Out(), []string{"ID", "状态", "模式", "架构", "进度", "成功", "失败", "跳过", "创建时间"}, rows)
	return nil
}

// printTaskDetail 输出任务摘要与明细表。
func printTaskDetail(a *App, t *taskVO) {
	a.Printf("任务 #%d  状态: %s  模式: %s  架构: %s  目标项目: %s\n",
		t.ID, StatusMark(t.Status), t.Mode, t.Arch, emptyDash(t.TargetProject))
	a.Printf("镜像: %d  成功: %d  失败: %d  跳过: %d  创建: %s\n",
		t.Total, t.Succeeded, t.Failed, t.Skipped, FmtTime(t.CreatedAt))
	if t.Error != "" {
		a.Printf("任务级错误: %s\n", t.Error)
	}
	if len(t.Items) > 0 {
		rows := make([][]string, 0, len(t.Items))
		for _, it := range t.Items {
			rows = append(rows, []string{
				fmt.Sprint(it.ID), it.SourceRef, it.TargetRef, it.Status,
				Truncate(emptyDash(it.Error), 60),
			})
		}
		PrintTable(a.Out(), []string{"明细ID", "源镜像", "目标镜像", "状态", "错误"}, rows)
	}
}

func cmdTasksGet(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs tasks get <id>"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	var t taskVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", fmt.Sprintf("/api/tasks/%d", id), nil, &t); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), t)
	}
	printTaskDetail(a, &t)
	return nil
}

// collectRefs 汇总 --refs(逗号分隔可多次)、--refs-file 与位置参数。
func collectRefs(fs *flag.FlagSet, positional []string) ([]string, error) {
	var refs []string
	addAll := func(items []string) {
		for _, r := range items {
			r = strings.TrimSpace(r)
			if r != "" {
				refs = append(refs, r)
			}
		}
	}
	if got := fs.Lookup("refs"); got != nil {
		addAll(strings.Split(got.Value.String(), ","))
	}
	if got := fs.Lookup("refs-file"); got != nil && got.Value.String() != "" {
		fileRefs, err := readRefsFile(got.Value.String())
		if err != nil {
			return nil, err
		}
		addAll(fileRefs)
	}
	addAll(positional)
	if len(refs) == 0 {
		return nil, fmt.Errorf("镜像列表为空:请用 --refs、--refs-file 或直接跟位置参数提供")
	}
	return refs, nil
}

func cmdTasksCreate(a *App, args []string) error {
	fs := a.newFlagSet("tasks create", "--source ID --target ID --mode M [--project P] [--arch A] --refs R1,R2 / --refs-file FILE [--wait]")
	source := fs.Uint("source", 0, "源仓库 ID(irs registries list 查看)")
	target := fs.Uint("target", 0, "目标仓库 ID")
	mode := fs.String("mode", "", "同步模式: flat / preserve_path / replace_host")
	project := fs.String("project", "", "目标 project(默认取目标仓库配置)")
	arch := fs.String("arch", "", "架构: amd64 / arm64 / all(默认 amd64)")
	fs.String("refs", "", "镜像列表,逗号分隔")
	fs.String("refs-file", "", "镜像列表文件,每行一条,# 注释")
	wait := fs.Bool("wait", false, "创建后跟随日志直到任务结束(失败时退出码 1)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source == 0 || *target == 0 {
		return fmt.Errorf("--source 与 --target 必填(irs registries list 查看 ID)")
	}
	switch *mode {
	case "flat", "preserve_path", "replace_host":
	case "":
		return fmt.Errorf("--mode 必填: flat / preserve_path / replace_host")
	default:
		return fmt.Errorf("--mode 无效: %s(可选 flat / preserve_path / replace_host)", *mode)
	}
	switch *arch {
	case "", "amd64", "arm64", "all":
	default:
		return fmt.Errorf("--arch 无效: %s(可选 amd64 / arm64 / all)", *arch)
	}
	refs, err := collectRefs(fs, fs.Args())
	if err != nil {
		return err
	}

	body := map[string]any{
		"source_registry_id": *source,
		"target_registry_id": *target,
		"mode":               *mode,
		"target_project":     *project,
		"arch":               *arch,
		"refs":               refs,
	}
	var t taskVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", "/api/tasks", body, &t); err != nil {
		return err
	}
	if a.JSONOut {
		if !*wait {
			return PrintJSON(a.Out(), t)
		}
		_ = PrintJSON(a.Out(), t)
	} else {
		a.Printf("任务 #%d 已创建并开始执行(%d 个镜像):\n", t.ID, t.Total)
	}
	if !*wait {
		a.Printf("跟踪进度: irs tasks logs %d --follow\n", t.ID)
		return nil
	}
	return followTask(a, c, t.ID)
}

func cmdTasksCancel(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs tasks cancel <id>"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	var res map[string]any
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", fmt.Sprintf("/api/tasks/%d/cancel", id), nil, &res); err != nil {
		return err
	}
	a.Printf("已请求取消任务 #%d\n", id)
	return nil
}

// cmdTasksLogs 默认打印当前快照;--follow 订阅 SSE 实时跟踪。
func cmdTasksLogs(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs tasks logs <id> [--follow]"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("tasks logs", "<id> [--follow]")
	follow := fs.Bool("follow", false, "实时跟踪直到任务结束")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	if !*follow {
		var t taskVO
		cCtx, cancel := ctx()
		defer cancel()
		if _, err := c.DoJSON(cCtx, "GET", fmt.Sprintf("/api/tasks/%d", id), nil, &t); err != nil {
			return err
		}
		if a.JSONOut {
			return PrintJSON(a.Out(), t)
		}
		printTaskDetail(a, &t)
		return nil
	}
	return followTask(a, c, id)
}

// followTask 订阅任务 SSE 流直到结束,逐镜像输出结果。
// 任务结束时若有失败镜像返回非 nil 错误(进程退出码 1)。
// Ctrl+C 可中断跟踪(任务在服务端继续执行,不受影响)。
func followTask(a *App, c *Client, id uint) error {
	sctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	watchCtx, watchCancel := context.WithTimeout(sctx, 6*time.Hour)
	defer watchCancel()

	failed := 0
	sawFinished := false
	err := c.StreamSSE(watchCtx, fmt.Sprintf("/api/tasks/%d/stream", id), func(ev SSEEvent) bool {
		// SSE 载荷是服务端 Event 信封:{type, task_id, source_ref, target_ref, message, data:{...}}。
		var env taskEvent
		_ = json.Unmarshal(ev.Data, &env)
		switch ev.Event {
		case "snapshot":
			if a.JSONOut {
				a.Printf("%s\n", ev.Data)
				break
			}
			var t taskVO
			if json.Unmarshal(env.Data, &t) == nil {
				printTaskDetail(a, &t)
			}
		case "item_started":
			if !a.JSONOut {
				a.Printf("[同步] %s -> %s\n", env.SourceRef, env.TargetRef)
			}
		case "item_success":
			if a.JSONOut {
				a.Printf("%s\n", ev.Data)
				break
			}
			a.Printf("[成功] %s -> %s  %s\n", env.SourceRef, env.TargetRef, env.digest())
		case "item_failed":
			failed++
			if a.JSONOut {
				a.Printf("%s\n", ev.Data)
				break
			}
			msg := env.Message
			if msg == "" {
				msg = "未知错误"
			}
			a.Printf("[失败] %s -> %s  %s\n", env.SourceRef, env.TargetRef, msg)
		case "task_finished":
			sawFinished = true
			if a.JSONOut {
				a.Printf("%s\n", ev.Data)
				break
			}
			var e struct {
				Status    string `json:"status"`
				Succeeded int    `json:"succeeded"`
				Failed    int    `json:"failed"`
				Skipped   int    `json:"skipped"`
			}
			_ = json.Unmarshal(env.Data, &e)
			a.Printf("任务结束: 状态 %s,成功 %d,失败 %d,跳过 %d\n", e.Status, e.Succeeded, e.Failed, e.Skipped)
		}
		return true
	})
	if err != nil {
		return err
	}
	if !sawFinished {
		return fmt.Errorf("事件流已断开但未收到任务结束事件(任务可能仍在执行,可用 irs tasks get %d 查看)", id)
	}
	if failed > 0 {
		return fmt.Errorf("任务有 %d 个镜像同步失败,详情: irs tasks get %d", failed, id)
	}
	return nil
}

// taskEvent 是 SSE 载荷的 Event 信封;具体业务数据在 Data 里按事件类型二次解析。
type taskEvent struct {
	ItemID    uint            `json:"item_id"`
	SourceRef string          `json:"source_ref"`
	TargetRef string          `json:"target_ref"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
}

// digest 从 item_success 的 Data 中取 digest(二次解析)。
func (e *taskEvent) digest() string {
	var d struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return ""
	}
	return d.Digest
}

// queryEscape URL 查询参数转义。
func queryEscape(s string) string {
	return url.QueryEscape(s)
}
