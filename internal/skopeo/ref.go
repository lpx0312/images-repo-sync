package skopeo

import (
	"fmt"
	"strings"

	"images-repo-sync/internal/model"
)

// ParseSource 把用户输入的镜像引用解析为 {host, path}。
//
// 规则:
//   - 无 host 部分(第一个段不含 '.' 且不含 ':' 或不是已知 registry 名)
//     视为隐式 docker.io;对 docker.io/library/* 自动补 library。
//   - host 部分指镜像仓库主机(可含端口),path 指 host 之后到 tag 为止的部分。
//   - path 至少形如 name[:tag] 或 ns/name[:tag]。
//
// 例:
//
//	nginx:1.25                              -> docker.io, library/nginx:1.25
//	bitnami/redis:7.0                       -> docker.io, bitnami/redis:7.0
//	library/nginx                           -> docker.io, library/nginx:latest
//	docker.io/library/nginx:1.25            -> docker.io, library/nginx:1.25
//	gcr.io/k8s-staging/cluster-api/clusterctl:v1  -> gcr.io, k8s-staging/cluster-api/clusterctl:v1
//	harbor.corp.net:5000/ns/img:v1          -> harbor.corp.net:5000, ns/img:v1
func ParseSource(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}

	// 去除可能的 docker:// 前缀。
	raw = strings.TrimPrefix(raw, "docker://")

	// 判断第一段是否是 host:第一个 '/' 之前的部分,若含 '.' 或 ':'(端口)或为 localhost 则视为 host。
	// 若整体不含 '/',说明没有 host 部分(形如 nginx:1.25),host 缺省为 docker.io。
	slash := strings.IndexByte(raw, '/')
	if slash < 0 {
		host = "docker.io"
		path = raw
	} else {
		first := raw[:slash]
		rest := raw[slash+1:]
		if isHost(first) {
			host = first
			path = rest
		} else {
			// 第一段不是 host(如 bitnami/redis:7.0),整体是 path。
			host = "docker.io"
			path = raw
		}
	}

	// docker.io 的官方镜像放在 library/ 下;补齐。
	if (host == "docker.io" || host == "index.docker.com") && !strings.Contains(path, "/") {
		path = "library/" + path
	}

	// 补默认 tag。
	if path != "" && !strings.Contains(tagPart(path), ":") {
		path += ":latest"
	}
	return host, path
}

// isHost 判断一段是否是 registry host:含 '.' 或 ':'(端口),或是 localhost。
func isHost(s string) bool {
	if s == "localhost" {
		return true
	}
	return strings.Contains(s, ".") || strings.Contains(s, ":")
}

// tagPart 返回最后一个 '/' 之后的部分(即镜像名:tag)。
func tagPart(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// ResolveTarget 按指定模式把源引用解析为目标引用。
//
//   - ModeFlat:        targetHost/project/<镜像名:tag>      (丢弃中间路径)
//   - ModePreservePath:targetHost/project/<源 host 后完整路径>
//   - ModeReplaceHost: targetHost/<源 host 后完整路径>      (不加 project)
//
// project 在 ModeReplaceHost 下被忽略。
func ResolveTarget(srcRef, targetHost, mode, project string) (string, error) {
	_, srcPath := ParseSource(srcRef)
	if srcPath == "" {
		return "", fmt.Errorf("无效的源镜像引用: %q", srcRef)
	}
	targetHost = strings.TrimRight(strings.TrimSpace(targetHost), "/")
	project = strings.TrimSpace(project)

	switch mode {
	case model.ModeFlat:
		if project == "" {
			return "", fmt.Errorf("模式 %s 需要指定 target project", mode)
		}
		last := tagPart(srcPath)
		return targetHost + "/" + project + "/" + last, nil

	case model.ModePreservePath:
		if project == "" {
			return "", fmt.Errorf("模式 %s 需要指定 target project", mode)
		}
		return targetHost + "/" + project + "/" + srcPath, nil

	case model.ModeReplaceHost:
		return targetHost + "/" + srcPath, nil

	default:
		return "", fmt.Errorf("未知模式: %q", mode)
	}
}
