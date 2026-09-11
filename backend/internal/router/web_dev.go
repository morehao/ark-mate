//go:build !release

package router

import "github.com/gin-gonic/gin"

// MountWeb 非 release 构建：不内嵌前端产物（面板走 vite dev proxy 联调）。
//
// 但未知路径仍要回**标准信封 404**。此前这里不注册 NoRoute，请求落到 gin 默认的纯文本
// "404 page not found"，与 release 形态不一致，也让「所有对外错误都走信封、都带注册表里的
// code」这条约定在开发态漏了一角——而开发态恰恰是最常被 curl 到的地方。非 API 路径在 dev 下
// 同样回 JSON：没有内嵌前端，就没有可回落的页面（只有 release 构建那边才回落 index.html）。
func MountWeb(r *gin.Engine) {
	r.NoRoute(writeNotFound)
}
