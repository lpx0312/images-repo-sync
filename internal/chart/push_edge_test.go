package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
)

func TestParseFileErrors(t *testing.T) {
	if _, _, err := ParseFile(filepath.Join(t.TempDir(), "missing.tgz")); err == nil {
		t.Error("不存在的文件应报错")
	}
	dir := t.TempDir()
	if _, _, err := ParseFile(dir); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Errorf("目录应报错: %v", err)
	}
	// 非法内容文件。
	p := filepath.Join(t.TempDir(), "bad.tgz")
	os.WriteFile(p, []byte("not gzip"), 0o644)
	if _, _, err := ParseFile(p); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("非 gzip 应报错: %v", err)
	}
}

func TestParseBytesEdgeCases(t *testing.T) {
	// 坏 tar:合法 gzip 但内容不是 tar。
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte("junk"))
	gz.Close()
	if _, err := ParseBytes(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "tar") {
		t.Errorf("坏 tar 应报错: %v", err)
	}

	// 无 Chart.yaml 的正常 tar。
	var buf2 bytes.Buffer
	gz2 := gzip.NewWriter(&buf2)
	tw := tar.NewWriter(gz2)
	content := "a: 1\n"
	tw.WriteHeader(&tar.Header{Name: "values.yaml", Mode: 0o644, Size: int64(len(content))})
	tw.Write([]byte(content))
	tw.Close()
	gz2.Close()
	if _, err := ParseBytes(buf2.Bytes()); err == nil || !strings.Contains(err.Error(), "未找到 Chart.yaml") {
		t.Errorf("缺 Chart.yaml 应报错: %v", err)
	}
}

func TestParseMetaEdgeCases(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"非法 YAML", "\t\ttab-indent", "解析 Chart.yaml 失败"},
		{"缺 name", "version: 1.0.0", "缺少 name"},
		{"缺 version", "name: c", "缺少 name"},
		{"version 非法 tag", "name: c\nversion: " + strings.Repeat("v", 200), "不是合法的 tag"},
		{"version 以点开头", "name: c\nversion: .1.0", "不是合法的 tag"},
	}
	for _, c := range cases {
		if _, err := parseMeta([]byte(c.body)); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 期望含 %q, got %v", c.name, c.wantErr, err)
		}
	}
	// 成功且 trim 空白。
	m, err := parseMeta([]byte("name:  spaced \nversion:  1.0.0 \n"))
	if err != nil || m.Name != "spaced" || m.Version != "1.0.0" {
		t.Fatalf("trim 失败: %+v %v", m, err)
	}
}

func TestRefChartMuseumWithProject(t *testing.T) {
	tg := Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "https", Host: "h.io", Project: "proj"}
	if got := tg.Ref("c", "1.0"); got != "https://h.io/api/chartrepo/proj/charts" {
		t.Errorf("Harbor CM ref = %q", got)
	}
	tg2 := Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http", Host: "h.io"}
	if got := tg2.Ref("c", "1.0"); got != "http://h.io/api/charts" {
		t.Errorf("标准 CM ref = %q", got)
	}
	// host 与 project 带多余斜杠。
	tg3 := Target{Type: model.ChartRepoTypeOCI, Scheme: "https", Host: "h.io/", Project: "/p/"}
	if got := tg3.Ref("chart", "0.1"); got != "oci://h.io/p/chart:0.1" {
		t.Errorf("斜杠清理 ref = %q", got)
	}
}

func TestNewOCIClientInsecure(t *testing.T) {
	// insecure=true 分支:克隆 transport 并跳过校验(对自签 TLS 服务可连通)。
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "https://")
	o := newOCIClient(Target{Scheme: "https", Host: host, Insecure: true})
	resp, err := o.attempt(context.Background(), http.MethodGet, ts.URL+"/v2/", nil, "")
	if err != nil {
		t.Fatalf("insecure 请求失败: %v", err)
	}
	resp.Body.Close()
}

func TestPushBlobPUTFailure(t *testing.T) {
	// PUT 上传阶段失败(500)。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/blobs/uploads/") {
			w.Header().Set("Location", "/up")
			w.WriteHeader(202)
			return
		}
		w.WriteHeader(500)
		fmt.Fprint(w, `{"errors":[{"message":"disk full"}]}`)
	}))
	defer ts.Close()
	o := newOCIClient(Target{Scheme: "http", Host: strings.TrimPrefix(ts.URL, "http://")})
	err := o.pushBlob(context.Background(), "r", []byte("d"))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("PUT 500 应报错: %v", err)
	}
}

func TestProbeChartMuseumAbnormalStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502) // /health 与 /api/health 都 502
	}))
	defer ts.Close()
	res := Probe(context.Background(), Target{Type: model.ChartRepoTypeChartMuseum, Scheme: "http",
		Host: strings.TrimPrefix(ts.URL, "http://")})
	if res.OK || !strings.Contains(res.Message, "异常状态码") {
		t.Fatalf("502 应报异常: %+v", res)
	}
}

func TestAuthedGetNetworkError(t *testing.T) {
	// 不可达地址:Do 返回错误(连接被拒绝)。
	_, err := authedGet(context.Background(), http.DefaultClient, "http://127.0.0.1:1/x", "")
	if err == nil {
		t.Fatal("不可达应报错")
	}
}

func TestAttemptInvalidURL(t *testing.T) {
	o := newOCIClient(Target{Scheme: "http", Host: "h"})
	if _, err := o.attempt(context.Background(), http.MethodGet, "://bad-url", nil, ""); err == nil {
		t.Fatal("非法 URL 应报错")
	}
}
