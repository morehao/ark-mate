// Package ctrcommandpolicy 承载命令门禁（A2）的读写面。
//
// 控制器只做参数绑定与响应落信封；校验/落盘/生效都在 `svccommandpolicy`。
package ctrcommandpolicy

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtocommandpolicy"
	"github.com/morehao/ark-mate/internal/app/commandpolicy"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// CommandPolicyCtr 命令门禁读写面。
type CommandPolicyCtr interface {
	Get(c *gin.Context)
	Put(c *gin.Context)
}

var _ CommandPolicyCtr = (*commandPolicyCtr)(nil)

type commandPolicyCtr struct{ svc commandpolicy.PolicySvc }

// NewCommandPolicyCtr 构造控制器（只收**已构造的用例**；规则文件路径与立即生效回调在装配根交给用例）。
func NewCommandPolicyCtr(svc commandpolicy.PolicySvc) CommandPolicyCtr {
	return &commandPolicyCtr{svc: svc}
}

// Get 当前规则（`GET /command-policy`）。
func (h *commandPolicyCtr) Get(c *gin.Context) {
	resp, err := h.svc.Get(c.Request.Context())
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Put 全量替换规则（`PUT /command-policy`）。
func (h *commandPolicyCtr) Put(c *gin.Context) {
	var req dtocommandpolicy.PolicyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.Put(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
