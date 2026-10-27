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
//	dualstream        → 成功,goroutine 并发写 stdout/stderr 各 200 行(测调用方双流回调串行化,配 -race)
//	longline          → 成功,先写一行 2MB 无换行(超调用方 bufio.Scanner 1MB 上限)再收尾(测缓冲上限与排空)
//	其余               → 成功,stdout/stderr 输出若干进度行
//
// list-tags 与 inspect 同样按引用关键字控制:
//
//	failtags    → list-tags 失败,stderr 含 unauthorized
//	badjson     → list-tags 输出非法 JSON(测解析错误分支)
//	failinspect → inspect 失败退出 1
//	varydigest  → inspect 按 ref 哈希输出可变 digest(测 digest 差异逻辑)
package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
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
		switch {
		case strings.Contains(repo, "failtags"):
			fmt.Fprintln(os.Stderr, "unauthorized: authentication required")
			os.Exit(1)
		case strings.Contains(repo, "badjson"):
			fmt.Println("{invalid json")
			return
		}
		fmt.Printf("{\n    \"Repository\": %q,\n    \"Tags\": [\n        \"1.25\",\n        \"1.26\"\n    ]\n}\n", repo)
		return
	case "inspect":
		ref := lastRef(args)
		switch {
		case strings.Contains(ref, "failinspect"):
			fmt.Fprintln(os.Stderr, "fake skopeo: inspect failed")
			os.Exit(1)
		case strings.Contains(ref, "varydigest"):
			h := fnv.New64a()
			_, _ = h.Write([]byte(ref))
			fmt.Printf("sha256:%016x%048x\n", h.Sum64(), 0)
			return
		}
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
		case strings.Contains(joined, "dualstream"):
			// 并发写 stdout 与 stderr,驱动调用方双流回调的并发(配合 -race 检测串行化)。
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					fmt.Printf("stdout line %d\n", i)
				}
			}()
			go func() {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					fmt.Fprintf(os.Stderr, "stderr line %d\n", i)
				}
			}()
			wg.Wait()
			return
		case strings.Contains(joined, "longline"):
			// 单行 2MB 无换行,超过调用方 bufio.Scanner 的 1MB 缓冲上限;随后继续收尾输出,
			// 验证调用方在 ErrTooLong 后是否继续排空管道(否则本进程会阻塞在满管道上)。
			fmt.Fprintf(os.Stderr, "%s", strings.Repeat("x", 2*1024*1024))
			fmt.Fprintln(os.Stderr, "Writing manifest to image destination")
			return
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
