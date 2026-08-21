package skopeo

import (
	"context"
	"fmt"
	"strings"
)

// 架构选择常量。
const (
	ArchAMD64 = "amd64" // 仅 x86-64
	ArchARM64 = "arm64" // 仅 ARM 64(如树莓派4/鲲鹏/Graviton)
	ArchAll   = "all"   // 所有架构(完整 manifest list)
)

// CopyOptions 描述一次 skopeo copy 调用的全部参数。
type CopyOptions struct {
	SrcRef      string // 完整源引用,如 gcr.io/foo/bar:v1(不含 docker://)
	SrcAuthFile string // 源 auth.json 路径(可空)
	SrcInsecure bool   // 源跳过 TLS 校验

	DstRef      string // 完整目标引用
	DstAuthFile string // 目标 auth.json 路径(可空)
	DstInsecure bool   // 目标跳过 TLS 校验

	PreserveDigests bool   // 是否保留 digest(--preserve-digests)
	Arch            string // 目标架构: amd64 / arm64 / all;空按 amd64 处理
}

// CopyResult 描述一次 copy 的结果。StderrTail 仅在失败时填充,便于排查。
type CopyResult struct {
	OK         bool
	StderrTail string
}

// Copy 执行 skopeo copy,把镜像从源复制到目标,实时回调输出行。
//
// 架构选择(Arch 字段)对应两种互斥的 skopeo 调用方式:
//   - ArchAll          → skopeo copy --all ...            (复制完整 manifest list)
//   - ArchAMD64/ARM64  → skopeo --override-arch=<arch> --override-os=linux copy ...
//     (仅复制指定架构,目标成为单架构镜像)
//
// 注意:--override-arch 是 skopeo 的全局 flag,必须放在 copy 子命令之前;
// 它与 --all 互斥,不能同时使用。
func Copy(ctx context.Context, opt CopyOptions, handler LineHandler) CopyResult {
	args := buildCopyArgs(opt)

	// 保留 stderr 末尾若干行用于错误定位。
	const tailLines = 6
	tail := newRingBuffer(tailLines)
	err := runWithStream(ctx, args, func(line string) {
		tail.push(line)
		if handler != nil {
			handler(line)
		}
	})
	if err == nil {
		return CopyResult{OK: true}
	}
	detail := err.Error()
	if t := tail.join("\n"); t != "" {
		detail = t
	}
	return CopyResult{OK: false, StderrTail: detail}
}

// buildCopyArgs 按选项拼装 skopeo 命令行参数。
// globalArgs 放 skopeo 的全局 flag(如 --override-arch),copyArgs 放 copy 子命令参数;
// 全局 flag 必须在 "copy" 之前,copy 的 flag(--all/--preserve-digests 等)在 "copy" 之后。
func buildCopyArgs(opt CopyOptions) []string {
	var globalArgs, copyArgs []string
	copyArgs = append(copyArgs, "copy") // 子命令必须先入队

	switch opt.Arch {
	case ArchAll:
		// 复制完整 manifest list 的所有架构。--all 是 copy 的 flag,在 "copy" 之后。
		copyArgs = append(copyArgs, "--all")
	case ArchARM64:
		// 仅复制 arm64 单架构。--override-arch 是全局 flag,在 "copy" 之前。
		globalArgs = append(globalArgs, "--override-arch="+ArchARM64, "--override-os=linux")
	case ArchAMD64, "":
		// 仅复制 amd64 单架构(默认)。
		globalArgs = append(globalArgs, "--override-arch="+ArchAMD64, "--override-os=linux")
	default:
		// 兜底:按 amd64 处理,避免拼出非法命令。
		globalArgs = append(globalArgs, "--override-arch="+ArchAMD64, "--override-os=linux")
	}

	if opt.PreserveDigests {
		copyArgs = append(copyArgs, "--preserve-digests")
	}
	if opt.SrcAuthFile != "" {
		copyArgs = append(copyArgs, "--src-authfile="+opt.SrcAuthFile)
	}
	if opt.DstAuthFile != "" {
		copyArgs = append(copyArgs, "--dest-authfile="+opt.DstAuthFile)
	}
	if opt.SrcInsecure {
		copyArgs = append(copyArgs, "--src-tls-verify=false")
	}
	if opt.DstInsecure {
		copyArgs = append(copyArgs, "--dest-tls-verify=false")
	}
	copyArgs = append(copyArgs, "docker://"+opt.SrcRef, "docker://"+opt.DstRef)

	return append(globalArgs, copyArgs...)
}

// IsPreserveDigestsConflict 判断 copy 失败输出是否属于「--preserve-digests 与目标格式不兼容」:
// 目标仓库拒绝了源镜像的 manifest 格式(如华为 SWR 基础版拒收顶层 OCI image index),
// skopeo 需要转换成目标支持的格式(如 Docker manifest list),但被 --preserve-digests 禁止改写。
// 该场景下去掉 --preserve-digests 重试即可成功(skopeo 在无需转换时仍会保持原 digest)。
func IsPreserveDigestsConflict(stderrTail string) bool {
	return strings.Contains(stderrTail, "Instructed to preserve digests")
}

// ringBuffer 是定长的环形缓冲,只保留最近 N 行,用于截取命令尾部输出。
type ringBuffer struct {
	n   int
	buf []string
	i   int
}

func newRingBuffer(n int) *ringBuffer {
	if n < 1 {
		n = 1
	}
	return &ringBuffer{n: n, buf: make([]string, 0, n)}
}

func (r *ringBuffer) push(s string) {
	if len(r.buf) < r.n {
		r.buf = append(r.buf, s)
		return
	}
	r.buf[r.i] = s
	r.i = (r.i + 1) % r.n
}

func (r *ringBuffer) join(sep string) string {
	if len(r.buf) < r.n {
		// 还没填满,顺序拼接。
		out := ""
		for i, s := range r.buf {
			if i > 0 {
				out += sep
			}
			out += s
		}
		return out
	}
	// 填满后从写入指针处开始顺序读,得到「最早 -> 最新」。
	out := ""
	for k := 0; k < r.n; k++ {
		s := r.buf[(r.i+k)%r.n]
		if k > 0 {
			out += sep
		}
		out += s
	}
	return out
}

// InspectDigest 用 skopeo inspect 取镜像 digest(用于成功后记录目标 digest)。
// authFile 复用调用方已生成的目标仓库 auth 文件,避免每条镜像重复落盘凭证。
func InspectDigest(ctx context.Context, ref, authFile string, insecure bool) (string, error) {
	args := []string{"inspect", "--format", "{{.Digest}}"}
	if insecure {
		args = append(args, "--tls-verify=false")
	}
	if authFile != "" {
		args = append(args, "--authfile", authFile)
	}
	args = append(args, "docker://"+ref)

	// --format 输出单行 digest;取最后一个非空行兜底(skopeo 偶发告警行)。
	var digest string
	if err := runWithStream(ctx, args, func(line string) {
		if s := strings.TrimSpace(line); s != "" {
			digest = s
		}
	}); err != nil {
		return "", fmt.Errorf("inspect 失败: %w", err)
	}
	if digest == "" {
		return "", fmt.Errorf("inspect 未返回 digest")
	}
	return digest, nil
}
