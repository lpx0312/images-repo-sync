package skopeo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"images-repo-sync/internal/config"
)

// LineHandler 接收命令的每一行输出(stdout+stderr 合并后按行)。
type LineHandler func(line string)

// runWithStream 执行 skopeo 子命令,把 stdout/stderr 按行实时回调给 handler。
//
// 返回命令的合并退出错误(若被取消则返回 ctx.Err())。
// 调用方可通过 cancel context 来终止运行中的命令(用于任务取消)。
func runWithStream(ctx context.Context, args []string, handler LineHandler) error {
	bin := config.AppConfig.SkopeoBin
	cmd := exec.CommandContext(ctx, bin, args...)
	// 隔离环境,避免继承宿主 DOCKER_CONFIG 等。
	cmd.Env = []string{"PATH=" + defaultPath()}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 skopeo 失败: %w", err)
	}

	// 合并 stdout+stderr 按行回调。顺序按各自读到的先后。
	// 串行化回调:两个读取 goroutine 只负责把行送入 channel,由下方单循环依次
	// 调用 handler —— handler 可能是非并发安全的(如 copy 的 ringBuffer),
	// 并发调用会触发数据竞争;经 channel 单点消费后回调天然串行且行不丢。
	lines := make(chan string, 128)
	var wg sync.WaitGroup
	wg.Add(2)
	pump := func(r io.Reader) {
		defer wg.Done()
		// 用 Reader+ReadString 而非 Scanner:Scanner 有 1MB 单行上限,
		// 超限即停读,子进程会阻塞在写满的管道上;ReadString 无长度限制,
		// 超长行也继续排空(整段作为一个回调行),保留逐行回调语义。
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				lines <- strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			}
			if err != nil {
				return // io.EOF 或读错误
			}
		}
	}
	go pump(stdout)
	go pump(stderr)
	go func() {
		wg.Wait()
		close(lines)
	}()

	for line := range lines {
		if handler != nil {
			handler(line)
		}
	}

	if err := cmd.Wait(); err != nil {
		// 被取消时优先返回 ctx 的错误,语义更清晰。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// defaultPath 返回子进程 PATH,确保能找到 skopeo。
func defaultPath() string {
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

// captureStdout 执行 skopeo 子命令,仅收集 stdout 全文(忽略 stderr)。
// 供需要解析完整 JSON 输出的命令使用(如 list-tags),避免 stderr 行混入。
func captureStdout(ctx context.Context, args []string) (string, error) {
	bin := config.AppConfig.SkopeoBin
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"PATH=" + defaultPath()}
	out, err := cmd.Output() // 只取 stdout;退出错误中附带 stderr 片段
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg := strings.TrimSpace(string(ee.Stderr))
			if len(msg) > 200 {
				msg = msg[:200] + "..."
			}
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}
