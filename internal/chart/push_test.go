package chart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

func TestJoinURL(t *testing.T) {
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{"h.io", "proj", "chart"}, "h.io/proj/chart"},
		{[]string{"h.io", "", "chart"}, "h.io/chart"},
		{[]string{"/h.io/", "/proj/"}, "h.io/proj"},
		{[]string{}, ""},
	}
	for _, c := range cases {
		if got := joinURL(c.parts...); got != c.want {
			t.Errorf("joinURL(%v) = %q, want %q", c.parts, got, c.want)
		}
	}
}

func TestDigestOf(t *testing.T) {
	d := digestOf([]byte("hello"))
	if !strings.HasPrefix(d, "sha256:") || len(d) != 7+64 {
		t.Fatalf("digest 格式不对: %q", d)
	}
	// 与已知 sha256 一致。
	if digestOf([]byte("hello")) != "sha256:"+fmt.Sprintf("%x", sha256Sum([]byte("hello"))) {
		t.Error("digest 计算不稳定")
	}
	if digestOf(nil) == digestOf([]byte("x")) {
		t.Error("不同输入 digest 应不同")
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, service := parseBearerChallenge(`Bearer realm="https://a/token",service="svc",other="x"`)
	if realm != "https://a/token" || service != "svc" {
		t.Errorf("parse = (%q,%q)", realm, service)
	}
	// 缺 realm 时为空。
	realm, _ = parseBearerChallenge(`Bearer service="only"`)
	if realm != "" {
		t.Errorf("缺 realm 应为空: %q", realm)
	}
}

func TestResolveLocation(t *testing.T) {
	if got := resolveLocation("https://r.io", "https://cdn.example.com/upload?sig=1"); got != "https://cdn.example.com/upload?sig=1" {
		t.Errorf("绝对地址应原样: %q", got)
	}
	if got := resolveLocation("https://r.io", "/v2/x/blobs/uploads/abc"); got != "https://r.io/v2/x/blobs/uploads/abc" {
		t.Errorf("相对路径应拼 base: %q", got)
	}
	if got := resolveLocation("https://r.io", "v2/rel"); got != "https://r.io/v2/rel" {
		t.Errorf("无斜杠相对路径应补 /: %q", got)
	}
}

func TestSameHost(t *testing.T) {
	if !sameHost("https://a.io:1/x", "https://a.io:1/y") {
		t.Error("同 host+port 应相同")
	}
	if sameHost("https://a.io", "https://b.io") {
		t.Error("不同 host 不应相同")
	}
	if sameHost("://bad", "https://a.io") {
		t.Error("解析失败应 false")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("短文本原样: %q", got)
	}
	if got := truncate("abcdef", 5); got != "abcde..." {
		t.Errorf("截断结果: %q", got)
	}
}

func TestReadErrBodyPush(t *testing.T) {
	mkResp := func(body string, code int) *http.Response {
		w := httptest.NewRecorder()
		w.WriteString(body)
		w.WriteHeader(code)
		return w.Result()
	}
	if got := readErrBody(mkResp(`{"errors":[{"message":"denied"}]}`, 403)); got != "denied" {
		t.Errorf("OCI errors 提取: %q", got)
	}
	if got := readErrBody(mkResp(`{"message":"plain"}`, 400)); got != "plain" {
		t.Errorf("单 message 提取: %q", got)
	}
	if got := readErrBody(mkResp(strings.Repeat("z", 300), 500)); len(got) != 203 {
		t.Errorf("超长应截断: %d", len(got))
	}
	if got := readErrBody(mkResp("", 500)); got != "" {
		t.Errorf("空 body: %q", got)
	}
}

func TestClassifyNetErrPush(t *testing.T) {
	errs := map[string]string{
		"i/o timeout":        "连接超时",
		"deadline exceeded":  "连接超时",
		"no such host":       "无法解析",
		"connection refused": "连接被拒绝",
		"x509: certificate":  "TLS 证书",
		"some other failure": "some other failure",
	}
	for raw, want := range errs {
		got := classifyNetErr(fmt.Errorf(raw)).Error()
		if !strings.Contains(got, want) {
			t.Errorf("classifyNetErr(%q) = %q, want 含 %q", raw, got, want)
		}
	}
}

// ---------- ChartMuseum 推送 ----------

func TestPushChartMuseumStandard(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotFormFile []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/charts" {
			// 非 Harbor:systeminfo 404。
			if strings.Contains(r.URL.Path, "systeminfo") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		file, _, err := r.FormFile("chart")
		if err == nil {
			gotFormFile, _ = io.ReadAll(file)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	m := &Meta{Name: "mychart", Version: "0.1.0"}
	data := testutil.BuildChartTgz(t, "mychart", "0.1.0", "mychart")
	res, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeChartMuseum, Scheme: "http", Host: host,
		Username: "u", Password: "p",
	}, m, data, "")
	if err != nil {
		t.Fatalf("推送失败: %v", err)
	}
	if gotPath != "/api/charts" {
		t.Errorf("应打标准 /api/charts: %q", gotPath)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !bytes.Equal(gotFormFile, data) {
		t.Error("上传的 chart 内容不一致")
	}
	if res.Message == "" {
		t.Error("应返回成功消息")
	}
}

func TestPushChartMuseumStatuses(t *testing.T) {
	cases := []struct {
		code    int
		wantErr string
	}{
		{http.StatusConflict, "不允许覆盖"},
		{http.StatusUnauthorized, "认证失败"},
		{http.StatusForbidden, "认证失败"},
		{http.StatusInternalServerError, "状态码 500"},
	}
	for _, c := range cases {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		m := &Meta{Name: "c", Version: "1.0.0"}
		_, err := Push(context.Background(), Target{
			Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
			Host: strings.TrimPrefix(ts.URL, "http://"),
		}, m, []byte("data"), "")
		ts.Close()
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("状态 %d: 期望含 %q, got %v", c.code, c.wantErr, err)
		}
	}
}

// harborSysteminfo 构造 Harbor 探测成功的响应。
func harborSysteminfo(w http.ResponseWriter, withCM bool) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"harbor_version":"2.9.0","with_chartmuseum":%v}`, withCM)
}

func TestPushChartMuseumHarbor(t *testing.T) {
	var pushed int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2.0/systeminfo":
			harborSysteminfo(w, true)
		case strings.HasPrefix(r.URL.Path, "/api/chartrepo/proj/charts"):
			pushed++
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	m := &Meta{Name: "h", Version: "0.2.0"}
	if _, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeChartMuseum, Scheme: "http", Host: host, Project: "proj",
	}, m, []byte("d"), ""); err != nil {
		t.Fatalf("Harbor ChartMuseum 推送失败: %v", err)
	}
	if pushed != 1 {
		t.Fatalf("应打 Harbor chartrepo 端点, pushed=%d", pushed)
	}

	// Harbor 但未填项目。
	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeChartMuseum, Scheme: "http", Host: host, Project: "",
	}, m, []byte("d"), "")
	if err == nil || !strings.Contains(err.Error(), "必须填写 Chart 项目") {
		t.Errorf("未填项目应报错: %v", err)
	}
}

func TestPushChartMuseumHarborNoCM(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2.0/systeminfo" {
			harborSysteminfo(w, false) // 未启用 ChartMuseum
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://"), Project: "p",
	}, &Meta{Name: "x", Version: "1"}, []byte("d"), "")
	if err == nil || !strings.Contains(err.Error(), "未启用 ChartMuseum") {
		t.Fatalf("未启用 CM 应报错: %v", err)
	}
}

// ---------- OCI 推送 ----------

// newOCIRegistry 模拟完整 OCI registry(basic 认证 + blob 上传 + manifest)。
func newOCIRegistry(t *testing.T, manifestStatus int, crossHostUpload bool) (*httptest.Server, *registryState) {
	t.Helper()
	st := &registryState{manifestStatus: manifestStatus}
	mux := http.NewServeMux()

	authMw := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate",
					fmt.Sprintf(`Bearer realm="http://%s/token",service="reg"`, r.Host))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "user" && p == "pass" {
			jsonToken(w, "oci-token")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})

	mux.HandleFunc("/v2/", authMw(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/blobs/uploads/") && r.Method == http.MethodPost {
			st.blobInits++
			w.Header().Set("Location", "/v2/upload-session-1")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if strings.Contains(path, "/upload-session-1") && r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			st.uploadedBlobs = append(st.uploadedBlobs, body)
			w.WriteHeader(http.StatusCreated)
			return
		}
		if strings.Contains(path, "/manifests/") && r.Method == http.MethodPut {
			st.manifests++
			body, _ := io.ReadAll(r.Body)
			st.lastManifest = body
			if st.manifestStatus != 0 && st.manifestStatus != http.StatusCreated {
				w.WriteHeader(st.manifestStatus)
				fmt.Fprint(w, `{"errors":[{"message":"manifest rejected"}]}`)
				return
			}
			w.Header().Set("Docker-Content-Digest", digestOf(body))
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	st.base = ts.URL
	return ts, st
}

type registryState struct {
	base           string
	blobInits      int
	uploadedBlobs  [][]byte
	manifests      int
	lastManifest   []byte
	manifestStatus int
}

func jsonToken(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"token":%q}`, token)
}

func TestPushOCIFullFlow(t *testing.T) {
	_, st := newOCIRegistry(t, 0, false)
	host := strings.TrimPrefix(st.base, "http://")

	m := &Meta{Name: "mychart", Version: "1.2.3"}
	data := testutil.BuildChartTgz(t, "mychart", "1.2.3", "mychart")
	res, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeOCI, Scheme: "http", Host: host, Project: "charts",
		Username: "user", Password: "pass",
	}, m, data, "")
	if err != nil {
		t.Fatalf("OCI 推送失败: %v", err)
	}
	if st.blobInits != 2 || len(st.uploadedBlobs) != 2 {
		t.Fatalf("应有 2 个 blob(config+chart), inits=%d blobs=%d", st.blobInits, len(st.uploadedBlobs))
	}
	if st.manifests != 1 {
		t.Fatalf("应推送 1 个 manifest, got %d", st.manifests)
	}
	if !strings.HasPrefix(res.Digest, "sha256:") {
		t.Errorf("digest = %q", res.Digest)
	}
	// manifest 结构校验。
	var mf struct {
		SchemaVersion int          `json:"schemaVersion"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	if err := json.Unmarshal(st.lastManifest, &mf); err != nil {
		t.Fatalf("manifest 非法: %v", err)
	}
	if mf.SchemaVersion != 2 || mf.Config.MediaType != mediaTypeConfig ||
		mf.Layers[0].MediaType != mediaTypeChart || mf.Layers[0].Digest != digestOf(data) {
		t.Errorf("manifest 结构不对: %s", st.lastManifest)
	}
}

func TestPushOCIBadCredentials(t *testing.T) {
	_, st := newOCIRegistry(t, 0, false)
	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host:     strings.TrimPrefix(st.base, "http://"),
		Username: "user", Password: "wrong",
	}, &Meta{Name: "c", Version: "1"}, []byte("d"), "")
	if err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("错误凭证应报认证失败: %v", err)
	}
}

func TestPushOCINoCredsForAuthRequired(t *testing.T) {
	_, st := newOCIRegistry(t, 0, false)
	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(st.base, "http://"),
	}, &Meta{Name: "c", Version: "1"}, []byte("d"), "")
	if err == nil || !strings.Contains(err.Error(), "填写账号密码") {
		t.Fatalf("需要认证但无凭证应提示填写: %v", err)
	}
}

func TestPushOCIManifestRejected(t *testing.T) {
	_, st := newOCIRegistry(t, http.StatusForbidden, false)
	host := strings.TrimPrefix(st.base, "http://")
	_, err := Push(context.Background(), Target{
		Type: model.ChartRepoTypeOCI, Scheme: "http", Host: host,
		Username: "user", Password: "pass",
	}, &Meta{Name: "c", Version: "1"}, []byte("d"), "")
	if err == nil || !strings.Contains(err.Error(), "manifest rejected") {
		t.Fatalf("manifest 被拒应透出错误: %v", err)
	}
}

// ---------- 连通性探测 ----------

func TestProbeOCISuccessAndFailures(t *testing.T) {
	// 匿名 200。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	res := Probe(context.Background(), Target{Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://")})
	if !res.OK {
		t.Fatalf("匿名 OCI 应成功: %+v", res)
	}
	ts.Close()

	// 401 → 无凭证提示填写。
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(ts2.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "账号密码") {
		t.Fatalf("401 无凭证应提示: %+v", res)
	}
	ts2.Close()

	// 404 → 非 OCI 仓库。
	ts3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(ts3.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "不是有效的 OCI") {
		t.Fatalf("404 应报地址错误: %+v", res)
	}
	ts3.Close()

	// 500 → 异常状态码。
	ts4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeOCI, Scheme: "http",
		Host: strings.TrimPrefix(ts4.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "异常状态码") {
		t.Fatalf("500 应报异常: %+v", res)
	}
	ts4.Close()

	// 网络错误 → 错误消息透出。
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeOCI, Scheme: "http", Host: "127.0.0.1:1"})
	if res.OK {
		t.Fatal("不可达应失败")
	}
}

func TestProbeChartMuseumStandard(t *testing.T) {
	// /health 200。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://")})
	if !res.OK || res.Endpoint == "" {
		t.Fatalf("/health 应成功: %+v", res)
	}
	ts.Close()

	// /health 404 → 回退 /api/health 200。
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts2.URL, "http://")})
	if !res.OK || !strings.Contains(res.Endpoint, "/api/health") {
		t.Fatalf("旧版回退应成功: %+v", res)
	}
	ts2.Close()

	// 两级都 401 → 认证失败。
	ts3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts3.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "认证失败") {
		t.Fatalf("401 应报认证失败: %+v", res)
	}
	ts3.Close()

	// 都 404 → 未找到端点。
	ts4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts4.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "未找到 /health") {
		t.Fatalf("全 404 应报未找到: %+v", res)
	}
	ts4.Close()
}

func TestProbeChartMuseumHarbor(t *testing.T) {
	// Harbor 未启用 CM。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "systeminfo") {
			harborSysteminfo(w, false)
			return
		}
		w.WriteHeader(404)
	}))
	res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "未启用 ChartMuseum") {
		t.Fatalf("未启用 CM 应报错: %+v", res)
	}
	ts.Close()

	// Harbor 启用 CM,未填项目:可达即可。
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "systeminfo") {
			harborSysteminfo(w, true)
			return
		}
		w.WriteHeader(404)
	}))
	res = Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts2.URL, "http://")})
	if !res.OK || !strings.Contains(res.Message, "Harbor ChartMuseum") {
		t.Fatalf("未填项目应可达成功: %+v", res)
	}
	ts2.Close()

	// Harbor + 项目:charts 端点 200 / 404 / 401。
	mk := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "systeminfo"):
				harborSysteminfo(w, true)
			case strings.Contains(r.URL.Path, "/api/chartrepo/"):
				w.WriteHeader(status)
			default:
				w.WriteHeader(404)
			}
		}))
	}
	if ts := mk(200); true {
		res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
			Host: strings.TrimPrefix(ts.URL, "http://"), Project: "p"})
		if !res.OK {
			t.Fatalf("Harbor charts 200 应成功: %+v", res)
		}
		ts.Close()
	}
	if ts := mk(404); true {
		res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
			Host: strings.TrimPrefix(ts.URL, "http://"), Project: "p"})
		if res.OK || !strings.Contains(res.Message, "不存在") {
			t.Fatalf("项目 404 应报不存在: %+v", res)
		}
		ts.Close()
	}
	if ts := mk(401); true {
		res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
			Host: strings.TrimPrefix(ts.URL, "http://"), Project: "p"})
		if res.OK || !strings.Contains(res.Message, "认证失败") {
			t.Fatalf("401 应报认证失败: %+v", res)
		}
		ts.Close()
	}
}

// TestDetectHarborNonJSON200 systeminfo 200 但无 harbor_version 字段 → 视为非 Harbor。
func TestDetectHarborNonJSON200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"other":"data"}`)
	}))
	defer ts.Close()
	d := detectHarbor(context.Background(), Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://")})
	if d.IsHarbor {
		t.Fatal("无 harbor_version 不应判定为 Harbor")
	}
}

// TestOCIClientBasicChallenge Basic challenge / 无 challenge 的 authenticate 分支。
func TestOCIClientBasicChallenge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			jsonToken(w, "t")
			return
		}
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(auth, "Basic ") {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(401)
	}))
	defer ts.Close()

	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://"), Username: "u", Password: "p"})
	resp, err := o.do(context.Background(), http.MethodGet, ts.URL+"/v2/", nil, "", "")
	if err != nil {
		t.Fatalf("Basic challenge 重试失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("状态 = %d", resp.StatusCode)
	}

	// 已带 authHeader 再遇 401 → 凭证无效错误。
	o2 := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://"), Username: "u", Password: "p"})
	o2.authHeader = "Bearer stale"
	if _, err := o2.do(context.Background(), http.MethodGet, ts.URL+"/v2/", nil, "", ""); err == nil ||
		!strings.Contains(err.Error(), "凭证无效") {
		t.Fatalf("已认证再 401 应报凭证无效: %v", err)
	}
}

// TestOCIClientBearerErrors Bearer 流程的错误分支。
func TestOCIClientBearerErrors(t *testing.T) {
	mk := func(tokenStatus int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				w.WriteHeader(tokenStatus)
				fmt.Fprint(w, body)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="s"`)
			w.WriteHeader(401)
		}))
	}

	// token 401。
	ts := mk(401, "")
	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://"), Username: "u", Password: "p"})
	if err := o.authenticate(context.Background(),
		`Bearer realm="`+ts.URL+`/token",service="s"`, "repo"); err == nil ||
		!strings.Contains(err.Error(), "用户名或密码错误") {
		t.Fatalf("token 401 应报凭证错误: %v", err)
	}
	ts.Close()

	// token 500。
	ts = mk(500, "oops")
	o = newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://"), Username: "u", Password: "p"})
	if err := o.authenticate(context.Background(),
		`Bearer realm="`+ts.URL+`/token",service="s"`, ""); err == nil ||
		!strings.Contains(err.Error(), "获取 token 失败") {
		t.Fatalf("token 500 应报获取失败: %v", err)
	}
	ts.Close()

	// token 响应非 JSON。
	ts = mk(200, "not-json")
	o = newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://"), Username: "u", Password: "p"})
	if err := o.authenticate(context.Background(),
		`Bearer realm="`+ts.URL+`/token",service="s"`, ""); err == nil ||
		!strings.Contains(err.Error(), "解析 token 响应失败") {
		t.Fatalf("非 JSON 应报解析失败: %v", err)
	}
	ts.Close()

	// challenge 缺 realm。
	o = newOCIClient(Target{Scheme: "http", Host: "h", Username: "u", Password: "p"})
	if err := o.authenticate(context.Background(), `Bearer service="s"`, ""); err == nil ||
		!strings.Contains(err.Error(), "无法解析仓库认证信息") {
		t.Fatalf("缺 realm 应报解析失败: %v", err)
	}

	// 无凭证 + Basic challenge。
	o = newOCIClient(Target{Scheme: "http", Host: "h"})
	if err := o.authenticate(context.Background(), `Basic realm="x"`, ""); err == nil ||
		!strings.Contains(err.Error(), "填写账号密码") {
		t.Fatalf("无凭证应提示填写: %v", err)
	}
}

// TestOCIClientCrossHostUploadBlob 跨主机 Location(预签名 URL)不应携带仓库凭证。
func TestOCIClientCrossHostUploadBlob(t *testing.T) {
	var uploadAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 一律要求 Bearer(简化:直接发 token)。
		if r.URL.Path == "/token" {
			jsonToken(w, "t")
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="s"`)
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/blobs/uploads/") {
			// 返回跨主机绝对地址。
			w.Header().Set("Location", crossHostSrv.URL+"/upload")
			w.WriteHeader(202)
			return
		}
		w.WriteHeader(404)
	}))
	defer target.Close()

	crossHostSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadAuth = r.Header.Get("Authorization")
		w.WriteHeader(201)
	}))
	defer crossHostSrv.Close()

	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(target.URL, "http://"), Username: "u", Password: "p"})
	if err := o.pushBlob(context.Background(), "proj/chart", []byte("data")); err != nil {
		t.Fatalf("跨主机上传失败: %v", err)
	}
	if uploadAuth != "" {
		t.Errorf("跨主机 PUT 不应携带 Authorization, got %q", uploadAuth)
	}
}

var crossHostSrv *httptest.Server

// TestPushBlobNoLocation201 201 无 Location(已存在)视为成功;202 无 Location 报错。
func TestPushBlobNoLocation(t *testing.T) {
	mk := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status) // 无 Location 头
		}))
	}
	ts := mk(201)
	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://")})
	if err := o.pushBlob(context.Background(), "r", []byte("d")); err != nil {
		t.Fatalf("201 无 Location 应视为成功: %v", err)
	}
	ts.Close()

	ts2 := mk(202)
	o = newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts2.URL, "http://")})
	if err := o.pushBlob(context.Background(), "r", []byte("d")); err == nil ||
		!strings.Contains(err.Error(), "未返回上传 Location") {
		t.Fatalf("202 无 Location 应报错: %v", err)
	}
	ts2.Close()

	// 初始化失败(500 带 OCI 错误)。
	ts3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
	}))
	o = newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts3.URL, "http://")})
	if err := o.pushBlob(context.Background(), "r", []byte("d")); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("初始化 500 应报错: %v", err)
	}
	ts3.Close()
}

// sha256Sum 供 TestDigestOf 对拍标准库。
func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}

// TestPushContextCanceled ctx 已取消时报错。
func TestPushContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Push(ctx, Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http", Host: "127.0.0.1:1"},
		&Meta{Name: "c", Version: "1"}, []byte("d"), "")
	if err == nil {
		t.Fatal("已取消 ctx 应报错")
	}
}
