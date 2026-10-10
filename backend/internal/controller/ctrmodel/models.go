// Package ctrmodel 承载模型目录控制器（运行时派生的 ACP session config options 投影）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctrmodel

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/service/svcagent"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// ModelCtr 模型面控制器：运行时派生的目录（读）+ 平台清单的写面（CRUD）。
type ModelCtr interface {
	List(c *gin.Context)
	Create(c *gin.Context)
	Apply(c *gin.Context)
	Update(c *gin.Context)
	Delete(c *gin.Context)
}

var _ ModelCtr = (*modelCtr)(nil)

// ModelsHandler 处理模型目录 REST（GET /models?agent=<id>）。
// 同时依赖两个端口：模型目录来自底座面（svcruntime），而"未指定 agent 时取哪个"
// 属于 Agent 管理面的知识（svcagent.DefaultAgentID）——窄接口让这层依赖显式可见。
type modelCtr struct {
	svc      svcruntime.RuntimeSvc
	agentSvc svcagent.AgentSvc
}

// NewModelCtr 构造 ModelCtr。
func NewModelCtr(svc svcruntime.RuntimeSvc, agentSvc svcagent.AgentSvc) ModelCtr {
	return &modelCtr{svc: svc, agentSvc: agentSvc}
}

// List 返回某 agent 的运行时派生模型目录。
//
// 目录来源是运行时（ACP session config options），不是平台内置清单：
// 无 agent 参数时取默认 agent（dsh），以便面板首屏直接可用。
func (h *modelCtr) List(c *gin.Context) {
	agentID := c.Query("agent")
	if agentID == "" {
		agentID = h.agentSvc.DefaultAgentID()
	}
	resp, err := h.svc.ModelCatalog(c.Request.Context(), agentID)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Apply 强制重新物化模型配置文件（`POST /models/apply`；`{runtime?}` 空 = 全部已声明落点）。
//
// 成功路径只回 `pending:true`——此刻拿不到 `advertised`，**不许假装已生效**。
func (h *modelCtr) Apply(c *gin.Context) {
	var req dtomodel.ModelApplyReq
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
			return
		}
	}
	resp, err := h.svc.ApplyModels(c.Request.Context(), req.Runtime)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Create 新建一条模型条目（同三元组已存在 → 40901 `model_exists`）。
func (h *modelCtr) Create(c *gin.Context) {
	var req dtomodel.ModelCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateModel(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 覆盖一条模型条目（指针更新；不存在 → 40401 `model_not_found`）。
func (h *modelCtr) Update(c *gin.Context) {
	var patch dtomodel.ModelPatchReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateModel(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除一条模型条目（幂等；默认模型 → 40501 `model_is_default` + 可执行出路）。
func (h *modelCtr) Delete(c *gin.Context) {
	resp, err := h.svc.DeleteModel(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
