// fakeskopeo 是 skopeo 命令的测试替身,供各包测试通过
// config.AppConfig.SkopeoBin 指向它来模拟真实的镜像复制行为。
//
// 支持的命令与行为(与真实 skopeo 的输出契约保持一致):
//
//	skopeo --version                       → stdout 输出版本行
//	skopeo list-tags ... docker://R        → stdout 输出 {"Repository":..,"Tags":[..]}
//	skopeo inspect --format {{.Digest}} .. → stdout 输出 sha256:...
//	skopeo copy ... docker://S docker://D  → 按引用中的关键字决定成败(见下)
//
// copy 的行为由源/目标引用中包含的关键字控制(任一命中即生效):
//
//	failcopy          → 失败,stderr 含 unauthorized(模拟认证失败)
//	noarch            → 失败,stderr 含 no image found in manifest list for architecture
//	preserveconflict  → 失败,stderr 含 Instructed to preserve digests
//	manifestunknown   → 失败,stderr 含 manifest unknown
//	slowcopy          → 睡 30s 模拟大镜像(测试取消;进程被 kill 后立即退出)
//	其余               → 成功,stdout/stderr 输出若干进度行
package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const fakeDigest = "sha256:faaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake skopeo: 缺少参数")
		os.Exit(1)
	}

	switch subcommand(args) {
	case "--version":
		fmt.Println("skopeo version 1.14.0-fake")
		return
	case "list-tags":
		repo := lastRef(args)
		fmt.Printf("{\n    \"Repository\": %q,\n    \"Tags\": [\n        \"1.25\",\n        \"1.26\"\n    ]\n}\n", repo)
		return
	case "inspect":
		fmt.Println(fakeDigest)
		return
	case "copy":
		src, dst := copyRefs(args)
		joined := src + " " + dst
		hasPreserve := false
		for _, a := range args {
			if a == "--preserve-digests" {
				hasPreserve = true
			}
		}
		fmt.Fprintf(os.Stderr, "Getting image list signatures\n")
		switch {
		case strings.Contains(joined, "slowcopy"):
			fmt.Fprintf(os.Stderr, "Copying blob ... (slow)\n")
			time.Sleep(30 * time.Second)
			fmt.Fprintf(os.Stderr, "unexpected exit\n")
			os.Exit(1)
		case strings.Contains(joined, "failcopy"):
			fmt.Fprintf(os.Stderr, "unauthorized: authentication required\n")
			os.Exit(1)
		case strings.Contains(joined, "noarch"):
			fmt.Fprintf(os.Stderr, "no image found in manifest list for architecture: amd64, variant \"\"\n")
			os.Exit(1)
		case strings.Contains(joined, "preserveconflict"):
			fmt.Fprintf(os.Stderr, "Instructed to preserve digests, but the requested transformation would change the digest\n")
			os.Exit(1)
		case strings.Contains(joined, "conflictonce") && hasPreserve:
			// 仅在带 --preserve-digests 时失败;去掉该参数重试则成功(模拟 SWR 场景)。
			fmt.Fprintf(os.Stderr, "Instructed to preserve digests, but the requested transformation would change the digest\n")
			os.Exit(1)
		case strings.Contains(joined, "manifestunknown"):
			fmt.Fprintf(os.Stderr, "manifest unknown\n")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Copying blob sha256:aaaa done\n")
		fmt.Fprintf(os.Stderr, "Writing manifest to image destination\n")
		fmt.Println("Storing signatures")
		return
	}
	fmt.Fprintf(os.Stderr, "fake skopeo: 不支持的命令 %q", subcommand(args))
	os.Exit(1)
}

// subcommand 返回第一个非 flag 参数(--override-arch 等全局 flag 可能在子命令之前)。
func subcommand(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	// 全是 flag:--version 之类。
	return args[0]
}

// lastRef 返回最后一个 docker:// 引用(去前缀)。
func lastRef(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if strings.HasPrefix(args[i], "docker://") {
			return strings.TrimPrefix(args[i], "docker://")
		}
	}
	return ""
}

// copyRefs 返回 copy 命令的源与目标引用。
func copyRefs(args []string) (src, dst string) {
	var refs []string
	for _, a := range args {
		if strings.HasPrefix(a, "docker://") {
			refs = append(refs, strings.TrimPrefix(a, "docker://"))
		}
	}
	if len(refs) >= 1 {
		src = refs[0]
	}
	if len(refs) >= 2 {
		dst = refs[1]
	}
	return src, dst
}
