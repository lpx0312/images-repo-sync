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

	// skopeo list-tags 输出 JSON: {"Repository":"...","Tags":["..."]}
	// 输出是多行缩进格式,须收集完整 stdout 再解析;stderr 不混入。
	raw, err := captureStdout(ctx, args)
	if err != nil {
		return nil, err
	}

	var out struct {
		Repository string   `json:"Repository"`
		Tags       []string `json:"Tags"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("解析 list-tags 输出失败: %w (raw=%q)", err, truncateForLog(raw))
	}
	return out.Tags, nil
}

// truncateForLog 错误信息里截断过长的原始输出,避免撑爆日志。
func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// _catalog 不可用(go-containerregistry 更合适),此处暂用 list-tags 即可。
// 提供 CatalogRepos 占位:对 Harbor 走专用接口;详见 api/catalog.go。

// BinVersion 返回 skopeo 版本字符串,用于启动期自检。
func BinVersion(ctx context.Context) string {
	var v string
	_ = runWithStream(ctx, []string{"--version"}, func(line string) { v = line })
	return v
}
