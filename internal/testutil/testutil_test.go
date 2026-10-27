// testutil 包自身的 B 类契约测试:
// 锁定 NewTestDB / WaitFor / BuildChartTgz / WriteChartTgz / UseFakeSkopeo
// 以及 fakeskopeo 各关键字行为的当前契约(必须全部通过)。
//
// 注意:NewTestDB 会替换全局 store.DB,本文件所有测试串行运行(不加 t.Parallel)。
package testutil_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/auth"
	"images-repo-sync/internal/chart"
	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/store"
	"images-repo-sync/internal/testutil"
)

// ---------------------------------------------------------------------------
// NewTestDB
// ---------------------------------------------------------------------------

// TestNewTestDB 验证 NewTestDB 的迁移、seed 与全局句柄契约。
func TestNewTestDB(t *testing.T) {
	db := testutil.NewTestDB(t)

	t.Run("八张表全部迁移成功", func(t *testing.T) {
		tables := []struct {
			name  string
			model any
		}{
			{"users", &model.User{}},
			{"registries", &model.Registry{}},
			{"sync_tasks", &model.SyncTask{}},
			{"sync_task_items", &model.SyncTaskItem{}},
			{"login_logs", &model.LoginLog{}},
			{"system_settings", &model.SystemSetting{}},
			{"chart_repos", &model.ChartRepo{}},
			{"chart_uploads", &model.ChartUpload{}},
		}
		for _, tb := range tables {
			if !db.Migrator().HasTable(tb.model) {
				t.Errorf("表 %s 未迁移成功", tb.name)
			}
		}
	})

	t.Run("store.DB 指向返回的句柄", func(t *testing.T) {
		if store.DB != db {
			t.Fatalf("store.DB(%p) != NewTestDB 返回值(%p)", store.DB, db)
		}
	})

	t.Run("默认 admin 已 seed 且密码为 Admin@123", func(t *testing.T) {
		var u model.User
		if err := db.Where("username = ?", "admin").First(&u).Error; err != nil {
			t.Fatalf("查询 admin 用户失败: %v", err)
		}
		if !auth.CheckPassword(u.PasswordHash, "Admin@123") {
			t.Errorf("admin 密码校验 Admin@123 失败(hash=%q)", u.PasswordHash)
		}
		// 反向校验,防止 CheckPassword 恒真的退化。
		if auth.CheckPassword(u.PasswordHash, "wrong-password") {
			t.Errorf("错误密码居然校验通过")
		}
	})

	t.Run("system_settings 的 default_arch 已 seed", func(t *testing.T) {
		var s model.SystemSetting
		if err := db.Where("`key` = ?", model.SettingKeyDefaultArch).First(&s).Error; err != nil {
			t.Fatalf("查询 default_arch 失败: %v", err)
		}
		if s.Value != "amd64" {
			t.Errorf("default_arch = %q, want amd64", s.Value)
		}
	})
}

// ---------------------------------------------------------------------------
// WaitFor
// ---------------------------------------------------------------------------

// fatalRecorder 捕获 WaitFor 超时路径的 Fatalf 调用(testing.TB 无法被外部
// 类型直接实现,这里嵌入 nil 接口并覆写被用到的两个方法)。
type fatalRecorder struct {
	testing.TB // 嵌入 nil 接口仅用于满足 testing.TB
	fataled    bool
	msg        string
}

func (r *fatalRecorder) Helper() {}
func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.fataled = true
	r.msg = fmt.Sprintf(format, args...)
}

func TestWaitFor(t *testing.T) {
	t.Run("条件立即满足则快速返回", func(t *testing.T) {
		calls := 0
		start := time.Now()
		testutil.WaitFor(t, 2*time.Second, "立即满足", func() bool {
			calls++
			return true
		})
		elapsed := time.Since(start)
		if calls == 0 {
			t.Fatalf("cond 未被调用")
		}
		if elapsed >= 1*time.Second {
			t.Errorf("立即满足耗时 %v,应远小于 1s", elapsed)
		}
	})

	t.Run("条件稍后满足则轮询到成功", func(t *testing.T) {
		readyAt := time.Now().Add(80 * time.Millisecond)
		start := time.Now()
		testutil.WaitFor(t, 3*time.Second, "稍后满足", func() bool {
			return time.Now().After(readyAt)
		})
		elapsed := time.Since(start)
		if elapsed < 70*time.Millisecond {
			t.Errorf("耗时 %v,条件 80ms 后才满足,不应提前返回", elapsed)
		}
		if elapsed >= 3*time.Second {
			t.Errorf("耗时 %v,超过 3s 说明没轮询到成功条件", elapsed)
		}
	})

	t.Run("条件不满足则按 timeout 报错", func(t *testing.T) {
		rec := &fatalRecorder{}
		start := time.Now()
		testutil.WaitFor(rec, 150*time.Millisecond, "永不满足的条件", func() bool { return false })
		elapsed := time.Since(start)
		if !rec.fataled {
			t.Fatalf("超时后未调用 Fatalf")
		}
		if !strings.Contains(rec.msg, "永不满足的条件") {
			t.Errorf("Fatalf 消息 %q 未包含 what 描述", rec.msg)
		}
		if !strings.Contains(rec.msg, "超时") {
			t.Errorf("Fatalf 消息 %q 未包含超时字样", rec.msg)
		}
		if elapsed < 140*time.Millisecond {
			t.Errorf("耗时 %v,应等待满 ~150ms 的 timeout", elapsed)
		}
	})
}

// ---------------------------------------------------------------------------
// BuildChartTgz / WriteChartTgz
// ---------------------------------------------------------------------------

func TestBuildChartTgz(t *testing.T) {
	t.Run("带顶层目录可被解析", func(t *testing.T) {
		data := testutil.BuildChartTgz(t, "demo", "1.2.3", "demo")
		m, err := chart.ParseBytes(data)
		if err != nil {
			t.Fatalf("ParseBytes 失败: %v", err)
		}
		if m.Name != "demo" || m.Version != "1.2.3" {
			t.Errorf("name/version = %q/%q, want demo/1.2.3", m.Name, m.Version)
		}
	})

	t.Run("无顶层目录也可被解析", func(t *testing.T) {
		data := testutil.BuildChartTgz(t, "bare", "0.0.1", "")
		m, err := chart.ParseBytes(data)
		if err != nil {
			t.Fatalf("ParseBytes 失败: %v", err)
		}
		if m.Name != "bare" || m.Version != "0.0.1" {
			t.Errorf("name/version = %q/%q, want bare/0.0.1", m.Name, m.Version)
		}
	})
}

func TestWriteChartTgz(t *testing.T) {
	p := testutil.WriteChartTgz(t, "mychart", "2.3.4")
	m, size, err := chart.ParseFile(p)
	if err != nil {
		t.Fatalf("ParseFile 失败: %v", err)
	}
	if m.Name != "mychart" || m.Version != "2.3.4" {
		t.Errorf("name/version = %q/%q, want mychart/2.3.4", m.Name, m.Version)
	}
	if st, err := os.Stat(p); err != nil || st.Size() == 0 {
		t.Errorf("产物文件异常: stat=%v err=%v", st, err)
	} else if size != st.Size() {
		t.Errorf("ParseFile 返回 size=%d, 与磁盘大小 %d 不符", size, st.Size())
	}
}

// ---------------------------------------------------------------------------
// fake skopeo 执行辅助
// ---------------------------------------------------------------------------

// fakeCmdTimeout 是所有 fake skopeo 子进程的硬超时兜底,
// 防止 longline/dualstream 等场景挂起拖死整个测试。
const fakeCmdTimeout = 30 * time.Second

// TestFakeSkopeoConflictOnce 锁定 conflictonce 关键字契约:
// 带 --preserve-digests 失败(SWR 拒收特征),不带则成功(降级重试语义)。
func TestFakeSkopeoConflictOnce(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	bin := config.AppConfig.SkopeoBin

	_, errOut, err := runSkopeo(t, bin,
		"copy", "--preserve-digests", "docker://conflictonce/img:1", "docker://conflictonce/dst:1")
	assertExitNonZero(t, "conflictonce 带 --preserve-digests", err)
	if !strings.Contains(errOut, "Instructed to preserve digests") {
		t.Errorf("stderr 应含 SWR 拒收特征, got %q", errOut)
	}

	if _, _, err := runSkopeo(t, bin,
		"copy", "docker://conflictonce/img:1", "docker://conflictonce/dst:1"); err != nil {
		t.Errorf("conflictonce 不带 --preserve-digests 应成功(SWR 降级重试语义): %v", err)
	}
}

// runSkopeo 执行指定二进制并返回 stdout/stderr/err;超时直接判失败。
func runSkopeo(t *testing.T, bin string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fakeCmdTimeout)
	defer cancel()
	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("fake skopeo 执行超时(>%v): args=%v", fakeCmdTimeout, args)
	}
	return out.String(), errBuf.String(), err
}

// assertExitNonZero 断言 err 是非零退出的 ExitError。
func assertExitNonZero(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 期望非零退出,实际成功", what)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Fatalf("%s: 期望 ExitError 非零退出码,得到 %v", what, err)
	}
}

// ---------------------------------------------------------------------------
// UseFakeSkopeo
// ---------------------------------------------------------------------------

func TestUseFakeSkopeo(t *testing.T) {
	before := config.AppConfig.SkopeoBin
	testutil.UseFakeSkopeo(t)
	bin := config.AppConfig.SkopeoBin

	t.Run("替换 SkopeoBin 为可执行文件", func(t *testing.T) {
		if bin == before {
			t.Fatalf("SkopeoBin 未被替换,仍为 %q", bin)
		}
		if bin == "" {
			t.Fatalf("SkopeoBin 被替换为空串")
		}
		st, err := os.Stat(bin)
		if err != nil {
			t.Fatalf("替换后的路径不可访问: %v", err)
		}
		if st.IsDir() {
			t.Fatalf("替换后的路径是目录: %s", bin)
		}
	})

	t.Run("--version 契约", func(t *testing.T) {
		out, _, err := runSkopeo(t, bin, "--version")
		if err != nil {
			t.Fatalf("--version 执行失败: %v (stderr 见上)", err)
		}
		if got := strings.TrimSpace(out); got != "skopeo version 1.14.0-fake" {
			t.Errorf("--version 输出 %q, want %q", got, "skopeo version 1.14.0-fake")
		}
	})

	t.Run("普通 copy 成功", func(t *testing.T) {
		_, errOut, err := runSkopeo(t, bin,
			"copy", "docker://ok/img:1", "docker://ok/dst:1")
		if err != nil {
			t.Fatalf("ok copy 失败: %v, stderr=%q", err, errOut)
		}
		if errOut == "" {
			t.Errorf("成功 copy 的 stderr 为空,应有进度输出")
		}
	})

	t.Run("failcopy 失败且 stderr 含 unauthorized", func(t *testing.T) {
		_, errOut, err := runSkopeo(t, bin,
			"copy", "docker://ok/failcopy:1", "docker://ok/dst:1")
		assertExitNonZero(t, "failcopy", err)
		if !strings.Contains(errOut, "unauthorized") {
			t.Errorf("failcopy stderr %q 未含 unauthorized", errOut)
		}
	})
}

// ---------------------------------------------------------------------------
// fake skopeo 关键字契约(直接 exec 二进制,是其余各包测试的地基)
// ---------------------------------------------------------------------------

func TestFakeSkopeoListTags(t *testing.T) {
	bin := testutil.FakeSkopeo(t)

	t.Run("普通引用输出合法 JSON", func(t *testing.T) {
		out, _, err := runSkopeo(t, bin, "list-tags", "docker://reg.example.com/app")
		if err != nil {
			t.Fatalf("list-tags 失败: %v", err)
		}
		var res struct {
			Repository string   `json:"Repository"`
			Tags       []string `json:"Tags"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("stdout 不是合法 JSON: %v, out=%q", err, out)
		}
		if res.Repository != "reg.example.com/app" {
			t.Errorf("Repository = %q, want reg.example.com/app", res.Repository)
		}
		if len(res.Tags) == 0 {
			t.Errorf("Tags 为空")
		}
	})

	t.Run("failtags 非零退出且 stderr 含 unauthorized", func(t *testing.T) {
		_, errOut, err := runSkopeo(t, bin, "list-tags", "docker://reg/failtags/app")
		assertExitNonZero(t, "list-tags failtags", err)
		if !strings.Contains(errOut, "unauthorized") {
			t.Errorf("stderr %q 未含 unauthorized", errOut)
		}
	})

	t.Run("badjson 退出 0 且 stdout 非法 JSON", func(t *testing.T) {
		out, _, err := runSkopeo(t, bin, "list-tags", "docker://reg/badjson/app")
		if err != nil {
			t.Fatalf("badjson 应退出 0,实际: %v", err)
		}
		if json.Valid([]byte(out)) {
			t.Errorf("stdout 应为非法 JSON,实际合法: %q", out)
		}
	})
}

// digestRe 匹配 sha256: + 64 位十六进制。
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func TestFakeSkopeoInspect(t *testing.T) {
	bin := testutil.FakeSkopeo(t)

	t.Run("普通引用输出固定 digest", func(t *testing.T) {
		out, _, err := runSkopeo(t, bin, "inspect", "--format", "{{.Digest}}", "docker://ok/plain/img:1")
		if err != nil {
			t.Fatalf("inspect 失败: %v", err)
		}
		got := strings.TrimSpace(out)
		if !digestRe.MatchString(got) {
			t.Fatalf("digest %q 不匹配 sha256+64hex 格式", got)
		}
		if !strings.HasPrefix(got, "sha256:faaaaa") {
			t.Errorf("普通 digest 应为固定值 sha256:faaa…,实际 %q", got)
		}
	})

	t.Run("failinspect 非零退出", func(t *testing.T) {
		_, _, err := runSkopeo(t, bin, "inspect", "--format", "{{.Digest}}", "docker://ok/failinspect/img:1")
		assertExitNonZero(t, "inspect failinspect", err)
	})

	t.Run("varydigest 格式/确定性/区分度", func(t *testing.T) {
		refA := "docker://ok/varydigest/a:1"
		refB := "docker://ok/varydigest/b:2"

		out1, _, err := runSkopeo(t, bin, "inspect", "--format", "{{.Digest}}", refA)
		if err != nil {
			t.Fatalf("inspect varydigest 失败: %v", err)
		}
		out2, _, err := runSkopeo(t, bin, "inspect", "--format", "{{.Digest}}", refA)
		if err != nil {
			t.Fatalf("inspect varydigest 第二次失败: %v", err)
		}
		outB, _, err := runSkopeo(t, bin, "inspect", "--format", "{{.Digest}}", refB)
		if err != nil {
			t.Fatalf("inspect varydigest refB 失败: %v", err)
		}

		d1, d2, dB := strings.TrimSpace(out1), strings.TrimSpace(out2), strings.TrimSpace(outB)
		for name, d := range map[string]string{"第一次": d1, "第二次": d2, "refB": dB} {
			if !digestRe.MatchString(d) {
				t.Errorf("%s输出 %q 不匹配 sha256+64hex 格式", name, d)
			}
		}
		if d1 != d2 {
			t.Errorf("同一 ref 两次输出不一致: %q vs %q", d1, d2)
		}
		if d1 == dB {
			t.Errorf("不同 ref 输出了相同 digest: %q", d1)
		}
	})
}

func TestFakeSkopeoCopy(t *testing.T) {
	bin := testutil.FakeSkopeo(t)

	t.Run("dualstream 成功且双流都有完整输出", func(t *testing.T) {
		stdout, stderr, err := runSkopeo(t, bin,
			"copy", "docker://ok/dualstream/src:1", "docker://ok/dualstream/dst:1")
		if err != nil {
			t.Fatalf("dualstream copy 失败: %v, stderr=%q", err, stderr)
		}
		if !strings.Contains(stdout, "stdout line 199") {
			t.Errorf("stdout 缺少末行输出(len=%d)", len(stdout))
		}
		if !strings.Contains(stderr, "stderr line 199") {
			t.Errorf("stderr 缺少末行输出(len=%d)", len(stderr))
		}
	})

	t.Run("longline 成功返回且携带 2MB 超长行", func(t *testing.T) {
		_, stderr, err := runSkopeo(t, bin,
			"copy", "docker://ok/longline/src:1", "docker://ok/longline/dst:1")
		if err != nil {
			t.Fatalf("longline copy 失败: %v", err)
		}
		if len(stderr) < 2*1024*1024 {
			t.Errorf("stderr 长度 %d, 应 >= 2MB(超长行未写出)", len(stderr))
		}
		if !strings.Contains(stderr, "Writing manifest to image destination") {
			t.Errorf("stderr 缺少收尾行")
		}
	})
}
