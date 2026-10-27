package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// waitForTimeout 异步上传/推送记录到达终态的轮询上限。
const waitForTimeout = 10 * time.Second

// doMultipart 发送 multipart 上传请求。
func doMultipart(r httpTestEngine, token, path, field, fileName string, content []byte, fields map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile(field, fileName)
	fw.Write(content)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type httpTestEngine interface {
	ServeHTTP(w http.ResponseWriter, req *http.Request)
}

// TestUploadFiles 校验+解析+创建记录+异步推送的完整链路(mock ChartMuseum)。
func TestUploadFiles(t *testing.T) {
	var pushed int
	cm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "systeminfo") {
			w.WriteHeader(404) // 非 Harbor
			return
		}
		pushed++
		w.WriteHeader(http.StatusCreated)
	}))
	defer cm.Close()

	r, db, token := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm-repo", "chartmuseum", cm.URL, "")

	good := testutil.BuildChartTgz(t, "mychart", "0.1.0", "mychart")
	w := doMultipart(r, token, "/api/charts/upload-files", "files", "mychart-0.1.0.tgz", good,
		map[string]string{"repo_id": fmt.Sprint(repoID)})
	if w.Code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", w.Code, w.Body.String())
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
	decode(t, w, &res)
	if len(res.Created) != 1 || len(res.Invalid) != 0 {
		t.Fatalf("created=%v invalid=%v", res.Created, res.Invalid)
	}

	// 异步推送完成后记录置 success 且临时文件被删。
	id := res.Created[0].ID
	testutil.WaitFor(t, waitForTimeout, "上传完成", func() bool {
		var u model.ChartUpload
		db.First(&u, id)
		return u.Status == model.TaskStatusSuccess || u.Status == model.TaskStatusFailed
	})
	var u model.ChartUpload
	db.First(&u, id)
	if u.Status != model.TaskStatusSuccess {
		t.Fatalf("上传终态 = %s err=%s", u.Status, u.Error)
	}
	if u.TargetRef == "" || u.ChartName != "mychart" || u.ChartVersion != "0.1.0" {
		t.Errorf("记录字段不对: %+v", u)
	}
	if pushed != 1 {
		t.Errorf("mock CM 收到 %d 次", pushed)
	}
	bad := []byte("not a tgz")
	w = doMultipart(r, token, "/api/charts/upload-files", "files", "bad.tgz", bad,
		map[string]string{"repo_id": fmt.Sprint(repoID)})
	decode(t, w, &res)
	if len(res.Created) != 0 || len(res.Invalid) != 1 {
		t.Fatalf("非法文件应被跳过: %+v", res)
	}
	if res.Invalid[0].Name != "bad.tgz" || res.Invalid[0].Reason == "" {
		t.Errorf("invalid 信息不对: %+v", res.Invalid[0])
	}
}

func TestUploadFilesValidation(t *testing.T) {
	r, db, token := newAuthed(t)
	seedChartRepo(t, db, "cm2", "chartmuseum", "http://127.0.0.1:1", "")

	good := testutil.BuildChartTgz(t, "c", "1.0.0", "c")

	// 缺 repo_id。
	if w := doMultipart(r, token, "/api/charts/upload-files", "files", "a.tgz", good, nil); w.Code != http.StatusBadRequest {
		t.Errorf("缺 repo_id 应 400: %d", w.Code)
	}
	// repo 不存在。
	if w := doMultipart(r, token, "/api/charts/upload-files", "files", "a.tgz", good,
		map[string]string{"repo_id": "999"}); w.Code != 404 {
		t.Errorf("repo 不存在应 404: %d", w.Code)
	}
	// 非 multipart body。
	req := httptest.NewRequest(http.MethodPost, "/api/charts/upload-files", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("非 multipart 应 400: %d", w.Code)
	}
}

func TestUploadPaths(t *testing.T) {
	r, db, token := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm3", "chartmuseum", "http://127.0.0.1:1", "")

	// 准备目录:2 个合法 tgz + 1 个非法 + 1 个非 tgz + 1 个子目录。
	dir := t.TempDir()
	for i, name := range []string{"a-1.0.0.tgz", "b-2.0.0.tgz"} {
		parts := strings.SplitN(name, "-", 2)
		if err := os.WriteFile(filepath.Join(dir, name), testutil.BuildChartTgz(t, parts[0], strings.TrimSuffix(parts[1], ".tgz"), parts[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	os.WriteFile(filepath.Join(dir, "bad.tgz"), []byte("junk"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

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
		"repo_id": repoID, "paths": []string{dir, filepath.Join(dir, "a-1.0.0.tgz"), filepath.Join(dir, "missing.tgz"), ""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("upload-paths 失败: %d %s", w.Code, w.Body.String())
	}
	decode(t, w, &res)
	// 目录扫描(2 合法 + bad.tgz 被解析拒绝)+ 单文件(重复提交) + missing 无效。
	if len(res.Created) != 3 {
		t.Fatalf("created = %d (res=%+v)", len(res.Created), res)
	}
	if len(res.Invalid) != 2 {
		t.Errorf("invalid = %+v", res.Invalid)
	}

	// 参数校验。
	if w := do(r, token, http.MethodPost, "/api/charts/upload-paths", map[string]any{"repo_id": 0, "paths": []string{"x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("缺 repo_id 应 400: %d", w.Code)
	}
	if w := do(r, token, http.MethodPost, "/api/charts/upload-paths", map[string]any{"repo_id": 999, "paths": []string{"x"}}); w.Code != 404 {
		t.Errorf("repo 不存在应 404: %d", w.Code)
	}
}

func TestChartUploadsListAndRetry(t *testing.T) {
	r, db, token := newAuthed(t)
	repoID := seedChartRepo(t, db, "cm4", "chartmuseum", "http://127.0.0.1:1", "")

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.tgz"), []byte("keep"), 0o644)

	records := []model.ChartUpload{
		{ChartRepoID: repoID, RepoName: "cm4", RepoType: "chartmuseum", FileName: "ok.tgz", Status: model.TaskStatusSuccess},
		{ChartRepoID: repoID, RepoName: "cm4", RepoType: "chartmuseum", FileName: "fail.tgz", Status: model.TaskStatusFailed, FilePath: filepath.Join(dir, "x.tgz")},
		{ChartRepoID: repoID, RepoName: "cm4", RepoType: "chartmuseum", FileName: "nofile.tgz", Status: model.TaskStatusFailed, FilePath: filepath.Join(dir, "gone.tgz")},
		{ChartRepoID: repoID, RepoName: "cm4", RepoType: "chartmuseum", FileName: "run.tgz", Status: model.TaskStatusRunning},
	}
	for i := range records {
		db.Create(&records[i])
	}

	// List 全量 + 状态过滤。
	var env struct {
		Total int64 `json:"total"`
	}
	w := do(r, token, http.MethodGet, "/api/charts/uploads", nil)
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Total != 4 {
		t.Fatalf("total = %d", env.Total)
	}
	w = do(r, token, http.MethodGet, "/api/charts/uploads?status=failed", nil)
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Total != 2 {
		t.Fatalf("failed total = %d", env.Total)
	}

	// Retry:非 failed 400。
	if w := do(r, token, http.MethodPost, "/api/charts/uploads/1/retry", nil); w.Code != http.StatusBadRequest {
		t.Errorf("非 failed 重试应 400: %d", w.Code)
	}
	// Retry:源文件消失 400。
	if w := do(r, token, http.MethodPost, "/api/charts/uploads/3/retry", nil); w.Code != http.StatusBadRequest {
		t.Errorf("文件消失应 400: %d %s", w.Code, w.Body.String())
	}
	// Retry:不存在 404。
	if w := do(r, token, http.MethodPost, "/api/charts/uploads/999/retry", nil); w.Code != 404 {
		t.Errorf("不存在应 404: %d", w.Code)
	}
	// Retry 成功入队(文件保留但内容非法,最终 failed)。
	if w := do(r, token, http.MethodPost, "/api/charts/uploads/2/retry", nil); w.Code != http.StatusOK {
		t.Fatalf("retry 失败: %d %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, waitForTimeout, "重试终态", func() bool {
		var u model.ChartUpload
		db.First(&u, records[1].ID)
		return u.Status == model.TaskStatusFailed // 内容 "keep" 非法 tgz
	})
}

// TestRunOneRepoDeleted 仓库配置被删后推送应置失败并说明。
func TestRunOneRepoDeleted(t *testing.T) {
	_, db, _ := newAuthed(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "c.tgz")
	os.WriteFile(p, testutil.BuildChartTgz(t, "c", "1.0.0", "c"), 0o644)

	u := model.ChartUpload{ChartRepoID: 999, FileName: "c.tgz", FilePath: p, Status: model.TaskStatusPending}
	db.Create(&u)

	h := NewChartUploadHandler(db)
	h.runOne(u.ID)

	var after model.ChartUpload
	db.First(&after, u.ID)
	if after.Status != model.TaskStatusFailed || !strings.Contains(after.Error, "仓库配置已被删除") {
		t.Fatalf("终态不对: %+v", after)
	}
}
