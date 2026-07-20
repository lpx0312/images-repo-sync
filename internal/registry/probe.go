// Package registry 实现对 OCI/Docker Registry v2 仓库的连通性与凭证探测,
// 不依赖 skopeo,直接走 HTTP,对 Harbor / ACR / DockerHub / 通用 registry 全通用。
package registry

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProbeResult 是连接探测结果。
type ProbeResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Endpoint string `json:"endpoint,omitempty"` // 实际命中的探测端点
}

// httpClient 构造探测用的 HTTP client(insecure 时跳过 TLS 校验)。
func httpClient(insecure bool) *http.Client {
	t := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: t}
}

// baseURL 根据 insecure 选 https/http 并返回形如 "https://host[:port]" 的根。
func baseURL(host string, insecure bool) string {
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	return scheme + "://" + host
}

// basicAuth 返回 "Basic base64(user:pass)"。user/pass 为空则返回空串。
func basicAuth(user, pass string) string {
	if user == "" && pass == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// doGet 发 GET 请求,返回响应对象(调用方负责 Close body)。
func doGet(ctx context.Context, client *http.Client, rawURL, authHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("User-Agent", "images-repo-sync/probe")
	return client.Do(req)
}

// readErrBody 读取响应体(限长),用于错误信息。
func readErrBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// 尝试解析 OCI 标准 {"errors":[{"message":"..."}]} 或 {"message":"..."}。
	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if len(parsed.Errors) > 0 && parsed.Errors[0].Message != "" {
			return parsed.Errors[0].Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	// 非 JSON,返回裁剪后的原文。
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// Probe 测试目标 registry 的连通性与凭证有效性。
//
// 采用 OCI/Docker Registry v2 标准流程(对所有 OCI 兼容 registry 通用):
//  1. GET /v2/(带 Basic Auth):
//     - 200 → 直接连通且凭证有效(公开 registry 或已认证)
//     - 401 → 需要进一步走 Bearer token 流程(401 不是失败!)
//     - 403 → 权限不足
//     - 404/405 → 不是合法 registry
//     - 连接错误/超时 → 网络不可达
//  2. 仅当第 1 步返回 401 时:从 WWW-Authenticate 头解析 realm/service,
//     用 Basic Auth 去 realm 拿 Bearer token:
//     - realm 端点 401 → 真正的「用户名或密码错误」
//     - 拿到 token 后带 Bearer 再访问 /v2/,200 → 成功
func Probe(ctx context.Context, host, user, pass string, insecure bool) ProbeResult {
	client := httpClient(insecure)
	root := baseURL(host, insecure)
	pingURL := root + "/v2/"

	// 第 1 步:带 Basic Auth 打 /v2/。
	resp, err := doGet(ctx, client, pingURL, basicAuth(user, pass))
	if err != nil {
		return classifyNetErr(err, host)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 200:直接成功(公开 registry 或 Basic Auth 直接通过)。
		return ProbeResult{OK: true, Message: "连接成功", Endpoint: pingURL}
	case http.StatusUnauthorized:
		// 401:走 Bearer token 流程(ACR/Harbor/DockerHub 都是这条路径)。
		return probeBearer(ctx, client, root, pingURL, resp, user, pass)
	case http.StatusForbidden:
		return ProbeResult{OK: false, Message: "权限不足:用户没有访问该仓库的权限", Endpoint: pingURL}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return ProbeResult{OK: false, Message: "地址错误:不是有效的镜像仓库(未返回 OCI v2 端点)", Endpoint: pingURL}
	default:
		return ProbeResult{OK: false, Message: fmt.Sprintf("仓库返回异常状态码 %d", resp.StatusCode), Endpoint: pingURL}
	}
}

// probeBearer 处理 401 后的 Bearer token 流程。
func probeBearer(ctx context.Context, client *http.Client, root, pingURL string, pingResp *http.Response, user, pass string) ProbeResult {
	authHeader := pingResp.Header.Get("WWW-Authenticate")
	if authHeader == "" {
		// 无 WWW-Authenticate 头的 401,通常是 Basic Auth 凭证错误。
		return ProbeResult{OK: false, Message: "认证失败:用户名或密码错误", Endpoint: pingURL}
	}

	realm, service, ok := parseChallenge(authHeader)
	if !ok {
		// 非 Bearer challenge(如 Basic challenge)→ 凭证错。
		return ProbeResult{OK: false, Message: "认证失败:用户名或密码错误", Endpoint: pingURL, Detail: "challenge: " + authHeader}
	}

	// 第 2 步:用 Basic Auth 去 realm 拿 token。
	tokenURL := realm
	if service != "" {
		tokenURL += "?service=" + url.QueryEscape(service)
	}
	tokenResp, err := doGet(ctx, client, tokenURL, basicAuth(user, pass))
	if err != nil {
		return classifyNetErr(err, realm)
	}
	tokenBody, _ := io.ReadAll(io.LimitReader(tokenResp.Body, 8*1024))
	tokenResp.Body.Close()

	if tokenResp.StatusCode == http.StatusUnauthorized {
		// realm 端点 401 → 真正的凭证错误。
		return ProbeResult{OK: false, Message: "认证失败:用户名或密码错误", Endpoint: tokenURL}
	}
	if tokenResp.StatusCode != http.StatusOK {
		return ProbeResult{OK: false, Message: fmt.Sprintf("获取 token 失败(状态码 %d)", tokenResp.StatusCode), Endpoint: tokenURL, Detail: string(tokenBody)}
	}

	// 解析 token。
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenBody, &tok); err != nil || tok.Token == "" {
		return ProbeResult{OK: false, Message: "解析 token 响应失败", Endpoint: tokenURL, Detail: string(tokenBody)}
	}

	// 第 3 步:带 Bearer token 再访问 /v2/。
	resp3, err := doGet(ctx, client, pingURL, "Bearer "+tok.Token)
	if err != nil {
		return classifyNetErr(err, pingURL)
	}
	io.Copy(io.Discard, resp3.Body)
	resp3.Body.Close()

	if resp3.StatusCode == http.StatusOK {
		return ProbeResult{OK: true, Message: "连接成功", Endpoint: pingURL}
	}
	if resp3.StatusCode == http.StatusUnauthorized || resp3.StatusCode == http.StatusForbidden {
		return ProbeResult{OK: false, Message: "认证失败:token 无效或权限不足", Endpoint: pingURL}
	}
	return ProbeResult{OK: false, Message: fmt.Sprintf("验证失败(状态码 %d)", resp3.StatusCode), Endpoint: pingURL}
}

// parseChallenge 解析 WWW-Authenticate: Bearer realm="...",service="...",...
// 返回 realm, service, ok。
func parseChallenge(header string) (realm, service string, ok bool) {
	s := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(s), "bearer ") {
		return "", "", false
	}
	s = s[len("bearer "):]
	// 按 ',' 分割,每段形如 key="value" 或 key=value。
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
	if realm == "" {
		return "", "", false
	}
	return realm, service, true
}

// classifyNetErr 把 net/http 的请求错误归类为友好的中文提示。
func classifyNetErr(err error, host string) ProbeResult {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return ProbeResult{OK: false, Message: "连接超时:仓库未在 15 秒内响应", Detail: err.Error()}
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup"):
		return ProbeResult{OK: false, Message: fmt.Sprintf("无法解析主机 %s(地址错误或 DNS 故障)", host), Detail: err.Error()}
	case strings.Contains(msg, "connection refused"):
		return ProbeResult{OK: false, Message: "连接被拒绝:目标端口未开放或服务未启动", Detail: err.Error()}
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"):
		return ProbeResult{OK: false, Message: "TLS 证书校验失败,可尝试开启「跳过 TLS」", Detail: err.Error()}
	default:
		return ProbeResult{OK: false, Message: "网络不可达", Detail: err.Error()}
	}
}
