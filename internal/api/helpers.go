package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ok 返回 200 + 标准 {data: ...} 包装。
func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// err 返回指定状态码 + {error: msg}。
func errResp(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// badReq 是 400 的快捷封装。
func badReq(c *gin.Context, msg string) { errResp(c, http.StatusBadRequest, msg) }

// serverErr 是 500 的快捷封装。
func serverErr(c *gin.Context, msg string) { errResp(c, http.StatusInternalServerError, msg) }
