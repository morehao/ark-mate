//go:build release

package router

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/web"
)

// MountWeb release 构建：把内嵌前端产物（web.Dist）以 SPA 方式挂到根路径。
// 未命中静态资源的路由回退 index.html；/v1/* 未注册路径仍返回 JSON 404（不是 SPA）。
func MountWeb(r *gin.Engine) {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// make all 未先拷贝前端产物即带 -tags release 编译：显式失败（见 Makefile web 目标）
		panic("web dist 未内嵌：请先执行 make web 再 release 编译")
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web dist 缺少 index.html")
	}
	indexResp := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	hfs := http.FS(sub)

	r.GET("/", indexResp)
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
			return
		}
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if p == "index.html" {
			indexResp(c)
			return
		}
		if _, err := fs.Stat(sub, p); err == nil && p != "" && !strings.HasSuffix(p, "/") {
			c.FileFromFS(p, hfs)
			return
		}
		indexResp(c)
	})
}
