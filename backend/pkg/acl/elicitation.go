package acl

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"
)

// 结构化提问通道（ACP elicitation/*，规范仍标 unstable）：
// agent 在 prompt 中途请求宿主向用户渲染一张表单（单选/多选/文本/数字/布尔，由 JSON
// Schema 驱动）或引导打开一个 URL，并**阻塞等待**用户提交/放弃（accept/decline/cancel）。
// 宿主未实现该能力时 SDK 回 method_not_found，agent 侧把返回值当普通错误处理即可。
//
// wire 形态（SDK v0.13.5，两端同版本收发可自洽）：
//   - form：{message, mode:"form", requestedSchema:{...}}；各字段为 JSON Schema 属性对象
//     （string+enum/oneOf=单选，array+items=多选，string/integer/number/boolean=对应输入）。
//   - url：{message, mode:"url", url, elicitationId}。
//
// 会话归属（过渡实现，决策见 docs/agent-interactions-acp-v1-design-20260907.md）：
// SDK v0.13.5 的 UnstableCreateElicitationForm/Url 未建模规范 scope 字段，顶层 sessionId/
// requestId 在 SDK 解码时被丢弃，但**变体里的 `_meta` 会被完整保留**。因此归属分两段：
//  1. 本层先读 `_meta`（sessionId/session_id/sessionId/session_id，
//     见 scope.go）→ 命中即填 SessionID 并置 ScopeSource=wire（零 fork 可用的 wire 归属）；
//  2. 未命中才由宿主（manager/orchestrator 按「连接上唯一在途 turn」）注入 SessionID 并置
//     ScopeSource=inferred_inflight；连接上无唯一在途 turn 时置 ScopeAmbiguous（不再无痕取消）。
// 规范顶层 scope 仍需补 SDK（触发式预案见文档 §3.2），届时本层的读取点无需再改。

// ElicitationSchema 是表单字段的 JSON Schema 表示（属性为原始 schema 对象，
// 本层不做逐字段校验，原样透传给 UI 渲染控件）。
type ElicitationSchema struct {
	Title       string
	Description string
	Properties  map[string]any // 属性名 → JSON Schema 属性对象（含 type/enum/oneOf/items…）
	Required    []string
}

// ElicitationRequest.ScopeSource 取值（C3 归属来源）。
const (
	// ScopeWire：scope 由 wire（SDK 顶层 sessionId/_meta）提供——SDK 支持后启用。
	ScopeWire = "wire"
	// ScopeInferred：scope 由宿主按「连接上唯一在途 turn」推断（SDK 未建模时的过渡实现）。
	ScopeInferred = "inferred_inflight"
	// ScopeAmbiguous：`_meta` 未给 scope 且连接上无唯一在途 turn（多会话并发在途 / 会话外
	// request-scope）→ 无法归属。宿主仍按取消回应（不能把 agent 挂住），但归属来源标为它并留痕，
	// 使「为什么这张提问卡没出现」在诊断里可查，而不是无痕消失。
	ScopeAmbiguous = "ambiguous"
)

// ElicitationRequest 是 agent 发起的一次结构化提问请求（宿主需渲染并征询用户）。
type ElicitationRequest struct {
	// SessionID 会话归属：acl 层先按 `_meta` 填（ScopeWire），未命中留空由宿主
	// （orchestrator）按连接上下文注入（ScopeInferred）。
	SessionID string
	// ScopeSource 归属来源（wire | inferred_inflight）：由注入方填写，供 REST/WS 载荷与排障展示。
	ScopeSource string
	Mode        string // "form" | "url"
	Message     string
	Schema      *ElicitationSchema // Mode=form
	URL         string             // Mode=url
	URLID       string             // Mode=url：ACP 侧 elicitationId（透传）
}

// ElicitationOutcome 是宿主对一次结构化提问的决策。
type ElicitationOutcome struct {
	// Action ∈ accept | decline | cancel；accept 可携带用户提交的 content。
	Action  string
	Content map[string]any
}

// elicitationRequestFromSDK 把 SDK 的 elicitation/create 折叠为内部 ElicitationRequest。
// SessionID/ScopeSource 先按 `_meta` 填（wire），未命中留空由宿主注入（见包注释）；
// Form/Url 均缺失时返回 ok=false。
func elicitationRequestFromSDK(p acpsdk.UnstableCreateElicitationRequest) (ElicitationRequest, bool) {
	switch {
	case p.Form != nil:
		s := p.Form.RequestedSchema
		req := ElicitationRequest{
			Mode:    "form",
			Message: p.Form.Message,
		}
		if sid := sessionIDFromMeta(p.Form.Meta); sid != "" {
			req.SessionID, req.ScopeSource = sid, ScopeWire
		}
		if len(s.Properties) > 0 || len(s.Required) > 0 || s.Title != nil || s.Description != nil {
			req.Schema = &ElicitationSchema{
				Title:       derefStr(s.Title),
				Description: derefStr(s.Description),
				Properties:  s.Properties,
				Required:    s.Required,
			}
		}
		return req, true
	case p.Url != nil:
		req := ElicitationRequest{
			Mode:    "url",
			Message: p.Url.Message,
			URL:     p.Url.Url,
			URLID:   string(p.Url.ElicitationId),
		}
		if sid := sessionIDFromMeta(p.Url.Meta); sid != "" {
			req.SessionID, req.ScopeSource = sid, ScopeWire
		}
		return req, true
	}
	return ElicitationRequest{}, false
}

// elicitationResponseFromOutcome 把宿主决策折叠为 SDK 的 elicitation/create 响应。
func elicitationResponseFromOutcome(d ElicitationOutcome) acpsdk.UnstableCreateElicitationResponse {
	switch d.Action {
	case "accept":
		return acpsdk.UnstableCreateElicitationResponse{
			Accept: &acpsdk.UnstableCreateElicitationAccept{Action: "accept", Content: d.Content},
		}
	case "decline":
		return acpsdk.UnstableCreateElicitationResponse{
			Decline: &acpsdk.UnstableCreateElicitationDecline{Action: "decline"},
		}
	default:
		return acpsdk.UnstableCreateElicitationResponse{
			Cancel: &acpsdk.UnstableCreateElicitationCancel{Action: "cancel"},
		}
	}
}

// hostClient 实现 SDK 可选接口（client_gen.go 的接口断言）：
// agent 发来 elicitation/create 时由此进入宿主决策链路（阻塞直到用户提交/放弃）。
func (h *hostClient) UnstableCreateElicitation(ctx context.Context, p acpsdk.UnstableCreateElicitationRequest) (acpsdk.UnstableCreateElicitationResponse, error) {
	req, ok := elicitationRequestFromSDK(p)
	if !ok || h.opts.CreateElicitation == nil {
		// 宿主未实现该能力/载荷不可识别 → 一律按取消回应（agent 按 cancelled 分支继续）。
		return elicitationResponseFromOutcome(ElicitationOutcome{Action: "cancel"}), nil
	}
	return elicitationResponseFromOutcome(h.opts.CreateElicitation(ctx, req)), nil
}

// UnstableCompleteElicitation 处理 agent 的 elicitation/complete 通知：URL 模式的提问已在 agent 侧
// 走完（用户在浏览器里完成授权/登录），agent 以此告知宿主**可以收敛卡片**。
// 通知只带 elicitationId（无会话归属），故宿主按 id 反查挂起项并收尾；注入的
// CompleteElicitation 为空时仅作协议容忍（返回 nil 即视为已处理，行为与历史一致）。
func (h *hostClient) UnstableCompleteElicitation(ctx context.Context, p acpsdk.UnstableCompleteElicitationNotification) error {
	if h.opts.CompleteElicitation != nil {
		h.opts.CompleteElicitation(ctx, string(p.ElicitationId))
	}
	return nil
}

// derefStr 解引用可空字符串（nil → ""）。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
