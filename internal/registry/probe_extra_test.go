package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBaseURL 协议选择规则。
func TestBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"harbor.example.com", "https://harbor.example.com"},
		{"https://harbor.example.com", "https://harbor.example.com"},
		{"http://harbor.example.com", "http://harbor.example.com"},
		{"http://192.168.0.1:5000", "http://192.168.0.1:5000"},
		{"  harbor.example.com/  ", "https://harbor.example.com/"},
		{"https://h.com/", "https://h.com/"}, // 尾斜杠保留(仅去左侧斜杠)
	}
	for _, c := range cases {
		if got := baseURL(c.in); got != c.want {
			t.Errorf("baseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newBearerMock 构造模拟 Bearer 认证流程的 registry:
// /v2/ 401 + Bearer challenge;token 端点与最终 /v2/ 行为由调用方决定。
func newBearerMock(t *testing.T, tokenHandler http.HandlerFunc, verifyHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer realm="http://%s/token",service="mock-registry"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		verifyHandler(w, r)
	})
	mux.HandleFunc("/token", tokenHandler)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func jsonToken(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"token":%q}`, token)
}

func TestProbePublicRegistry(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	res := Probe(context.Background(), ts.URL, "", "", false)
	if !res.OK || res.Message != "连接成功" {
		t.Fatalf("公开 registry 应探测成功: %+v", res)
	}
}

func TestProbeBearerFlowSuccess(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) {
			// token 端点应收到 Basic 凭证。
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			jsonToken(w, "mock-token")
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer mock-token" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		})
	res := Probe(context.Background(), ts.URL, "alice", "pw", false)
	if !res.OK {
		t.Fatalf("Bearer 流程应成功: %+v", res)
	}
}

func TestProbeBearerBadCredentials(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	res := Probe(context.Background(), ts.URL, "alice", "wrong", false)
	if res.OK || !strings.Contains(res.Message, "用户名或密码错误") {
		t.Fatalf("凭证错误应识别: %+v", res)
	}
}

func TestProbeBearerTokenEndpointError(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	res := Probe(context.Background(), ts.URL, "a", "b", false)
	if res.OK || !strings.Contains(res.Message, "获取 token 失败") {
		t.Fatalf("token 端点 500 应报获取失败: %+v", res)
	}
}

func TestProbeBearerUnparsableToken(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"not_token":true}`) },
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	res := Probe(context.Background(), ts.URL, "a", "b", false)
	if res.OK || !strings.Contains(res.Message, "解析 token 响应失败") {
		t.Fatalf("无 token 字段应报解析失败: %+v", res)
	}
}

func TestProbeBearerTokenRejectedAtV2(t *testing.T) {
	ts := newBearerMock(t,
		func(w http.ResponseWriter, r *http.Request) { jsonToken(w, "bad-token") },
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	res := Probe(context.Background(), ts.URL, "a", "b", false)
	if res.OK || !strings.Contains(res.Message, "token 无效或权限不足") {
		t.Fatalf("token 被拒应报无效: %+v", res)
	}
}

func TestProbeBasicFlow(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantOK    bool
		wantInMsg string
	}{
		{"Basic 成功", http.StatusOK, true, "连接成功"},
		{"Basic 凭证错误", http.StatusUnauthorized, false, "用户名或密码错误"},
		{"Basic 403", http.StatusForbidden, false, "用户名或密码错误"},
		{"Basic 后 500", http.StatusInternalServerError, false, "认证后仍返回 500"},
	}
	for _, c := range cases {
		mux := http.NewServeMux()
		mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(c.status)
		})
		ts := httptest.NewServer(mux)
		res := Probe(context.Background(), ts.URL, "u", "p", false)
		ts.Close()
		if res.OK != c.wantOK || !strings.Contains(res.Message, c.wantInMsg) {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
}

func TestProbeNoChallenge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized) // 无 WWW-Authenticate 头
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	res := Probe(context.Background(), ts.URL, "", "", false)
	if res.OK || !strings.Contains(res.Message, "无法获取认证方式") {
		t.Fatalf("无 challenge 应报无法获取认证方式: %+v", res)
	}
}

func TestProbeHTTPStatuses(t *testing.T) {
	cases := []struct {
		code      int
		wantInMsg string
	}{
		{http.StatusForbidden, "权限不足"},
		{http.StatusNotFound, "地址错误"},
		{http.StatusMethodNotAllowed, "地址错误"},
		{http.StatusInternalServerError, "异常状态码 500"},
	}
	for _, c := range cases {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		res := Probe(context.Background(), ts.URL, "", "", false)
		ts.Close()
		if res.OK || !strings.Contains(res.Message, c.wantInMsg) {
			t.Errorf("状态 %d: 期望消息含 %q, got %+v", c.code, c.wantInMsg, res)
		}
	}
}

func TestProbeNetworkErrors(t *testing.T) {
	// 连接被拒绝:未监听的保留端口。
	res := Probe(context.Background(), "http://127.0.0.1:1", "", "", false)
	if res.OK || !strings.Contains(res.Message, "连接被拒绝") {
		t.Errorf("connection refused 应被归类: %+v", res)
	}

	// 超时:1ms 超时的 ctx + 慢响应服务器。
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res = Probe(ctx, slow.URL, "", "", false)
	if res.OK || !strings.Contains(res.Message, "连接超时") {
		t.Errorf("超时应被归类: %+v", res)
	}

	// TLS 证书校验失败:自签 TLS 服务 + 不开 insecure。
	tlsTS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer tlsTS.Close()
	host := strings.TrimPrefix(tlsTS.URL, "https://")
	res = Probe(context.Background(), host, "", "", false)
	if res.OK || !strings.Contains(res.Message, "TLS 证书校验失败") {
		t.Errorf("x509 应被归类: %+v", res)
	}
	// 开 insecure 后成功。
	res = Probe(context.Background(), host, "", "", true)
	if !res.OK {
		t.Errorf("insecure 应成功: %+v", res)
	}

	// DNS 无法解析(域名保留字,公网 NXDOMAIN)。
	res = Probe(context.Background(), "https://nonexistent.invalid-registry-example.test", "", "", false)
	if res.OK || !strings.Contains(res.Message, "无法解析主机") {
		t.Errorf("DNS 失败应被归类: %+v", res)
	}
}

func TestReadErrBody(t *testing.T) {
	mkResp := func(body string) *http.Response {
		w := httptest.NewRecorder()
		w.WriteString(body)
		return w.Result()
	}
	cases := []struct {
		name, body, want string
	}{
		{"OCI errors JSON", `{"errors":[{"message":"manifest unknown"}]}`, "manifest unknown"},
		{"单 message JSON", `{"message":"denied"}`, "denied"},
		{"非 JSON", "plain text error", "plain text error"},
		{"空 body", "", ""},
	}
	for _, c := range cases {
		if got := readErrBody(mkResp(c.body)); !strings.Contains(got, c.want) {
			t.Errorf("%s: readErrBody = %q, want 含 %q", c.name, got, c.want)
		}
	}
	// 长 body 截断。
	long := strings.Repeat("x", 600)
	if got := readErrBody(mkResp(long)); len(got) > 210 {
		t.Errorf("长 body 应截断: %d", len(got))
	}
}

func TestHTTPClientShared(t *testing.T) {
	if HTTPClient(false) != defaultHTTPClient {
		t.Error("默认应返回共享 client")
	}
	if HTTPClient(true) != insecureHTTPClient {
		t.Error("insecure 应返回共享 insecure client")
	}
}

func TestBasicAuthHeader(t *testing.T) {
	if h := BasicAuthHeader("", ""); h != "" {
		t.Errorf("匿名应为空, got %q", h)
	}
	h := BasicAuthHeader("u", "p")
	if !strings.HasPrefix(h, "Basic ") {
		t.Fatalf("前缀不对: %q", h)
	}
	// base64(u:p)。
	if want := "Basic dTpw"; h != want {
		t.Errorf("BasicAuthHeader = %q, want %q", h, want)
	}
	// 只有密码无用户名也应生成。
	if BasicAuthHeader("", "p") == "" {
		t.Error("只有密码时不应为空")
	}
}
