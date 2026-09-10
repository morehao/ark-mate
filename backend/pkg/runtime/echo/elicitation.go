package echo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// 结构化提问演示场景（ACP elicitation/*，与宿主 internal/acl 的 elicitation 通道配套）：
// 输入含特定关键词时，echo 在本轮 prompt 中途发起一次 elicitation/create 并向用户渲染一张
// 表单（单选/多选/复合表单）或一个引导链接，阻塞等待用户提交/放弃后回显结果继续。
// 零依赖：不调用任何真实模型/工具，纯 wire 演示「agent → 宿主提问卡 → 用户 → agent」闭环。
//
// wire 说明：SDK v0.13.5 未建模 elicitation 的 scope 字段，echo 的 create 不带 sessionId
// （规范上属于缺 scope 的过渡形态）；会话归属由宿主按「连接上唯一在途 turn」推断（过渡实现，
// 见 docs/agent-interactions-acp-v1-design-20260907.md）。真实 runtime 接入 elicitation 后，
// 本演示改用规范 scope（SDK 补丁后 helper 变体）发送。

// maybeElicitation 按关键词路由 elicitation 演示场景；返回 handled=true 表示已处理
// （场景内部自行收尾并推送消息）。未命中任何关键词 → handled=false，走默认权限演示。
func (a *EchoAgent) maybeElicitation(ctx context.Context, sid acpsdk.SessionId, text string) (bool, error) {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "单选") || strings.Contains(low, "single"):
		return true, a.elicitationSingle(ctx, sid)
	case strings.Contains(low, "多选") || strings.Contains(low, "multi"):
		return true, a.elicitationMulti(ctx, sid)
	case strings.Contains(low, "表单") || strings.Contains(low, "form") || strings.Contains(low, "复合"):
		return true, a.elicitationForm(ctx, sid)
	case strings.Contains(low, "链接") || strings.Contains(low, "url"):
		return true, a.elicitationURL(ctx, sid)
	}
	return false, nil
}

// elicitationSingle 单选表单：string + oneOf（带标题选项）→ 宿主渲染 radio。
func (a *EchoAgent) elicitationSingle(ctx context.Context, sid acpsdk.SessionId) error {
	a.pushUpdates(ctx, sid,
		acpsdk.UpdateAgentThoughtText("（echo agent · elicitation 单选：向用户发起一次结构化单选提问，验证宿主表单卡的 wire 闭环。）"),
		acpsdk.UpdateAgentMessageText("为了继续，我需要你确认部署目标。请在下方的提问卡里选一个环境（单选表单演示）。"),
	)
	req := acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{
		Title:       acpsdk.Ptr("部署目标"),
		Description: acpsdk.Ptr("请选择本次要使用的部署环境（单选：string + oneOf）"),
		Properties: map[string]any{
			"env": map[string]any{
				"type":        "string",
				"title":       "目标环境",
				"description": "选择环境后我会按你的选择继续（仅演示，不真实部署）",
				"oneOf": []any{
					map[string]any{"const": "dev", "title": "开发环境 dev"},
					map[string]any{"const": "staging", "title": "预发环境 staging"},
					map[string]any{"const": "prod", "title": "生产环境 prod"},
				},
			},
		},
		Required: []string{"env"},
	})
	return a.elicitationAccept(ctx, sid, req, "单选")
}

// elicitationMulti 多选表单：array + items（enum + minItems/maxItems）→ 宿主渲染 checkbox。
func (a *EchoAgent) elicitationMulti(ctx context.Context, sid acpsdk.SessionId) error {
	a.pushUpdates(ctx, sid,
		acpsdk.UpdateAgentThoughtText("（echo agent · elicitation 多选：array + items + minItems/maxItems，验证宿主表单卡的多选与约束。）"),
		acpsdk.UpdateAgentMessageText("我需要你勾选本次要执行的任务（可多选，1~3 项）。请在下方的提问卡里选择（多选表单演示）。"),
	)
	req := acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{
		Title:       acpsdk.Ptr("任务清单"),
		Description: acpsdk.Ptr("勾选要执行的任务（1~3 项）"),
		Properties: map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"title":       "要执行的任务",
				"description": "可多选；至少 1 项、至多 3 项",
				"items":       map[string]any{"type": "string", "enum": []any{"构建", "单元测试", "部署到 staging", "发布到 prod"}},
				"minItems":    1,
				"maxItems":    3,
			},
		},
		Required: []string{"tasks"},
	})
	return a.elicitationAccept(ctx, sid, req, "多选")
}

// elicitationForm 复合表单：文本（必填）+ 打分 + 布尔 + 单选，多字段一次收齐；
// 覆盖 required/min/max/default 等约束与前端校验。
func (a *EchoAgent) elicitationForm(ctx context.Context, sid acpsdk.SessionId) error {
	a.pushUpdates(ctx, sid,
		acpsdk.UpdateAgentThoughtText("（echo agent · elicitation 复合表单：多字段（必填文本/打分/开关/单选）一次收齐，验证宿主表单卡的字段渲染与校验。）"),
		acpsdk.UpdateAgentMessageText("在动手前，我想先收集一点背景信息。请填写下方的复合表单（表单演示）。"),
	)
	req := acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{
		Title:       acpsdk.Ptr("项目背景收集"),
		Description: acpsdk.Ptr("复合表单演示：多字段 + 必填/范围校验"),
		Properties: map[string]any{
			"name": map[string]any{
				"type": "string", "title": "项目代号", "description": "必填，1~40 字符",
				"minLength": 1, "maxLength": 40,
			},
			"score": map[string]any{
				"type": "integer", "title": "当前满意度", "description": "打分 1~5（默认 3）",
				"minimum": 1, "maximum": 5, "default": float64(3),
			},
			"public": map[string]any{
				"type": "boolean", "title": "是否公开", "description": "默认否",
				"default": false,
			},
			"urgency": map[string]any{
				"type": "string", "title": "优先级", "description": "单选",
				"enum": []any{"低", "中", "高"},
			},
		},
		Required: []string{"name", "score", "urgency"},
	})
	return a.elicitationAccept(ctx, sid, req, "复合表单")
}

// elicitationURL URL 引导：宿主渲染「打开链接完成」卡，用户点「我已完成/拒绝/取消」。
func (a *EchoAgent) elicitationURL(ctx context.Context, sid acpsdk.SessionId) error {
	a.pushUpdates(ctx, sid,
		acpsdk.UpdateAgentThoughtText("（echo agent · elicitation URL：引导用户打开链接完成一次模拟 OAuth 授权，验证 URL 模式提问卡。）"),
		acpsdk.UpdateAgentMessageText("下面这步需要你在浏览器里完成一次模拟 OAuth 授权：请在提问卡上打开链接，完成后点「我已完成」。"),
	)
	req := acpsdk.NewUnstableCreateElicitationRequestUrl(
		acpsdk.UnstableElicitationId(fmt.Sprintf("elurl-%d", a.n.Add(1))),
		"https://example.com/demo-oauth",
	)
	req.Url.Message = "请打开链接完成模拟 OAuth 授权，然后回到面板点「我已完成」"
	return a.elicitationAccept(ctx, sid, req, "URL")
}

// elicitationAccept 发起 elicitation/create 并阻塞等待宿主决策，随后按决策回显结果。
func (a *EchoAgent) elicitationAccept(ctx context.Context, sid acpsdk.SessionId, req acpsdk.UnstableCreateElicitationRequest, scene string) error {
	if req.Form != nil {
		req.Form.Mode = "form"
		if req.Form.Message == "" {
			// 卡内引导文案缺省用 schema 的描述/标题兜底。
			if req.Form.RequestedSchema.Description != nil {
				req.Form.Message = *req.Form.RequestedSchema.Description
			} else if req.Form.RequestedSchema.Title != nil {
				req.Form.Message = *req.Form.RequestedSchema.Title
			} else {
				req.Form.Message = "请填写以下表单"
			}
		}
	} else if req.Url != nil {
		req.Url.Mode = "url"
	}
	resp, err := a.conn.UnstableCreateElicitation(ctx, req)
	if err != nil {
		// 宿主未实现 elicitation（method_not_found 等）→ 优雅降级告知用户，不把整轮打成错误。
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "method_not_found") || strings.Contains(low, "method not found") ||
			strings.Contains(low, "unsupported") {
			a.pushUpdates(ctx, sid, acpsdk.UpdateAgentMessageText(
				fmt.Sprintf("（%s 场景）当前宿主不支持 elicitation 提问通道（%v），本轮按跳过处理。", scene, err)))
			return nil
		}
		return err
	}
	switch {
	case resp.Accept != nil:
		summary := summarizeContent(resp.Accept.Content)
		a.pushUpdates(ctx, sid, acpsdk.UpdateAgentMessageText(
			fmt.Sprintf("已收到你的提交（%s）：%s。我将按这些信息继续。", scene, summary)))
	case resp.Decline != nil:
		a.pushUpdates(ctx, sid, acpsdk.UpdateAgentMessageText(
			fmt.Sprintf("（%s 场景）用户拒绝了本次提问，跳过该步骤继续。", scene)))
	default:
		a.pushUpdates(ctx, sid, acpsdk.UpdateAgentMessageText(
			fmt.Sprintf("（%s 场景）提问被取消（cancelled），跳过该步骤继续。", scene)))
	}
	return nil
}

// summarizeContent 把用户提交的表单内容拼成可读摘要（键稳定序；空内容给出提示）。
func summarizeContent(content map[string]any) string {
	if len(content) == 0 {
		return "（未提供内容）"
	}
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s = %v", k, content[k]))
	}
	return strings.Join(parts, "；")
}

// pushUpdates 向宿主推送一条或多条 session/update（与 Prompt 内联版等价，供场景复用）。
func (a *EchoAgent) pushUpdates(ctx context.Context, sid acpsdk.SessionId, updates ...acpsdk.SessionUpdate) {
	if a.conn == nil {
		return
	}
	for _, u := range updates {
		_ = a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{SessionId: sid, Update: u})
	}
}
