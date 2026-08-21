package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// chartCommands Chart 仓库配置、chart 包上传与记录查询命令。
var chartCommands = []command{
	{name: "chart-repos list", usage: "", desc: "列出全部 Chart 仓库配置", run: cmdChartReposList},
	{name: "chart-repos create", usage: "--name N --type oci|chartmuseum --host H [--project P] [选项]", desc: "新增 Chart 仓库配置", run: cmdChartReposCreate},
	{name: "chart-repos update", usage: "<id> [选项]", desc: "修改 Chart 仓库配置(未给的项保留原值)", run: cmdChartReposUpdate},
	{name: "chart-repos delete", usage: "<id> --yes", desc: "删除 Chart 仓库配置", run: cmdChartReposDelete},
	{name: "chart-repos test", usage: "<id>", desc: "测试 Chart 仓库连通性(OCI ping / ChartMuseum health)", run: cmdChartReposTest},
	{name: "charts upload", usage: "<repo-id> <文件或目录>... [--no-wait]", desc: "上传本地 chart 包(目录取一层 *.tgz)并等待结果", run: cmdChartsUpload},
	{name: "charts upload-path", usage: "<repo-id> <服务端路径>... [--no-wait]", desc: "上传平台服务器上的 chart 文件/目录", run: cmdChartsUploadPath},
	{name: "charts uploads", usage: "[--status S] [--page N] [--size N]", desc: "查询 chart 上传记录", run: cmdChartsUploads},
	{name: "charts retry", usage: "<id> [--wait]", desc: "重试失败的上传记录", run: cmdChartsRetry},
}

// chartRepoVO 与服务端 Chart 仓库视图对应。
type chartRepoVO struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Project     string `json:"project"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
	Insecure    bool   `json:"insecure"`
}

func cmdChartReposList(a *App, args []string) error {
	fs := a.newFlagSet("chart-repos list", "[--json]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	var list []chartRepoVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/chart-repos", nil, &list); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), list)
	}
	rows := make([][]string, 0, len(list))
	for _, r := range list {
		rows = append(rows, []string{
			fmt.Sprint(r.ID), r.Name, r.Type, r.Host, emptyDash(r.Project),
			r.Username, boolMark(r.HasPassword), boolMark(r.Insecure),
		})
	}
	a.Printf("共 %d 个 Chart 仓库配置:\n", len(list))
	PrintTable(a.Out(), []string{"ID", "名称", "类型", "地址", "项目", "用户", "有密码", "跳过TLS"}, rows)
	return nil
}

// chartRepoForm 承载 create/update 的表单字段。
type chartRepoForm struct {
	name, typ, host, project, username, password *string
	insecure                                     *bool
}

// newChartRepoForm 注册 Chart 仓库表单参数;def 非 nil 时(update)预填当前值。
func newChartRepoForm(fs *flag.FlagSet, def *chartRepoVO) *chartRepoForm {
	d := func(get func(*chartRepoVO) string) string {
		if def == nil {
			return ""
		}
		return get(def)
	}
	f := &chartRepoForm{
		name:     fs.String("name", d(func(v *chartRepoVO) string { return v.Name }), "仓库名称(唯一)"),
		typ:      fs.String("type", d(func(v *chartRepoVO) string { return v.Type }), "类型: oci / chartmuseum"),
		host:     fs.String("host", d(func(v *chartRepoVO) string { return v.Host }), "仓库地址,OCI 如 harbor.example.com(可带 http:// 前缀走明文)"),
		project:  fs.String("project", d(func(v *chartRepoVO) string { return v.Project }), "chart 项目/命名空间,如 datacenter-test-chart(OCI 必填)"),
		username: fs.String("username", d(func(v *chartRepoVO) string { return v.Username }), "用户名"),
		password: fs.String("password", "", "密码(留空=创建时无密码/更新时保留原值)"),
	}
	if def != nil {
		f.insecure = fs.Bool("insecure", def.Insecure, "跳过 TLS 证书校验(自签证书)")
	} else {
		f.insecure = fs.Bool("insecure", false, "跳过 TLS 证书校验(自签证书)")
	}
	return f
}

func (f *chartRepoForm) body() map[string]any {
	body := map[string]any{
		"name": *f.name, "type": *f.typ, "host": *f.host,
		"project": *f.project, "username": *f.username, "insecure": *f.insecure,
	}
	if *f.password != "" {
		body["password"] = *f.password
	}
	return body
}

func cmdChartReposCreate(a *App, args []string) error {
	fs := a.newFlagSet("chart-repos create", "--name N --type oci|chartmuseum --host H [--project P] [--username U] [--password P] [--insecure]")
	form := newChartRepoForm(fs, nil)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *form.name == "" || *form.host == "" {
		return fmt.Errorf("--name 与 --host 必填")
	}
	switch *form.typ {
	case "oci", "chartmuseum":
	case "":
		return fmt.Errorf("--type 必填: oci / chartmuseum")
	default:
		return fmt.Errorf("--type 无效: %s(可选 oci / chartmuseum)", *form.typ)
	}
	if *form.typ == "oci" && *form.project == "" {
		return fmt.Errorf("OCI 类型必须提供 --project(如 datacenter-test-chart)")
	}
	var created chartRepoVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", "/api/chart-repos", form.body(), &created); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), created)
	}
	a.Printf("已创建 Chart 仓库: ID %d %s (%s)\n", created.ID, created.Name, chartRepoDesc(&created))
	return nil
}

func cmdChartReposUpdate(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs chart-repos update <id> [选项]"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	current, err := fetchChartRepo(c, id)
	if err != nil {
		return err
	}
	fs := a.newFlagSet("chart-repos update", "<id> [--name N] [--type T] [--host H] [--project P] [--username U] [--password P] [--insecure]")
	form := newChartRepoForm(fs, current)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *form.typ == "oci" && *form.project == "" {
		return fmt.Errorf("OCI 类型必须提供 --project")
	}
	var updated chartRepoVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "PUT", fmt.Sprintf("/api/chart-repos/%d", id), form.body(), &updated); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), updated)
	}
	a.Printf("已更新 Chart 仓库: ID %d %s (%s)\n", updated.ID, updated.Name, chartRepoDesc(&updated))
	return nil
}

func cmdChartReposDelete(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs chart-repos delete <id> --yes"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("chart-repos delete", "<id> --yes")
	yes := yesFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := ensureYes(*yes, "删除 Chart 仓库配置"); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "DELETE", fmt.Sprintf("/api/chart-repos/%d", id), nil, nil); err != nil {
		return err
	}
	a.Printf("已删除 Chart 仓库配置 ID %d\n", id)
	return nil
}

func cmdChartReposTest(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs chart-repos test <id>"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	var res probeResult
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", fmt.Sprintf("/api/chart-repos/%d/test", id), nil, &res); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), res)
	}
	if res.OK {
		a.Printf("连接正常: %s\n", joinNonEmpty(res.Message, res.Endpoint))
	} else {
		a.Printf("连接失败: %s\n", res.Message)
		if res.Detail != "" {
			a.Printf("  详情: %s\n", res.Detail)
		}
	}
	return nil
}

// fetchChartRepo 取单个 Chart 仓库配置(list 后按 ID 过滤;服务端无单查接口)。
func fetchChartRepo(c *Client, id uint) (*chartRepoVO, error) {
	var list []chartRepoVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/chart-repos", nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("Chart 仓库 ID %d 不存在", id)
}

// chartRepoDesc 仓库的可读描述。
func chartRepoDesc(r *chartRepoVO) string {
	if r.Project != "" {
		return fmt.Sprintf("%s, 项目 %s", r.Host, r.Project)
	}
	return r.Host
}

// chartUploadVO 与服务端上传记录视图对应。
type chartUploadVO struct {
	ID           uint   `json:"id"`
	ChartRepoID  uint   `json:"chart_repo_id"`
	RepoName     string `json:"repo_name"`
	RepoType     string `json:"repo_type"`
	TargetRef    string `json:"target_ref"`
	FileName     string `json:"file_name"`
	ChartName    string `json:"chart_name"`
	ChartVersion string `json:"chart_version"`
	Size         int64  `json:"size"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	Digest       string `json:"digest"`
	CreatedAt    string `json:"created_at"`
	FinishedAt   string `json:"finished_at"`
}

// expandChartArgs 把位置参数展开为 tgz 文件列表:
// 文件原样保留;目录扫描第一层 *.tgz(与服务端 upload-paths 行为一致,不递归)。
func expandChartArgs(args []string) ([]string, error) {
	var files []string
	for _, p := range args {
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("路径不存在或不可访问: %s", p)
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("读取目录失败: %s (%v)", p, err)
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".tgz") {
				continue
			}
			names = append(names, filepath.Join(p, e.Name()))
		}
		sort.Strings(names)
		files = append(files, names...)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("没有找到任何待上传文件")
	}
	return files, nil
}

// uploadCreated 服务端创建上传记录接口的返回结构。
type uploadCreated struct {
	Created []struct {
		ID uint `json:"id"`
	} `json:"created"`
	Invalid []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

// printInvalid 输出被跳过的非法文件。
func (a *App) printInvalid(inv []struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}) {
	for _, v := range inv {
		a.Printf("[跳过] %s: %s\n", v.Name, v.Reason)
	}
}

// cmdChartsUpload 上传本地文件:multipart 到 /api/charts/upload-files,随后轮询等待结果。
func cmdChartsUpload(a *App, args []string) error {
	fs := a.newFlagSet("charts upload", "<repo-id> <文件或目录>... [--no-wait]")
	noWait := fs.Bool("no-wait", false, "只提交不等待结果")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if err := requireArgs(rest, 2, "irs charts upload <repo-id> <文件或目录>..."); err != nil {
		return err
	}
	repoID, err := parseIDArg("<repo-id>", rest[0])
	if err != nil {
		return err
	}
	files, err := expandChartArgs(rest[1:])
	if err != nil {
		return err
	}
	if len(files) > 100 {
		return fmt.Errorf("单次最多上传 100 个文件,当前 %d 个,请分批提交", len(files))
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	a.Printf("上传 %d 个 chart 包到仓库 ID %d...\n", len(files), repoID)
	uctx, ucancel := ctx2(30 * time.Minute)
	defer ucancel()
	env, err := c.UploadFiles(uctx, "/api/charts/upload-files", "files", files, map[string]string{
		"repo_id": fmt.Sprint(repoID),
	})
	if err != nil {
		return err
	}
	var res uploadCreated
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &res)
	}
	if a.JSONOut {
		_ = PrintJSON(a.Out(), res)
	} else {
		a.printInvalid(res.Invalid)
		a.Printf("已创建 %d 条上传记录\n", len(res.Created))
	}
	if *noWait || len(res.Created) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(res.Created))
	for _, cr := range res.Created {
		ids = append(ids, cr.ID)
	}
	return a.waitUploads(c, ids, !*noWait)
}

// cmdChartsUploadPath 上传平台服务器上的路径:JSON 到 /api/charts/upload-paths。
func cmdChartsUploadPath(a *App, args []string) error {
	fs := a.newFlagSet("charts upload-path", "<repo-id> <服务端路径>... [--no-wait]")
	noWait := fs.Bool("no-wait", false, "只提交不等待结果")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if err := requireArgs(rest, 2, "irs charts upload-path <repo-id> <服务端路径>..."); err != nil {
		return err
	}
	repoID, err := parseIDArg("<repo-id>", rest[0])
	if err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	body := map[string]any{"repo_id": repoID, "paths": rest[1:]}
	var res uploadCreated
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", "/api/charts/upload-paths", body, &res); err != nil {
		return err
	}
	if a.JSONOut {
		_ = PrintJSON(a.Out(), res)
	} else {
		a.printInvalid(res.Invalid)
		a.Printf("已创建 %d 条上传记录\n", len(res.Created))
	}
	if *noWait || len(res.Created) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(res.Created))
	for _, cr := range res.Created {
		ids = append(ids, cr.ID)
	}
	return a.waitUploads(c, ids, !*noWait)
}

// waitUploads 轮询上传记录直到全部到达终态,逐条输出结果;
// 任一失败则返回错误(进程退出码 1)。
func (a *App) waitUploads(c *Client, ids []uint, wait bool) error {
	if !wait {
		return nil
	}
	pending := make(map[uint]bool, len(ids))
	for _, id := range ids {
		pending[id] = true
	}
	var results []chartUploadVO
	deadline := time.Now().Add(30 * time.Minute)
	for len(pending) > 0 {
		if time.Now().After(deadline) {
			return fmt.Errorf("等待上传结果超时(剩余 %d 条,可用 irs charts uploads 查看)", len(pending))
		}
		time.Sleep(1 * time.Second)
		// 记录按 id DESC,本批新记录通常在第 1 页;并发上传可能把它们挤到后面,
		// 因此按页扫描直到找齐或翻完。
		for page := 1; len(pending) > 0; page++ {
			var list []chartUploadVO
			cCtx, cancel := ctx()
			env, err := c.DoJSON(cCtx, "GET",
				fmt.Sprintf("/api/charts/uploads?page=%d&size=100", page), nil, &list)
			cancel()
			if err != nil {
				return err
			}
			for i := range list {
				u := list[i]
				if !pending[u.ID] {
					continue
				}
				switch u.Status {
				case "success", "failed":
					delete(pending, u.ID)
					results = append(results, u)
					if !a.JSONOut {
						printUploadResult(a, &u)
					} else {
						_ = PrintJSON(a.Out(), u)
					}
				}
			}
			// 翻完所有页仍没找齐(部分记录可能还没入库或更靠后),下一轮再扫。
			if int64(page)*100 >= env.Total {
				break
			}
		}
	}
	var failed int
	for _, r := range results {
		if r.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 个 chart 上传失败,详情: irs charts uploads", failed)
	}
	if !a.JSONOut {
		a.Printf("全部 %d 个 chart 上传成功\n", len(results))
	}
	return nil
}

// printUploadResult 输出单条上传结果。
func printUploadResult(a *App, u *chartUploadVO) {
	if u.Status == "success" {
		a.Printf("[成功] %s:%s -> %s  %s\n", u.ChartName, u.ChartVersion, u.TargetRef, u.Digest)
	} else {
		a.Printf("[失败] %s -> %s  %s\n", emptyDash(u.FileName), emptyDash(u.TargetRef), emptyDash(u.Error))
	}
}

// cmdChartsUploads 上传记录列表。
func cmdChartsUploads(a *App, args []string) error {
	fs := a.newFlagSet("charts uploads", "[--status S] [--page N] [--size N]")
	status := fs.String("status", "", "按状态过滤: pending/running/success/failed")
	page := fs.Int("page", 1, "页码")
	size := fs.Int("size", 20, "每页条数")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var list []chartUploadVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	path := fmt.Sprintf("/api/charts/uploads?page=%d&size=%d", *page, *size)
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
	for _, u := range list {
		rows = append(rows, []string{
			fmt.Sprint(u.ID), u.RepoName, u.ChartName, u.ChartVersion,
			FmtSize(u.Size), StatusMark(u.Status),
			Truncate(emptyDash(u.Error), 50), FmtTime(u.CreatedAt),
		})
	}
	a.Printf("共 %d 条上传记录(第 %d 页):\n", env.Total, env.Page)
	PrintTable(a.Out(), []string{"ID", "仓库", "chart", "版本", "大小", "状态", "错误", "时间"}, rows)
	return nil
}

// cmdChartsRetry 重试失败记录(仅失败状态可重试;源文件需仍存在)。
func cmdChartsRetry(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs charts retry <id> [--wait]"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("charts retry", "<id> [--wait]")
	wait := fs.Bool("wait", true, "等待重试结果(默认等待,--wait=false 立即返回)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var res map[string]any
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", fmt.Sprintf("/api/charts/uploads/%d/retry", id), nil, &res); err != nil {
		return err
	}
	a.Printf("已重新入队上传记录 %d\n", id)
	return a.waitUploads(c, []uint{id}, *wait)
}

// ctx2 构造自定义超时的 context(上传等待等长操作用)。
func ctx2(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
