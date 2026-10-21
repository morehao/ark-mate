package run

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session/event"
)

// toolCallOfACP 把 ACP 的工具调用详情搬成领域值。
//
// 为什么这里还有一份：**事件面**那份已随翻译搬进适配器（`runtimeconn/events.go` 的
// `acpToolCallSignal`），而权限征询里的 `toolCall` 仍是协议形状——本文件整体还在协议边界上
// （下一刀随 `interaction` 值面收）。两份的字段清单必须一致；收口后这一份删除。
func toolCallOfACP(d acp.ToolCallInfo) event.ToolCall {
	return event.ToolCall{
		ID: d.ID, Title: d.Title, Status: d.Status, Kind: d.Kind,
		Input: d.Input, Output: d.Output, ContentText: d.ContentText,
	}
}

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
		ToolCall:  toolCallOfACP(req.ToolCall),
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
