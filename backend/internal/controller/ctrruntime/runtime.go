// Package ctrruntime 承载底座面控制器（可用 runtime 概要）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrruntime

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// RuntimeCtr 底座面控制器（可用 runtime 概要）。
type RuntimeCtr interface {
	List(c *gin.Context)
}

var _ RuntimeCtr = (*runtimeCtr)(nil)

// RuntimeHandler 处理 Runtime 相关 REST 请求。
type runtimeCtr struct {
	svc svcruntime.RuntimeSvc
}

// NewRuntimeCtr 构造 RuntimeCtr。
func NewRuntimeCtr(svc svcruntime.RuntimeSvc) RuntimeCtr {
	return &runtimeCtr{svc: svc}
}

// List 列出可用 Runtime。
func (h *runtimeCtr) List(c *gin.Context) {
	runtimes, err := h.svc.ListRuntimes()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, runtimes)
}
