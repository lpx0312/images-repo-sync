// Package chart 实现 chart tgz 包解析与向 OCI registry / ChartMuseum 的推送。
//
// 不依赖 helm 二进制:OCI 推送直接实现 Registry v2 API(与 helm push 的
// artifact 结构完全一致:helm.config + chart tgz layer + OCI manifest),
// ChartMuseum 推送走其标准 POST /api/charts 接口。
package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Meta 是 Chart.yaml 中的关键元数据,用于生成 OCI config blob 与目标 tag。
// json tag 同时决定 OCI config 的序列化字段(helm 端按同名字段反解)。
type Meta struct {
	APIVersion  string `yaml:"apiVersion" json:"apiVersion,omitempty"`
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Type        string `yaml:"type" json:"type,omitempty"`
	AppVersion  string `yaml:"appVersion" json:"appVersion,omitempty"`
	Description string `yaml:"description" json:"description,omitempty"`
}

// maxChartYAML 限制 Chart.yaml 读取上限,防止异常大包撑爆内存。
const maxChartYAML = 1 << 20

// tagRe 是 OCI tag 的合法字符集(chart version 作为 tag 推送,需先校验)。
var tagRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// ParseFile 读取磁盘上的 chart tgz 并解析元数据,返回元数据与文件大小。
func ParseFile(path string) (*Meta, int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("文件不存在或不可访问: %w", err)
	}
	if st.IsDir() {
		return nil, 0, fmt.Errorf("路径是目录,不是 tgz 文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("读取文件失败: %w", err)
	}
	m, err := ParseBytes(data)
	if err != nil {
		return nil, 0, err
	}
	return m, st.Size(), nil
}

// ParseBytes 从 tgz 字节流中定位并解析 Chart.yaml。
// helm 打包固定为「顶层目录/Chart.yaml」,这里同时兼容无顶层目录的裸包。
func ParseBytes(data []byte) (*Meta, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 gzip/tgz 文件: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("包内未找到 Chart.yaml,不是有效的 helm chart 包")
		}
		if err != nil {
			return nil, fmt.Errorf("读取 tar 失败(文件可能损坏): %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name != "Chart.yaml" && !isTopLevelChartYAML(name) {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, maxChartYAML))
		if err != nil {
			return nil, fmt.Errorf("读取 Chart.yaml 失败: %w", err)
		}
		return parseMeta(raw)
	}
}

// isTopLevelChartYAML 判断 tar 条目是否为「单层目录/Chart.yaml」。
func isTopLevelChartYAML(name string) bool {
	return strings.HasSuffix(name, "/Chart.yaml") && strings.Count(name, "/") == 1
}

// parseMeta 解析 Chart.yaml 文本并校验必填字段。
func parseMeta(raw []byte) (*Meta, error) {
	var m Meta
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 Chart.yaml 失败: %w", err)
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	if m.Name == "" || m.Version == "" {
		return nil, fmt.Errorf("Chart.yaml 缺少 name 或 version 字段")
	}
	if !tagRe.MatchString(m.Version) {
		return nil, fmt.Errorf("chart version %q 不是合法的 tag(仅允许字母数字与 . _ -)", m.Version)
	}
	return &m, nil
}
