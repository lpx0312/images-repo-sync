package skopeo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
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
	var wg sync.WaitGroup
	wg.Add(2)
	stream := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			line := s.Text()
			if handler != nil {
				handler(line)
			}
		}
	}
	go stream(stdout)
	go stream(stderr)

	wg.Wait()

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
