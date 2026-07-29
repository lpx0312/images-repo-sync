package api

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"images-repo-sync/internal/crypto"
	"images-repo-sync/internal/middleware"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/skopeo"
)

// CatalogHandler 提供浏览仓库 repo/tag 的能力,供「新建同步」浏览目录使用。
type CatalogHandler struct {
	DB *gorm.DB
}

func NewCatalogHandler(db *gorm.DB) *CatalogHandler { return &CatalogHandler{DB: db} }

// loadReg 根据 id 取出仓库配置并解密密码。
func (h *CatalogHandler) loadReg(id uint) (*model.Registry, string, error) {
	var m model.Registry
	if err := h.DB.First(&m, id).Error; err != nil {
		return nil, "", err
	}
	// 密码为空(匿名仓库)直接返回空,避免 Decrypt("") 在加密模式下报错。
	if m.PasswordEnc == "" {
		return &m, "", nil
	}
	pass, err := crypto.Decrypt(m.PasswordEnc)
	if err != nil {
		return nil, "", err
	}
	return &m, pass, nil
}

// Repos GET /api/catalog/:id/repos?project=&q=
//
// 按仓库类型选择策略:
//   - harbor: 调 v2 API /api/v2.0/projects/.../repositories(可选 project 过滤)
//   - 其他:   调 registry v2 /v2/_catalog(常被禁用,失败时提示用户用粘贴列表)
func (h *CatalogHandler) Repos(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	m, pass, err := h.loadReg(uint(id))
	if err != nil {
		errResp(c, 404, "仓库不存在")
		return
	}
	project := strings.TrimSpace(c.Query("project"))
	q := strings.TrimSpace(c.Query("q"))

	var repos []string
	switch strings.ToLower(m.Type) {
	case "harbor":
		repos, err = harborRepos(c.Request, m.Host, m.Username, pass, m.Insecure, project)
	default:
		repos, err = registryCatalog(c.Request, m.Host, m.Username, pass, m.Insecure)
	}
	if err != nil {
		ok(c, gin.H{"ok": false, "message": err.Error(), "repos": []string{}})
		return
	}
	if q != "" {
		filtered := repos[:0]
		for _, r := range repos {
			if strings.Contains(strings.ToLower(r), strings.ToLower(q)) {
				filtered = append(filtered, r)
			}
		}
		repos = filtered
	}
	ok(c, gin.H{"ok": true, "repos": repos})
}

// Tags GET /api/catalog/:id/tags?repo=<repo>
//
// 用 skopeo list-tags 列出某 repo 的所有 tag。
// repo 形如 docker.io/library/nginx 或 harbor.corp/library/nginx(不含 docker://)。
func (h *CatalogHandler) Tags(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	m, pass, err := h.loadReg(uint(id))
	if err != nil {
		errResp(c, 404, "仓库不存在")
		return
	}
	repo := strings.TrimSpace(c.Query("repo"))
	if repo == "" {
		badReq(c, "repo 不能为空")
		return
	}
	// repo 可能只给了 host 后路径,也可能给了完整引用;统一取 path 部分作 list-tags 的输入。
	host, path := skopeo.ParseSource(repo)
	if path == "" {
		badReq(c, "repo 无效")
		return
	}
	// list-tags 的 repo 参数不应含 tag。
	if i := strings.LastIndexByte(path, ':'); i >= 0 {
		path = path[:i]
	}
	listRef := host + "/" + path

	tags, err := skopeo.ListTags(c.Request.Context(), listRef, m.Host, m.Username, pass, m.Insecure)
	if err != nil {
		ok(c, gin.H{"ok": false, "message": err.Error(), "tags": []string{}})
		return
	}
	ok(c, gin.H{"ok": true, "tags": tags, "repo": listRef})
}

// Projects GET /api/catalog/:id/projects
//
// 列出目标仓库可用于「目标 project」的候选列表。
//   - harbor:  走 v2 API /api/v2.0/projects 返回所有可见 project 名。
//   - 其他(acr/generic/dockerhub): 无法可靠列出 namespace,返回 ok=false,
//     前端据此退化为纯手输(并默认填入 registry.default_project)。
func (h *CatalogHandler) Projects(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	m, pass, err := h.loadReg(uint(id))
	if err != nil {
		errResp(c, 404, "仓库不存在")
		return
	}

	if strings.ToLower(m.Type) != "harbor" {
		// 非 Harbor 仓库:返回不支持,前端走手输模式。
		ok(c, gin.H{"ok": false, "supported": false, "projects": []string{}, "default_project": m.DefaultProject})
		return
	}

	// Harbor 单页上限 100,这里自动翻页拉取全部 project。
	names, err := harborListAll(c.Request, m.Host, m.Username, pass, m.Insecure, "/api/v2.0/projects", "name")
	if err != nil {
		ok(c, gin.H{"ok": false, "supported": true, "message": err.Error(), "projects": []string{}, "default_project": m.DefaultProject})
		return
	}
	ok(c, gin.H{"ok": true, "supported": true, "projects": names, "default_project": m.DefaultProject})
}

// RegisterRoutes 注册受保护的 catalog 路由。
func (h *CatalogHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/catalog")
	g.Use(middleware.AuthRequired())
	g.GET("/:id/repos", h.Repos)
	g.GET("/:id/tags", h.Tags)
	g.GET("/:id/projects", h.Projects)
}

// registryURL 拼出完整的 HTTPS URL。insecure(跳过 TLS 校验)不改变协议,
// registry 一律走 HTTPS;真正的跳过证书校验在调用方用自定义 Transport 实现。
func registryURL(host string, insecure bool, path string) string {
	return fmt.Sprintf("https://%s%s", host, path)
}

// insecureClient 是跳过 TLS 证书校验的 HTTP client(复用连接池)。
var insecureClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// doRequest 发请求,返回响应体。insecure 为 true 时跳过 TLS 证书校验(自签证书)。
func doRequest(req *http.Request, insecure bool) ([]byte, int, error) {
	c := http.DefaultClient
	if insecure {
		c = insecureClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// basicAuthHeader 生成 Basic 认证头。
func basicAuthHeader(user, pass string) string {
	if user == "" && pass == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// registryCatalog 调 registry v2 /v2/_catalog。
func registryCatalog(req *http.Request, host, user, pass string, insecure bool) ([]string, error) {
	u := registryURL(host, insecure, "/v2/_catalog")
	r, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if h := basicAuthHeader(user, pass); h != "" {
		r.Header.Set("Authorization", h)
	}
	body, code, err := doRequest(r, insecure)
	if err != nil {
		return nil, fmt.Errorf("请求仓库失败: %w", err)
	}
	if code == http.StatusNotFound {
		return nil, fmt.Errorf("该仓库不支持 _catalog 接口,请改用「粘贴列表」方式")
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("认证失败:用户名或密码错误")
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("仓库返回 %d", code)
	}
	var out struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return out.Repositories, nil
}

// harborRepos 调 Harbor v2 API 列 repo。
// project 非空时只列该项目下的 repo。
func harborRepos(req *http.Request, host, user, pass string, insecure bool, project string) ([]string, error) {
	if project == "" {
		// 先列 project,再逐 project 列 repo;Harbor 单 repo 接口要 project 维度。
		projects, err := harborListAll(req, host, user, pass, insecure, "/api/v2.0/projects", "name")
		if err != nil {
			return nil, err
		}
		var all []string
		for _, p := range projects {
			names, err := harborListAll(req, host, user, pass, insecure, fmt.Sprintf("/api/v2.0/projects/%s/repositories", url.PathEscape(p)), "name")
			if err != nil {
				continue
			}
			for _, n := range names {
				all = append(all, p+"/"+n)
			}
		}
		return all, nil
	}
	names, err := harborListAll(req, host, user, pass, insecure, fmt.Sprintf("/api/v2.0/projects/%s/repositories", url.PathEscape(project)), "name")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, project+"/"+n)
	}
	return out, nil
}

// harborListRaw 调 Harbor v2 列表接口的单页,从每个元素抽取 field 字段。
// Harbor 默认返回的 name 对 repo 是「项目内名字」(不含 project 前缀)。
func harborListRaw(req *http.Request, host, user, pass string, insecure bool, path, field string) ([]string, error) {
	// Harbor v2 list 接口要求 page 与 page_size 同时存在,缺 page 会返回 422。
	if strings.Contains(path, "page_size=") && !strings.Contains(path, "page=") {
		path += "&page=1"
	}
	u := registryURL(host, insecure, path)
	r, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if h := basicAuthHeader(user, pass); h != "" {
		r.Header.Set("Authorization", h)
	}
	// Harbor v2 必须带 Accept: application/json,否则可能 422。
	r.Header.Set("Accept", "application/json")
	body, code, err := doRequest(r, insecure)
	if err != nil {
		return nil, fmt.Errorf("请求 Harbor 失败: %w", err)
	}
	if code == http.StatusUnauthorized {
		return nil, fmt.Errorf("认证失败:用户名或密码错误")
	}
	if code != http.StatusOK {
		// 把响应体摘要进错误信息,便于排查 422 等。
		detail := extractHarborErr(body)
		if detail != "" {
			return nil, fmt.Errorf("Harbor 返回 %d: %s", code, detail)
		}
		return nil, fmt.Errorf("Harbor 返回 %d", code)
	}
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, fmt.Errorf("解析 Harbor 响应失败: %w", err)
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		if v, ok := el[field].(string); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// harborListAll 自动翻页拉取 Harbor 列表接口的全部结果。
// basePath 不含分页参数,如 "/api/v2.0/projects"。
// Harbor 单页最多 100 条,通过 page=1,2,... 循环直到返回空。
func harborListAll(req *http.Request, host, user, pass string, insecure bool, basePath, field string) ([]string, error) {
	var all []string
	for page := 1; page <= 100; page++ { // 最多 100 页(10000 条)兜底,防死循环
		path := fmt.Sprintf("%s?page=%d&page_size=100", basePath, page)
		buf, err := harborListRaw(req, host, user, pass, insecure, path, field)
		if err != nil {
			return nil, err
		}
		if len(buf) == 0 {
			break // 空页 = 拉完了
		}
		all = append(all, buf...)
		if len(buf) < 100 {
			break // 不足一页,说明是最后一页
		}
	}
	return all, nil
}

// extractHarborErr 从 Harbor 错误响应体提取可读信息。
// Harbor 错误格式: {"errors":[{"code":"...","message":"...","detail":...}]}。
func extractHarborErr(body []byte) string {
	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
		msgs := make([]string, 0, len(parsed.Errors))
		for _, e := range parsed.Errors {
			if e.Message != "" {
				msgs = append(msgs, e.Message)
			}
		}
		return strings.Join(msgs, "; ")
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 150 {
		s = s[:150] + "..."
	}
	return s
}
