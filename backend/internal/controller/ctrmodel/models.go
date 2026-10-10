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

// ModelCtr 模型面控制器：运行时派生的目录（读）+ 平台清单的写面（CRUD + 同步）。
type ModelCtr interface {
	List(c *gin.Context)
	Catalog(c *gin.Context)
	Create(c *gin.Context)
	Sync(c *gin.Context)
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

// List 返回模型读面：**平台清单 + 聚合生效结论 + 逐 runtime 机器面明细（sync[]）**。
//
// 参数：`?runtime=<id>`（只看那一个 runtime 的机器面明细）、`?agent=<id>`（缺省当前 Agent）、
// `?refresh=1`（保留参数以兼容既有调用；读面本身**无副作用**，不重跑同步）。
func (h *modelCtr) List(c *gin.Context) {
	agentID := c.Query("agent")
	if agentID == "" {
		agentID = h.agentSvc.DefaultAgentID()
	}
	resp, err := h.svc.ModelsView(c.Request.Context(), c.Query("runtime"), agentID, c.Query("refresh") == "1")
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Catalog 返回某 agent 的**运行时派生**模型目录（会话级模型选择器的数据来源）。
//
// 它与 `List`（平台声明）刻意分开：这里返回的 `options[].values[].value` 是底座的
// **不透明串**，必须原样回传（dsh 是 `JSON.stringify([provider,model])`、opencode 是
// `provider/model`），前端不得解析或拼接。
func (h *modelCtr) Catalog(c *gin.Context) {
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

// Sync 重新同步模型清单（`POST /models/sync`；`{runtime?}` 空 = 全部已注册 runtime）。
//
// 成功路径只回逐 runtime 的结论——此刻拿不到 `advertised`，**不许假装已生效**。
func (h *modelCtr) Sync(c *gin.Context) {
	var req dtomodel.ModelSyncReq
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
			return
		}
	}
	resp, err := h.svc.SyncModels(c.Request.Context(), req.Runtime)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Create 新建一条模型条目（同 `(provider_id, model_id)` 已存在 → 40901 `model_exists`）。
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

// Delete 删除一条模型条目（幂等；若它是默认模型则**连带清空**并在响应里说明）。
func (h *modelCtr) Delete(c *gin.Context) {
	resp, err := h.svc.DeleteModel(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
