// Package cli 实现 irs 命令行工具:以 images-repo-sync 平台的 REST API 为后端,
// 提供登录、仓库管理、同步任务、chart 上传等能力的命令行入口,
// 供人工使用与 AI(skill)自动化调用。
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client 是平台 REST API 的极薄封装:统一携带鉴权头、解包 {data}/{error}
// 响应包装、401 时用账号密码自动重登一次,以及 multipart 文件上传与 SSE 订阅。
type Client struct {
	Server string // 基地址,如 http://localhost:8080(无尾斜杠)
	Token  string // Bearer token;空表示匿名(仅 /healthz 等公开接口可用)

	// Username/Password 供 401 自动重登使用,可空(来自 IRS_USER/IRS_PASSWORD)。
	Username string
	Password string

	// OnTokenRefresh 在自动重登成功后回调(用于把新 token 持久化到配置文件),可空。
	OnTokenRefresh func(token, expiresAt string)

	http *http.Client
}

// NewClient 创建客户端并规范化服务端地址(补 http:// 前缀、去尾斜杠)。
func NewClient(server, token string) *Client {
	server = strings.TrimSpace(server)
	if server != "" && !strings.Contains(server, "://") {
		server = "http://" + server
	}
	server = strings.TrimRight(server, "/")
	c := &Client{
		Server: server,
		Token:  token,
		// 常规 JSON 调用远小于该时长;multipart 批量上传 chart 可能较慢,
		// SSE 流走独立 client 不受此限制。
		http: &http.Client{Timeout: 10 * time.Minute},
	}
	return c
}

// APIError 表示服务端返回的非 2xx 响应。
type APIError struct {
	Status  int
	Message string // 服务端 {error} 消息;解析失败时退化为响应体片段
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Envelope 是列表接口的顶层包装 {"data":..., "total":n, "page":p, "size":s}。
type Envelope struct {
	Data  json.RawMessage `json:"data"`
	Total int64           `json:"total"`
	Page  int             `json:"page"`
	Size  int             `json:"size"`
}

// DoJSON 调用一个 JSON 接口并把 {data:...} 解包到 out(out 为 nil 时丢弃)。
// 返回完整 Envelope 以便调用方读取 total 等分页字段。
func (c *Client) DoJSON(ctx context.Context, method, path string, body, out any) (*Envelope, error) {
	raw, err := c.request(ctx, method, path, body, "application/json", false)
	if err != nil {
		return nil, err
	}
	env := &Envelope{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, env); err != nil {
			return nil, fmt.Errorf("响应不是合法 JSON: %w", err)
		}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("解析响应数据失败: %w", err)
		}
	}
	return env, nil
}

// request 执行一次请求。body 非 nil 时按 JSON 序列化;401 且配置了账号密码时
// 自动重登并重试一次(登录接口自身不重试)。
func (c *Client) request(ctx context.Context, method, path string, body any, contentType string, isLogin bool) ([]byte, error) {
	raw, err := c.doOnce(ctx, method, path, body, contentType)
	if err == nil {
		return raw, nil
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusUnauthorized || isLogin || c.Username == "" || c.Password == "" {
		return nil, err
	}
	// 401:token 过期,尝试用账号密码换新 token 后重试。
	if _, err := c.login(ctx, c.Username, c.Password, false); err != nil {
		return nil, err
	}
	return c.doOnce(ctx, method, path, body, contentType)
}

// doOnce 发送单次请求并读取响应。
func (c *Client) doOnce(ctx context.Context, method, path string, body any, contentType string) ([]byte, error) {
	if c.Server == "" {
		return nil, fmt.Errorf("未配置服务端地址:请用 --server、IRS_SERVER 环境变量或先执行 irs login")
	}
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rd = bytes.NewReader(b)
		default:
			data, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("序列化请求体失败: %w", err)
			}
			rd = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "irs-cli/"+Version)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := ""
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		} else if len(raw) > 0 {
			msg = string(bytes.TrimSpace(raw))
			if len(msg) > 300 {
				msg = msg[:300] + "..."
			}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return raw, nil
}

// LoginResult 登录成功后返回的关键信息。
type LoginResult struct {
	Token     string
	ExpiresAt string
	Username  string
}

// login 用账号密码换取 token(isLogin 标记避免递归重试)。
func (c *Client) login(ctx context.Context, username, password string, remember bool) (*LoginResult, error) {
	payload := map[string]any{"username": username, "password": password, "remember_me": remember}
	raw, err := c.doOnce(ctx, http.MethodPost, "/api/auth/login", payload, "application/json")
	if err != nil {
		return nil, err
	}
	var env struct {
		Data struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
			User      struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Data.Token == "" {
		return nil, fmt.Errorf("登录响应异常,未取到 token")
	}
	c.Token = env.Data.Token
	res := &LoginResult{Token: env.Data.Token, ExpiresAt: env.Data.ExpiresAt, Username: env.Data.User.Username}
	if c.OnTokenRefresh != nil {
		c.OnTokenRefresh(res.Token, res.ExpiresAt)
	}
	return res, nil
}

// Login 公开登录入口。
func (c *Client) Login(ctx context.Context, username, password string, remember bool) (*LoginResult, error) {
	return c.login(ctx, username, password, remember)
}

// UploadFiles 以 multipart/form-data 上传本地文件到 path,
// fieldName 为文件字段名,fields 为附加表单字段(如 repo_id)。
func (c *Client) UploadFiles(ctx context.Context, path, fieldName string, files []string, fields map[string]string) (*Envelope, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, fmt.Errorf("打开文件 %s 失败: %w", f, err)
		}
		part, err := mw.CreateFormFile(fieldName, filepath.Base(f))
		if err != nil {
			fh.Close()
			return nil, err
		}
		if _, err := io.Copy(part, fh); err != nil {
			fh.Close()
			return nil, fmt.Errorf("读取文件 %s 失败: %w", f, err)
		}
		fh.Close()
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "irs-cli/"+Version)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上传请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := ""
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	env := &Envelope{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, env); err != nil {
			return nil, fmt.Errorf("响应不是合法 JSON: %w", err)
		}
	}
	return env, nil
}

// SSEEvent 是服务端推送的一条 SSE 事件(已解析 event 名与 data 载荷)。
type SSEEvent struct {
	Event string
	Data  []byte
}

// StreamSSE 订阅指定 GET 接口的 SSE 流,逐条回调;回调返回 false 时主动断开。
// 连接中断(服务端结束流)时返回 nil,由调用方根据是否已收到结束事件判断成败。
// 注意:此请求不走 c.http 的整体超时,超时由 ctx 控制。
func (c *Client) StreamSSE(ctx context.Context, path string, fn func(SSEEvent) bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "irs-cli/"+Version)

	client := &http.Client{} // 不设整体超时:长连接由 ctx 控制生命周期
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接事件流失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(raw))
		return &APIError{Status: resp.StatusCode, Message: msg}
	}

	// 按行解析 SSE 帧:event:/data: 成对出现,空行表示一条事件结束。
	// 心跳注释行(: ping)被自然跳过。
	var event strings.Builder
	var data bytes.Buffer
	scannerLine := func(line string) bool {
		switch {
		case line == "":
			if event.Len() > 0 || data.Len() > 0 {
				d := append([]byte(nil), data.Bytes()...)
				// SSE 规范:data 多行以 \n 连接;本平台事件均为单行 JSON。
				if !fn(SSEEvent{Event: strings.TrimSpace(event.String()), Data: d}) {
					return false
				}
			}
			event.Reset()
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// 注释/心跳,忽略。
		case strings.HasPrefix(line, "event:"):
			event.WriteString(trimOneSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(trimOneSpace(strings.TrimPrefix(line, "data:")))
		}
		return true
	}

	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" || err == nil {
			if !scannerLine(line) {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// trimOneSpace 按 SSE 规范去掉字段值前的一个空格。
func trimOneSpace(s string) string {
	if strings.HasPrefix(s, " ") {
		return s[1:]
	}
	return s
}

// Ping 健康检查(公开接口,无需 token)。
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Server+"/api/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("服务不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode}
	}
	return nil
}

// NormalizeServer 供测试与调用方复用的地址规范化。
func NormalizeServer(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimRight(s, "/")
}
