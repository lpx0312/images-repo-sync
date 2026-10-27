package api

// chartupload_size_test 是 A4 回归测试:upload-paths 路径与 runOne 补上单文件大小上限。
// maxChartFileSize 已改为 var,这里临时调小上限构造超限文件,断言被拒绝后还原。

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// oversizedChartTgz 构造一个「合法可解析但体积超限」的 chart tgz:
// 在标准 tgz 尾部追加填充字节(gzip 按流解析会忽略尾部数据,解析本身仍成功)。
func oversizedChartTgz(t *testing.T, name, version string, padding int) []byte {
	t.Helper()
	return append(testutil.BuildChartTgz(t, name, version, name), bytes.Repeat([]byte("x"), padding)...)
}

// TestUploadPathsRejectsOversizedFile 上限调小后,超过上限的 tgz 应被拒绝且不产生记录。
func TestUploadPathsRejectsOversizedFile(t *testing.T) {
	r, db, token := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm-size", "chartmuseum", "http://127.0.0.1:1", "")

	// 临时把上限调小到 4KB;测试结束还原,不影响其他用例。
	saved := maxChartFileSize
	maxChartFileSize = 4 << 10
	t.Cleanup(func() { maxChartFileSize = saved })

	dir := t.TempDir()
	p := filepath.Join(dir, "big-1.0.0.tgz")
	if err := os.WriteFile(p, oversizedChartTgz(t, "big", "1.0.0", 8<<10), 0o644); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Created []struct {
			ID uint `json:"id"`
		} `json:"created"`
		Invalid []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"invalid"`
	}
	w := do(r, token, http.MethodPost, "/api/charts/upload-paths", map[string]any{
		"repo_id": repoID, "paths": []string{p},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("upload-paths 失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &res)
	if len(res.Created) != 0 || len(res.Invalid) != 1 {
		t.Fatalf("超限文件应被拒绝: created=%v invalid=%v", res.Created, res.Invalid)
	}
	if !strings.Contains(res.Invalid[0].Reason, "超过") || !strings.Contains(res.Invalid[0].Reason, "上限") {
		t.Errorf("超限提示不对: %q", res.Invalid[0].Reason)
	}
}

// TestRunOneRejectsOversizedFile runOne 在读文件前也应按大小拦截,置失败而非读入内存。
func TestRunOneRejectsOversizedFile(t *testing.T) {
	_, db, _ := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm-size2", "chartmuseum", "http://127.0.0.1:1", "")

	saved := maxChartFileSize
	maxChartFileSize = 4 << 10
	t.Cleanup(func() { maxChartFileSize = saved })

	p := filepath.Join(t.TempDir(), "big-1.0.0.tgz")
	if err := os.WriteFile(p, oversizedChartTgz(t, "big", "1.0.0", 8<<10), 0o644); err != nil {
		t.Fatal(err)
	}

	u := model.ChartUpload{ChartRepoID: repoID, FileName: "big-1.0.0.tgz", FilePath: p, Status: model.TaskStatusPending}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}

	NewChartUploadHandler(db).runOne(u.ID)

	var after model.ChartUpload
	db.First(&after, u.ID)
	if after.Status != model.TaskStatusFailed || !strings.Contains(after.Error, "超过") {
		t.Fatalf("超限文件应置 failed: %+v", after)
	}
}
