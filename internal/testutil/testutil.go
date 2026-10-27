// Package testutil 提供跨包共享的测试基础设施:
// 临时 SQLite 数据库、fake skopeo 二进制与 chart tgz 构造工具。
//
// 仅被各包的 _test.go 导入,不进入生产构建产物。
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/store"
)

// resetMu 串行化全局配置/数据库的替换(Go 测试默认串行,这里再加一道保险,
// 防止误用 t.Parallel 时互相踩踏)。
var resetMu sync.Mutex

// NewTestDB 把全局配置指向临时目录并初始化 store.DB(纯 Go SQLite),
// 自动 seed 默认 admin(admin/Admin@123)。返回可用的 gorm 句柄。
//
// 多次调用安全:每次都会把 store.DB 指向新的临时库,测试结束后关闭连接。
func NewTestDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	resetMu.Lock()
	defer resetMu.Unlock()

	dbPath := filepath.Join(tb.TempDir(), "test.db")
	config.AppConfig.DBPath = dbPath
	config.AppConfig.AdminUsername = "admin"
	config.AppConfig.AdminPassword = "Admin@123"
	config.AppConfig.JWTSecret = "test-jwt-secret"
	config.AppConfig.TaskConcurrency = 1
	config.AppConfig.TaskRetentionDays = 30
	config.AppConfig.LoginLogRetentionDays = 180
	config.AppConfig.EncryptKey = "" // 默认明文;需要加密的测试自行设置

	old := store.DB
	if err := store.Init(); err != nil {
		tb.Fatalf("初始化测试数据库失败: %v", err)
	}
	db := store.DB
	tb.Cleanup(func() {
		// 先还原全局再关闭本测试创建的连接:否则 store.DB 悬挂指向已关闭的
		// 连接池,后续任何不先调 NewTestDB 的用例会拿到 "database is closed"。
		store.DB = old
		if db != old { // 关闭本测试创建的连接,避免句柄泄漏。
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
	})
	return db
}

// fakeBinOnce 保证 fake skopeo 只编译一次并缓存路径。
var (
	fakeBinOnce sync.Once
	fakeBinPath string
	fakeBinErr  error
)

// FakeSkopeo 编译并返回 fake skopeo 测试替身的路径(带 .exe 后缀,Windows/Linux 通用)。
// 替身行为见 internal/testutil/fakeskopeo/main.go 的注释。
func FakeSkopeo(tb testing.TB) string {
	tb.Helper()
	fakeBinOnce.Do(func() {
		out := filepath.Join(os.TempDir(), fmt.Sprintf("irs-fake-skopeo-%d.exe", os.Getpid()))
		cmd := exec.Command("go", "build", "-o", out, "images-repo-sync/internal/testutil/fakeskopeo")
		if buildOut, err := cmd.CombinedOutput(); err != nil {
			fakeBinErr = fmt.Errorf("编译 fake skopeo 失败: %v: %s", err, buildOut)
			return
		}
		fakeBinPath = out
	})
	if fakeBinErr != nil {
		tb.Fatalf("%v", fakeBinErr)
	}
	return fakeBinPath
}

// UseFakeSkopeo 把全局 SkopeoBin 指向 fake 替身,测试结束恢复原值。
func UseFakeSkopeo(tb testing.TB) {
	tb.Helper()
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = FakeSkopeo(tb)
	tb.Cleanup(func() { config.AppConfig.SkopeoBin = saved })
}

// BuildChartTgz 在内存中构造一个最小可解析的 helm chart tgz。
// name/version 写入 Chart.yaml;topDir 非空时把 Chart.yaml 放到该目录下。
func BuildChartTgz(tb testing.TB, name, version, topDir string) []byte {
	tb.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\ndescription: test chart\n", name, version)
	path := "Chart.yaml"
	if topDir != "" {
		path = topDir + "/Chart.yaml"
	}
	if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(chartYAML))}); err != nil {
		tb.Fatalf("写 tar 头失败: %v", err)
	}
	if _, err := tw.Write([]byte(chartYAML)); err != nil {
		tb.Fatalf("写 tar 内容失败: %v", err)
	}
	// 附带一个无关文件,验证解析器会跳过。
	extra := "placeholder"
	if err := tw.WriteHeader(&tar.Header{Name: "values.yaml", Mode: 0o644, Size: int64(len(extra))}); err != nil {
		tb.Fatalf("写 tar 头失败: %v", err)
	}
	_, _ = tw.Write([]byte(extra))

	if err := tw.Close(); err != nil {
		tb.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buf.Bytes()
}

// WriteChartTgz 把 BuildChartTgz 的产物写到临时文件,返回路径。
func WriteChartTgz(tb testing.TB, name, version string) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), name+"-"+version+".tgz")
	if err := os.WriteFile(p, BuildChartTgz(tb, name, version, name), 0o644); err != nil {
		tb.Fatalf("写 chart tgz 失败: %v", err)
	}
	return p
}

// WaitFor 轮询 cond 直到超时;cond 返回 true 表示条件满足。
// 用于等待异步 worker/上传 goroutine 更新数据库状态。
func WaitFor(tb testing.TB, timeout time.Duration, what string, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("等待 %s 超时(%v)", what, timeout)
}
