// Package svcsession 定义会话面的服务端口：会话元数据、轮次执行、
// 事件历史、挂起权限与结构化提问（elicitation）的决策入口。
package svcsession

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// SessionSvc 会话面（过渡期端口：实现仍在 `manager`，逐刀并入本包）。
//
// **只放用例**：读模型（列表 / 单条 / 轮次 / 排队输入 / 检索 / 用量）不在这里——它们由控制器
// 直接经域平面取（见 `ctrsession` 的 `plane` 字段注释）。
//
// **剩余形状（P4a 正文第四十三刀之后）**：只剩 4 条，且都不是"顺手可搬"的小块——
//
//	`UpdateSession`  一半是**模型面**（`resolveSessionModel` 要 store/reg/供应商面 +
//	                 `ApplySessionModel` 是执行面），与 `svcmodel` 那一批同刀；
//	`CreateSession`  工作空间解析 + `StartSession`/`Prompt`（编排）；
//	`PromptSession`  `NormalizeDelivery`（域）+ `PromptWithDelivery`（编排）；
//	`CancelRun`      **纯编排**：域里连存在性判据都没有（`o.CancelRun` 自己回 40401），
//	                 搬过来只会得到一行转发 ⇒ 随 `Orchestrator` 在 P5 一起收口。
//
// 会话面的其余部分已按**面**切开，各有自己的接口：`HistorySvc`（第十刀）/ `DetailSvc`（第四十刀）/
// `InteractionSvc`（第四十一刀）/ `QueueSvc`（第四十二刀）——**不要把新面塞回这里**（守卫有反向判据）。
type SessionSvc interface {
	// CreateSession 建立会话并（可选）启动首轮 prompt（后台执行，事件经 WS 出流）。
	CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error)

	// PromptSession 向既有会话追加一轮 prompt（delivery 见 PromptReq：忙时可直接执行 /
	// 排队 / 取消当前轮后投递）。
	PromptSession(ctx context.Context, id string, req dtosession.PromptReq) (*dtosession.SessionPromptResp, error)

	// CancelRun 取消进行中的 run（无 run 时幂等 no-op）。
	CancelRun(ctx context.Context, id string) error
}
