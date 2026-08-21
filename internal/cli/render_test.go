package cli

import (
	"bytes"
	"testing"
)

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	PrintTable(&buf, []string{"ID", "名称"}, [][]string{
		{"1", "nginx"},
		{"22", "redis"},
	})
	out := buf.String()
	want := "ID  名称\n1   nginx\n22  redis\n"
	if out != want {
		t.Fatalf("输出不符:\n got: %q\nwant: %q", out, want)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 4, "abc…"},
		{"中文长度测试", 3, "中文…"},
		{"abc", 0, "abc"},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.n); got != c.want {
			t.Errorf("Truncate(%q,%d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestFmtTime(t *testing.T) {
	// RFC3339 带纳秒(服务端 time.Time JSON 序列化格式)应能解析。
	got := FmtTime("2026-08-22T00:51:19.820311619+08:00")
	if got == "2026-08-22T00:51:19.820311619+08:00" {
		t.Fatalf("应解析为本地格式,原样返回: %s", got)
	}
	if FmtTime("") != "-" {
		t.Fatalf("空时间应为 -")
	}
	if FmtTime("garbage") != "garbage" {
		t.Fatalf("非法输入应原样返回")
	}
}

func TestFmtSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{500, "500B"},
		{2048, "2.0KB"},
		{5 << 20, "5.0MB"},
	}
	for _, c := range cases {
		if got := FmtSize(c.in); got != c.want {
			t.Errorf("FmtSize(%d) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestStatusMark(t *testing.T) {
	if StatusMark("success") != "success ✓" || StatusMark("failed") != "failed ✗" || StatusMark("pending") != "pending" {
		t.Fatalf("StatusMark 行为变化")
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := []struct{ in, want string }{
		{"localhost:8080", "http://localhost:8080"},
		{"http://localhost:8080/", "http://localhost:8080"},
		{"https://a.b.c", "https://a.b.c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeServer(c.in); got != c.want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
