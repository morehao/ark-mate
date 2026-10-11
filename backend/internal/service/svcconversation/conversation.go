// Package svcconversation 定义会话面的服务端口：会话元数据、轮次执行、
// 事件历史、挂起权限与结构化提问（elicitation）的决策入口。
package svcconversation

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtoconversation"
)

// ConversationSvc 会话面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ ConversationSvc = (*T)(nil) 保证一致性。
type ConversationSvc interface {
	// ListConversations 返回会话概要（可按 archived 过滤）。
	//
	// 两个工作空间过滤参数口径不同（设计 ADR-2/ADR-5），都为空则不过滤：
	//   - cwd：**执行事实**的字符串相等（绝对路径；历史行为，兼容保留）；
	//   - workspaceID：**归属事实**（v14 左栏分组用它，可索引）。按默认工作空间过滤时
	//     **同时包含** `workspace_id=''` 的历史会话（那些行读面归默认工作空间）。
	ListConversations(archived bool, cwd, workspaceID string) ([]dtoconversation.ConversationResp, error)

	// SearchConversations 按关键词搜索会话（标题/Agent/正文；含归档；updated 降序截断 limit）。
	SearchConversations(ctx context.Context, q string, limit int) ([]dtoconversation.ConversationSearchHit, error)

	// GetConversation 返回单会话详情（含元数据 + 挂起权限）。
	GetConversation(ctx context.Context, id string) (*dtoconversation.ConversationDetailResp, error)

	// UpdateConversation 更新会话元数据（title/pinned/archived 子集更新）。
	UpdateConversation(ctx context.Context, id string, patch dtoconversation.ConversationPatch) (*dtoconversation.ConversationResp, error)

	// CreateConversation 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateConversation(ctx context.Context, req dtoconversation.ConversationCreateReq) (*dtoconversation.ConversationCreateResp, error)

	// PromptConversation 向既有会话追加一轮 prompt（delivery 见 PromptReq：忙时可直接执行 /
	// 排队 / 取消当前轮后投递）。
	PromptConversation(ctx context.Context, id string, req dtoconversation.PromptReq) (*dtoconversation.ConversationPromptResp, error)

	// CancelTurn 取消进行中的 turn（无 turn 时幂等 no-op）。
	CancelTurn(ctx context.Context, id string) error

	// ListQueuedInputs 列出某会话待投递的排队输入（§6；冷挂载/刷新后重建队列视图）。
	ListQueuedInputs(ctx context.Context, id string) ([]dtoconversation.QueuedInputResp, error)
	// DeleteQueuedInput 撤下一条尚未投递的排队输入（§6）；返回是否存在待投递项被撤下
	// （已投递/已作废是台账行，不可撤；不存在的 id 幂等返回 false）。
	DeleteQueuedInput(ctx context.Context, id, inputID string) (bool, error)

	// ConversationHistory 返回会话的事件历史（sqlite 模式读 events 表，内存模式回放 eventbus；
	// after（向后）/before（向前）互斥游标 + limit 分页）。
	ConversationHistory(ctx context.Context, id string, q dtoconversation.HistoryQuery) (*dtoconversation.ConversationHistoryResp, error)

	// ListRuns 列出某会话的轮次明细（runs 表读模型：状态/耗时/usage/失败轮回溯；内存模式返回空）。
	ListRuns(ctx context.Context, id string) ([]dtoconversation.RunResp, error)

	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]dtoconversation.PermissionResp, error)

	// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回实际回执。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*dtoconversation.PermissionDecisionResp, error)

	// ListPermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧，供详情/撤销 UI）。
	ListPermissionMemories(ctx context.Context, id string) ([]dtoconversation.MemoryResp, error)

	// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
	RevokeMemory(ctx context.Context, id, title, kind string) error

	// ListPendingElicitations 列出某会话的挂起结构化提问（elicitation/create）。
	ListPendingElicitations(ctx context.Context, id string) ([]dtoconversation.ElicitationResp, error)

	// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel）。
	ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any) error
}
