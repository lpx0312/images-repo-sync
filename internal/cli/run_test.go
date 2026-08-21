package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBytes 测试辅助:写临时文件。
func writeBytes(path string, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// TestParseGlobals 全局选项应支持 --key=value 与 --key value 两种形式。
func TestParseGlobals(t *testing.T) {
	app, rest, err := parseGlobals([]string{"--json", "registries", "list"})
	if err != nil || app.JSONOut != true || len(rest) != 2 {
		t.Fatalf("--json: %v %v", rest, err)
	}

	app, rest, err = parseGlobals([]string{"--server", "http://a:1", "--token=tok", "whoami"})
	if err != nil || app.Server != "http://a:1" || app.Token != "tok" || len(rest) != 1 || rest[0] != "whoami" {
		t.Fatalf("--server value/--token=value: %+v %v", app, rest)
	}

	if _, _, err := parseGlobals([]string{"--server"}); err == nil {
		t.Fatalf("--server 缺值应报错")
	}
}

// TestRunDispatch 命令分发与退出码。
func TestRunDispatch(t *testing.T) {
	if code := Run([]string{"no-such-command"}); code != 2 {
		t.Fatalf("未知命令应返回 2,实际 %d", code)
	}
	if code := Run([]string{"help"}); code != 0 {
		t.Fatalf("help 应返回 0,实际 %d", code)
	}
	if code := Run([]string{}); code != 0 {
		t.Fatalf("无参数(显示帮助)应返回 0,实际 %d", code)
	}
	// version 不依赖服务端。
	if code := Run([]string{"version"}); code != 0 {
		t.Fatalf("version 应返回 0,实际 %d", code)
	}
}

// TestParseIDArg ID 参数校验。
func TestParseIDArg(t *testing.T) {
	if _, err := parseIDArg("id", "12"); err != nil {
		t.Fatalf("合法 ID 报错: %v", err)
	}
	if _, err := parseIDArg("id", "abc"); err == nil {
		t.Fatalf("非数字应报错")
	}
	if _, err := parseIDArg("id", ""); err == nil {
		t.Fatalf("空串应报错")
	}
}

// TestExpandChartArgs 目录展开(一层 *.tgz、排序、忽略子目录与非 tgz)。
func TestExpandChartArgs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.tgz", "a.tgz", "c.txt", "d.TGZ"} {
		if err := writeBytes(filepath.Join(dir, name), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeBytes(filepath.Join(sub, "nested.tgz"), "x"); err != nil {
		t.Fatal(err)
	}

	got, err := expandChartArgs([]string{dir})
	if err != nil {
		t.Fatalf("expandChartArgs: %v", err)
	}
	want := []string{filepath.Join(dir, "a.tgz"), filepath.Join(dir, "b.tgz"), filepath.Join(dir, "d.TGZ")}
	if len(got) != len(want) {
		t.Fatalf("应展开 3 个 tgz(含大写后缀,不含子目录与 txt): %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("顺序/内容不符: got %v", got)
			break
		}
	}

	// 混合:文件 + 目录。
	got, err = expandChartArgs([]string{filepath.Join(dir, "c.txt"), dir})
	if err != nil || len(got) != 4 {
		t.Fatalf("文件应原样保留: %v %v", got, err)
	}

	// 不存在的路径。
	if _, err := expandChartArgs([]string{filepath.Join(dir, "none")}); err == nil {
		t.Fatalf("不存在路径应报错")
	}
}

// TestReadRefsFile refs 文件解析(# 注释与空行跳过)。
func TestReadRefsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refs.txt")
	if err := writeBytes(path, "# 注释\nnginx:1.25\n\nredis:7.0\n"); err != nil {
		t.Fatal(err)
	}
	refs, err := readRefsFile(path)
	if err != nil || len(refs) != 2 || refs[0] != "nginx:1.25" || refs[1] != "redis:7.0" {
		t.Fatalf("解析不符: %v %v", refs, err)
	}
}

// TestCollectRefs --refs 逗号分隔 + 位置参数汇总,去空白。
func TestCollectRefs(t *testing.T) {
	fs := newFlagSet("test", "")
	refsFlag := fs.String("refs", "", "")
	_ = fs.String("refs-file", "", "")
	if err := fs.Parse([]string{"--refs", " a:1,b:2 ,", "c:3"}); err != nil {
		t.Fatal(err)
	}
	got, err := collectRefs(fs, fs.Args())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a:1", "b:2", "c:3"}
	if len(got) != len(want) {
		t.Fatalf("collectRefs: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectRefs 顺序不符: %v", got)
		}
	}
	_ = refsFlag
}
