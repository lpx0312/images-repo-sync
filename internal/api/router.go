package api

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupRouter 创建并配置 Gin 引擎。
//
// staticFS 为前端构建产物(web/dist)的嵌入文件系统(已 Sub 到 dist 根);
// 为 nil 时不挂载前端(开发期由 Vite 代理)。
func SetupRouter(db *gorm.DB, staticFS fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// chart 上传走 multipart,放宽内存缓冲上限(超出部分自动落盘临时文件)。
	r.MaxMultipartMemory = 64 << 20

	// CORS:仅放行本地开发前端(Vite dev server, 通常 :3000/:5173)与同源请求。
	// 生产环境前后端同源,不需要跨域。鉴权用 Bearer token(非 cookie),无需 AllowCredentials。
	r.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			// 同源(空 Origin 或与后端同 host)直接放行。
			if origin == "" {
				return true
			}
			// 本地开发前端。
			for _, p := range []string{"http://localhost:3000", "http://127.0.0.1:3000",
				"http://localhost:5173", "http://127.0.0.1:5173"} {
				if origin == p {
					return true
				}
			}
			return false
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: false,
	}))

	api := r.Group("/api")
	{
		api.GET("/healthz", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})

		NewAuthHandler(db).RegisterRoutes(api)
		NewRegistryHandler(db).RegisterRoutes(api)
		NewCatalogHandler(db).RegisterRoutes(api)
		NewTaskHandler(db).RegisterRoutes(api)
		NewSettingsHandler(db).RegisterRoutes(api)
		NewChartRepoHandler(db).RegisterRoutes(api)
		NewChartUploadHandler(db).RegisterRoutes(api)
	}

	if staticFS != nil {
		// /assets/* 直出静态资源。文件名带内容 hash,内容变即换名,
		// 可放心让浏览器长期缓存(embed 文件无 ModTime,不设置的话连 304 协商都做不了)。
		if _, err := fs.Stat(staticFS, "assets"); err == nil {
			assetsFS, err := fs.Sub(staticFS, "assets")
			if err == nil {
				fileServer := http.StripPrefix("/assets", http.FileServer(http.FS(assetsFS)))
				r.GET("/assets/*filepath", func(c *gin.Context) {
					c.Header("Cache-Control", "public, max-age=31536000, immutable")
					fileServer.ServeHTTP(c.Writer, c.Request)
				})
			}
		}
		// 其余非 /api 路径回退到 index.html,交给前端路由。
		r.NoRoute(func(c *gin.Context) {
			// API 路径的 404 不回退,直接返回 JSON(避免前端拿到 HTML)。
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "接口不存在"})
				return
			}
			// 优先尝试精确文件(favicon 等),否则回退 index.html。
			path := strings.TrimPrefix(c.Request.URL.Path, "/")
			if path != "" {
				if f, err := staticFS.Open(path); err == nil {
					defer f.Close()
					c.Status(http.StatusOK)
					serveContent(c, f)
					return
				}
			}
			if f, err := staticFS.Open("index.html"); err == nil {
				defer f.Close()
				c.Status(http.StatusOK)
				// 入口页禁止长缓存,保证发版后用户能立即拿到新版本的资源引用。
				c.Header("Content-Type", "text/html; charset=utf-8")
				c.Header("Cache-Control", "no-cache")
				_, _ = io.Copy(c.Writer, f)
				return
			}
			c.String(http.StatusNotFound, "frontend not built")
		})
	}

	return r
}

// serveContent 把一个 fs.File 原样输出(用于 favicon 等小文件)。
// 根据扩展名设置正确的 Content-Type,否则浏览器不识别(如 favicon.svg 被当文本)。
func serveContent(c *gin.Context, f fs.File) {
	path := c.Request.URL.Path
	switch {
	case strings.HasSuffix(path, ".svg"):
		c.Header("Content-Type", "image/svg+xml")
	case strings.HasSuffix(path, ".png"):
		c.Header("Content-Type", "image/png")
	case strings.HasSuffix(path, ".ico"):
		c.Header("Content-Type", "image/x-icon")
	case strings.HasSuffix(path, ".json"):
		c.Header("Content-Type", "application/json")
	case strings.HasSuffix(path, ".js"):
		c.Header("Content-Type", "application/javascript")
	case strings.HasSuffix(path, ".css"):
		c.Header("Content-Type", "text/css")
	}
	c.Header("Cache-Control", "no-cache")
	_, _ = io.Copy(c.Writer, f)
}
