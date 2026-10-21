package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/domain/interaction"
)

// ——— 权限/结构化提问的翻译（C2 第四刀从 `app/run` 搬来）———
//
// `domain/interaction` 只认领域值（`PermissionRequest`/`PermissionDecision`/`ElicitationRequest`/
// `ElicitationOutcome`）；ACP 的请求与决策在这一侧翻译，与事件出口（`events.go`）同一条纪律：
// **逐字段搬运、不改语义**。
//
// 决策回执的两半必须分开看：「用户点了什么」是领域值，「回给底座什么」是协议结果
// （选项可能被兜底成第一个、或整条降级成取消）。两个方向的翻译都在这里，别在调用点内联。

// clientOptionsOf 把装配侧交下来的**平台形状**回调集合折成 ACP 的拨号回调。
//
// `hooks.OnEvent` 由调用方单独挂（事件那一刀已收口，走 `DialOptions.OnEvent`）；这里只处理
// 权限/提问三件。`callbacks` 的零值安全：nil 回调保留 ACP 的零值实现，不 panic。
func clientOptionsOf(cb interaction.Callbacks) acp.ClientOptions {
	var out acp.ClientOptions
	if cb.OnPermission != nil {
		out.RequestPermission = func(ctx context.Context, req acp.PermissionRequest) acp.PermissionDecision {
			return permissionDecisionToACP(cb.OnPermission(ctx, permissionRequestToDomain(req)))
		}
	}
	if cb.OnElicitation != nil {
		out.CreateElicitation = func(ctx context.Context, req acp.ElicitationRequest) acp.ElicitationOutcome {
			return elicitationOutcomeToACP(cb.OnElicitation(ctx, elicitationRequestToDomain(req)))
		}
	}
	if cb.OnElicitationComplete != nil {
		out.CompleteElicitation = cb.OnElicitationComplete
	}
	return out
}

// permissionRequestToDomain 把 ACP 的权限征询翻成领域值。
func permissionRequestToDomain(req acp.PermissionRequest) interaction.PermissionRequest {
	return interaction.PermissionRequest{
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		ToolCall:  acpToolCallSignal(req.ToolCall),
		Options:   permissionOptionsToDomain(req.Options),
	}
}

// permissionOptionsToDomain 把 ACP 的可选项翻成领域值。
func permissionOptionsToDomain(opts []acp.PermissionOption) []interaction.PermissionOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]interaction.PermissionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, interaction.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return out
}

// permissionDecisionToACP 把领域决策翻成回给底座的结果。
func permissionDecisionToACP(d interaction.PermissionDecision) acp.PermissionDecision {
	return acp.PermissionDecision{Allowed: d.Allowed, Rejected: d.Rejected, OptionID: d.OptionID, Always: d.Always}
}

// elicitationRequestToDomain 把 ACP 的结构化提问翻成领域值。
func elicitationRequestToDomain(req acp.ElicitationRequest) interaction.ElicitationRequest {
	out := interaction.ElicitationRequest{
		SessionID:   req.SessionID,
		ScopeSource: req.ScopeSource,
		Mode:        req.Mode,
		Message:     req.Message,
		URL:         req.URL,
		URLID:       req.URLID,
	}
	if req.Schema != nil {
		out.Schema = &interaction.ElicitationSchema{
			Title:       req.Schema.Title,
			Description: req.Schema.Description,
			Properties:  req.Schema.Properties,
			Required:    req.Schema.Required,
		}
	}
	return out
}

// elicitationOutcomeToACP 把领域决策翻成回给底座的结果。
func elicitationOutcomeToACP(d interaction.ElicitationOutcome) acp.ElicitationOutcome {
	return acp.ElicitationOutcome{Action: d.Action, Content: d.Content}
}
