package api

// review_red_test 是针对 2026-10-27 全仓代码审查(见 docs/review-2026-10-27.md)
// internal/api P1 缺陷的 A 类回归测试:断言「修复后的正确行为」。
// 注释带 回归锁定(已修复) 的用例在缺陷修复前允许失败;修复后必须全部转绿。

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/task"
	"images-repo-sync/internal/testutil"
)

// httpRegistry 起明文 HTTP mock registry,返回带 http:// 前缀的完整地址
// (对应生产里 Host 配置为 "http://host:port" 的明文仓库)。
func httpRegistry(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// 回归锁定(已修复): A1 明文 HTTP 仓库(host 带 http:// 前缀)的 Repos/Projects 应正常返回。
//
// 现状:catalog.go registryURL 硬编码 https,拼出 "https://http://127.0.0.1:x/..."
// 导致 URL 解析失败,三接口对 HTTP 仓库全失效(仓库"测试连接"能通过,浏览目录却必然失败)。
func TestCatalogReposProjectsHTTPRegistry(t *testing.T) {
	srv := httpRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/_catalog":
			fmt.Fprint(w, `{"repositories":["library/nginx","team/app"]}`)
		case "/api/v2.0/projects":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"name":"alpha"},{"name":"beta"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		default:
			w.WriteHeader(404)
		}
	})

	r, db, token := newAuthed(t)
	gen := model.Registry{Name: "http-gen", Host: srv, Type: "generic", Insecure: true}
	if err := db.Create(&gen).Error; err != nil {
		t.Fatal(err)
	}
	hb := model.Registry{Name: "http-hb", Host: srv, Type: "harbor", Insecure: true}
	if err := db.Create(&hb).Error; err != nil {
		t.Fatal(err)
	}

	var res struct {
		OK        bool     `json:"ok"`
		Message   string   `json:"message"`
		Repos     []string `json:"repos"`
		Supported bool     `json:"supported"`
		Projects  []string `json:"projects"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+itoa(gen.ID)+"/repos", nil), &res)
	if !res.OK || len(res.Repos) != 2 || res.Repos[0] != "library/nginx" {
		t.Errorf("HTTP 仓库 Repos 应成功: ok=%v msg=%q repos=%v", res.OK, res.Message, res.Repos)
	}

	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+itoa(hb.ID)+"/projects", nil), &res)
	if !res.OK || !res.Supported || len(res.Projects) != 2 || res.Projects[0] != "alpha" {
		t.Errorf("HTTP 仓库 Projects 应成功: ok=%v supported=%v msg=%q projects=%v",
			res.OK, res.Supported, res.Message, res.Projects)
	}
}

// 回归锁定(已修复): A1 明文 HTTP 仓库的 Tags:buildListRef 产出的 list-tags 引用不应携带 scheme。
//
// 现状:buildListRef 直接用带 http:// 前缀的 host 拼引用,得到 "docker://http://host/path"
// 的畸形引用(真实 skopeo 必然失败);这里用 fake skopeo 断言返回的 repo 字段应为去 scheme 的形式。
func TestCatalogTagsHTTPRegistry(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "http-tags", Host: "http://reg.example.com", Type: "generic", Insecure: true}
	if err := db.Create(&reg).Error; err != nil {
		t.Fatal(err)
	}

	var res struct {
		OK   bool     `json:"ok"`
		Tags []string `json:"tags"`
		Repo string   `json:"repo"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+itoa(reg.ID)+"/tags?repo=proj/myapp", nil), &res)
	if !res.OK || len(res.Tags) == 0 {
		t.Fatalf("HTTP 仓库 Tags 应成功: %+v", res)
	}
	if res.Repo != "reg.example.com/proj/myapp" {
		t.Errorf("list-tags 引用不应携带 scheme: got %q, want %q",
			res.Repo, "reg.example.com/proj/myapp")
	}
}

// 回归锁定(已修复): A2 取消已完结任务必须被 4xx 拒绝,且不得改写终态(success、finished_at、error)。
//
// 主线行为(任务跑到 success 后再取消 → 4xx 且状态不变)由本用例固化;
// 「预检查通过后、Cancel 返回 false 的竞态窗口」由 task.go 取消接口的
// 条件更新(WHERE status IN pending/running,RowsAffected=0 时不改写)防护,
// 该竞态无法在黑盒 HTTP 层确定性构造,依赖代码审查与条件更新语义保证。
func TestCancelFinishedTaskKeepsTerminalState(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)

	// mock registry:/v2/ 200 供源仓库预登录探测。
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer regSrv.Close()
	_ = task.Instance() // 启动真实 worker

	srcID := createRegistryViaAPI(t, r, token, "a2-src", regSrv.URL, "generic")
	dstID := createRegistryViaAPI(t, r, token, "a2-dst", "dst.example.com", "generic")

	var created taskVO
	w := do(r, token, http.MethodPost, "/api/tasks", map[string]any{
		"source_registry_id":  srcID,
		"target_registry_ids": []uint{dstID},
		"mode":                "flat",
		"target_project":      "p",
		"refs":                []string{"nginx:1.25"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("创建任务失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &created)

	testutil.WaitFor(t, 30*time.Second, "任务完成", func() bool {
		var tk model.SyncTask
		db.First(&tk, created.ID)
		return tk.Status == model.TaskStatusSuccess || tk.Status == model.TaskStatusFailed
	})
	var before model.SyncTask
	if err := db.First(&before, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.Status != model.TaskStatusSuccess {
		t.Fatalf("前置条件失败: 任务应为 success, got %s (%s)", before.Status, before.Error)
	}
	if before.FinishedAt == nil {
		t.Fatal("前置条件失败: finished_at 应已写入")
	}

	// 任务已完结:取消应被拒绝。
	if w := do(r, token, http.MethodPost, "/api/tasks/"+itoa(created.ID)+"/cancel", nil); w.Code < 400 || w.Code >= 500 {
		t.Errorf("取消已完结任务应 4xx: got %d %s", w.Code, w.Body.String())
	}

	// DB 终态不得被改写。
	var after model.SyncTask
	if err := db.First(&after, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != model.TaskStatusSuccess {
		t.Errorf("状态被改写: got %s, want success", after.Status)
	}
	if after.FinishedAt == nil || after.FinishedAt.Unix() != before.FinishedAt.Unix() {
		t.Errorf("finished_at 被改写: got %v, want %v", after.FinishedAt, before.FinishedAt)
	}
	if after.Error != "" {
		t.Errorf("error 字段被写入: %q", after.Error)
	}
}

// multipartTempFiles 返回系统临时目录中 multipart 表单解析产生的临时文件名集合。
// Go 的 mime/multipart 会把超过 MaxMultipartMemory(router.go 中为 64MB)的部件
// 落成 "multipart-*" 临时文件,只有 form.RemoveAll() 才会清理。
func multipartTempFiles() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "multipart-") {
			out[e.Name()] = true
		}
	}
	return out
}

// 回归锁定(已修复): A3 multipart 上传的表单临时文件应在请求结束后被清理(form.RemoveAll),
// 而不是遗留到进程退出。
//
// 观测方式:构造超过 64MB MaxMultipartMemory 的部件(必然落盘为 multipart-* 临时文件),
// 对比请求前后系统临时目录中 multipart-* 文件集合;文件内容故意非法(tgz 解析失败),
// 使 handler 不产生上传记录与后台 goroutine,隔离出唯一的残留来源。
// 稳定性说明:测试进程外其他程序若恰好在窗口内创建/保留 multipart-* 文件会误报,
// 本机测试进程内无其他 multipart 来源;若环境噪声过大导致不稳定可跳过本用例。
func TestUploadFilesMultipartTempCleanup(t *testing.T) {
	r, db, token := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm-leak", "chartmuseum", "http://127.0.0.1:1", "")

	before := multipartTempFiles()
	// 64MB 内存阈值 + 1MB,保证部件落盘;内容非法使 ParseFile 拒绝。
	big := bytes.Repeat([]byte("x"), (64<<20)+(1<<20))
	w := doMultipart(r, token, "/api/charts/upload-files", "files", "big.tgz", big,
		map[string]string{"repo_id": fmt.Sprint(repoID)})
	if w.Code != http.StatusOK {
		t.Fatalf("上传请求失败: %d %s", w.Code, w.Body.String())
	}

	time.Sleep(300 * time.Millisecond) // 给文件系统与清理一点时间
	after := multipartTempFiles()
	var leaked []string
	for name := range after {
		if !before[name] {
			leaked = append(leaked, name)
		}
	}
	t.Cleanup(func() { // 无论成败,清掉本用例泄漏的文件,避免污染 %TEMP%
		for _, name := range leaked {
			_ = os.Remove(filepath.Join(os.TempDir(), name))
		}
	})
	if len(leaked) > 0 {
		t.Errorf("multipart 表单临时文件泄漏(缺 form.RemoveAll): %v", leaked)
	}
}

// A4(已兑现):maxChartFileSize 已转为 var,UploadPaths 与 runOne 均在读取前 os.Stat
// 校验大小;超限拒绝路径由 chartupload_size_test.go 覆盖(临时调小上限构造超限文件)。
