package sessionruntime

import (
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/infra/acp"
)

// ——— 权限/结构化提问的**翻译边界**（C2 第四刀）———
//
// `domain/interaction` 只认领域值（`PermissionRequest`/`PermissionDecision`/`ElicitationRequest`/
// `ElicitationOutcome`）；ACP 的请求与决策在这一侧翻译，与事件出口（`event_bridge.go`）同一条纪律：
// **逐字段搬运、不改语义**。
//
// 决策回执的两半必须分开看：「用户点了什么」是领域值，「回给底座什么」是协议结果
// （选项可能被兜底成第一个、或整条降级成取消）。两个方向的翻译都在这里，别在调用点内联。

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
