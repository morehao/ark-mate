package handler

import (
	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// Service 面向控制面的领域服务抽象，由上层（如 internal/manager）实现。
// handler 依赖该接口而非具体实现，便于解耦与测试。
type Service interface {
	// ListAgents 返回注册的全部 Agent 概要。
	ListAgents() ([]AgentResp, error)
	// ListRuntimes 返回可用 Runtime 概要。
	ListRuntimes() ([]RuntimeResp, error)
	// ListSessions 返回会话概要。
	ListSessions() ([]SessionResp, error)
}

// AgentResp Agent 的对外表示。
type AgentResp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	State   string `json:"state"`
}

// RuntimeResp Runtime 的对外表示。
type RuntimeResp struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SessionResp 会话的对外表示。
type SessionResp struct {
	ID      string           `json:"id"`
	AgentID string           `json:"agent_id"`
	Runtime string           `json:"runtime"`
	Status  string           `json:"status"`
	History []eventbus.Event `json:"history,omitempty"`
}

// Handler 汇总各功能的链式入口（agents / sessions / runtimes）。
// 实时事件通道（WS）经 Bus() 订阅 eventbus，见设计文档 §4.5（M0 落地）。
type Handler struct {
	Agent   *AgentHandler
	Session *SessionHandler
	Runtime *RuntimeHandler
	bus     *eventbus.Bus
	svc     Service
}

// New 组装 Handler，注入领域服务与会话事件总线。
// bus 承载归一化会话事件（架构文档 §2 范式），由实时通道（WS，M0 落地）订阅推送。
func New(svc Service, bus *eventbus.Bus) *Handler {
	return &Handler{
		Agent:   &AgentHandler{svc: svc},
		Session: &SessionHandler{svc: svc},
		Runtime: &RuntimeHandler{svc: svc},
		bus:     bus,
		svc:     svc,
	}
}

// Bus 返回内部事件总线（供实时通道适配器订阅，及后续 manager 注入真实 ACP 事件）。
func (h *Handler) Bus() *eventbus.Bus { return h.bus }
