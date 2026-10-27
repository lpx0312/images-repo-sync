package cli

import (
	"net/http"
	"testing"
)

// TestAllCommandsAPIError 每个命令在服务端 500 时的错误分支:
// 错误应原样透传给调用方(进程退出码 1)。
func TestAllCommandsAPIError(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		errResp(w, 500, "server boom")
	})

	cmds := []struct {
		name string
		fn   func(a *App, args []string) error
		args []string
	}{
		{"whoami", cmdWhoami, nil},
		{"registries list", cmdRegistriesList, nil},
		{"registries create", cmdRegistriesCreate, []string{"--name", "n", "--host", "h"}},
		{"registries update", cmdRegistriesUpdate, nil},
		{"registries delete", cmdRegistriesDelete, []string{"1", "--yes"}},
		{"registries test", cmdRegistriesTest, []string{"1"}},
		{"catalog projects", cmdCatalogProjects, []string{"1"}},
		{"catalog repos", cmdCatalogRepos, []string{"1"}},
		{"catalog tags", cmdCatalogTags, []string{"1", "--repo", "x"}},
		{"tasks list", cmdTasksList, nil},
		{"tasks get", cmdTasksGet, []string{"1"}},
		{"tasks cancel", cmdTasksCancel, []string{"1"}},
		{"tasks logs", cmdTasksLogs, []string{"1"}},
		{"chart-repos list", cmdChartReposList, nil},
		{"chart-repos create", cmdChartReposCreate, []string{"--name", "n", "--type", "oci", "--host", "h", "--project", "p"}},
		{"chart-repos delete", cmdChartReposDelete, []string{"1", "--yes"}},
		{"chart-repos test", cmdChartReposTest, []string{"1"}},
		{"charts upload-path", cmdChartsUploadPath, []string{"9", "/x/y.tgz", "--no-wait"}},
		{"charts uploads", cmdChartsUploads, nil},
		{"settings get", cmdSettingsGet, nil},
		{"settings set", cmdSettingsSet, []string{"--default-arch", "all"}},
	}
	for _, c := range cmds {
		if err := c.fn(a, c.args); err == nil {
			t.Errorf("%s: 服务端 500 应报错", c.name)
		}
	}

	// registries update 需要 fetch 成功,单独用可成功 list 的 server 测透传。
	b, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/registries" {
			dataResp(w, []map[string]any{{"id": 1, "name": "n", "type": "generic", "host": "h"}})
			return
		}
		errResp(w, 500, "boom")
	})
	if err := cmdRegistriesUpdate(b, []string{"1"}); err == nil {
		t.Error("registries update PUT 500 应报错")
	}

	// chart-repos update 同理。
	d, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/chart-repos" {
			dataResp(w, []map[string]any{{"id": 1, "name": "n", "type": "oci", "host": "h", "project": "p"}})
			return
		}
		errResp(w, 500, "boom")
	})
	if err := cmdChartReposUpdate(d, []string{"1"}); err == nil {
		t.Error("chart-repos update PUT 500 应报错")
	}
}

// TestAllCommandsParamErrors 常见参数错误。
func TestAllCommandsParamErrors(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { errResp(w, 500, "no") })

	badIDs := []struct {
		name string
		fn   func(a *App, args []string) error
	}{
		{"registries update", cmdRegistriesUpdate},
		{"registries delete", cmdRegistriesDelete},
		{"registries test", cmdRegistriesTest},
		{"catalog projects", cmdCatalogProjects},
		{"catalog repos", cmdCatalogRepos},
		{"catalog tags", cmdCatalogTags},
		{"tasks get", cmdTasksGet},
		{"tasks cancel", cmdTasksCancel},
		{"tasks logs", cmdTasksLogs},
		{"chart-repos update", cmdChartReposUpdate},
		{"chart-repos delete", cmdChartReposDelete},
		{"chart-repos test", cmdChartReposTest},
	}
	for _, c := range badIDs {
		if err := c.fn(a, nil); err == nil {
			t.Errorf("%s: 缺参数应报错", c.name)
		}
	}

	// flag 解析错误。
	flagErrs := []struct {
		name string
		fn   func(a *App, args []string) error
	}{
		{"registries list", cmdRegistriesList},
		{"tasks list", cmdTasksList},
		{"chart-repos list", cmdChartReposList},
		{"charts uploads", cmdChartsUploads},
		{"settings get", cmdSettingsGet},
		{"catalog repos", cmdCatalogRepos},
		{"catalog tags", cmdCatalogTags},
		{"charts upload", cmdChartsUpload},
		{"charts upload-path", cmdChartsUploadPath},
		{"charts retry", cmdChartsRetry},
		{"tasks logs", cmdTasksLogs},
	}
	for _, c := range flagErrs {
		if err := c.fn(a, []string{"--definitely-not-a-flag"}); err == nil {
			t.Errorf("%s: 未知 flag 应报错", c.name)
		}
	}
}

// TestTasksCreateFlagError tasks create 的 flag 解析错误。
func TestTasksCreateFlagError(t *testing.T) {
	a, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request) { errResp(w, 500, "no") })
	if err := cmdTasksCreate(a, []string{"--bogus"}); err == nil {
		t.Error("未知 flag 应报错")
	}
	if err := cmdSettingsSet(a, []string{"--bogus"}); err == nil {
		t.Error("settings set 未知 flag 应报错")
	}
	if err := cmdChartReposCreate(a, []string{"--bogus"}); err == nil {
		t.Error("chart-repos create 未知 flag 应报错")
	}
}
