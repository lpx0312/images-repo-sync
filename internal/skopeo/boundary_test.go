package skopeo

// 本文件为 B 类边界测试(GREEN:当前实现必须通过,固化既有边界行为)。
// 不加 t.Parallel():SkopeoBin 是全局配置,串行执行。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
)

// TestParseSourceBoundaryRefs 覆盖 digest 引用、无 tag、host 尾斜杠、host 带端口等边界。
func TestParseSourceBoundaryRefs(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantHost string
		wantPath string
	}{
		// digest 引用:tag 为空,不得再补 :latest(tagPart 已含 ':' 于 sha256: 后)。
		{"digest 引用带命名空间", "a/b@sha256:abc", "docker.io", "a/b@sha256:abc"},
		{"digest 引用官方镜像补 library", "nginx@sha256:abc", "docker.io", "library/nginx@sha256:abc"},
		{"host + digest 引用", "quay.io/jetstack/cm@sha256:deadbeef", "quay.io", "jetstack/cm@sha256:deadbeef"},
		// 无 tag:统一补 :latest。
		{"无 host 无 tag", "busybox", "docker.io", "library/busybox:latest"},
		{"host 无 tag", "gcr.io/x/y", "gcr.io", "x/y:latest"},
		{"host 无 tag 深路径", "harbor.corp.net/proj/ns/img", "harbor.corp.net", "proj/ns/img:latest"},
		// host 带端口:端口中的 ':' 不影响 host 判定与 tag 补齐。
		{"host 带端口", "harbor.corp.net:5000/ns/img:v1", "harbor.corp.net:5000", "ns/img:v1"},
		{"host 带端口无 tag", "harbor.corp.net:5000/ns/img", "harbor.corp.net:5000", "ns/img:latest"},
		// host 带尾斜杠(host 后出现 '//'):当前实现保留 path 的前导斜杠(特征固化)。
		{"host 尾斜杠", "registry.example.com//ns/img:1", "registry.example.com", "/ns/img:1"},
		// 空输入。
		{"空串", "", "", ""},
		{"纯空白", "   ", "", ""},
	}
	for _, c := range cases {
		gotHost, gotPath := ParseSource(c.in)
		if gotHost != c.wantHost || gotPath != c.wantPath {
			t.Errorf("%s: ParseSource(%q) = (%q,%q), want (%q,%q)",
				c.name, c.in, gotHost, gotPath, c.wantHost, c.wantPath)
		}
	}
}

// TestResolveTargetHostSchemeAndPort 验证 targetHost 带 scheme + 端口时,
// StripScheme 剥掉 http(s):// 与尾斜杠后按裸 host:port 拼目标引用。
func TestResolveTargetHostSchemeAndPort(t *testing.T) {
	const src = "gcr.io/k8s-staging/cluster-api/clusterctl:v1"
	cases := []struct {
		name string
		host string
		mode string
		want string
	}{
		{"flat http+端口", "http://h:5000", model.ModeFlat, "h:5000/mirror/clusterctl:v1"},
		{"preserve_path https+端口+尾斜杠", "https://h:5000/", model.ModePreservePath, "h:5000/mirror/k8s-staging/cluster-api/clusterctl:v1"},
		{"replace_host http+端口", "http://h:5000", model.ModeReplaceHost, "h:5000/k8s-staging/cluster-api/clusterctl:v1"},
		{"裸 host+端口", "h:5000", model.ModeFlat, "h:5000/mirror/clusterctl:v1"},
	}
	for _, c := range cases {
		got, err := ResolveTarget(src, c.host, c.mode, "mirror")
		if err != nil {
			t.Fatalf("%s: err=%v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got=%q want=%q", c.name, got, c.want)
		}
	}
}

// TestResolveTargetFlatWithDigestSource 验证 ModeFlat 叠加 digest 引用源:
// 取最后一个 '/' 后的「name@digest」整体作为镜像名,不补 tag。
func TestResolveTargetFlatWithDigestSource(t *testing.T) {
	got, err := ResolveTarget("a/b@sha256:abc", "harbor.example.com", model.ModeFlat, "mirror")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if want := "harbor.example.com/mirror/b@sha256:abc"; got != want {
		t.Errorf("flat + digest 源: got=%q want=%q", got, want)
	}

	// preserve_path 模式保持完整源 path(含 digest)。
	got, err = ResolveTarget("a/b@sha256:abc", "harbor.example.com", model.ModePreservePath, "mirror")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if want := "harbor.example.com/mirror/a/b@sha256:abc"; got != want {
		t.Errorf("preserve_path + digest 源: got=%q want=%q", got, want)
	}
}

// TestListTagsErrorKeywords 校验 fake 关键字驱动的两个错误分支。
func TestListTagsErrorKeywords(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// failtags:错误信息应携带 stderr 中的 unauthorized。
	_, err := ListTags(ctx, "docker.io/x/failtags", "registry.example.com", "u", "p", false)
	if err == nil {
		t.Fatal("failtags 应返回错误")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("failtags 错误应含 unauthorized, got %q", err.Error())
	}

	// badjson:stdout 非法 JSON 应返回解析错误(而非 panic / 空成功)。
	_, err = ListTags(ctx, "docker.io/x/badjson", "registry.example.com", "u", "p", false)
	if err == nil {
		t.Fatal("badjson 应返回错误")
	}
	if !strings.Contains(err.Error(), "解析 list-tags 输出失败") {
		t.Errorf("badjson 应为解析错误, got %q", err.Error())
	}
}

// TestListTagsCanceledContext 验证 ctx 已取消时 ListTags 直接返回 ctx 错误。
func TestListTagsCanceledContext(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 先取消再调用

	_, err := ListTags(ctx, "docker.io/x/any", "registry.example.com", "u", "p", false)
	if err == nil {
		t.Fatal("已取消 ctx 的 ListTags 应返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("应为 context.Canceled, got %v", err)
	}
}

// TestCaptureStdoutTruncatesLongStderr 覆盖 captureStdout 的 stderr 截断分支
// (stderr 超 200 字节时截断加 "...")。
// 构造方式:向 fake 传一个超长未知子命令名,fake 会在 stderr 原样回显
// 「不支持的命令 "<300 字节>"」并退出 1,天然得到 >200 字节的 stderr。
// 注:list-tags/copy 的既有关键字都无法产出「超长 stderr + 非零退出」的组合,
// 故借此通用报错路径覆盖该分支。
func TestCaptureStdoutTruncatesLongStderr(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := captureStdout(ctx, []string{strings.Repeat("z", 300)})
	if err == nil {
		t.Fatal("未知子命令应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "不支持的命令") {
		t.Errorf("错误应包含 fake 回显的 stderr, got %q", msg)
	}
	if !strings.HasSuffix(msg, "...") {
		t.Errorf("超长 stderr 应被截断并以 ... 结尾, got %q", msg)
	}
	// 错误文本 = 退出错误前缀 + 截断后 200 字节 + "...",不应携带全部 300 字节。
	if strings.Count(msg, "z") > 210 {
		t.Errorf("stderr 未按 200 字节截断,错误信息含 %d 个 z", strings.Count(msg, "z"))
	}
}

// TestBinVersionFakeExactString fake 下 BinVersion 返回完整版本行
// (runWithStream 单行 stdout 回调的取值语义)。
func TestBinVersionFakeExactString(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if v := BinVersion(ctx); v != "skopeo version 1.14.0-fake" {
		t.Errorf("BinVersion = %q, want %q", v, "skopeo version 1.14.0-fake")
	}
}
