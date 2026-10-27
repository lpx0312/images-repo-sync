package cli

import (
	"encoding/json"
	"net/http"

	"strings"
	"testing"
)

func TestRegistriesListCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/registries" {
			dataResp(w, []map[string]any{
				{"id": 1, "name": "harbor", "type": "harbor", "host": "h.example.com",
					"username": "u", "has_password": true, "insecure": false, "default_project": "lib"},
			})
			return
		}
		errResp(w, 404, "no")
	})
	if err := cmdRegistriesList(a, nil); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	out := readOut(t, a)
	for _, want := range []string{"共 1 个", "harbor", "h.example.com", "是"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q: %q", want, out)
		}
	}

	// JSON 模式。
	a.JSONOut = true
	if err := cmdRegistriesList(a, nil); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a); !strings.Contains(out, `"name": "harbor"`) {
		t.Errorf("json: %q", out)
	}
}

func TestRegistriesCreateCmd(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"id": 7, "name": "new", "host": "h"})
	})
	if err := cmdRegistriesCreate(a, []string{
		"--name", "new", "--host", "h.example.com", "--type", "harbor",
		"--username", "u", "--password", "p", "--default-project", "lib",
	}); err != nil {
		t.Fatalf("create 失败: %v", err)
	}
	if gotBody["host"] != "h.example.com" || gotBody["password"] != "p" || gotBody["insecure"] != false {
		t.Errorf("请求体: %v", gotBody)
	}
	if !strings.Contains(readOut(t, a), "ID 7") {
		t.Errorf("输出: %q", readOut(t, a))
	}

	// 缺必填。
	if err := cmdRegistriesCreate(a, []string{"--name", "x"}); err == nil || !strings.Contains(err.Error(), "必填") {
		t.Errorf("缺 host 应报错: %v", err)
	}
	// flag 错误。
	if err := cmdRegistriesCreate(a, []string{"--nope"}); err == nil {
		t.Error("未知 flag 应报错")
	}
	// 不带密码:请求体不应含 password 字段。
	var gotBody2 map[string]any
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody2)
		dataResp(w, map[string]any{"id": 8, "name": "n", "host": "h"})
	})
	if err := cmdRegistriesCreate(a2, []string{"--name", "n", "--host", "h"}); err != nil {
		t.Fatal(err)
	}
	if _, has := gotBody2["password"]; has {
		t.Error("空密码不应提交 password 字段")
	}
}

func TestRegistriesUpdateCmd(t *testing.T) {
	var gotBody map[string]any
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			dataResp(w, []map[string]any{
				{"id": 3, "name": "cur", "type": "generic", "host": "old.example.com",
					"username": "u", "has_password": true, "insecure": true, "default_project": "dp"},
			})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"id": 3, "name": "cur", "host": "new.example.com"})
	})
	// 只改 host:其余字段应预填当前值。
	if err := cmdRegistriesUpdate(a, []string{"3", "--host", "new.example.com"}); err != nil {
		t.Fatalf("update 失败: %v", err)
	}
	if gotBody["name"] != "cur" || gotBody["host"] != "new.example.com" ||
		gotBody["type"] != "generic" || gotBody["insecure"] != true || gotBody["default_project"] != "dp" {
		t.Errorf("预填默认值不对: %v", gotBody)
	}
	// 缺参数 / ID 非法 / 不存在的 ID。
	if err := cmdRegistriesUpdate(a, nil); err == nil {
		t.Error("缺 ID 应报错")
	}
	if err := cmdRegistriesUpdate(a, []string{"abc"}); err == nil {
		t.Error("非数字 ID 应报错")
	}
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, []map[string]any{})
	})
	if err := cmdRegistriesUpdate(a2, []string{"3"}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("ID 不存在应报错: %v", err)
	}
}

func TestRegistriesDeleteCmd(t *testing.T) {
	var deleted bool
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			dataResp(w, map[string]any{"message": "ok"})
			return
		}
		errResp(w, 404, "no")
	})
	// 未加 --yes 拒绝。
	if err := cmdRegistriesDelete(a, []string{"5"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("应要求 --yes: %v", err)
	}
	if deleted {
		t.Fatal("未确认不应发起删除")
	}
	if err := cmdRegistriesDelete(a, []string{"5", "--yes"}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if !deleted || !strings.Contains(readOut(t, a), "已删除") {
		t.Error("删除未执行")
	}
	// 服务端报错透传。
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		errResp(w, 500, "boom")
	})
	if err := cmdRegistriesDelete(a2, []string{"5", "--yes"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误透传: %v", err)
	}
}

func TestRegistriesTestCmd(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": true, "message": "连接成功", "endpoint": "https://h/v2/"})
	})
	if err := cmdRegistriesTest(a, []string{"1"}); err != nil {
		t.Fatalf("test 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "连接正常") {
		t.Errorf("输出: %q", out)
	}

	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": false, "message": "认证失败", "detail": "401"})
	})
	if err := cmdRegistriesTest(a2, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a2); !strings.Contains(out, "连接失败") || !strings.Contains(out, "401") {
		t.Errorf("输出: %q", out)
	}
	// 缺参数。
	if err := cmdRegistriesTest(a, nil); err == nil {
		t.Error("缺 ID 应报错")
	}
}

func TestCatalogCommands(t *testing.T) {
	mux := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects"):
			dataResp(w, map[string]any{"ok": true, "supported": true,
				"projects": []string{"alpha", "beta"}, "default_project": ""})
		case strings.HasSuffix(r.URL.Path, "/repos"):
			dataResp(w, map[string]any{"ok": true, "repos": []string{"library/nginx", "team/app"}})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			dataResp(w, map[string]any{"ok": true, "tags": []string{"1.25", "1.26"}, "repo": "r/library/nginx"})
		default:
			errResp(w, 404, "no")
		}
	}

	// projects。
	a, _ := apiServer(t, mux)
	if err := cmdCatalogProjects(a, []string{"1"}); err != nil {
		t.Fatalf("projects 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "共 2 个项目") || !strings.Contains(out, "alpha") {
		t.Errorf("输出: %q", out)
	}
	// 不支持的类型。
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": false, "supported": false, "message": "不支持", "default_project": "dp"})
	})
	if err := cmdCatalogProjects(a2, []string{"1"}); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Errorf("不支持应报错: %v", err)
	}
	// ok=false。
	a3, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": false, "supported": true, "message": "网络错误"})
	})
	if err := cmdCatalogProjects(a3, []string{"1"}); err == nil || !strings.Contains(err.Error(), "网络错误") {
		t.Errorf("失败应报错: %v", err)
	}

	// repos 带 project/q 过滤参数。
	var gotQuery string
	a4, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		dataResp(w, map[string]any{"ok": true, "repos": []string{"library/nginx"}})
	})
	if err := cmdCatalogRepos(a4, []string{"1", "--project", "p1", "--q", "nginx"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "project=p1") || !strings.Contains(gotQuery, "q=nginx") {
		t.Errorf("query = %q", gotQuery)
	}

	// tags:--repo 必填。
	a5, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"ok": true, "tags": []string{"1.25", "1.26"}, "repo": "r/library/nginx"})
	})
	if err := cmdCatalogTags(a5, []string{"1"}); err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Errorf("缺 repo 应报错: %v", err)
	}
	if err := cmdCatalogTags(a5, []string{"1", "--repo", "library/nginx"}); err != nil {
		t.Fatal(err)
	}
	if out := readOut(t, a5); !strings.Contains(out, "r/library/nginx:1.25") {
		t.Errorf("输出: %q", out)
	}
}

func TestSettingsCommands(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		dataResp(w, map[string]any{"default_arch": "amd64"})
	})
	if err := cmdSettingsGet(a, nil); err != nil {
		t.Fatalf("get 失败: %v", err)
	}
	if out := readOut(t, a); !strings.Contains(out, "default_arch") || !strings.Contains(out, "amd64") {
		t.Errorf("输出: %q", out)
	}

	// set 校验。
	if err := cmdSettingsSet(a, nil); err == nil || !strings.Contains(err.Error(), "至少指定") {
		t.Errorf("空设置应报错: %v", err)
	}
	if err := cmdSettingsSet(a, []string{"--default-arch", "riscv"}); err == nil {
		t.Error("非法架构应报错")
	}
	var gotBody map[string]any
	a2, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		dataResp(w, map[string]any{"message": "ok"})
	})
	if err := cmdSettingsSet(a2, []string{"--default-arch", "all"}); err != nil {
		t.Fatal(err)
	}
	if gotBody["default_arch"] != "all" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestSortedKeys(t *testing.T) {
	if got := sortedKeys(map[string]string{"b": "1", "a": "2", "c": "3"}); len(got) != 3 ||
		got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("sortedKeys = %v", got)
	}
}

// TestParseIDArgEdge id 解析边界。
func TestParseIDArgEdge(t *testing.T) {
	if _, err := parseIDArg("x", "12a"); err == nil {
		t.Error("含字母应报错")
	}
	if _, err := parseIDArg("x", "99999999999999999999"); err == nil {
		t.Error("超长数字应报错")
	}
	if _, err := parseIDArg("x", ""); err == nil {
		t.Error("空串应报错")
	}
	if v, err := parseIDArg("x", "42"); err != nil || v != 42 {
		t.Errorf("42 = %d %v", v, err)
	}

}
