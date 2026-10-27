package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"images-repo-sync/internal/testutil"
)

// ---------- A 类回归(缺陷已修复,应保持绿色) ----------

// 回归锁定(已修复): A1 Chart.yaml 超过 1MB(maxChartYAML)时应明确报「过大/超限」,
// 而不是被 io.LimitReader 静默截断后误报「解析 Chart.yaml 失败」。
// 构造恰好 1MB+1 字节的合法 Chart.yaml:截断后引号未闭合必然 YAML 解析失败,
// 以此区分「截断导致的误报」与「真正的解析错误」。
func TestParseChartYAMLOversizeExplicitError(t *testing.T) {
	prefix := "apiVersion: v2\nname: big\nversion: 1.0.0\ndescription: \""
	suffix := "\""
	pad := (1<<20 + 1) - len(prefix) - len(suffix)
	if pad <= 0 {
		t.Fatal("padding 长度计算错误")
	}
	body := prefix + strings.Repeat("A", pad) + suffix
	if len(body) != 1<<20+1 {
		t.Fatalf("Chart.yaml 应恰为 1MB+1 字节, got %d", len(body))
	}
	data := buildRawTgz(t, []tgzEntry{{name: "big/Chart.yaml", body: body}})

	m, err := ParseBytes(data)
	if err == nil {
		t.Fatalf("A1: 超过 1MB 的 Chart.yaml 应报错,却解析成功: %+v", m)
	}
	msg := err.Error()
	if !strings.Contains(msg, "过大") && !strings.Contains(msg, "超限") {
		t.Errorf("A1: 错误应明确提示文件过大/超限, got: %v", err)
	}
	if strings.Contains(msg, "解析 Chart.yaml 失败") {
		t.Errorf("A1: 不应把截断误报成解析失败: %v", err)
	}
}

// GREEN: 恰好 1MB(未超限)的 Chart.yaml 应正常解析(限制语义:超出才拒绝)。
func TestParseChartYAMLExactlyAtLimit(t *testing.T) {
	prefix := "apiVersion: v2\nname: limit\nversion: 1.0.0\ndescription: \""
	suffix := "\""
	pad := 1<<20 - len(prefix) - len(suffix)
	body := prefix + strings.Repeat("A", pad) + suffix
	if len(body) != 1<<20 {
		t.Fatalf("应恰为 1MB, got %d", len(body))
	}
	data := buildRawTgz(t, []tgzEntry{{name: "limit/Chart.yaml", body: body}})
	m, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("恰 1MB 未超限,应解析成功: %v", err)
	}
	if m.Name != "limit" || m.Version != "1.0.0" {
		t.Errorf("解析结果不对: %+v", m)
	}
}

// 回归锁定(已修复): A2 Chart name 含路径分隔符(/、..)时 ParseBytes/ParseFile 应拒绝,
// 否则 name 会拼进 OCI 仓库路径(pushOCI 的 repo = joinURL(project, name))造成路径注入。
func TestParseRejectsPathInjectionName(t *testing.T) {
	for _, name := range []string{"a/b", "../evil", ".."} {
		data := testutil.BuildChartTgz(t, name, "1.0.0", "pkg")
		m, err := ParseBytes(data)
		if err == nil {
			t.Errorf("A2: name=%q 应被拒绝,却解析成功: %+v", name, m)
			continue
		}
		if m != nil {
			t.Errorf("A2: name=%q 报错时 meta 应为 nil, got %+v", name, m)
		}
	}
	// 合法 name 对照:不应误伤。
	m, err := ParseBytes(testutil.BuildChartTgz(t, "my-chart_1", "1.0.0", "pkg"))
	if err != nil || m == nil || m.Name != "my-chart_1" {
		t.Fatalf("合法 name 不应被拒: %+v %v", m, err)
	}

	// ParseFile 完整入口(读盘)同样应拒绝。
	dir := t.TempDir()
	p := filepath.Join(dir, "evil.tgz")
	if err := os.WriteFile(p, testutil.BuildChartTgz(t, "../evil", "1.0.0", "pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFile(p); err == nil {
		t.Error("A2: ParseFile 对含 .. 的 name 应报错")
	}
}

// ---------- B 类边界(GREEN) ----------

// TestParseBytesRootLevelChartYAML 根级裸 Chart.yaml(无子目录)与 ./ 前缀的 tgz 均可解析。
func TestParseBytesRootLevelChartYAML(t *testing.T) {
	data := testutil.BuildChartTgz(t, "rootchart", "0.2.0", "") // topDir 为空 → 根级
	m, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("根级 Chart.yaml 应可解析: %v", err)
	}
	if m.Name != "rootchart" || m.Version != "0.2.0" {
		t.Errorf("解析结果不对: %+v", m)
	}

	// "./Chart.yaml" 前缀:解析器应剥离 ./ 后识别。
	dotted := buildRawTgz(t, []tgzEntry{
		{name: "./Chart.yaml", body: "apiVersion: v2\nname: dotted\nversion: 2.0.0\n"},
	})
	m, err = ParseBytes(dotted)
	if err != nil {
		t.Fatalf("./ 前缀应可解析: %v", err)
	}
	if m.Name != "dotted" || m.Version != "2.0.0" {
		t.Errorf("解析结果不对: %+v", m)
	}
}

// TestParseMetaFullFields 正常包的 name/version/appVersion 等字段提取。
func TestParseMetaFullFields(t *testing.T) {
	data := buildRawTgz(t, []tgzEntry{
		{name: "full/Chart.yaml", body: "apiVersion: v2\nname: fullchart\nversion: 1.2.3\nappVersion: 4.5.6\ndescription: a full chart\ntype: application\n"},
	})
	m, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.APIVersion != "v2" || m.Name != "fullchart" || m.Version != "1.2.3" ||
		m.AppVersion != "4.5.6" || m.Description != "a full chart" || m.Type != "application" {
		t.Errorf("字段提取不全: %+v", m)
	}
}

// ---------- helpers ----------

// tgzEntry 是手工构造 tgz 时的单个 tar 条目。
type tgzEntry struct {
	name string
	body string
}

// buildRawTgz 手工构造 gzip+tar 字节流(与 testutil.BuildChartTgz 互补,可精确控制条目名与内容)。
func buildRawTgz(tb testing.TB, entries []tgzEntry) []byte {
	tb.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))}); err != nil {
			tb.Fatalf("写 tar 头失败: %v", err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			tb.Fatalf("写 tar 内容失败: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		tb.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buf.Bytes()
}
