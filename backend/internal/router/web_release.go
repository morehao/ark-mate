//go:build release

package router

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/pkg/web"
)

// currentWebSource 资产来源（/meta 的诊断字段）：release 构建内嵌了前端产物。
func currentWebSource() string { return "builtin" }

// MountWeb release 构建：把内嵌前端产物（web.Dist）以 SPA 方式挂到根路径。
// 未命中静态资源的路由回退 index.html；/v1/* 未注册路径仍回标准信封 404（不是 SPA）。
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
		// 浏览器入口的文档也下发 CSP（与桌面壳同一个头，M1-5）：前端产物不得依赖 eval，
		// 也不允许任何外域连接——这条与"R3 零出站"是同一条纪律的两面。
		setDocumentCSP(c)
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	hfs := http.FS(sub)

	r.GET("/", indexResp)
	r.NoRoute(func(c *gin.Context) {
		if isAPIPath(c.Request.URL.Path) {
			writeNotFound(c)
			return
		}
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if p == "index.html" {
			indexResp(c)
			return
		}
		if _, err := fs.Stat(sub, p); err == nil && p != "" && !strings.HasSuffix(p, "/") {
			setDocumentCSP(c)
			c.FileFromFS(p, hfs)
			return
		}
		indexResp(c)
	})
}
