package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"images-repo-sync/internal/config"
	"images-repo-sync/internal/model"
	"images-repo-sync/internal/testutil"
)

// tlsRegistry 起 TLS mock registry(catalog 走 https;测试客户端开 insecure)。
// handler 返回的 host 是裸 host(无 scheme),registries 记录需 insecure=true。
func tlsRegistry(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "https://")
}

func TestCatalogReposGeneric(t *testing.T) {
	host := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/_catalog" {
			fmt.Fprint(w, `{"repositories":["library/nginx","team/app","team/redis"]}`)
			return
		}
		w.WriteHeader(404)
	})

	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "gen", Host: host, Type: "generic", Username: "u", Insecure: true}
	db.Create(&reg)

	// 全量。
	var res struct {
		OK    bool     `json:"ok"`
		Repos []string `json:"repos"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg.ID), 10)+"/repos", nil), &res)
	if !res.OK || len(res.Repos) != 3 {
		t.Fatalf("repos = %+v", res)
	}

	// q 过滤(大小写不敏感)。
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg.ID), 10)+"/repos?q=NGINX", nil), &res)
	if !res.OK || len(res.Repos) != 1 || res.Repos[0] != "library/nginx" {
		t.Fatalf("过滤 = %+v", res)
	}

	// 404 不支持 → ok=false 提示改粘贴。
	host404 := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	reg2 := model.Registry{Name: "no404", Host: host404, Type: "generic", Insecure: true}
	db.Create(&reg2)
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg2.ID), 10)+"/repos", nil), &res)
	if res.OK || !strings.Contains(strings.Join(res.Repos, ""), "") {
		t.Fatalf("404 应 ok=false: %+v", res)
	}

	// 401 认证失败。
	host401 := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	reg3 := model.Registry{Name: "denied", Host: host401, Type: "generic", Insecure: true}
	db.Create(&reg3)
	w := do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg3.ID), 10)+"/repos", nil)
	var res3 struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	decode(t, w, &res3)
	if res3.OK || !strings.Contains(res3.Message, "认证失败") {
		t.Fatalf("401: %+v", res3)
	}

	// 仓库不存在 404。
	if w := do(r, token, http.MethodGet, "/api/catalog/999/repos", nil); w.Code != 404 {
		t.Errorf("仓库不存在应 404: %d", w.Code)
	}
}

func TestCatalogReposHarbor(t *testing.T) {
	// Harbor:projects 翻页 + 每 project 的 repositories(名字已带前缀)。
	host := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2.0/projects":
			page := r.URL.Query().Get("page")
			if page == "1" {
				fmt.Fprint(w, `[{"name":"proj-a"},{"name":"proj-b"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.URL.Path == "/api/v2.0/projects/proj-a/repositories":
			fmt.Fprint(w, `[{"name":"proj-a/nginx"},{"name":"proj-a/redis"}]`)
		case r.URL.Path == "/api/v2.0/projects/proj-b/repositories":
			fmt.Fprint(w, `[{"name":"app"}]`) // 旧版:名字不带前缀
		default:
			w.WriteHeader(404)
		}
	})

	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "hb", Host: host, Type: "harbor", Insecure: true}
	db.Create(&reg)

	var res struct {
		OK    bool     `json:"ok"`
		Repos []string `json:"repos"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg.ID), 10)+"/repos", nil), &res)
	if !res.OK {
		t.Fatalf("ok=false: %+v", res)
	}
	want := []string{"proj-a/nginx", "proj-a/redis", "proj-b/app"} // 已带前缀不重复拼;旧版缺前缀才补
	if len(res.Repos) != 3 || res.Repos[0] != want[0] || res.Repos[2] != want[2] {
		t.Fatalf("repos = %v, want %v", res.Repos, want)
	}

	// project 过滤。
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg.ID), 10)+"/repos?project=proj-a", nil), &res)
	if !res.OK || len(res.Repos) != 2 {
		t.Fatalf("project 过滤 = %+v", res)
	}
}

func TestCatalogProjects(t *testing.T) {
	host := tlsRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2.0/projects" {
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"name":"alpha"},{"name":"beta"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
			return
		}
		w.WriteHeader(404)
	})

	r, db, token := newAuthed(t)
	hb := model.Registry{Name: "hb2", Host: host, Type: "harbor", Insecure: true, DefaultProject: "alpha"}
	db.Create(&hb)
	gen := model.Registry{Name: "gen2", Host: "g.example.com", Type: "generic", DefaultProject: "dp"}
	db.Create(&gen)

	var res struct {
		OK             bool     `json:"ok"`
		Supported      bool     `json:"supported"`
		Projects       []string `json:"projects"`
		DefaultProject string   `json:"default_project"`
	}
	// Harbor:支持并列出。
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(hb.ID), 10)+"/projects", nil), &res)
	if !res.OK || !res.Supported || len(res.Projects) != 2 || res.DefaultProject != "alpha" {
		t.Fatalf("harbor projects = %+v", res)
	}
	// 非 Harbor:不支持,带默认项目。
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(gen.ID), 10)+"/projects", nil), &res)
	if res.OK || res.Supported || res.DefaultProject != "dp" {
		t.Fatalf("generic projects = %+v", res)
	}
	// Harbor 请求失败 → ok=false 但 supported=true。
	hbBad := model.Registry{Name: "hb3", Host: "127.0.0.1:1", Type: "harbor"}
	db.Create(&hbBad)
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(hbBad.ID), 10)+"/projects", nil), &res)
	if res.OK || !res.Supported {
		t.Fatalf("失败 harbor = %+v", res)
	}
}

func TestCatalogTags(t *testing.T) {
	testutil.UseFakeSkopeo(t)
	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "tg", Host: "reg.example.com", Type: "generic"}
	db.Create(&reg)
	base := "/api/catalog/" + strconv.FormatUint(uint64(reg.ID), 10) + "/tags"

	var res struct {
		OK   bool     `json:"ok"`
		Tags []string `json:"tags"`
		Repo string   `json:"repo"`
	}
	// repo 参数校验。
	if w := do(r, token, http.MethodGet, base+"?repo=", nil); w.Code != http.StatusBadRequest {
		t.Errorf("空 repo 应 400: %d", w.Code)
	}
	if w := do(r, token, http.MethodGet, base, nil); w.Code != http.StatusBadRequest {
		t.Errorf("缺 repo 应 400: %d", w.Code)
	}
	// 非法 repo(host 前缀剥完为空)。
	if w := do(r, token, http.MethodGet, base+"?repo=reg.example.com/", nil); w.Code != http.StatusBadRequest {
		t.Errorf("空 path 应 400: %d", w.Code)
	}

	// 成功:fake skopeo 返回固定 tags。
	decode(t, do(r, token, http.MethodGet, base+"?repo=proj/myapp", nil), &res)
	if !res.OK || len(res.Tags) != 2 || res.Repo != "reg.example.com/proj/myapp" {
		t.Fatalf("tags = %+v", res)
	}

	// 完整引用输入也被锚定到配置 host。
	decode(t, do(r, token, http.MethodGet, base+"?repo=docker.io/library/nginx", nil), &res)
	if !res.OK || res.Repo != "reg.example.com/library/nginx" {
		t.Fatalf("锚定 = %+v", res)
	}

	// 仓库不存在 404。
	if w := do(r, token, http.MethodGet, "/api/catalog/999/tags?repo=x", nil); w.Code != 404 {
		t.Errorf("仓库不存在应 404: %d", w.Code)
	}
}

// TestCatalogTagsSkopeoFailure skopeo 不可用时 ok=false 带错误信息。
func TestCatalogTagsSkopeoFailure(t *testing.T) {
	// 显式指向不存在的二进制(可能被先前测试的 fake 全局状态影响)。
	saved := config.AppConfig.SkopeoBin
	config.AppConfig.SkopeoBin = "definitely-not-exist-skopeo"
	t.Cleanup(func() { config.AppConfig.SkopeoBin = saved })

	r, db, token := newAuthed(t)
	reg := model.Registry{Name: "tg2", Host: "reg2.example.com", Type: "generic"}
	db.Create(&reg)

	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	decode(t, do(r, token, http.MethodGet, "/api/catalog/"+strconv.FormatUint(uint64(reg.ID), 10)+"/tags?repo=x/y", nil), &res)
	if res.OK || res.Message == "" {
		t.Fatalf("skopeo 失败应 ok=false: %+v", res)
	}
}
