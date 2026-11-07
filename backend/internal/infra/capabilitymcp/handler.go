// Package capabilitymcp 把「专家定义」暴露成一条 **MCP 工具面**，交给底座（dsh）经 ACP 的
// `mcpServers` 拉起（`mate mcp platform` 的 expert 域，见 internal/bootstrap/maintenance/platform_mcp.go）。
//
// 目的：让用户用**自然语言**创建/编辑专家——模型把"帮我建个代码审查员专家，用 glm 模型"翻译成
// `expert_create` 调用，而不是让用户手写 JSON 或去面板逐字段填。
//
// 三条边界（与 automationmcp 一脉相承）：
//   - **自声明窄端口**（`Service`，用 `dtocapability` 值），本包**不 import 用例层**（层规：
//     只有装配根与控制器可以引用 `service/svc*`）；
//   - **schema 与 `capability.ValidateExpert` 同口径**：能机器表达的约束（id 正则 / model 前缀 /
//     instructions 字节上限 / avatar 枚举 / 引用条数）都写进 schema，让模型一次填对；但
//     **服务端 `ValidateExpert` 仍是唯一闸门**（危险标记、UTF-8 字节精确值等 schema 表达不了的，
//     服务端必拒，返 `40001 definition_invalid`）；
//   - 工具 description **不向用户暴露内部格式**，要求用自然语言与用户确认（对齐 DSH 教训）。
package capabilitymcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// 工具名（固定；与 `mate mcp platform` 的 expert 域前缀一致）。
const (
	ToolCreate     = "expert_create"
	ToolUpdate     = "expert_update"
	ToolList       = "expert_list"
	ToolGet        = "expert_get"
	ToolDelete     = "expert_delete"
	ToolSetEnabled = "expert_set_enabled"
)

// Service 是专家工具面需要的**窄端口**（本包自声明，由装配根用 svccapability 实现）。
//
// 只声明工具面真正用到的五个方法（用 dto 值，不 import 用例层）。写面（Create/Update）透传
// `ExpertWriteReq`（原文 JSON patch），让"只改出现过的键"这条语义在 MCP 侧与 REST 侧一致。
type Service interface {
	ListExperts(ctx context.Context) ([]dtocapability.ExpertResp, error)
	GetExpert(ctx context.Context, id string) (*dtocapability.ExpertResp, error)
	WriteExpert(ctx context.Context, pathID string, req dtocapability.ExpertWriteReq, create bool) (*dtocapability.ExpertWriteResp, error)
	DeleteExpert(ctx context.Context, id string) (*dtocapability.ExpertDeleteResp, error)
	SetExpertEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.ExpertToggleResp, error)
}

// Handler 是专家工具面的实现（满足 mcp.Handler）。
type Handler struct {
	svc Service
}

// New 构造 Handler（svc 由装配根交下来）。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// 工具描述（会给模型看）。
const (
	descCreate = "创建一个专家（会话级能力预设：推荐技能/MCP/模型 + 角色设定）。参数：id（必填，小写字母数字连字符，如 code-reviewer，**不要从 name 直接派生**）、name（必填，1-40 字）、" +
		"description（≤120 字）、avatar（spark|chart|code|pen|search|briefcase）、model（平台模型行 id，形如 mdl_…，可空）、skills/mcps（字符串数组，≤50，允许暂指不存在的 id）、" +
		"tags（≤8 个，每个 ≤20 字）、instructions（角色设定：纯文本，≤4096 **字节**，**不能含 {{ }} <!-- --> </system-reminder>**）、enabled（默认 true）。" +
		"**先用自然语言和用户确认专家名/用途/推荐能力再建**。专家是「新建会话时的预设」——创建成功**不会让当前会话变成该专家**，要告诉用户下次新建会话可选它。"
	descUpdate = "增量更新一个专家（只改传入的键，未传的保留；null 删键）。参数：id（必填，不可改——要换 id 就删掉重建），以及要改的 name/description/avatar/model/skills/mcps/tags/instructions/enabled。**用自然语言确认，不暴露 JSON 细节**。"
	descList   = "列出全部专家（含已禁用的）。返回 [{id,name,description,avatar,model,skills,mcps,enabled,tags,has_instructions}]；**不含 instructions 正文**（要看正文用 expert_get）。"
	descGet    = "取一个专家详情（含 instructions 全文）。参数：id（必填）。"
	descDelete = "删除一个专家（幂等：不存在 → deleted=false 且无错）。参数：id（必填）。**删前先用 expert_get 把要删的专家复述给用户确认**——删除不可撤销。"
	descToggle = "启用/禁用一个专家。参数：id（必填）、enabled（必填）。**禁用只影响新会话的选择，不删除定义、不影响历史会话**。"
)

// expertSchema 专家已知字段的 JSON Schema 片段（create/update 共用，required 不同）。
//
// 与 `capability.ValidateExpert`（backend/internal/core/capability/expert.go:79-127）逐条对齐：
// id 正则 ≤64 / name 1-40 / description ≤120 / avatar 枚举 / model ^mdl_ / skills·mcps ≤50 去重 /
// tags ≤8 每个 ≤20 / instructions ≤4096 字节（schema 的 maxLength 是**字符**近似，服务端按字节硬判）。
func expertSchema(withID bool) string {
	idField := ""
	if withID {
		idField = `"id":{"type":"string","pattern":"^[a-z0-9]+(-[a-z0-9]+)*$","maxLength":64,"description":"小写字母数字连字符，如 code-reviewer，创建后不可改"},`
	}
	return fmt.Sprintf(`{
  "type":"object",
  "properties":{
    %s
    "name":{"type":"string","minLength":1,"maxLength":40},
    "description":{"type":"string","maxLength":120},
    "avatar":{"type":"string","enum":["spark","chart","code","pen","search","briefcase"]},
    "model":{"type":"string","pattern":"^mdl_","description":"平台模型行 id，形如 mdl_…"},
    "skills":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true},
    "mcps":{"type":"array","items":{"type":"string"},"maxItems":50,"uniqueItems":true},
    "tags":{"type":"array","items":{"type":"string","maxLength":20},"maxItems":8},
    "instructions":{"type":"string","maxLength":4096,"description":"角色设定：纯文本，UTF-8 ≤4096 字节，禁止含 {{ }} <!-- --> </system-reminder>"},
    "enabled":{"type":"boolean"}
  },
  "additionalProperties":false
}`, idField)
}

// ListTools 给出专家域的全部工具。
func (h *Handler) ListTools(_ context.Context) ([]protocol.McpTool, error) {
	createSchema := strings.Replace(expertSchema(true), `"additionalProperties":false`,
		`"required":["id","name"],"additionalProperties":false`, 1)
	updateSchema := strings.Replace(expertSchema(true), `"additionalProperties":false`,
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
func buildWriteReq(in map[string]json.RawMessage) (id string, patch dtocapability.ExpertWriteReq, err error) {
	patch = dtocapability.ExpertWriteReq{}
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
		return errResult(fmt.Errorf("缺少 id（专家 id 必填，小写字母数字连字符，如 code-reviewer）")), nil
	}
	// 写面 create 分支从 patch 的 `id` 取 id（pathID 传空）。
	rawID, _ := json.Marshal(id)
	patch["id"] = rawID
	resp, err := h.svc.WriteExpert(ctx, "", patch, true)
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
		return errResult(fmt.Errorf("缺少 id（要改哪一个专家）")), nil
	}
	resp, err := h.svc.WriteExpert(ctx, id, patch, false)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callList(ctx context.Context) (protocol.McpCallResult, error) {
	items, err := h.svc.ListExperts(ctx)
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
	resp, err := h.svc.GetExpert(ctx, in.ID)
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
	resp, err := h.svc.DeleteExpert(ctx, in.ID)
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
	resp, err := h.svc.SetExpertEnabled(ctx, in.ID, *in.Enabled)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}
