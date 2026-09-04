//go:build !release

package router

import "github.com/gin-gonic/gin"

// MountWeb 非 release 构建：不内嵌前端产物（面板走 vite dev proxy 联调）。
func MountWeb(_ *gin.Engine) {}
