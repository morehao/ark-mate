package svcsession

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// DetailSvc 会话**详情读面**：单会话详情 + 三份交互快照（挂起权限 / 权限记忆 / 挂起提问）。
//
// 为什么这四条是一个面（P4a 正文第四十刀）：四者共用**同一组投影**——详情响应里的 `pending`
// 与 `memories` 就是把 `ListPendingPermissions` / `ListPermissionMemories` 的结果折进去的。
// 投影只该有一份，所以"搬一半"在这里没有中间态：只搬三条会让 `GetSession` 那边留下一份
// 一模一样的投影，两份实现的分叉方式正是本仓反复吃亏的那类静默失效。
//
// 这一面与"读模型"（列表 / 轮次 / 排队输入 / 检索 / 用量）的分界，仍是同一把尺子：
// **有没有请求校验与错误码出口**。读模型没有（控制器直接调域平面，见 `ctrsession.plane` 的注释）；
// 这一面有——会话不存在要回 `session_not_found`(40401)，而且四条出口共用同一份文案。
type DetailSvc interface {
	// GetSession 返回单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
	GetSession(ctx context.Context, id string) (*dtosession.SessionDetailResp, error)

	// ListPendingPermissions 列出某会话的挂起权限请求。
	ListPendingPermissions(ctx context.Context, id string) ([]dtosession.PermissionResp, error)

	// ListPermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧，供详情/撤销 UI）。
	ListPermissionMemories(ctx context.Context, id string) ([]dtosession.MemoryResp, error)

	// ListPendingElicitations 列出某会话的挂起结构化提问（elicitation/create）。
	ListPendingElicitations(ctx context.Context, id string) ([]dtosession.ElicitationResp, error)
}

// InteractionSource 三份交互事实的**窄端口**。
//
// 为什么不直接拿 `*interaction.PermissionManager` / `*ElicitationManager`：那两份状态
// **归执行面持有**（`Orchestrator` 在 `attachOrchestrator` 里造，`perm`/`elic` 是它的私有字段），
// 在 P5 把 `Orchestrator` 拆开之前，用例层不该认识执行面的内部装配。三个方法正好是这一面要的
// 三份事实，而且都是**纯读**（域管理器自持锁、返回副本，没有副作用）。
type InteractionSource interface {
	PendingPermissions(sessionID string) []interaction.PermissionItem
	PermissionMemories(sessionID string) []interaction.MemoryItem
	PendingElicitations(sessionID string) []interaction.ElicitationItem
}

// Detail 详情读面用例。依赖事实：会话域平面（存在性 + 元数据投影）+ 交互事实来源（窄端口）。
type Detail struct {
	plane *session.Plane
	src   InteractionSource
}

// NewDetail 造详情读面用例（依赖事实由装配点交下来，用例实例在这里造）。
func NewDetail(plane *session.Plane, src InteractionSource) DetailSvc {
	return &Detail{plane: plane, src: src}
}

var _ DetailSvc = (*Detail)(nil)

// sessionNotFoundErr 会话不存在的**唯一**出口（四条判据共用）。
//
// C3 立场：不存在的会话与"别的会话的 id"一律按"不存在"处理，不泄露他会话的状态。
// 文案与码单点声明——四条出口各写一遍的表现是同一个原因几个文案，而 `session_not_found`
// 是前端用来做跳转判断的稳定码，文案漂移只会让人以为遇到了另一种情况。
func sessionNotFoundErr(id string) error {
	return code.FromError(code.RequestError("session_not_found", fmt.Sprintf("未知会话 %q", id)))
}

// GetSession 返回单会话详情（元数据 + 挂起权限 + 会话级始终允许记忆）。
func (d *Detail) GetSession(_ context.Context, id string) (*dtosession.SessionDetailResp, error) {
	// 一次取锁拿"会话 + 过程状态"（同一时刻的事实）；不存在也由它一并回答。
	// 不用 `HasSession` + `SessionResp` + `RunPhaseOf` 三次调用：那会得到三个瞬间的快照，
	// 而它们在同一行字上（`SessionSnapshot` 的注释里有完整理由）。
	resp, run, ok := d.plane.SessionSnapshot(id)
	if !ok {
		return nil, sessionNotFoundErr(id)
	}
	pending := d.src.PendingPermissions(id)
	perms := make([]dtosession.PermissionResp, 0, len(pending))
	for _, p := range pending {
		perms = append(perms, toPermissionResp(p))
	}
	// 过程状态（ADR-6 的读面）来自同一次快照——**投影在域里**，这一层只把字段搬过去。
	resp.Run = run
	detail := dtosession.SessionDetailResp{
		SessionResp: sessionRespDTO(resp),
		Pending:     perms,
		Memories:    memoryResps(d.src.PermissionMemories(id)),
	}
	return &detail, nil
}

// ListPendingPermissions 列出某会话的挂起权限请求。
func (d *Detail) ListPendingPermissions(_ context.Context, id string) ([]dtosession.PermissionResp, error) {
	if !d.plane.HasSession(id) {
		return nil, sessionNotFoundErr(id)
	}
	pending := d.src.PendingPermissions(id)
	out := make([]dtosession.PermissionResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toPermissionResp(p))
	}
	return out, nil
}

// ListPermissionMemories 列出某会话的会话级权限记忆（allow/reject 双侧）。
func (d *Detail) ListPermissionMemories(_ context.Context, id string) ([]dtosession.MemoryResp, error) {
	if !d.plane.HasSession(id) {
		return nil, sessionNotFoundErr(id)
	}
	return memoryResps(d.src.PermissionMemories(id)), nil
}

// ListPendingElicitations 列出某会话的挂起结构化提问（表单/URL 两种模式）。
func (d *Detail) ListPendingElicitations(_ context.Context, id string) ([]dtosession.ElicitationResp, error) {
	if !d.plane.HasSession(id) {
		return nil, sessionNotFoundErr(id)
	}
	pending := d.src.PendingElicitations(id)
	out := make([]dtosession.ElicitationResp, 0, len(pending))
	for _, p := range pending {
		out = append(out, toElicitationResp(p))
	}
	return out, nil
}

// memoryResps 权限记忆的投影（`Since` 统一 RFC3339 UTC；空集回 `[]` 而不是 nil）。
func memoryResps(items []interaction.MemoryItem) []dtosession.MemoryResp {
	out := make([]dtosession.MemoryResp, 0, len(items))
	for _, it := range items {
		out = append(out, dtosession.MemoryResp{
			Title: it.Title, Kind: it.Kind, Decision: it.Decision,
			Since: it.Since.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// toPermissionResp 把域里的挂起权限折叠为 DTO（唯一一份：`GetSession` 与
// `ListPendingPermissions` 共用）。
func toPermissionResp(p interaction.PermissionItem) dtosession.PermissionResp {
	opts := make([]dtosession.PermissionOptionResp, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, dtosession.PermissionOptionResp{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return dtosession.PermissionResp{
		CallID:    p.CallID,
		SessionID: p.SessionID,
		Title:     p.Title,
		ToolCall:  p.ToolCall.ID,
		Kind:      p.ToolCall.Kind,
		Input:     p.ToolCall.Input,
		Options:   opts,
		TimeoutAt: p.TimeoutAt.UTC().Format(time.RFC3339),
	}
}

// toElicitationResp 把域里的挂起提问折叠为 DTO（唯一一份）。
//
// `mode` 决定回 `schema` 还是 `url`——两者互斥，两个都回会让前端要在两处判"该用哪个"。
func toElicitationResp(p interaction.ElicitationItem) dtosession.ElicitationResp {
	resp := dtosession.ElicitationResp{
		ElicitationID: p.ID,
		SessionID:     p.SessionID,
		ScopeSource:   p.ScopeSource,
		Mode:          p.Mode,
		Message:       p.Message,
		TimeoutAt:     p.TimeoutAt.UTC().Format(time.RFC3339),
	}
	if p.Mode == "form" {
		resp.Schema = p.Schema
	} else {
		resp.URL = p.URL
	}
	return resp
}
