package handler

import (
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/service/svcagent"
	"github.com/morehao/ark-forge/internal/service/svccapability"
	"github.com/morehao/ark-forge/internal/service/svcconversation"
	"github.com/morehao/ark-forge/internal/service/svcerr"
	"github.com/morehao/ark-forge/internal/service/svcexport"
	"github.com/morehao/ark-forge/internal/service/svcruntime"
	"github.com/morehao/ark-forge/internal/service/svcsearch"
	"github.com/morehao/ark-forge/internal/service/svcusage"
	"github.com/morehao/ark-forge/internal/service/svcworkspace"
)

// Deps 汇总控制面各领域服务端口（按领域拆分的窄接口，定义在 internal/service/svc*）。
//
// 为什么不是一个汇总大接口：传输层各控制器只需要自己那一片能力——Agent 控制器不该
// 拿到 ExportConversations，能力目录控制器也不该拿到工作区白名单写入口。窄接口让
// "谁依赖什么"在编译期可见，也让领域实现可以分片演进（P4 把 manager 拆成多个实现时，
// 各端口可由不同对象承担）。
//
// 当前实现方是单一聚合 internal/manager.Service，它同时满足全部端口。
type Deps struct {
	Agent        svcagent.AgentSvc
	Conversation svcconversation.ConversationSvc
	Workspace    svcworkspace.WorkspaceSvc
	Runtime      svcruntime.RuntimeSvc
	Capability   svccapability.CapabilitySvc
	Usage        svcusage.UsageSvc
	Search       svcsearch.SearchSvc
	Export       svcexport.ExportSvc
	// ErrMapper 领域错误 → 传输层错误码（仅需要把领域错误分类外露的控制器使用；
	// P5 错误码收敛到 pkg/code 后该端口删除）。
	ErrMapper svcerr.Mapper
}

// Handler 汇总各功能的链式入口（agents / conversations / runtimes / 能力目录 / ws 事件通道）。
type Handler struct {
	Agent        *AgentHandler
	Conversation *ConversationHandler
	Workspace    *WorkspaceHandler
	Runtime      *RuntimeHandler
	Provider     *ProviderHandler
	Model        *ModelsHandler
	Capability   *CapabilityHandler
	Usage        *UsageHandler
	Search       *SearchHandler
	Export       *ExportHandler
	WS           *WSHandler
	bus          *eventbus.Bus
	deps         Deps
}

// New 组装 Handler，注入各领域服务端口与会话事件总线。
// bus 承载归一化会话事件（架构文档 §2 范式）；WS 事件通道（M0）与后续 manager 事件注入共用它。
func New(deps Deps, bus *eventbus.Bus) *Handler {
	return &Handler{
		Agent:        &AgentHandler{svc: deps.Agent, mapper: deps.ErrMapper},
		Conversation: &ConversationHandler{svc: deps.Conversation},
		Workspace:    &WorkspaceHandler{svc: deps.Workspace, mapper: deps.ErrMapper},
		Runtime:      &RuntimeHandler{svc: deps.Runtime},
		Provider:     &ProviderHandler{svc: deps.Runtime},
		Model:        &ModelsHandler{svc: deps.Runtime, agentSvc: deps.Agent},
		Capability:   &CapabilityHandler{svc: deps.Capability},
		Usage:        &UsageHandler{svc: deps.Usage},
		Search:       &SearchHandler{svc: deps.Search, mapper: deps.ErrMapper},
		Export:       &ExportHandler{svc: deps.Export, mapper: deps.ErrMapper},
		WS:           NewWSHandler(bus),
		bus:          bus,
		deps:         deps,
	}
}

// Bus 返回内部事件总线（供 manager 注入真实 ACP 事件，及测试注入）。
func (h *Handler) Bus() *eventbus.Bus { return h.bus }
