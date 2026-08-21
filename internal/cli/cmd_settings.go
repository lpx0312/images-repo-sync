package cli

import (
	"fmt"
)

// settingsCommands 系统设置相关命令。
var settingsCommands = []command{
	{name: "settings get", usage: "", desc: "查看系统设置(默认同步架构等)", run: cmdSettingsGet},
	{name: "settings set", usage: "--default-arch amd64|arm64|all", desc: "修改系统设置", run: cmdSettingsSet},
}

func cmdSettingsGet(a *App, args []string) error {
	fs := a.newFlagSet("settings get", "[--json]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var m map[string]string
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "GET", "/api/settings", nil, &m); err != nil {
		return err
	}
	if a.JSONOut {
		return PrintJSON(a.Out(), m)
	}
	keys := sortedKeys(m)
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []string{k, m[k]})
	}
	PrintTable(a.Out(), []string{"设置项", "值"}, rows)
	return nil
}

func cmdSettingsSet(a *App, args []string) error {
	fs := a.newFlagSet("settings set", "--default-arch amd64|arm64|all")
	arch := fs.String("default-arch", "", "默认同步架构: amd64 / arm64 / all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *arch == "" {
		return fmt.Errorf("请至少指定一个要修改的设置项,如 --default-arch amd64")
	}
	switch *arch {
	case "amd64", "arm64", "all":
	default:
		return fmt.Errorf("--default-arch 无效: %s(可选 amd64 / arm64 / all)", *arch)
	}
	body := map[string]any{"default_arch": *arch}
	var res map[string]any
	c, err := a.confirmClient()
	if err != nil {
		return err
	}
	cCtx, cancel := ctx()
	defer cancel()
	if _, err := c.DoJSON(cCtx, "PUT", "/api/settings", body, &res); err != nil {
		return err
	}
	a.Printf("已保存: default_arch=%s\n", *arch)
	return nil
}

// sortedKeys 返回字典序排列的 key 列表(输出稳定便于比对)。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
