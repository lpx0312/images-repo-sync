package skopeo

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListTags 返回某 repo 的全部 tag(via skopeo list-tags)。
// repo 形如 docker.io/library/nginx 或 harbor.corp/library/nginx(不含 docker://)。
func ListTags(ctx context.Context, repo, host, user, pass string, insecure bool) ([]string, error) {
	authPath, err := WriteAuthFile(host, user, pass)
	if err != nil {
		return nil, err
	}
	defer CleanupAuthFiles(authPath)

	args := []string{"list-tags", "--authfile", authPath, "docker://" + repo}
	if insecure {
		args = []string{"list-tags", "--tls-verify=false", "--authfile", authPath, "docker://" + repo}
	}

	var raw string
	err = runWithStream(ctx, args, func(line string) { raw = line })
	if err != nil {
		return nil, err
	}

	// skopeo list-tags 输出 JSON: {"Repository":"...","Tags":["..."]}
	var out struct {
		Repository string   `json:"Repository"`
		Tags       []string `json:"Tags"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("解析 list-tags 输出失败: %w (raw=%q)", err, raw)
	}
	return out.Tags, nil
}

// _catalog 不可用(go-containerregistry 更合适),此处暂用 list-tags 即可。
// 提供 CatalogRepos 占位:对 Harbor 走专用接口;详见 api/catalog.go。

// BinVersion 返回 skopeo 版本字符串,用于启动期自检。
func BinVersion(ctx context.Context) string {
	var v string
	_ = runWithStream(ctx, []string{"--version"}, func(line string) { v = line })
	return v
}
