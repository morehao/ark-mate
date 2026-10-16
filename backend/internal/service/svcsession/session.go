// Package svcsession 定义会话面的服务端口：会话元数据、轮次执行、
// 事件历史、挂起权限与结构化提问（elicitation）的决策入口。
package svcsession

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtosearch"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/dto/dtousage"
)

// SessionSvc 会话面（过渡期端口：实现仍在 `manager`，逐刀并入本包）。
//
// **只放用例**：读模型（列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量）不在这里——它们由控制器
// 直接经域平面取（见 `ctrsession` 的 `plane` 字段注释）。
type SessionSvc interface {
	// SearchSessions 按关键词搜索会话（标题/Agent/正文；含归档；updated 降序截断 limit）。
	SearchSessions(ctx context.Context, q string, limit int) ([]dtosession.SessionSearchHit, error)

	// GetSession 返回单会话详情（含元数据 + 挂起权限）。
	GetSession(ctx context.Context, id string) (*dtosession.SessionDetailResp, error)

	// UpdateSession 更新会话元数据（title/pinned/archived 子集更新）。
	UpdateSession(ctx context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error)

	// CreateSession 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error)

	// PromptSession 向既有会话追加一轮 prompt（delivery 见 PromptReq：忙时可直接执行 /
	// 排队 / 取消当前轮后投递）。
	PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error)

	// CancelRun 取消进行中的 run（无 run 时幂等 no-op）。
	CancelRun(ctx context.Context, id string) error

	// ListQueuedInputs 列出某会话待投递的排队输入（§6；冷挂载/刷新后重建队列视图）。
	ListQueuedInputs(ctx context.Context, id string) ([]dtosession.QueuedInputResp, error)
	// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6）；返回是否存在待投递项被撤下
	// （已投递/已作废是台账行，不可撤；不存在的 id 幂等返回 false）。
	DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error)

	// ListRuns 列出某会话的轮次明细（runs 表读模型：状态/耗时/usage/失败轮回溯；内存模式返回空）。
	ListRuns(ctx context.Context, id string) ([]dtosession.RunResp, error)

	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]dtosession.PermissionResp, error)

	// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回实际回执。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*dtosession.PermissionDecisionResp, error)

	// ListPermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧，供详情/撤销 UI）。
	ListPermissionMemories(ctx context.Context, id string) ([]dtosession.MemoryResp, error)

	// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
	RevokeMemory(ctx context.Context, id, title, kind string) error

	// ListPendingElicitations 列出某会话的挂起结构化提问（elicitation/create）。
	ListPendingElicitations(ctx context.Context, id string) ([]dtosession.ElicitationResp, error)

	// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel）。
	ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error
	// SearchMessages 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转）。
	//
	// ADR-2：原 `svcsearch` 的单方法面并入这里——它是**会话域**的读面（检索的就是消息）。
	SearchMessages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error)
	// UsageSummary 聚合用量统计（M3 首项：runs 计数/时长 + 会话上下文/费用快照）。
	//
	// ADR-2：原 `svcusage` 的单方法面并入这里——runs 与上下文快照都挂在会话上。
	UsageSummary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error)
}
