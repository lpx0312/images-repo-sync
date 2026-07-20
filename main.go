package main

import (
	"context"
	"embed"
	"fmt"
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
// 开发期若该目录为空,embed 仍可编译(只要有这个目录存在);为空时 staticFS 为 nil。
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

	// 触发 task manager 单例(启动 worker goroutine)。
	_ = task.Instance()

	// 自检 skopeo 是否可用(仅打印,不阻断启动)。
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer probeCancel()
	if v := skopeo.BinVersion(probeCtx); v != "" {
		log.Printf("[main] %s", v)
	} else {
		log.Printf("[main] 警告: 未检测到 skopeo,镜像同步功能将不可用")
	}

	// 准备前端嵌入 FS(若 web/dist 为空目录,则不挂载前端)。
	var staticFS fs.FS
	if entries, err := webDist.ReadDir("web/dist"); err == nil && len(entries) > 0 {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[main] 关闭异常: %v", err)
	}
	log.Println("[main] 已退出")
}

// keepEmbed 确保编译期保留 embed 指令的引用(避免被 go vet 误判)。
var _ = fmt.Sprint
