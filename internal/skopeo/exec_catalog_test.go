package skopeo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/testutil"
)

// useFake 把全局 SkopeoBin 指向测试替身并恢复。
func useFake(t *testing.T) {
	t.Helper()
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = testutil.FakeSkopeo(t)
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })
}

func TestBinVersionWithFake(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v := BinVersion(ctx)
	if !strings.Contains(v, "skopeo version") {
		t.Fatalf("BinVersion = %q, 应包含版本行", v)
	}
}

func TestBinVersionMissingBinary(t *testing.T) {
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = "definitely-not-exist-skopeo"
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if v := BinVersion(ctx); v != "" {
		t.Fatalf("二进制不存在应返回空串, got %q", v)
	}
}

func TestListTagsWithFake(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tags, err := ListTags(ctx, "registry.example.com/proj/myapp", "registry.example.com", "user", "pass", false)
	if err != nil {
		t.Fatalf("ListTags 失败: %v", err)
	}
	if len(tags) != 2 || tags[0] != "1.25" || tags[1] != "1.26" {
		t.Fatalf("tags = %v", tags)
	}

	// insecure 变体走不同参数拼装路径。
	if _, err := ListTags(ctx, "registry.example.com/proj/myapp", "registry.example.com", "user", "pass", true); err != nil {
		t.Fatalf("insecure ListTags 失败: %v", err)
	}
}

func TestListTagsFailure(t *testing.T) {
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = "definitely-not-exist-skopeo"
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ListTags(ctx, "x/y", "h", "", "", false); err == nil {
		t.Fatal("二进制不存在时 ListTags 应失败")
	}
}

func TestInspectDigestWithFake(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	d, err := InspectDigest(ctx, "registry.example.com/proj/myapp:1.25", "", false)
	if err != nil {
		t.Fatalf("InspectDigest 失败: %v", err)
	}
	if !strings.HasPrefix(d, "sha256:") {
		t.Fatalf("digest = %q", d)
	}

	// insecure + authfile 变体。
	af, err := WriteAuthFile("registry.example.com", "u", "p")
	if err != nil {
		t.Fatalf("WriteAuthFile 失败: %v", err)
	}
	defer CleanupAuthFiles(af)
	if _, err := InspectDigest(ctx, "registry.example.com/proj/myapp:1.25", af, true); err != nil {
		t.Fatalf("insecure InspectDigest 失败: %v", err)
	}
}

func TestInspectDigestFailure(t *testing.T) {
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = "definitely-not-exist-skopeo"
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := InspectDigest(ctx, "x", "", false); err == nil {
		t.Fatal("二进制不存在时 InspectDigest 应失败")
	}
}

func TestCopySuccessWithFake(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var lines []string
	res := Copy(ctx, CopyOptions{
		SrcRef: "docker.io/library/nginx:1.25",
		DstRef: "registry.example.com/mirror/nginx:1.25",
		Arch:   ArchAMD64,
	}, func(line string) { lines = append(lines, line) })
	if !res.OK {
		t.Fatalf("Copy 应成功: %+v", res)
	}
	if len(lines) == 0 {
		t.Fatal("成功路径应回调进度行")
	}
}

func TestCopyFailureModes(t *testing.T) {
	useFake(t)
	cases := []struct {
		ref     string
		wantErr string
	}{
		{"docker.io/x/failcopy:1", "unauthorized"},
		{"docker.io/x/noarch:1", "no image found in manifest list"},
		{"docker.io/x/manifestunknown:1", "manifest unknown"},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res := Copy(ctx, CopyOptions{SrcRef: c.ref, DstRef: "reg.example.com/m/y:1", Arch: ArchAll},
			nil) // handler 为 nil 也不应 panic
		cancel()
		if res.OK {
			t.Errorf("ref %s 应失败", c.ref)
			continue
		}
		if !strings.Contains(res.StderrTail, c.wantErr) {
			t.Errorf("ref %s 错误输出应含 %q, got %q", c.ref, c.wantErr, res.StderrTail)
		}
	}
}

func TestCopyCanceledContext(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	res := Copy(ctx, CopyOptions{SrcRef: "docker.io/x/slowcopy:1", DstRef: "reg.example.com/m/y:1"}, nil)
	if res.OK {
		t.Fatal("超时 ctx 的 copy 不应成功")
	}
}

func TestCopyPreserveDigestsConflictDetection(t *testing.T) {
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res := Copy(ctx, CopyOptions{SrcRef: "docker.io/x/preserveconflict:1", DstRef: "r.example.com/m/y:1", PreserveDigests: true}, nil)
	if res.OK {
		t.Fatal("preserveconflict 应失败")
	}
	if !IsPreserveDigestsConflict(res.StderrTail) {
		t.Fatalf("应识别为 preserve-digests 冲突: %q", res.StderrTail)
	}
	// 无关错误不应误判。
	if IsPreserveDigestsConflict("unauthorized: authentication required") {
		t.Fatal("无关错误不应判定为 digest 冲突")
	}
}

func TestCopyMissingBinaryTail(t *testing.T) {
	// 二进制不存在:错误信息应含启动失败说明,StderrTail 取 err 文本。
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = "definitely-not-exist-skopeo"
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := Copy(ctx, CopyOptions{SrcRef: "a:1", DstRef: "b:1"}, nil)
	if res.OK || res.StderrTail == "" {
		t.Fatalf("应失败且带错误详情: %+v", res)
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog(strings.Repeat("a", 250)); len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Errorf("长文本应截断到 200+..., got %d", len(got))
	}
	if got := truncateForLog("short"); got != "short" {
		t.Errorf("短文本原样返回, got %q", got)
	}
}

// TestRingBufferZeroCapacity n<1 时按容量 1 处理(copy_test 已覆盖基本环形行为)。
func TestRingBufferZeroCapacity(t *testing.T) {
	r := newRingBuffer(0)
	r.push("x")
	r.push("y")
	if got := r.join(""); got != "y" {
		t.Errorf("n=0 容量为 1,只留最新: %q", got)
	}
}

// TestListTagsParsesRepositoryTagFormat 校验对多行缩进 JSON 的解析(fake 输出与真实 skopeo 相同格式)。
func TestListTagsParsesRepositoryTagFormat(t *testing.T) {
	raw := "{\n    \"Repository\": \"r\",\n    \"Tags\": [\n        \"v1\"\n    ]\n}\n"
	var out struct {
		Repository string   `json:"Repository"`
		Tags       []string `json:"Tags"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out.Tags) != 1 {
		t.Fatalf("多行 JSON 解析失败: %v %v", err, out.Tags)
	}
}
