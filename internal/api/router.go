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

	// CORS:开发期前端在 :3000、后端在 :8080;生产同源。
	r.Use(cors.New(cors.Config{
		AllowOriginFunc:  func(string) bool { return true },
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
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
	}

	if staticFS != nil {
		// /assets/* 直出静态资源(带 hash 文件名,可长期缓存)。
		assetsFS, err := fs.Sub(staticFS, "assets")
		if err == nil {
			r.StaticFS("/assets", http.FS(assetsFS))
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
				c.Header("Content-Type", "text/html; charset=utf-8")
				_, _ = io.Copy(c.Writer, f)
				return
			}
			c.String(http.StatusNotFound, "frontend not built")
		})
	}

	return r
}

// serveContent 把一个 fs.File 原样输出(用于 favicon 等小文件)。
func serveContent(c *gin.Context, f fs.File) {
	c.Header("Cache-Control", "no-cache")
	_, _ = io.Copy(c.Writer, f)
}
