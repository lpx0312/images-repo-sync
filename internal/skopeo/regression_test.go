package skopeo

// 本文件为代码审查缺陷的 A 类回归测试(断言「修复后」的正确行为,缺陷已修复,应保持绿色)。
// 约定:若这些用例变红说明行为回归。
// 不加 t.Parallel():SkopeoBin 是全局配置,串行执行。

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"testing"
	"time"

	"images-repo-sync/internal/model"
)

// TestCopyDualStream 验证 runWithStream 的 stdout/stderr 双流回调已串行化。
// fake 对 dualstream 引用并发写 stdout/stderr 各 200 行:
//   - 修复后:Copy 成功,handler 恰好收到 200+200=400 行,统计本身无数据竞争;
//   - 当前:两个 stream goroutine 并发调用同一个 handler(ringBuffer.push 无锁、
//     测试内计数器/切片无锁),`go test -race` 会直接报告 DATA RACE
//     (race 导致测试以 race 报告失败,而非断言失败;无 -race 时计数可能不足 400)。
func TestCopyDualStream(t *testing.T) {
	// 回归锁定(已修复): A1
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		mu          sync.Mutex // 修复后 handler 串行化,锁只是防御;当前实现下两 goroutine 并发进入
		stdoutCount int
		stderrCount int
	)
	res := Copy(ctx, CopyOptions{
		SrcRef: "dualstream/test/img:1",
		DstRef: "registry.example.com/mirror/img:1",
		Arch:   ArchAMD64,
	}, func(line string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(line, "stdout line "):
			stdoutCount++
		case strings.HasPrefix(line, "stderr line "):
			stderrCount++
		}
	})
	if !res.OK {
		t.Fatalf("dualstream copy 应成功: %+v", res)
	}
	if stdoutCount != 200 || stderrCount != 200 {
		t.Fatalf("应收到 stdout/stderr 各 200 行,实际 stdout=%d stderr=%d(总 %d,期望 400)",
			stdoutCount, stderrCount, stdoutCount+stderrCount)
	}
}

// TestInspectDigestConcurrentVaryDigest 验证 InspectDigest 的 digest 闭包写入在
// runWithStream 串行化修复后仍正确:多个 goroutine 并发 inspect 不同的 varydigest
// 引用,各自返回与自身引用哈希一致的 digest,互不串扰。
// 说明:当前实现下 inspect 仅产生 stdout 单流,该测试侥幸可通过;它是 A1 修复的
// 护栏 —— 串行化改造不得破坏并发调用与「取最后一个非空行」语义。
func TestInspectDigestConcurrentVaryDigest(t *testing.T) {
	// 回归锁定(已修复): A1(配套护栏)
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const n = 8
	refs := make([]string, n)
	for i := range refs {
		refs[i] = fmt.Sprintf("registry.example.com/varydigest/img%d:1", i)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := InspectDigest(ctx, refs[i], "", false)
			if err != nil {
				errs[i] = err
				return
			}
			// 与 fake 相同的 FNV-64a 哈希推导期望值。
			h := fnv.New64a()
			_, _ = h.Write([]byte(refs[i]))
			want := fmt.Sprintf("sha256:%016x%048x", h.Sum64(), 0)
			if got != want {
				errs[i] = fmt.Errorf("ref %s digest 串扰: got %q want %q", refs[i], got, want)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("并发 inspect #%d: %v", i, err)
		}
	}
}

// TestCopyLongLine 验证单行超过 bufio.Scanner 1MB 上限后调用方仍能排空管道:
// fake 对 longline 引用先输出一行 2MB 无换行内容再正常收尾退出。
//   - 修复后:子进程不被写满的管道阻塞,10s 内正常退出,Copy 成功;
//   - 当前:Scanner 停读 → 子进程阻塞在写管道 → 只能等 ctx 超时被杀,
//     Copy 返回 context deadline exceeded(超时失败即本用例的 RED 表现)。
func TestCopyLongLine(t *testing.T) {
	// 回归锁定(已修复): A2
	useFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	res := Copy(ctx, CopyOptions{
		SrcRef: "longtest/longline/img:1",
		DstRef: "registry.example.com/mirror/img:1",
		Arch:   ArchAll,
	}, nil)
	elapsed := time.Since(start)
	if !res.OK {
		t.Fatalf("longline copy 应成功返回(而非阻塞到 ctx 超时): ok=%v tail=%q elapsed=%v",
			res.OK, res.StderrTail, elapsed)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("copy 用时 %v 接近/达到 10s 超时上限,说明仍是靠超时兜底", elapsed)
	}
}

// TestResolveTargetEmptyHost 验证空 targetHost 直接报错,而不是拼出
// 以 '/' 开头的非法引用(如 "/mirror/clusterctl:v1")。三种模式均须拦截,
// 纯空白 host 同样视为空。
func TestResolveTargetEmptyHost(t *testing.T) {
	// 回归锁定(已修复): A3
	const src = "gcr.io/k8s-staging/cluster-api/clusterctl:v1"
	cases := []struct {
		name string
		host string
		mode string
	}{
		{"flat 空 host", "", model.ModeFlat},
		{"preserve_path 空 host", "", model.ModePreservePath},
		{"replace_host 空 host", "", model.ModeReplaceHost},
		{"flat 空白 host", "   ", model.ModeFlat},
		{"flat 仅斜杠 host", "/", model.ModeFlat},
	}
	for _, c := range cases {
		got, err := ResolveTarget(src, c.host, c.mode, "mirror")
		if err == nil {
			t.Errorf("%s: 空/空白 targetHost 应返回 error,实际拼出 %q", c.name, got)
		}
		if got != "" {
			t.Errorf("%s: 出错时目标引用应为空串,实际 %q", c.name, got)
		}
	}
}

// TestParseSourceCaseInsensitive 验证 scheme 剥离与 host 归一化对大小写不敏感:
//   - HTTP://docker.io/x → 剥 scheme 后按 docker.io 处理,补 library/;
//   - Docker.IO/x      → host 归一化为 docker.io,官方镜像补 library/。
// 当前实现 TrimPrefix/比较均区分大小写:前者把 "HTTP:" 当 host,后者不补 library。
func TestParseSourceCaseInsensitive(t *testing.T) {
	// 回归锁定(已修复): A4
	cases := []struct {
		in       string
		wantHost string
		wantPath string
	}{
		{"HTTP://docker.io/x", "docker.io", "library/x:latest"},
		{"http://Docker.IO/x", "docker.io", "library/x:latest"},
		{"Docker.IO/x", "docker.io", "library/x:latest"},
		{"DOCKER://quay.io/jetstack/cm:v1", "quay.io", "jetstack/cm:v1"},
	}
	for _, c := range cases {
		gotHost, gotPath := ParseSource(c.in)
		if gotHost != c.wantHost || gotPath != c.wantPath {
			t.Errorf("ParseSource(%q) = (%q,%q), want (%q,%q)",
				c.in, gotHost, gotPath, c.wantHost, c.wantPath)
		}
	}
}
