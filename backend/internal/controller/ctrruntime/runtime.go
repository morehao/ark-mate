// Package ctrruntime 承载运行时诊断控制器（底座此刻的事实：进程态 / 握手结论 / stderr / 最后错误）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrruntime

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// RuntimeCtr 运行时诊断控制器。
type RuntimeCtr interface {
	Diagnostics(c *gin.Context)
}

var _ RuntimeCtr = (*runtimeCtr)(nil)

// runtimeCtr 处理运行时诊断 REST（`GET /runtime/diagnostics`）。
//
// v19：它原是 `GET /agents/:id/diagnostics`——agent 概念整个并入 runtime 之后，路径里的 id
// 只剩暗示"底座可以选"这一个作用（底座由构建期固定），所以路由不带参数、端口也不带参数。
type runtimeCtr struct {
	svc svcruntime.RuntimeSvc
}

// NewRuntimeCtr 构造 RuntimeCtr：交出底座**执行面**，在这里造出用例。
//
// `plane` 由装配侧交出（`*runtimeconn.Plane`）——`app` 交的是执行面而不是用例，因为
// `app` 与 `router` 都不该 import `service/svc*`（`internal/archtest/svc_layer_consumer_test.go`）。
func NewRuntimeCtr(svc svcruntime.RuntimeSvc) RuntimeCtr {
	return &runtimeCtr{svc: svc}
}

// Diagnostics 返回底座诊断快照（只读：不主动拨号，只读此刻已建立的事实）。
func (h *runtimeCtr) Diagnostics(c *gin.Context) {
	resp, err := h.svc.RuntimeDiagnostics(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
