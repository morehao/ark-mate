// Package automationmcp 把「定时任务」暴露成一条 **MCP 工具面**，交给底座（dsh）经 ACP 的
// `mcpServers` 拉起（`mate mcp automation` 子命令，见 internal/bootstrap/maintenance/automation_mcp.go）。
//
// 目的：让用户用**自然语言**创建/编辑定时任务——模型把"每天 9 点在这个会话提醒我喝水"翻译成
// `automation_create` 调用，而不是让用户手写 JSON 或规则串。
//
// 三条边界：
//   - **自声明窄端口**（`Service`，用 `dtoautomation` 值），本包**不 import 用例层**（层规：
//     只有装配根与控制器可以引用 `service/svc*`）；
//   - **schedule 五选一（oneOf）**：once / interval / daily / weekly / monthly，**没有 cron**
//     ——模型永远只填语义字段，不产语法串（ADR v35）；
//   - 工具的 description **不给用户看规则串**、要求用自然语言对话（对齐 DSH 教训）。
package automationmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// 工具名（固定；G5 契约快照钉住）。
const (
	ToolCreate  = "automation_create"
	ToolUpdate  = "automation_update"
	ToolList    = "automation_list"
	ToolGet     = "automation_get"
	ToolDelete  = "automation_delete"
	ToolPreview = "automation_preview"
)

// Service 是定时任务工具面需要的**窄端口**（本包自声明，由装配根用 svcautomation 实现）。
//
// 只声明工具面真正用到的六个方法（用 dto 值，不 import 用例层）。写面（Create/Update）透传
// `AutomationWriteReq`（原文 JSON patch），让"只改出现过的键"这条语义在 MCP 侧与 REST 侧一致。
type Service interface {
	ListAutomations() ([]dtoautomation.AutomationResp, error)
	GetAutomation(id string) (*dtoautomation.AutomationResp, error)
	CreateAutomation(ctx context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error)
	UpdateAutomation(ctx context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error)
	DeleteAutomation(ctx context.Context, id string) (*dtoautomation.AutomationDeleteResp, error)
	PreviewAutomation(req dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error)
	ListAutomationRuns(id string) (*dtoautomation.AutomationRunsResp, error)
}

// Handler 是定时任务的工具面实现（满足 mcp.Handler）。
type Handler struct {
	svc Service
}

// New 构造 Handler（svc 由装配根/子命令交下来）。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// 工具描述（会给模型看）。
const (
	descCreate = "创建一条定时任务。参数：id（必填，小写字母数字连字符，如 water-reminder，别从 name 派生）、name（必填，≤120 字）、" +
		"schedule（五选一：once{timezone,at} / interval{timezone,every_seconds≥60} / daily{timezone,hour,minute} / weekly{timezone,hour,minute,days_of_week} / monthly{timezone,hour,minute,day 1-31}）、" +
		"target（必填：prompt=到点要发给模型的话**必须放在 target 里**；delivery=session(默认，投递回指定会话，需 session_id) 或 new_session(需 cwd 绝对路径)）。" +
		"**用自然语言和用户确认时间，绝不把规则串给用户看**。用户说\"在这个会话/在这里/提醒我\"→delivery=session 且 session_id=当前会话；说\"单独跑/别打扰我\"→new_session。"
	descUpdate = "增量更新一条定时任务（只改传入的键，未传的保留）。参数：id（必填）、以及要改的 name/schedule/target/enabled/state（prompt 在 target.prompt 里改）。" +
		"**用自然语言确认，不暴露规则串**。"
	descList    = "列出全部定时任务（含装载失败带 error 的那些）。返回 [{id,name,enabled,state,schedule,target,next_due_at,last_status,error}]；next_due_at 是 Unix 毫秒时间戳，schedule.at 仍是 RFC3339。"
	descGet     = "取一条定时任务详情 + 最近运行史摘要。参数：id（必填）。返回 {automation, runs}。"
	descDelete  = "删除一条定时任务（幂等：不存在 → deleted=false 且无错）。参数：id（必填）。"
	descPreview = "只计算未来若干次到期时刻，**不落盘、不建会话**——用来跟用户确认\"到底几点跑\"。参数：schedule（必填，同 create 的五选一）、count（默认 5，上限 20）、from（可选 RFC3339 起点）。返回未来时刻列表（含本地墙钟）。"
)

// scheduleSchema 五选一（oneOf），无 cron。
func scheduleSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "description":"触发时间，五选一",
  "oneOf":[
    {"type":"object","properties":{"kind":{"const":"once"},"timezone":{"type":"string","description":"IANA 时区，如 Asia/Shanghai"},"at":{"type":"string","description":"绝对时刻 RFC3339"}},"required":["kind","timezone","at"],"additionalProperties":false},
    {"type":"object","properties":{"kind":{"const":"interval"},"timezone":{"type":"string"},"every_seconds":{"type":"integer","minimum":60,"description":"每多少秒触发一次（≥60）"}},"required":["kind","every_seconds"],"additionalProperties":false},
    {"type":"object","properties":{"kind":{"const":"daily"},"timezone":{"type":"string"},"hour":{"type":"integer","minimum":0,"maximum":23},"minute":{"type":"integer","minimum":0,"maximum":59}},"required":["kind","timezone","hour","minute"],"additionalProperties":false},
    {"type":"object","properties":{"kind":{"const":"weekly"},"timezone":{"type":"string"},"hour":{"type":"integer","minimum":0,"maximum":23},"minute":{"type":"integer","minimum":0,"maximum":59},"days_of_week":{"type":"array","items":{"type":"integer","minimum":0,"maximum":6},"description":"0=周日"}},"required":["kind","timezone","hour","minute","days_of_week"],"additionalProperties":false},
    {"type":"object","properties":{"kind":{"const":"monthly"},"timezone":{"type":"string"},"hour":{"type":"integer","minimum":0,"maximum":23},"minute":{"type":"integer","minimum":0,"maximum":59},"day":{"type":"integer","minimum":1,"maximum":31,"description":"每月第几日（当月无此日则跳过）"}},"required":["kind","timezone","hour","minute","day"],"additionalProperties":false}
  ]
}`)
}

func targetSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "delivery":{"type":"string","enum":["session","new_session"],"description":"投递模式，缺省 session"},
    "session_id":{"type":"string","description":"delivery=session 时的目标会话 id"},
    "cwd":{"type":"string","description":"delivery=new_session 时的工作目录（绝对路径）"},
    "model":{"type":"string"},
    "prompt":{"type":"string","description":"到点发给模型的话"}
  },
  "required":["prompt"],
  "additionalProperties":false
}`)
}

// ListTools 返回六个工具。
func (h *Handler) ListTools(ctx context.Context) ([]protocol.McpTool, error) {
	createSchema := fmt.Sprintf(`{"type":"object","properties":{"id":{"type":"string","pattern":"^[a-z0-9]+([-_][a-z0-9]+)*$","description":"任务 id（小写字母数字，连字符/下划线分段，创建后不可改）"},"name":{"type":"string","minLength":1,"maxLength":120},"schedule":%s,"target":%s,"enabled":{"type":"boolean"}},"required":["id","name","schedule","target"],"additionalProperties":false}`,
		scheduleSchema(), targetSchema())
	updateSchema := fmt.Sprintf(`{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string","minLength":1,"maxLength":120},"schedule":%s,"target":%s,"enabled":{"type":"boolean"},"state":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
		scheduleSchema(), targetSchema())
	return []protocol.McpTool{
		{Name: ToolCreate, Description: descCreate, InputSchema: json.RawMessage(createSchema)},
		{Name: ToolUpdate, Description: descUpdate, InputSchema: json.RawMessage(updateSchema)},
		{Name: ToolList, Description: descList, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		{Name: ToolGet, Description: descGet, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: ToolDelete, Description: descDelete, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		{Name: ToolPreview, Description: descPreview, InputSchema: json.RawMessage(`{"type":"object","properties":{"schedule":{"type":"object"},"count":{"type":"integer","minimum":1,"maximum":20},"from":{"type":"string"}},"required":["schedule"],"additionalProperties":false}`)},
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
	case ToolPreview:
		return h.callPreview(ctx, args)
	default:
		return protocol.McpCallResult{}, mcp.ErrToolNotFound
	}
}

// textResult 编成一段 JSON 文本块。
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

// summary 一条 automation 的紧凑投影（list/get 用；不泄露无关运行态）。
func summary(a *dtoautomation.AutomationResp) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"id":          a.ID,
		"name":        a.Name,
		"enabled":     a.Enabled,
		"state":       a.State,
		"schedule":    a.Schedule,
		"target":      a.Target,
		"next_due_at": a.NextDueAt,
		"last_status": a.LastStatus,
		"error":       a.Error,
	}
}

// buildWriteReq 把工具入参拆成「id + patch」：id 不进 patch 体（Create 用 id 定文件名，Update 用路径 id）。
func buildWriteReq(in map[string]json.RawMessage) (id string, patch dtoautomation.AutomationWriteReq, err error) {
	patch = dtoautomation.AutomationWriteReq{}
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
		return errResult(fmt.Errorf("缺少 id（任务 id 必填，小写字母数字连字符）")), nil
	}
	// id 同时要进 patch（写面 Create 从 patch 取 id 做文件名与 body 一致性校验）。
	rawID, _ := json.Marshal(id)
	patch["id"] = rawID
	resp, err := h.svc.CreateAutomation(ctx, patch)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(summary(resp))
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
		return errResult(fmt.Errorf("缺少 id（要改哪一条）")), nil
	}
	resp, err := h.svc.UpdateAutomation(ctx, id, patch)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(summary(resp))
}

func (h *Handler) callList(ctx context.Context) (protocol.McpCallResult, error) {
	items, err := h.svc.ListAutomations()
	if err != nil {
		return errResult(err), nil
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, summary(&items[i]))
	}
	return textResult(out)
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
	resp, err := h.svc.GetAutomation(in.ID)
	if err != nil {
		return errResult(err), nil
	}
	out := map[string]any{"automation": summary(resp)}
	// 运行史摘要（拿不到不影响主体返回）。
	if runs, rerr := h.svc.ListAutomationRuns(in.ID); rerr == nil && runs != nil {
		out["runs"] = runs.Runs
	}
	return textResult(out)
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
	resp, err := h.svc.DeleteAutomation(ctx, in.ID)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callPreview(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		Schedule json.RawMessage `json:"schedule"`
		Count    int             `json:"count"`
		From     string          `json:"from"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if len(in.Schedule) == 0 {
		return errResult(fmt.Errorf("缺少 schedule（要预览的触发时间）")), nil
	}
	resp, err := h.svc.PreviewAutomation(dtoautomation.AutomationPreviewReq{
		Schedule: in.Schedule, Count: in.Count, From: in.From,
	})
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

// 断言：*Handler 满足 mcp.Handler。
var _ mcp.Handler = (*Handler)(nil)
