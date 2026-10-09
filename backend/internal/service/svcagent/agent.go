// Package svcagent 定义 Agent 管理面的服务端口（控制面用例边界）。
//
// 由领域实现层（internal/manager）实现；控制器只依赖本接口，从而与实现解耦。
package svcagent

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
)

// AgentSvc Agent 管理面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ AgentSvc = (*T)(nil) 保证一致性。
type AgentSvc interface {
	// ListAgents 返回注册的全部 Agent 概要。
	ListAgents() ([]dtoagent.AgentResp, error)

	// CreateAgent 注册自定义 agent（definitions 持久化；校验 runtime 已注册）。
	CreateAgent(ctx context.Context, req dtoagent.AgentCreateReq) (*dtoagent.AgentResp, error)

	// UpdateAgent 更新 agent 元数据（name/runtime/provider 子集）。
	UpdateAgent(ctx context.Context, id string, patch dtoagent.AgentUpdateReq) (*dtoagent.AgentResp, error)

	// SetDefaultAgent 切换「当前 Agent」（全局默认单例：新建会话与模型目录缺省取它；未知 agent 报错）。
	SetDefaultAgent(ctx context.Context, id string) (*dtoagent.AgentResp, error)

	// DeleteAgent 删除自定义 agent（种子不可删）：返回影响面（绑定会话数 / 已摘除的活跃会话数 /
	// 是否「当前 Agent」）。历史会话保留可读但无法继续提问；有在途 turn 时拒绝。
	DeleteAgent(ctx context.Context, id string) (*dtoagent.AgentDeleteResp, error)

	// StartAgent 启动 agent 对应 runtime 连接（校验可启动）。
	StartAgent(ctx context.Context, id string) error

	// StopAgent 停止 agent 对应 runtime 进程（有在途 turn 时拒绝）。
	StopAgent(ctx context.Context, id string) error

	// DefaultAgentID 返回默认 agent（面板未显式指定 agent 时，用它取模型目录）。
	DefaultAgentID() string

	// AgentDiagnostics 返回某 agent 的诊断快照（A3：stderr 诊断尾巴外露）。
	AgentDiagnostics(ctx context.Context, id string) (*dtoagent.AgentDiagnosticsResp, error)
}
