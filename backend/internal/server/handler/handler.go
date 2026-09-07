package handler

import (
	"context"

	"github.com/morehao/ark-forge/internal/server/eventbus"
)

// Service 面向控制面的领域服务抽象，由上层（如 internal/manager）实现。
// handler 依赖该接口而非具体实现，便于解耦与测试。
type Service interface {
	// ListAgents 返回注册的全部 Agent 概要。
	ListAgents() ([]AgentResp, error)
	// ListRuntimes 返回可用 Runtime 概要。
	ListRuntimes() ([]RuntimeResp, error)
	// ListSessions 返回会话概要（可按 archived 过滤）。
	ListSessions(archived bool) ([]SessionResp, error)
	// GetSession 返回单会话详情（含元数据 + 挂起权限）。
	GetSession(ctx context.Context, id string) (*SessionDetailResp, error)
	// UpdateSession 更新会话元数据（title/pinned/archived 子集更新）。
	UpdateSession(ctx context.Context, id string, patch SessionPatch) (*SessionResp, error)

	// CreateSession 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateSession(ctx context.Context, req SessionCreateReq) (*SessionCreateResp, error)
	// PromptSession 向既有会话追加一轮 prompt。
	PromptSession(ctx context.Context, id, prompt string) (*SessionPromptResp, error)
	// CancelTurn 取消进行中的 turn（无 turn 时幂等 no-op）。
	CancelTurn(ctx context.Context, id string) error
	// SessionHistory 返回会话的事件历史（eventbus 按聚合回放；after/limit 分页）。
	SessionHistory(ctx context.Context, id string, after int64, limit int) (*SessionHistoryResp, error)
	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]PermissionResp, error)
	// ResolvePermission 按 call_id 决策（allow/reject × once/always）。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) error
	// RevokeAlways 撤销会话级「始终允许」记忆（title+kind 均空 = 一键撤销全部）。
	RevokeAlways(ctx context.Context, id, title, kind string) error
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
	Name string         `json:"name"`
	ID   string         `json:"id"`
	Vers string         `json:"version"`
	Caps []CapabilityKV `json:"capabilities"`
}

// CapabilityKV 单个能力基线（静态声明）的 key/value 表示。
type CapabilityKV struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
}

// SessionResp 会话的对外表示。
type SessionResp struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Runtime   string `json:"runtime"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Pinned    bool   `json:"pinned"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SessionDetailResp 单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
type SessionDetailResp struct {
	SessionResp
	Pending       []PermissionResp  `json:"pending"`
	AlwaysAllowed []AlwaysAllowResp `json:"always_allowed"`
}

// AlwaysAllowResp 会话级「始终允许」记忆的对外表示。
type AlwaysAllowResp struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Since string `json:"since"` // RFC3339，授予时刻
}

// AlwaysRevokeReq 撤销「始终允许」记忆请求体（title+kind 均空 = 一键撤销全部）。
type AlwaysRevokeReq struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// SessionPatch 会话元数据子集更新（nil/零值不更新）。
type SessionPatch struct {
	Title    *string `json:"title"`
	Pinned   *bool   `json:"pinned"`
	Archived *bool   `json:"archived"`
}

// SessionCreateReq 新建会话请求体。
type SessionCreateReq struct {
	AgentID string `json:"agent_id" binding:"required"`
	Prompt  string `json:"prompt"`
	Cwd     string `json:"cwd,omitempty"`
}

// SessionCreateResp 新建会话响应。
type SessionCreateResp struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Runtime  string `json:"runtime"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // 已发布事件水位（WS attach 续传起点）
}

// SessionPromptResp 追加 prompt 响应。
type SessionPromptResp struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	EventSeq int64  `json:"event_seq"` // user_message 事件后的水位
}

// SessionHistoryResp 会话历史分页响应（事件按 seq 升序）。
type SessionHistoryResp struct {
	ID      string           `json:"id"`
	Events  []eventbus.Event `json:"events"`
	HasMore bool             `json:"has_more"` // 是否还有更早/后续页（客户端用末条 seq 作为下页 after）
}

// PermissionResp 挂起权限请求的对外表示。
type PermissionResp struct {
	CallID    string                 `json:"call_id"`
	SessionID string                 `json:"session_id"`
	Title     string                 `json:"title"`
	ToolCall  string                 `json:"tool_call_id"`
	Kind      string                 `json:"kind"`
	Input     any                    `json:"input,omitempty"`
	Options   []PermissionOptionResp `json:"options"`
	TimeoutAt string                 `json:"timeout_at"`
}

// PermissionOptionResp 权限可选项的对外表示。
type PermissionOptionResp struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionResolveReq 权限决策请求体。
type PermissionResolveReq struct {
	CallID   string `json:"call_id" binding:"required"`
	OptionID string `json:"option_id"`
	Always   bool   `json:"always"`
	Allowed  bool   `json:"allowed"`
}

// Handler 汇总各功能的链式入口（agents / sessions / runtimes / ws 事件通道）。
type Handler struct {
	Agent   *AgentHandler
	Session *SessionHandler
	Runtime *RuntimeHandler
	WS      *WSHandler
	bus     *eventbus.Bus
	svc     Service
}

// New 组装 Handler，注入领域服务与会话事件总线。
// bus 承载归一化会话事件（架构文档 §2 范式）；WS 事件通道（M0）与后续 manager 事件注入共用它。
func New(svc Service, bus *eventbus.Bus) *Handler {
	return &Handler{
		Agent:   &AgentHandler{svc: svc},
		Session: &SessionHandler{svc: svc},
		Runtime: &RuntimeHandler{svc: svc},
		WS:      NewWSHandler(bus),
		bus:     bus,
		svc:     svc,
	}
}

// Bus 返回内部事件总线（供 manager 注入真实 ACP 事件，及测试注入）。
func (h *Handler) Bus() *eventbus.Bus { return h.bus }
