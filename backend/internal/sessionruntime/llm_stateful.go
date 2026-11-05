package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtime"
)

// internalSessionOrigin 内部（模型网关）会话在 `sessions.origin` 上的取值。
//
// 与既有 `”`（人工）/ `'automation:<id>'`（自动化产物）同一轴：会话列表 / 全文搜索 / 用户维度
// 用量 UI 统一按 `origin LIKE 'internal:%'` 过滤掉它们（它们不是用户的会话，是辅助调用）。
const internalSessionOrigin = "internal:llm"

// StatefulExecutor 是 `corellm.Executor` 的**会话执行器**实现（L7）：一次调用 = 建一个内部会话
// → 发一轮 prompt → 等结束 → 读回正文与 token 明细。
//
// 为什么放在 sessionruntime：它必须驱动 `Orchestrator`（建会话 / Prompt / WaitRun），这是会话
// 编排的职责；放在别处要么越层、要么把整条编排链路复制一遍。它实现的是 L1 契约 `corellm.Executor`，
// 故只需装配根把本类型塞进网关的 executors 表。
type StatefulExecutor struct {
	orch *Orchestrator
	// defaultModel 未指定模型时的兜底（空 = 交由建会话的默认模型逻辑）。
	defaultModel string
	// cwd 内部会话的工作目录（空 = 用平台当前工作目录，与 RunOnce 同语义）。
	cwd string
}

var _ corellm.Executor = (*StatefulExecutor)(nil)

// NewStatefulExecutor 构造会话执行器（由装配根调用）。
func NewStatefulExecutor(orch *Orchestrator, defaultModel, cwd string) *StatefulExecutor {
	return &StatefulExecutor{orch: orch, defaultModel: defaultModel, cwd: cwd}
}

// Call 实现 corellm.Executor：把请求编成一段文本 prompt，跑一个内部会话，读回正文与 token 明细。
//
// 注意：会话执行器**即用即弃**（每次调用建一个新会话，不复用历史），所以它适合"要工具/多轮
// 能力、但不需要跨调用上下文"的辅助调用。需要跨调用上下文的场景应由调用方显式带 messages。
func (e *StatefulExecutor) Call(ctx context.Context, req corellm.Request) (corellm.Result, error) {
	if e.orch == nil {
		return corellm.Result{}, fmt.Errorf("llm: 会话执行器未接 Orchestrator")
	}
	prompt := composeInternalPrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return corellm.Result{}, fmt.Errorf("llm: 会话执行器的请求没有可发送的文本")
	}
	model := req.Model
	if model == "" {
		model = e.defaultModel
	}

	res, err := e.orch.startSessionOn(ctx, session.StartOptions{
		Cwd:    e.cwd,
		Model:  runtime.ModelConfig{Provider: req.Provider, Model: model},
		Origin: internalSessionOrigin,
	})
	if err != nil {
		return corellm.Result{}, err
	}
	if _, err := e.orch.Prompt(ctx, res.SessionID, prompt); err != nil {
		return corellm.Result{}, err
	}
	if _, err := e.orch.WaitRun(ctx, res.SessionID); err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return corellm.Result{}, err
	}
	return e.collectResult(ctx, res.SessionID)
}

// collectResult 从会话读回本轮的助手正文与 token 明细。
//
// 正文：拼该会话全部 `session.message` 事件的 text（内部会话只跑一轮，故整会话即本轮）。
// 明细：取该会话最后一条已收口 run 的 `token_usage_json`（步骤 1 落的逐轮 token 流水）。
func (e *StatefulExecutor) collectResult(ctx context.Context, sessionID string) (corellm.Result, error) {
	pl := e.orch.svc.sessionPlane

	evs, err := pl.SessionEvents(ctx, sessionID)
	if err != nil {
		return corellm.Result{}, err
	}
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Type != session.BusMessage {
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ev.PayloadJSON), &p) == nil && p.Text != "" {
			sb.WriteString(p.Text)
		}
	}

	var usage objusage.TokenUsage
	runs, err := pl.SessionRuns(ctx, sessionID)
	if err != nil {
		return corellm.Result{}, err
	}
	if n := len(runs); n > 0 {
		last := runs[n-1]
		if last.TokenUsageJSON != "" {
			_ = json.Unmarshal([]byte(last.TokenUsageJSON), &usage)
		}
	}
	return corellm.Result{Text: sb.String(), Usage: usage}, nil
}

// composeInternalPrompt 把 Request（System + Messages）编成送给会话的纯文本 prompt。
//
// 会话执行器只吃纯文本（ACP prompt 的 text part）：system 作为前置说明、messages 逐条以
// `角色: 内容` 拼接。这是"用 agent 能力跑一次辅助调用"的既定形态；结构化多轮应走直连执行器。
func composeInternalPrompt(req corellm.Request) string {
	var sb strings.Builder
	if strings.TrimSpace(req.System) != "" {
		sb.WriteString(req.System)
		sb.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = corellm.RoleUser
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(m.Text)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}
