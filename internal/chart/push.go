package chart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/registry"
)

// OCI artifact 相关 media type,与 helm push 的产物保持一致,
// 保证 helm pull / Harbor UI 都能正确识别为 chart。
const (
	mediaTypeManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeConfig   = "application/vnd.cncf.helm.config.v1+json"
	mediaTypeChart    = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// Target 描述一个 chart 仓库的推送目标(已从 ChartRepo 配置解析)。
type Target struct {
	Type     string // model.ChartRepoTypeOCI / model.ChartRepoTypeChartMuseum
	Scheme   string // http / https(由 Host 的 scheme 前缀或默认 https 决定)
	Host     string // 不含 scheme 的 host[:port][/子路径]
	Project  string // chart 项目/命名空间路径,如 datacenter-test-chart
	Username string
	Password string
	Insecure bool
}

// SplitHost 把配置中保存的 Host 拆成 scheme 与裸 host。
// 配置里带 http:// 前缀表示明文 HTTP;https:// 或无前缀均按 HTTPS。
func SplitHost(host string) (scheme, hostOnly string) {
	host = strings.TrimSpace(host)
	switch {
	case strings.HasPrefix(host, "http://"):
		return "http", strings.Trim(host[len("http://"):], "/")
	case strings.HasPrefix(host, "https://"):
		return "https", strings.Trim(host[len("https://"):], "/")
	default:
		return "https", strings.Trim(host, "/")
	}
}

// Ref 返回人类可读的推送目标地址(记录在 ChartUpload.TargetRef)。
func (t Target) Ref(chartName, version string) string {
	if t.Type == model.ChartRepoTypeChartMuseum {
		return t.Scheme + "://" + joinURL(t.Host, "api/charts")
	}
	return "oci://" + joinURL(joinURL(t.Host, t.Project), chartName) + ":" + version
}

// joinURL 以 / 连接非空段落。
func joinURL(parts ...string) string {
	var ps []string
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			ps = append(ps, p)
		}
	}
	return strings.Join(ps, "/")
}

// PushResult 推送结果。
type PushResult struct {
	Digest  string `json:"digest,omitempty"` // OCI 推送成功后的 manifest digest
	Message string `json:"message,omitempty"`
}

// Push 把 chart 包推送到目标仓库,按仓库类型分发到 OCI 或 ChartMuseum 实现。
func Push(ctx context.Context, t Target, m *Meta, data []byte, fileName string) (*PushResult, error) {
	if t.Type == model.ChartRepoTypeChartMuseum {
		return pushChartMuseum(ctx, t, m, data, fileName)
	}
	return pushOCI(ctx, t, m, data)
}

// ---------- ChartMuseum ----------

// pushChartMuseum 走 ChartMuseum 的 POST /api/charts(multipart 字段 chart)。
func pushChartMuseum(ctx context.Context, t Target, m *Meta, data []byte, fileName string) (*PushResult, error) {
	if fileName == "" {
		fileName = m.Name + "-" + m.Version + ".tgz"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("chart", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	endpoint := t.Scheme + "://" + joinURL(t.Host, "api/charts")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if h := registry.BasicAuthHeader(t.Username, t.Password); h != "" {
		req.Header.Set("Authorization", h)
	}

	resp, err := registry.HTTPClient(t.Insecure).Do(req)
	if err != nil {
		return nil, classifyNetErr(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusCreated:
		return &PushResult{Message: fmt.Sprintf("已上传 %s-%s", m.Name, m.Version)}, nil
	case resp.StatusCode == http.StatusConflict:
		return nil, fmt.Errorf("仓库中已存在 %s-%s 且不允许覆盖", m.Name, m.Version)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("认证失败:用户名或密码错误,或无推送权限(HTTP %d)", resp.StatusCode)
	default:
		return nil, fmt.Errorf("ChartMuseum 返回状态码 %d: %s", resp.StatusCode, readErrBody(resp))
	}
}

// ---------- OCI ----------

// ociClient 封装对单个 OCI registry 的带认证请求。
// 认证策略与 registry.Probe 一致:首次请求匿名,由 401 challenge 判断走
// Bearer token(Harbor/ACR 等)还是 Basic;拿到 token 后后续请求复用。
type ociClient struct {
	client     *http.Client
	base       string // scheme://host
	basic      string // "Basic ..." 或空
	authHeader string // 当前应携带的 Authorization(匿名/Bearer/Basic)
}

// newOCIClient 构造 OCI 客户端。超时放宽到 10 分钟以容忍大 chart 与慢网络。
func newOCIClient(t Target) *ociClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if t.Insecure {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	return &ociClient{
		client: &http.Client{Timeout: 10 * time.Minute, Transport: transport},
		base:   t.Scheme + "://" + t.Host,
		basic:  registry.BasicAuthHeader(t.Username, t.Password),
	}
}

// do 发送请求并处理 401 认证重试(repo 用于 Bearer scope,可为空)。
// body 为 nil 表示无请求体;重试时用 bytes.Reader 重新构造,可安全重放。
func (o *ociClient) do(ctx context.Context, method, rawURL string, body []byte, contentType, repo string) (*http.Response, error) {
	resp, err := o.attempt(ctx, method, rawURL, body, contentType)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	// 401:按 challenge 获取认证后重试一次。
	challenge := resp.Header.Get("WWW-Authenticate")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if o.authHeader != "" {
		return nil, fmt.Errorf("认证失败:凭证无效或无推送权限")
	}
	if err := o.authenticate(ctx, challenge, repo); err != nil {
		return nil, err
	}
	return o.attempt(ctx, method, rawURL, body, contentType)
}

// attempt 真正发起一次 HTTP 请求。
func (o *ociClient) attempt(ctx context.Context, method, rawURL string, body []byte, contentType string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if o.authHeader != "" {
		req.Header.Set("Authorization", o.authHeader)
	}
	return o.client.Do(req)
}

// authenticate 解析 WWW-Authenticate challenge 并更新 authHeader。
// challenge 为空视为 Basic 处理(部分发行版不带 challenge 直接 401)。
func (o *ociClient) authenticate(ctx context.Context, challenge, repo string) error {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "bearer ") {
		// Basic challenge(或无 challenge):有凭证直接切换为 Basic 认证。
		if o.basic != "" {
			o.authHeader = o.basic
			return nil
		}
		return fmt.Errorf("仓库需要认证,请在仓库配置中填写账号密码")
	}
	if o.basic == "" {
		return fmt.Errorf("仓库需要认证,请在仓库配置中填写账号密码")
	}
	realm, service := parseBearerChallenge(challenge)
	if realm == "" {
		return fmt.Errorf("无法解析仓库认证信息(challenge: %s)", challenge)
	}
	// scope 必须带 push 权限,Harbor 会严格校验;纯探测(repo 为空)时不带 scope。
	q := url.Values{}
	if service != "" {
		q.Set("service", service)
	}
	if repo != "" {
		q.Set("scope", "repository:"+repo+":pull,push")
	}
	tokenURL := realm
	if encoded := q.Encode(); encoded != "" {
		tokenURL += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", o.basic)
	resp, err := o.client.Do(req)
	if err != nil {
		return classifyNetErr(err)
	}
	defer resp.Body.Close()
	tokBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("认证失败:用户名或密码错误")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("获取 token 失败(状态码 %d): %s", resp.StatusCode, truncate(string(tokBody), 200))
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokBody, &tok); err != nil || (tok.Token == "" && tok.AccessToken == "") {
		return fmt.Errorf("解析 token 响应失败")
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	o.authHeader = "Bearer " + tok.Token
	return nil
}

// pushOCI 按 helm 的 artifact 结构推送:config blob + chart layer + manifest。
func pushOCI(ctx context.Context, t Target, m *Meta, data []byte) (*PushResult, error) {
	o := newOCIClient(t)
	repo := joinURL(t.Project, m.Name)

	// 1. config blob(Chart.yaml 元数据的 JSON 形式)。
	cfgBytes, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	// 2. chart tgz layer。两个 blob 均推送后再发 manifest。
	for _, blob := range [][]byte{cfgBytes, data} {
		if err := o.pushBlob(ctx, repo, blob); err != nil {
			return nil, err
		}
	}

	// 3. manifest(tag = chart version,与 helm push 一致)。
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": descriptor{
			MediaType: mediaTypeConfig,
			Digest:    digestOf(cfgBytes),
			Size:      int64(len(cfgBytes)),
		},
		"layers": []descriptor{{
			MediaType: mediaTypeChart,
			Digest:    digestOf(data),
			Size:      int64(len(data)),
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestURL := o.base + "/v2/" + repo + "/manifests/" + url.PathEscape(m.Version)
	resp, err := o.do(ctx, http.MethodPut, manifestURL, manifestBytes, mediaTypeManifest, repo)
	if err != nil {
		return nil, fmt.Errorf("推送 manifest 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("推送 manifest 失败(HTTP %d): %s", resp.StatusCode, readErrBody(resp))
	}
	// 响应体不需要,但保持连接复用前读完。
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = digestOf(manifestBytes)
	}
	return &PushResult{
		Digest:  digest,
		Message: fmt.Sprintf("已推送 %s:%s", m.Name, m.Version),
	}, nil
}

// descriptor 是 OCI manifest 中的 blob 描述。
type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// pushBlob 以「两步 monolithic upload」推送单个 blob:
// POST 初始化拿 Location → PUT 携带 digest 一次性上传。
func (o *ociClient) pushBlob(ctx context.Context, repo string, content []byte) error {
	initURL := o.base + "/v2/" + repo + "/blobs/uploads/"
	resp, err := o.do(ctx, http.MethodPost, initURL, nil, "", repo)
	if err != nil {
		return fmt.Errorf("初始化 blob 上传失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusAccepted:
		// 正常:202 + Location;少数 registry 对已存在的 digest 直接 201。
	default:
		return fmt.Errorf("初始化 blob 上传失败(HTTP %d): %s", resp.StatusCode, readErrBody(resp))
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		// 无 Location 且已 201:blob 已存在(极少数 registry 行为),视为成功。
		if resp.StatusCode == http.StatusCreated {
			return nil
		}
		return fmt.Errorf("仓库未返回上传 Location")
	}
	uploadURL := resolveLocation(o.base, loc)
	sep := "?"
	if strings.Contains(uploadURL, "?") {
		sep = "&"
	}
	uploadURL += sep + "digest=" + url.QueryEscape(digestOf(content))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	// Location 可能是跨主机的预签名 URL(云厂商 registry);此时不能携带仓库凭证,
	// 仅当上传地址与仓库同主机时附加 Authorization。
	if o.authHeader != "" && sameHost(o.base, uploadURL) {
		req.Header.Set("Authorization", o.authHeader)
	}
	putResp, err := o.client.Do(req)
	if err != nil {
		return classifyNetErr(err)
	}
	defer putResp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(putResp.Body, 4096))

	switch putResp.StatusCode {
	case http.StatusCreated, http.StatusAccepted, http.StatusOK, http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("上传 blob 失败(HTTP %d): %s", putResp.StatusCode, readErrBody(putResp))
	}
}

// resolveLocation 把仓库返回的 Location(绝对或相对)拼成完整 URL。
func resolveLocation(base, loc string) string {
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		return loc
	}
	if !strings.HasPrefix(loc, "/") {
		loc = "/" + loc
	}
	return base + loc
}

// sameHost 判断两个 URL 的主机(含端口)是否相同。
func sameHost(a, b string) bool {
	ua, erra := url.Parse(a)
	ub, errb := url.Parse(b)
	if erra != nil || errb != nil {
		return false
	}
	return ua.Host == ub.Host
}

// digestOf 计算 sha256 摘要的 OCI 表示("sha256:<hex>")。
func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// parseBearerChallenge 解析 Bearer challenge 中的 realm 与 service。
func parseBearerChallenge(header string) (realm, service string) {
	s := strings.TrimSpace(header)[len("Bearer "):]
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.Trim(strings.TrimSpace(part[eq+1:]), "\"")
		switch k {
		case "realm":
			realm = v
		case "service":
			service = v
		}
	}
	return
}

// readErrBody 读取响应体并尽量提取 OCI 标准 {"errors":[...]} 里的 message。
func readErrBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			return truncate(parsed.Errors[0].Message, 200)
		}
		if parsed.Message != "" {
			return truncate(parsed.Message, 200)
		}
	}
	return truncate(strings.TrimSpace(string(body)), 200)
}

// truncate 截断过长文本。
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// classifyNetErr 把网络错误转成友好提示(与 registry.Probe 的分类口径一致)。
func classifyNetErr(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return fmt.Errorf("连接超时:仓库未在时限内响应(%v)", err)
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup"):
		return fmt.Errorf("无法解析仓库主机名(地址错误或 DNS 故障): %v", err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("连接被拒绝:目标端口未开放或服务未启动: %v", err)
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		return fmt.Errorf("TLS 证书校验失败,可在仓库配置中开启「跳过 TLS」: %v", err)
	default:
		return err
	}
}

// ---------- 连通性探测 ----------

// Probe 测试 chart 仓库连通性与凭证。OCI 走 /v2/ ping,ChartMuseum 走 /health。
func Probe(ctx context.Context, t Target) registry.ProbeResult {
	if t.Type == model.ChartRepoTypeChartMuseum {
		return probeChartMuseum(ctx, t)
	}
	return probeOCI(ctx, t)
}

// probeOCI 用与推送一致的认证流程 ping /v2/。
func probeOCI(ctx context.Context, t Target) registry.ProbeResult {
	o := newOCIClient(t)
	pingURL := o.base + "/v2/"
	resp, err := o.do(ctx, http.MethodGet, pingURL, nil, "", "")
	if err != nil {
		return registry.ProbeResult{OK: false, Message: err.Error(), Endpoint: pingURL}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusOK:
		return registry.ProbeResult{OK: true, Message: "连接成功", Endpoint: pingURL}
	case http.StatusUnauthorized, http.StatusForbidden:
		return registry.ProbeResult{OK: false, Message: "认证失败:用户名或密码错误,或无访问权限", Endpoint: pingURL}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return registry.ProbeResult{OK: false, Message: "地址错误:不是有效的 OCI 仓库(未返回 /v2/ 端点)", Endpoint: pingURL}
	default:
		return registry.ProbeResult{OK: false, Message: fmt.Sprintf("仓库返回异常状态码 %d", resp.StatusCode), Endpoint: pingURL}
	}
}

// probeChartMuseum 探测 ChartMuseum 的 /health(旧版本回退 /api/health)。
func probeChartMuseum(ctx context.Context, t Target) registry.ProbeResult {
	client := registry.HTTPClient(t.Insecure)
	base := t.Scheme + "://" + t.Host
	auth := registry.BasicAuthHeader(t.Username, t.Password)

	for _, p := range []string{"/health", "/api/health"} {
		endpoint := base + p
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return registry.ProbeResult{OK: false, Message: err.Error()}
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			return registry.ProbeResult{OK: false, Message: classifyNetErr(err).Error(), Endpoint: endpoint}
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			return registry.ProbeResult{OK: true, Message: "连接成功", Endpoint: endpoint}
		case http.StatusUnauthorized, http.StatusForbidden:
			return registry.ProbeResult{OK: false, Message: "认证失败:用户名或密码错误,或无访问权限", Endpoint: endpoint}
		case http.StatusNotFound:
			continue // 试下一个候选路径
		default:
			return registry.ProbeResult{OK: false, Message: fmt.Sprintf("仓库返回异常状态码 %d", resp.StatusCode), Endpoint: endpoint}
		}
	}
	return registry.ProbeResult{OK: false, Message: "未找到 /health 端点:地址不是 ChartMuseum 或版本过旧", Endpoint: base}
}
