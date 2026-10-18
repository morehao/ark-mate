package svcsession

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/code"
)

// InteractionSvc 会话交互的**动作面**三条：权限决策 / 结构化提问决策 / 权限记忆撤销。
//
// 与只读四条（`DetailSvc`）的分界不是"读 vs 写"这种表面说法，而是**要不要把决定投递给底座**：
// 只读那四条只在自己的状态里取数（`InteractionSource` 三条纯读事实），而这三条要走执行面把
// 决策送进正在跑的那一轮（`InteractionDelivery` 三条投递）。两族的窄端口也因此刻意分开。
type InteractionSvc interface {
	// ResolvePermission 按 call_id 决策（allow/reject × once/always），返回**实际投递的回执**。
	ResolvePermission(ctx context.Context, id, callID, optionID string, always, allowed bool) (*dtosession.PermissionDecisionResp, error)

	// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel；accept 可带 content）。
	//
	// `late=true` 回答一条**已经超时转 pending** 的提问（ADR-11）：那条路不投给旧轮，
	// 而是作为**新一轮**的用户消息进去。
	ResolveElicitation(ctx context.Context, id, elicitationID, action string, content map[string]any, late bool) error

	// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
	RevokeMemory(ctx context.Context, id, title, kind string) error
}

// InteractionDelivery 三条动作面的**投递口**（把决定送给底座）。
//
// 为什么与 `InteractionSource`（只读）分两个接口：这一族有**副作用**（投递 + 删挂起项），
// 合成一个接口会让"只想列一下挂起项"的调用方也拿到投递能力——与 `domain/mcp` 把
// `Secrets`（纯解析）和实时面分开是同一条理由。
//
// 三个签名只吃**域值对象**（`interaction.DecisionOutcome`）与基础类型，这是刻意的：
// 适配器因此能落在 `manager`（它不许 import `service/svc*`，层守卫钉着）——端口一旦用
// svc 层自己声明的值类型，适配器就只能塞进"某个同时认识两边的层"，而那一层不存在。
type InteractionDelivery interface {
	// ResolvePermission 投递权限决策；ok=false = 挂起项不存在/已过期/不属于该会话。
	ResolvePermission(sessionID, callID, optionID string, always, allowed bool) (interaction.DecisionOutcome, bool, error)

	// ResolveElicitation 投递结构化提问决策（accept/decline/cancel）；`late` = 回答已超时转 pending 的那条。
	ResolveElicitation(sessionID, elicitationID, action string, content map[string]any, late bool) (bool, error)

	// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
	RevokeMemory(sessionID, title, kind string) (bool, error)
}

// Interaction 动作面用例。依赖事实：会话域平面（存在性）+ 投递口（窄端口）。
type Interaction struct {
	plane   *session.Plane
	deliver InteractionDelivery
}

// NewInteraction 造交互动作面用例（依赖事实由装配点交下来，用例实例在这里造）。
func NewInteraction(plane *session.Plane, deliver InteractionDelivery) InteractionSvc {
	return &Interaction{plane: plane, deliver: deliver}
}

var _ InteractionSvc = (*Interaction)(nil)

// ResolvePermission 按 call_id 决策（allow/reject × once/always），回**实际投递给 agent 的回执**。
//
// 回执里两组字段的来源不同，别混：
//   - `allowed` / `rejected` / `always` / `option_id` 是**请求参数的回带**（域构造 `d` 时用的就是
//     这几个入参，见 `acl.PermissionDecision`），前端拿它核对"我发的是什么"；
//   - `outcome` / `selected_option_id` 才来自**域的实际回执**：`option_id` 可能被兜底成第一个选项、
//     或整条降级成 `cancelled`，所以这两个绝不许回显请求（回显会让界面说"已允许"而底座收到取消）。
//
// C3 归属：只有登记该请求的会话能决策；跨会话（陈旧标签页/串号请求）由域判 `ok=false`，
// 这里统一出 `permission_not_found`——不泄露他会话挂起项的存在。
func (h *Interaction) ResolvePermission(_ context.Context, id, callID, optionID string, always, allowed bool) (*dtosession.PermissionDecisionResp, error) {
	if !h.plane.HasSession(id) {
		return nil, sessionNotFoundErr(id)
	}
	out, ok, err := h.deliver.ResolvePermission(id, callID, optionID, always, allowed)
	if err != nil {
		return nil, code.FromError(err)
	}
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "permission_not_found",
			fmt.Sprintf("权限请求 %q 不存在或已过期", callID), nil))
	}
	return &dtosession.PermissionDecisionResp{
		CallID:           callID,
		Allowed:          allowed,
		Rejected:         !allowed,
		Always:           always,
		OptionID:         optionID,
		Outcome:          out.Kind,
		SelectedOptionID: out.OptionID,
	}, nil
}

// ResolveElicitation 按 elicitation_id 决策（accept/decline/cancel；accept 可带 content）。
// C3 归属与权限面同一立场：跨会话调用按「不存在」处理（40401，不泄露他会话挂起项）。
func (h *Interaction) ResolveElicitation(_ context.Context, id, elicitationID, action string, content map[string]any, late bool) error {
	if !h.plane.HasSession(id) {
		return sessionNotFoundErr(id)
	}
	ok, err := h.deliver.ResolveElicitation(id, elicitationID, action, content, late)
	if err != nil {
		return code.FromError(err)
	}
	if !ok {
		return code.FromError(acl.NewError(acl.ClassRequest, "elicitation_not_found",
			fmt.Sprintf("结构化提问 %q 不存在或已过期", elicitationID), nil))
	}
	return nil
}

// RevokeMemory 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。
//
// 撤销的返回值（是否命中）**不进对外契约**：两种入口（单条 / 全部）都是幂等的"撤完即可"，
// 回一个"撤了几条"会让前端开始依赖计数，而那个数在并发撤销下本来就没有稳定含义。
func (h *Interaction) RevokeMemory(_ context.Context, id, title, kind string) error {
	if !h.plane.HasSession(id) {
		return sessionNotFoundErr(id)
	}
	if _, err := h.deliver.RevokeMemory(id, title, kind); err != nil {
		return code.FromError(err)
	}
	return nil
}
