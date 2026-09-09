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
// wire 形态（SDK v0.13.5，与 echo agent 同版本收发可自洽）：
//   - form：{message, mode:"form", requestedSchema:{...}}；各字段为 JSON Schema 属性对象
//     （string+enum/oneOf=单选，array+items=多选，string/integer/number/boolean=对应输入）。
//   - url：{message, mode:"url", url, elicitationId}。
//
// 会话归属（过渡实现，Rev2 决策，见 docs/agent-interactions-acp-v1-design-20260907.md）：
// SDK v0.13.5 的 UnstableCreateElicitationForm/Url 未建模规范 scope 字段，顶层 sessionId/
// requestId 在 SDK 解码时被丢弃，本层读不到。因此 ElicitationRequest.SessionID **不由本层
// 填充**，由宿主（manager/orchestrator 按「连接上唯一在途 turn」推断）在调用钩子前注入，
// 并把注入方式写进 ScopeSource（C3：归属来源显式化，REST/WS 载荷可观测）；
// acl 层保持零假设（不复用 _meta，也不猜 wire）。真实 runtime 首现 elicitation 时再切标准
// scope 解析（SDK 补丁预案见 §6 文档），宿主仅需改注入点为 scope 读取并置 ScopeWire。

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
)

// ElicitationRequest 是 agent 发起的一次结构化提问请求（宿主需渲染并征询用户）。
type ElicitationRequest struct {
	// SessionID 会话归属：过渡期由宿主（orchestrator）按连接上下文注入，acl 层恒为空。
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
// SessionID 不在本层填充（宿主注入，见包注释）；Form/Url 均缺失时返回 ok=false。
func elicitationRequestFromSDK(p acpsdk.UnstableCreateElicitationRequest) (ElicitationRequest, bool) {
	switch {
	case p.Form != nil:
		s := p.Form.RequestedSchema
		req := ElicitationRequest{
			Mode:    "form",
			Message: p.Form.Message,
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
		return ElicitationRequest{
			Mode:    "url",
			Message: p.Url.Message,
			URL:     p.Url.Url,
			URLID:   string(p.Url.ElicitationId),
		}, true
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

// UnstableCompleteElicitation 处理 agent 的 elicitation/complete 通知（URL 模式收尾告知）。
// unstable wire 未带会话归属，宿主无可定位 UI 卡片，仅作协议容忍（返回 nil 即视为已处理）。
func (h *hostClient) UnstableCompleteElicitation(context.Context, acpsdk.UnstableCompleteElicitationNotification) error {
	return nil
}

// derefStr 解引用可空字符串（nil → ""）。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
