// Package appmcp 把「应用定义（App）」暴露成一条 **MCP 工具面**，作为 `mate mcp platform` 的
// `app` 域（与 `expert` 域并列，见 internal/bootstrap/assembly/platform_mcp.go）。
//
// 目的：让用户用**自然语言**创建/编辑应用——模型把"帮我建个'每日知识简报'应用，组合某某专家+定时任务"
// 翻译成 `app_create` 调用，而不是手写 JSON 或去面板逐字段填。
//
// **这是「管理面」，不是「业务面」**（设计 §5.8 两类必须分清）：这里 6 个工具只管**定义**（增删改查启停），
// 与 `expert_create` 同性质，进 platform 域、恒注入；app 携带的**业务工具**（`appfoo_query` 这类）走
// **逐 app 独立注入**通道（`infra/apptool` + `sessionruntime.appendAppMcpServers`，见 WP5），**绝不**进本域。
//
// 三条边界（与 capabilitymcp 一脉相承）：
//   - **自声明窄端口**（`Service`，用 `dtoapp` 值），本包**不 import 用例层**（层规：只有装配根与
//     控制器可以引用 `service/svc*`）；
//   - **schema 与 `capability.ValidateApp` 同口径**：能机器表达的约束（id 正则 / name 上限 /
//     description 上限 / tags / api_version caret / tool_domains 域键 / slots impl_id 词法）都写进
//     schema，让模型一次填对；但**服务端 `ValidateApp` + 白名单在册判断仍是唯一闸门**（危险标记、
//     builder 校验等 schema 表达不了的，服务端必拒，返 `40001 app_invalid`）；
//   - 工具 description **不向用户暴露内部格式**，要求用自然语言与用户确认（对齐 DSH 教训）。
package appmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
)

// 工具名（固定；与 `mate mcp platform` 的 app 域前缀一致）。
const (
	ToolCreate     = "app_create"
	ToolUpdate     = "app_update"
	ToolList       = "app_list"
	ToolGet        = "app_get"
	ToolDelete     = "app_delete"
	ToolSetEnabled = "app_set_enabled"
)

// Service 是 app 工具面需要的**窄端口**（本包自声明，由装配根用 svcapp 实现）。
//
// 只声明工具面真正用到的五个方法（用 dto 值，不 import 用例层）。写面（Create/Update）透传
// `AppWriteReq`（原文 JSON patch），让"只改出现过的键"这条语义在 MCP 侧与 REST 侧一致。
type Service interface {
	ListApps(ctx context.Context) ([]dtoapp.AppResp, error)
	GetApp(ctx context.Context, id string) (*dtoapp.AppResp, error)
	WriteApp(ctx context.Context, pathID string, req dtoapp.AppWriteReq, create bool) (*dtoapp.AppWriteResp, error)
	DeleteApp(ctx context.Context, id string) (*dtoapp.AppDeleteResp, error)
	SetAppEnabled(ctx context.Context, id string, enabled bool) (*dtoapp.AppToggleResp, error)
}

// Handler 是 app 工具面的实现（满足 mcp.Handler）。
type Handler struct {
	svc Service
}

// New 构造 Handler（svc 由装配根交下来）。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// 工具描述（会给模型看）。
const (
	descCreate = "创建一个应用（组合型/实体型：把既有专家/自动化/技能/MCP 组合起来，可选声明实体、工具域、页面）。参数：id（必填，小写字母数字连字符，如 daily-digest，**不要从 name 直接派生**）、name（必填，1-40 字）、" +
		"description（≤120 字）、avatar（spark|chart|code|pen|search|briefcase）、tags（≤8 个，每个 ≤20 字）、members{experts,automations,skills,mcps}（各 ≤50，引用既有 id）、" +
		"entities[{kind}]（≤16，kind 须平台在册）、tool_domains（≤16，单段小写字母数字、不含下划线）、slots[{impl_id}]（≤16，impl_id 须平台在册）、page{nav_label,route,component}、settings_schema、api_version（caret，如 ^1）、enabled（默认 true）。" +
		"**先用自然语言和用户确认应用名/用途/组合哪些能力再建**。"
	descUpdate = "增量更新一个应用（只改传入的键，未传的保留；null 删键）。参数：id（必填，不可改——要换 id 就删掉重建），以及要改的 name/description/avatar/tags/members/entities/tool_domains/slots/page/settings_schema/api_version/enabled。**用自然语言确认，不暴露 JSON 细节**。"
	descList   = "列出全部应用（含已禁用的）。返回 [{id,name,description,avatar,tags,members,entities,tool_domains,slots,api_version,enabled,has_page}]；**不含 settings_schema 正文**（要看正文用 app_get）。"
	descGet    = "取一个应用详情（含 settings_schema 全文与 page）。参数：id（必填）。"
	descDelete = "删除一个应用（幂等：不存在 → deleted=false 且无错）。参数：id（必填）。**删前先用 app_get 把要删的应用复述给用户确认**——删除不可撤销。"
	descToggle = "启用/禁用一个应用。参数：id（必填）、enabled（必填）。**禁用只影响新会话/新注入，不删除定义、不影响历史会话**。"
)

// appSchema 应用已知字段的 JSON Schema 片段（create/update 共用，required 不同）。
//
// 与 `capability.ValidateApp`（backend/internal/core/capability/app.go）逐条对齐：
// id 正则 ≤64 / name 1-40 / description ≤120 / avatar 枚举 / tags ≤8 每个 ≤20 /
// members 每类 ≤50 / entities ≤16（kind 词法）/ tool_domains ≤16（单段、不含下划线）/
// slots ≤16（impl_id 词法）/ page 三字段 / api_version caret。
// **服务端 ValidateApp + 白名单在册判断仍是唯一闸门**：schema 只是让模型一次填对。
func appSchema(withID bool) string {
	idField := ""
	if withID {
		idField = `"id":{"type":"string","pattern":"^[a-z0-9]+(-[a-z0-9]+)*$","maxLength":64,"description":"小写字母数字连字符，如 daily-digest，创建后不可改"},`
	}
	return fmt.Sprintf(`{
  "type":"object",
  "properties":{
    %s
    "name":{"type":"string","minLength":1,"maxLength":40},
    "description":{"type":"string","maxLength":120},
    "avatar":{"type":"string","enum":["spark","chart","code","pen","search","briefcase"]},
    "tags":{"type":"array","items":{"type":"string","maxLength":20},"maxItems":8},
    "members":{"type":"object","properties":{
      "experts":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true},
      "automations":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true},
      "skills":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true},
      "mcps":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true}
    },"additionalProperties":false},
    "entities":{"type":"array","maxItems":16,"items":{"type":"object","properties":{
      "kind":{"type":"string","pattern":"^[a-z][a-z0-9_]*$"},
      "table":{"type":"string"},
      "id_prefix":{"type":"string"}
    },"required":["kind"],"additionalProperties":false}},
    "tool_domains":{"type":"array","items":{"type":"string","pattern":"^[a-z][a-z0-9]*$"},"maxItems":16,"uniqueItems":true,"description":"域键单段、不含下划线（前缀分派要求）"},
    "slots":{"type":"array","maxItems":16,"items":{"type":"object","properties":{
      "impl_id":{"type":"string","pattern":"^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$"}
    },"required":["impl_id"],"additionalProperties":false}},
    "page":{"type":"object","properties":{
      "nav_label":{"type":"string","minLength":1},
      "route":{"type":"string","pattern":"^/[a-z0-9]+(?:[/-][a-z0-9]+)*/?$"},
      "component":{"type":"string","minLength":1}
    },"required":["nav_label","route","component"],"additionalProperties":false},
    "settings_schema":{"type":"string"},
    "api_version":{"type":"string","pattern":"^\\^\\d+(?:\\.\\d+)?$","description":"caret 范围，如 ^1 或 ^1.5"},
    "enabled":{"type":"boolean"}
  },
  "additionalProperties":false
}`, idField)
}

// ListTools 给出 app 域的全部工具。
func (h *Handler) ListTools(_ context.Context) ([]protocol.McpTool, error) {
	createSchema := strings.Replace(appSchema(true), `"additionalProperties":false`,
		`"required":["id","name"],"additionalProperties":false`, 1)
	updateSchema := strings.Replace(appSchema(true), `"additionalProperties":false`,
		`"required":["id"],"additionalProperties":false`, 1)
	return []protocol.McpTool{
		{Name: ToolCreate, Description: descCreate, InputSchema: json.RawMessage(createSchema)},
		{Name: ToolUpdate, Description: descUpdate, InputSchema: json.RawMessage(updateSchema)},
		{Name: ToolList, Description: descList, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{Name: ToolGet, Description: descGet, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: ToolDelete, Description: descDelete, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: ToolSetEnabled, Description: descToggle, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"enabled":{"type":"boolean"}},"required":["id","enabled"],"additionalProperties":false}`)},
	}, nil
}

// CallTool 分发。
func (h *Handler) CallTool(ctx context.Context, name string, args json.RawMessage) (protocol.McpCallResult, error) {
	switch name {
	case ToolCreate:
		return h.callCreate(ctx, args)
	case ToolUpdate:
		return h.callUpdate(ctx, args)
	case ToolList:
		return h.callList(ctx)
	case ToolGet:
		return h.callGet(ctx, args)
	case ToolDelete:
		return h.callDelete(ctx, args)
	case ToolSetEnabled:
		return h.callSetEnabled(ctx, args)
	default:
		return errResult(fmt.Errorf("未知工具 %q", name)), nil
	}
}

// textResult 把任意值编成一条 text 内容结果（模型直接读到 JSON）。
func textResult(v any) (protocol.McpCallResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return protocol.McpCallResult{}, err
	}
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": string(raw)})
	return protocol.McpCallResult{Content: []json.RawMessage{blk}}, nil
}

// errResult 编成 isError 结果（模型看得到原因，会话不炸）。
func errResult(err error) protocol.McpCallResult {
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": "错误：" + err.Error()})
	return protocol.McpCallResult{Content: []json.RawMessage{blk}, IsError: true}
}

// buildWriteReq 把工具入参拆成「id + patch」：id 不进 patch 体（create 用 id 定 body，update 用路径 id）。
func buildWriteReq(in map[string]json.RawMessage) (id string, patch dtoapp.AppWriteReq, err error) {
	patch = dtoapp.AppWriteReq{}
	for k, v := range in {
		if k == "id" {
			var s string
			if e := json.Unmarshal(v, &s); e != nil {
				return "", nil, fmt.Errorf("id 须为字符串: %w", e)
			}
			id = s
			continue
		}
		patch[k] = v
	}
	return id, patch, nil
}

func (h *Handler) callCreate(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
		}
	}
	id, patch, err := buildWriteReq(in)
	if err != nil {
		return errResult(err), nil
	}
	if strings.TrimSpace(id) == "" {
		return errResult(fmt.Errorf("缺少 id（应用 id 必填，小写字母数字连字符，如 daily-digest）")), nil
	}
	// 写面 create 分支从 patch 的 `id` 取 id（pathID 传空）。
	rawID, _ := json.Marshal(id)
	patch["id"] = rawID
	resp, err := h.svc.WriteApp(ctx, "", patch, true)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callUpdate(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	id, patch, err := buildWriteReq(in)
	if err != nil {
		return errResult(err), nil
	}
	if strings.TrimSpace(id) == "" {
		return errResult(fmt.Errorf("缺少 id（要改哪一个应用）")), nil
	}
	resp, err := h.svc.WriteApp(ctx, id, patch, false)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callList(ctx context.Context) (protocol.McpCallResult, error) {
	items, err := h.svc.ListApps(ctx)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(items)
}

func (h *Handler) callGet(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if strings.TrimSpace(in.ID) == "" {
		return errResult(fmt.Errorf("缺少 id")), nil
	}
	resp, err := h.svc.GetApp(ctx, in.ID)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callDelete(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if strings.TrimSpace(in.ID) == "" {
		return errResult(fmt.Errorf("缺少 id")), nil
	}
	resp, err := h.svc.DeleteApp(ctx, in.ID)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callSetEnabled(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if strings.TrimSpace(in.ID) == "" {
		return errResult(fmt.Errorf("缺少 id")), nil
	}
	if in.Enabled == nil {
		return errResult(fmt.Errorf("缺少 enabled（true 启用 / false 禁用）")), nil
	}
	resp, err := h.svc.SetAppEnabled(ctx, in.ID, *in.Enabled)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}
