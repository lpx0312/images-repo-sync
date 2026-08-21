package chart

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// TestParseBytes_ValidChart 用最小可用的 chart tgz 验证解析。
// tgz 结构与 helm package 产物一致:顶层目录 + Chart.yaml。
func TestParseBytes_ValidChart(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "demo-0.1.0.tgz")
	if err := buildTestChart(tgz, "demo", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	m, size, err := ParseFile(tgz)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo" || m.Version != "0.1.0" {
		t.Fatalf("解析结果不对: %+v", m)
	}
	if size == 0 {
		t.Fatal("文件大小不应为 0")
	}
}

// TestParseBytes_BadVersion 校验非法 version(不能作为 OCI tag)被拒绝。
func TestParseBytes_BadVersion(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "bad.tgz")
	if err := buildTestChart(tgz, "demo", "1.0.0+meta"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFile(tgz); err == nil {
		t.Fatal("非法 version 应报错")
	}
}

// TestParseBytes_NotChart 校验无 Chart.yaml 的 tgz 被拒绝。
func TestParseBytes_NotChart(t *testing.T) {
	if _, err := ParseBytes([]byte("not a tgz at all")); err == nil {
		t.Fatal("非 tgz 内容应报错")
	}
}

// TestSplitHost 校验 host 的 scheme 解析规则。
func TestSplitHost(t *testing.T) {
	cases := []struct {
		in            string
		scheme, host  string
	}{
		{"dockerhub.kubekey.local", "https", "dockerhub.kubekey.local"},
		{"https://a.b.com", "https", "a.b.com"},
		{"http://a.b.com/", "http", "a.b.com"},
		{" a.b.com/ ", "https", "a.b.com"},
	}
	for _, c := range cases {
		s, h := SplitHost(c.in)
		if s != c.scheme || h != c.host {
			t.Fatalf("SplitHost(%q) = %q,%q; want %q,%q", c.in, s, h, c.scheme, c.host)
		}
	}
}

// TestTargetRef 校验推送目标的展示地址。
func TestTargetRef(t *testing.T) {
	oci := Target{Type: "oci", Scheme: "https", Host: "h.io", Project: "p1"}
	if got := oci.Ref("nginx", "0.1.0"); got != "oci://h.io/p1/nginx:0.1.0" {
		t.Fatalf("OCI ref = %q", got)
	}
	cm := Target{Type: "chartmuseum", Scheme: "http", Host: "cm.io/pre"}
	if got := cm.Ref("nginx", "0.1.0"); got != "http://cm.io/pre/api/charts" {
		t.Fatalf("CM ref = %q", got)
	}
}

// buildTestChart 构造一个最小 chart tgz 用于测试。
func buildTestChart(path, name, version string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeTestChart(f, name, version)
}

// writeTestChart 向 w 写入「顶层目录/Chart.yaml」结构的最小 chart tgz。
func writeTestChart(f *os.File, name, version string) error {
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	body := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\ndescription: test\n"
	hdr := &tar.Header{
		Name: name + "/Chart.yaml",
		Mode: 0o644,
		Size: int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		return err
	}
	return nil
}
