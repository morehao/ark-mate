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
	// UpdateSession 更新会话元数据（title/pinned/archived 子集更新）。
	UpdateSession(ctx context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error)

	// CreateSession 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error)

	// PromptSession 向既有会话追加一轮 prompt（delivery 见 PromptReq：忙时可直接执行 /
	// 排队 / 取消当前轮后投递）。
	PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error)

	// CancelRun 取消进行中的 run（无 run 时幂等 no-op）。
	CancelRun(ctx context.Context, id string) error
	// SearchMessages 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转）。
	//
	// ADR-2：原 `svcsearch` 的单方法面并入这里——它是**会话域**的读面（检索的就是消息）。
	SearchMessages(ctx context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error)
	// UsageSummary 聚合用量统计（M3 首项：runs 计数/时长 + 会话上下文/费用快照）。
	//
	// ADR-2：原 `svcusage` 的单方法面并入这里——runs 与上下文快照都挂在会话上。
	UsageSummary(ctx context.Context, q dtousage.UsageQueryReq) (*dtousage.UsageSummaryResp, error)
}
