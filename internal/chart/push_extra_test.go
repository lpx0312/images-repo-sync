package chart

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// A6(resp.Body 泄漏,push.go:597-610 一带):资源泄漏类问题难以在单测中可靠断言
// (需 runtime 指标或泄漏检测工具),按要求跳过,待修复后以工具验证。

// ---------- A 类回归(缺陷已修复,应保持绿色) ----------

// 回归锁定(已修复): A3 OCI 推送中 bearer token 过期(请求中途 401)时应重新走
// challenge 换新 token 重试,而不是误报「认证失败:凭证无效或无推送权限」。
// 场景:token 端点首发 tokenA(blob 阶段有效),推 manifest 时 tokenA 被判 401,
// 重新 challenge 拿 tokenB 后应整体推送成功。
func TestPushOCITokenRefreshOnExpiry(t *testing.T) {
	var tokenCalls int32
	var manifests int
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if atomic.AddInt32(&tokenCalls, 1) == 1 {
			jsonToken(w, "tokenA") // 首发 token A
			return
		}
		jsonToken(w, "tokenB") // 刷新后的 token B
	})

	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		challenge := fmt.Sprintf(`Bearer realm="http://%s/token",service="reg"`, r.Host)
		if auth == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		// 模拟 tokenA 到 manifest 阶段已过期:仅对 manifest PUT 拒绝 tokenA。
		if r.Method == http.MethodPut && strings.Contains(path, "/manifests/") && auth == "Bearer tokenA" {
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(path, "/blobs/uploads/") && r.Method == http.MethodPost:
			w.Header().Set("Location", "/v2/upload-session-1")
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(path, "/upload-session-1") && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(path, "/manifests/") && r.Method == http.MethodPut:
			manifests++
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Docker-Content-Digest", digestOf(body))
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	res, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://"), Project: "charts",
		Username: "user", Password: "pass",
	}, &Meta{Name: "mychart", Version: "1.2.3"},
		testutil.BuildChartTgz(t, "mychart", "1.2.3", "mychart"), "")
	if err != nil {
		t.Fatalf("A3: token 过期应重新走 challenge 刷新并推送成功, got: %v", err)
	}
	if manifests != 1 {
		t.Errorf("A3: manifest 应成功推送 1 次, got %d", manifests)
	}
	if res == nil || !strings.HasPrefix(res.Digest, "sha256:") {
		t.Errorf("A3: 应返回有效 digest, got %+v", res)
	}
	if n := atomic.LoadInt32(&tokenCalls); n != 2 {
		t.Errorf("A3: token 端点应被调用 2 次(A→B), got %d", n)
	}
}

// 回归锁定(已修复): A5 ChartMuseum 的 JSON 错误体 {"error":"..."} 应解析出其中的
// 可读文本,而不是把原始 JSON 整段塞进错误信息。
func TestPushChartMuseumErrorBodyParsed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "systeminfo") {
			w.WriteHeader(http.StatusNotFound) // 非 Harbor
			return
		}
		if r.URL.Path == "/api/charts" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"chart validation failed: icon required"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://"),
	}, &Meta{Name: "c", Version: "1.0.0"}, []byte("tgz-bytes"), "")
	if err == nil {
		t.Fatal("A5: 400 应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "chart validation failed") {
		t.Errorf("A5: 错误信息应含错误体中的可读文本, got: %v", err)
	}
	if strings.Contains(msg, `"error"`) {
		t.Errorf("A5: 不应把原始 JSON 错误体整段透出, got: %v", err)
	}
}

// ---------- B 类边界(GREEN) ----------

// TestPushBlobLocationWithQuery Location 已带 query(expires 等)时,digest 参数
// 应以 & 追加而非重复 ?(push.go pushBlob 的分支)。
func TestPushBlobLocationWithQuery(t *testing.T) {
	var gotQuery url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/blobs/uploads/") && r.Method == http.MethodPost:
			// Location 相对路径且已带 query(部分 registry 会带 expires 等签名参数)。
			w.Header().Set("Location", "/v2/upload-session-9?expires=1717017600")
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(r.URL.Path, "/upload-session-9") && r.Method == http.MethodPut:
			gotQuery = r.URL.Query()
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://")})
	payload := []byte("blob-payload")
	if err := o.pushBlob(context.Background(), "proj/chart", payload); err != nil {
		t.Fatalf("带 query 的 Location 上传应成功: %v", err)
	}
	if gotQuery.Get("expires") != "1717017600" {
		t.Errorf("Location 原有 query 参数应保留: %v", gotQuery)
	}
	if got := gotQuery.Get("digest"); got != digestOf(payload) {
		t.Errorf("digest 参数应正确追加(用 & 拼接): got %q, want %q", got, digestOf(payload))
	}
}

// TestDetectHarborLegacyPathFallback /api/v2.0/systeminfo 404 时回退旧版 /api/systeminfo 识别 Harbor。
func TestDetectHarborLegacyPathFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2.0/systeminfo":
			w.WriteHeader(http.StatusNotFound) // 旧版 Harbor 无 v2.0 API
		case "/api/systeminfo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"harbor_version":"1.10.0","with_chartmuseum":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	d := detectHarbor(context.Background(), Target{Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://")})
	if !d.IsHarbor || !d.WithChartMuseum {
		t.Fatalf("v2.0 404 应回退旧版 systeminfo 并识别为 Harbor: %+v", d)
	}
}

// TestPushContextCanceledOCI OCI 推送 + 已取消 ctx:快速返回取消错误
// (与现有 TestPushContextCanceled 的 ChartMuseum 场景互补)。
func TestPushContextCanceledOCI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Push(ctx, Target{Type: model.ChartRepoTypeOCI, Scheme: "http", Host: "127.0.0.1:1"},
		&Meta{Name: "c", Version: "1"}, []byte("d"), "")
	if err == nil {
		t.Fatal("已取消 ctx 的 OCI 推送应报错")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("应快速返回 context canceled: %v", err)
	}
}
