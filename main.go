package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"images-repo-sync/internal/api"
	"images-repo-sync/internal/config"
	"images-repo-sync/internal/skopeo"
	"images-repo-sync/internal/store"
	"images-repo-sync/internal/task"
)

// webDist 嵌入前端构建产物。Dockerfile 在构建后端阶段会先 COPY --from=web /web/dist。
// 仓库里只保留 web/dist/.gitkeep 占位保证目录存在(否则 go:embed 编不过);
// 未构建前端时该目录无 index.html,staticFS 为 nil,由 Vite dev server 提供页面。
//
//go:embed all:web/dist
var webDist embed.FS

func main() {
	if _, err := config.Load(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	if err := store.Init(); err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}

	// 触发 task manager 单例(按 TASK_CONCURRENCY 启动 worker goroutine)。
	_ = task.Instance()
	// 恢复上次进程退出时遗留的任务(running 置 failed,pending 重新入队)。
	task.RecoverStuckTasks()

	// 历史数据保留清理:启动跑一次,之后每天一次。
	retainCtx, retainCancel := context.WithCancel(context.Background())
	go store.StartRetention(retainCtx)

	// 自检 skopeo 是否可用(仅打印,不阻断启动)。
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer probeCancel()
	if v := skopeo.BinVersion(probeCtx); v != "" {
		log.Printf("[main] %s", v)
	} else {
		log.Printf("[main] 警告: 未检测到 skopeo,镜像同步功能将不可用")
	}

	// 准备前端嵌入 FS(仅当前端已构建,即存在 index.html 时挂载)。
	var staticFS fs.FS
	if _, err := fs.Stat(webDist, "web/dist/index.html"); err == nil {
		if dist, err := fs.Sub(webDist, "web/dist"); err == nil {
			staticFS = dist
		}
	}

	r := api.SetupRouter(store.DB, staticFS)

	addr := ":" + config.AppConfig.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("[main] 服务启动: http://0.0.0.0%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	// 优雅退出。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[main] 正在关闭...")

	retainCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[main] 关闭异常: %v", err)
	}
	log.Println("[main] 已退出")
}
