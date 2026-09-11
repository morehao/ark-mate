// Package ctragent 承载Agent 管理面控制器（注册/更新/启停/删除自定义 agent）。
//
// 控制器只依赖 internal/service/svc* 的窄端口，不感知领域实现；
// 错误一律经 gincontext 落到统一响应信封（{code,requestID,msg,data}）。
package ctragent

import (
	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-forge/internal/dto/dtoagent"
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/golib/biz/gcontext/gincontext"
)

// AgentCtr Agent 管理面控制器（注册/更新/启停/删除自定义 agent）。
type AgentCtr interface {
	List(c *gin.Context)
	Create(c *gin.Context)
	Update(c *gin.Context)
	SetDefault(c *gin.Context)
	Delete(c *gin.Context)
	Start(c *gin.Context)
	Stop(c *gin.Context)
	Diagnostics(c *gin.Context)
}

var _ AgentCtr = (*agentCtr)(nil)

// AgentHandler 处理 Agent 相关 REST 请求（P2：注册向导 + 启停/热更新 + definitions 持久化）。
type agentCtr struct {
	svc svcagent.AgentSvc
}

// NewAgentCtr 构造 AgentCtr。
func NewAgentCtr(svc svcagent.AgentSvc) AgentCtr {
	return &agentCtr{svc: svc}
}

// List 列出全部 Agent。
func (h *agentCtr) List(c *gin.Context) {
	agents, err := h.svc.ListAgents()
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, agents)
}

// Create 注册自定义 agent（P2：不写代码新增；校验 runtime 已注册即「可启动预检」的最小实现，
// 能力基线冻结于 runtime 侧；启动动作由 Start 显式/会话按需触发）。
func (h *agentCtr) Create(c *gin.Context) {
	var req dtoagent.AgentCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.CreateAgent(c.Request.Context(), req)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Update 热更新 agent 元数据（name/runtime/provider；种子 runtime 只读）。
func (h *agentCtr) Update(c *gin.Context) {
	var patch dtoagent.AgentUpdateReq
	if err := c.ShouldBindJSON(&patch); err != nil {
		gincontext.Fail(c, code.Error(code.InvalidParam, "参数错误: "+err.Error()))
		return
	}
	resp, err := h.svc.UpdateAgent(c.Request.Context(), c.Param("id"), patch)
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// SetDefault 把该 agent 切换为「当前 Agent」（全局默认单例：新建会话、模型目录缺省都取它；
// 幂等，选择由 manager 落盘以便重启保持）。
func (h *agentCtr) SetDefault(c *gin.Context) {
	resp, err := h.svc.SetDefaultAgent(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Delete 删除自定义 agent（种子 agent 返回只读错误）：返回影响面（绑定会话数 / 摘除的活跃会话数 /
// 是否「当前 Agent」），供面板如实告知删除后果。
func (h *agentCtr) Delete(c *gin.Context) {
	resp, err := h.svc.DeleteAgent(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}

// Start 启动 agent runtime 连接（校验可启动；已运行幂等）。
func (h *agentCtr) Start(c *gin.Context) {
	if err := h.svc.StartAgent(c.Request.Context(), c.Param("id")); err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"id": c.Param("id"), "state": "running"})
}

// Stop 停止 agent runtime 进程（有在途 turn 时返回 agent_busy）。
func (h *agentCtr) Stop(c *gin.Context) {
	if err := h.svc.StopAgent(c.Request.Context(), c.Param("id")); err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, gin.H{"id": c.Param("id"), "state": "stopped"})
}

// Diagnostics agent 诊断快照：GET /agents/:id/diagnostics（A3：stderr 诊断尾巴外露）。
func (h *agentCtr) Diagnostics(c *gin.Context) {
	resp, err := h.svc.AgentDiagnostics(c.Request.Context(), c.Param("id"))
	if err != nil {
		gincontext.Fail(c, code.FromError(err))
		return
	}
	gincontext.Success(c, resp)
}
