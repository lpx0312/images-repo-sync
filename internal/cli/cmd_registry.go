package cli

import (
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// registryCommands 镜像仓库配置 CRUD 与连接测试。
var registryCommands = []command{
	{name: "registries list", usage: "", desc: "列出全部镜像仓库配置", run: cmdRegistriesList},
	{name: "registries create", usage: "--name N --host H [选项]", desc: "新增镜像仓库配置", run: cmdRegistriesCreate},
	{name: "registries update", usage: "<id> [选项]", desc: "修改镜像仓库配置(未给的项保留原值)", run: cmdRegistriesUpdate},
	{name: "registries delete", usage: "<id> --yes", desc: "删除镜像仓库配置", run: cmdRegistriesDelete},
	{name: "registries test", usage: "<id>", desc: "测试镜像仓库连通性与凭证", run: cmdRegistriesTest},
}

// catalogCommands 浏览仓库目录(Harbor 走 v2 API,其他走 _catalog)。
var catalogCommands = []command{
	{name: "catalog projects", usage: "<registry-id>", desc: "列出仓库的项目/namespace(Harbor)", run: cmdCatalogProjects},
	{name: "catalog repos", usage: "<registry-id> [--project P] [--q 关键字]", desc: "列出仓库中的镜像 repo", run: cmdCatalogRepos},
	{name: "catalog tags", usage: "<registry-id> --repo R", desc: "列出某镜像的 tag", run: cmdCatalogTags},
}

// registryVO 与服务端返回的仓库视图对应(不含密码)。
type registryVO struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	Type           string `json:"type"`
	Username       string `json:"username"`
	HasPassword    bool   `json:"has_password"`
	Insecure       bool   `json:"insecure"`
	DefaultProject string `json:"default_project"`
	CreatedAt      string `json:"created_at"`
}

func cmdRegistriesList(a *App, args []string) error {
	fs := a.newFlagSet("registries list", "[--json]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	var list []registryVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/registries", nil, &list); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), list)
	}
	rows := make([][]string, 0, len(list))
	for _, r := range list {
		rows = append(rows, []string{
			fmt.Sprint(r.ID), r.Name, r.Type, r.Host, r.Username,
			boolMark(r.HasPassword), boolMark(r.Insecure), emptyDash(r.DefaultProject),
		})
	}
	a.Printf("共 %d 个镜像仓库配置:\n", len(list))
	PrintTable(a.Out(), []string{"ID", "名称", "类型", "地址", "用户", "有密码", "跳过TLS", "默认项目"}, rows)
	return nil
}

// registryForm 承载 create/update 的表单字段。
type registryForm struct {
	name, host, typ, username, password, project *string
	insecure                                     *bool
}

// newRegistryForm 在 FlagSet 上注册镜像仓库表单参数;def 提供各字段默认值
// (update 场景传入当前配置,实现「未指定的项保留原值」)。
func newRegistryForm(fs *flag.FlagSet, def *registryVO) *registryForm {
	f := &registryForm{
		name:     fs.String("name", orDefault(def, func() string { return def.Name }), "仓库名称(唯一)"),
		host:     fs.String("host", orDefault(def, func() string { return def.Host }), "仓库地址,如 harbor.example.com"),
		typ:      fs.String("type", orDefault(def, func() string { return def.Type }), "类型: generic / harbor / dockerhub / acr / swr"),
		username: fs.String("username", orDefault(def, func() string { return def.Username }), "用户名(匿名仓库可空)"),
		password: fs.String("password", "", "密码(留空=创建时无密码/更新时保留原值)"),
		project:  fs.String("default-project", orDefault(def, func() string { return def.DefaultProject }), "默认 project"),
	}
	if def != nil {
		f.insecure = fs.Bool("insecure", def.Insecure, "跳过 TLS 证书校验(自签证书)")
	} else {
		f.insecure = fs.Bool("insecure", false, "跳过 TLS 证书校验(自签证书)")
	}
	return f
}

// orDefault 返回预填默认值;def 为 nil(update 未预取到)时空串。
func orDefault(def *registryVO, get func() string) string {
	if def == nil {
		return ""
	}
	return get()
}

// body 构造请求体(密码仅在显式填写时携带)。
func (f *registryForm) body() map[string]any {
	body := map[string]any{
		"name": *f.name, "host": *f.host, "type": *f.typ,
		"username": *f.username, "insecure": *f.insecure, "default_project": *f.project,
	}
	if *f.password != "" {
		body["password"] = *f.password
	}
	return body
}

func cmdRegistriesCreate(a *App, args []string) error {
	fs := a.newFlagSet("registries create", "--name N --host H [--type T] [--username U] [--password P] [--insecure] [--default-project P]")
	form := newRegistryForm(fs, nil)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *form.name == "" || *form.host == "" {
		return fmt.Errorf("--name 与 --host 必填")
	}
	var created registryVO
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "POST", "/api/registries", form.body(), &created); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), created)
	}
	a.Printf("已创建镜像仓库: ID %d %s (%s)\n", created.ID, created.Name, created.Host)
	return nil
}

// fetchRegistry 先取当前配置,供 update 预填默认值。
func fetchRegistry(c *Client, id uint) (*registryVO, error) {
	var list []registryVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/registries", nil, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("镜像仓库 ID %d 不存在", id)
}

func cmdRegistriesUpdate(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs registries update <id> [选项]"); err != nil {
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
	current, err := fetchRegistry(c, id)
	if err != nil {
		return err
	}
	fs := a.newFlagSet("registries update", "<id> [--name N] [--host H] [--type T] [--username U] [--password P] [--insecure] [--default-project P]")
	form := newRegistryForm(fs, current)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var updated registryVO
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "PUT", fmt.Sprintf("/api/registries/%d", id), form.body(), &updated); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), updated)
	}
	a.Printf("已更新镜像仓库: ID %d %s (%s)\n", updated.ID, updated.Name, updated.Host)
	return nil
}

func cmdRegistriesDelete(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs registries delete <id> --yes"); err != nil {
		return err
	}
	id, err := parseIDArg("<id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("registries delete", "<id> --yes")
	yes := yesFlag(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := ensureYes(*yes, "删除镜像仓库配置"); err != nil {
		return err
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "DELETE", fmt.Sprintf("/api/registries/%d", id), nil, nil); err != nil {
		return err
	}
	a.Printf("已删除镜像仓库配置 ID %d\n", id)
	return nil
}

func cmdRegistriesTest(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs registries test <id>"); err != nil {
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
	if _, err := c.DoJSON(cCtx, "POST", fmt.Sprintf("/api/registries/%d/test", id), nil, &res); err != nil {
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

// probeResult /api/registries/:id/test 与 /api/chart-repos/:id/test 的共同返回结构。
type probeResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
}

func cmdCatalogProjects(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs catalog projects <registry-id>"); err != nil {
		return err
	}
	id, err := parseIDArg("<registry-id>", args[0])
	if err != nil {
		return err
	}
	var res struct {
		OK             bool     `json:"ok"`
		Supported      bool     `json:"supported"`
		Message        string   `json:"message"`
		Projects       []string `json:"projects"`
		DefaultProject string   `json:"default_project"`
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", fmt.Sprintf("/api/catalog/%d/projects", id), nil, &res); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), res)
	}
	if !res.Supported {
		return fmt.Errorf("该仓库类型不支持列出项目:%s(默认项目: %s)", res.Message, emptyDash(res.DefaultProject))
	}
	if !res.OK {
		return fmt.Errorf("获取项目列表失败: %s", res.Message)
	}
	for _, p := range res.Projects {
		a.Printf("%s\n", p)
	}
	a.Printf("共 %d 个项目\n", len(res.Projects))
	return nil
}

func cmdCatalogRepos(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs catalog repos <registry-id> [--project P] [--q 关键字]"); err != nil {
		return err
	}
	id, err := parseIDArg("<registry-id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("catalog repos", "<registry-id> [--project P] [--q 关键字]")
	project := fs.String("project", "", "按项目过滤(Harbor)")
	q := fs.String("q", "", "名称过滤关键字")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var res struct {
		OK      bool     `json:"ok"`
		Message string   `json:"message"`
		Repos   []string `json:"repos"`
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/catalog/%d/repos", id)
	params := []string{}
	if *project != "" {
		params = append(params, "project="+url.QueryEscape(*project))
	}
	if *q != "" {
		params = append(params, "q="+url.QueryEscape(*q))
	}
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", path, nil, &res); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), res)
	}
	if !res.OK {
		return fmt.Errorf("获取 repo 列表失败: %s", res.Message)
	}
	for _, r := range res.Repos {
		a.Printf("%s\n", r)
	}
	a.Printf("共 %d 个 repo\n", len(res.Repos))
	return nil
}

func cmdCatalogTags(a *App, args []string) error {
	if err := requireArgs(args, 1, "irs catalog tags <registry-id> --repo R"); err != nil {
		return err
	}
	id, err := parseIDArg("<registry-id>", args[0])
	if err != nil {
		return err
	}
	fs := a.newFlagSet("catalog tags", "<registry-id> --repo R")
	repo := fs.String("repo", "", "镜像 repo,如 library/nginx")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *repo == "" {
		return fmt.Errorf("--repo 必填")
	}
	var res struct {
		OK      bool     `json:"ok"`
		Message string   `json:"message"`
		Tags    []string `json:"tags"`
		Repo    string   `json:"repo"`
	}
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", fmt.Sprintf("/api/catalog/%d/tags?repo=%s", id, url.QueryEscape(*repo)), nil, &res); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), res)
	}
	if !res.OK {
		return fmt.Errorf("获取 tag 列表失败: %s", res.Message)
	}
	for _, t := range res.Tags {
		a.Printf("%s:%s\n", res.Repo, t)
	}
	a.Printf("共 %d 个 tag\n", len(res.Tags))
	return nil
}

// boolMark 布尔值在表格中的展示。
func boolMark(b bool) string {
	if b {
		return "是"
	}
	return "-"
}

// emptyDash 空字符串显示为 -。
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// joinNonEmpty 以 " / " 连接非空片段。
func joinNonEmpty(parts ...string) string {
	var ps []string
	for _, p := range parts {
		if p != "" {
			ps = append(ps, p)
		}
	}
	return strings.Join(ps, " / ")
}
