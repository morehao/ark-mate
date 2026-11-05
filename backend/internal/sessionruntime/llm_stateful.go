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

	"github.com/morehao/golib/glog"
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
	// G2 结论：ACP 协议层不提供强制结构化输出，Stateful 无法产出 Result.Structured。
	// 收到非空 Schema 时显式告警（不静默当作没要求），结构化需求应改用 Stateless 执行器。
	if req.Schema != nil {
		glog.Warnw(ctx, "llm: Stateful 执行器不支持结构化输出，Schema 被忽略；结构化请改用 Stateless",
			"purpose", string(req.Purpose))
	}
	// 硬约束：agent 轮次若既无步数也无超时，默认拒绝（防内部会话跑飞）。AllowUnbounded 可显式放行。
	if req.Purpose == corellm.PurposeAgentTurn && req.MaxSteps <= 0 && req.Timeout <= 0 && !req.AllowUnbounded {
		return corellm.Result{Outcome: corellm.OutcomeFailed}, corellm.ErrUnboundedAgentTurn
	}
	prompt := composeInternalPrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return corellm.Result{}, fmt.Errorf("llm: 会话执行器的请求没有可发送的文本")
	}
	model := req.Model
	if model == "" {
		model = e.defaultModel
	}

	// 超时：Request.Timeout 叠加在 ctx 上（谁先到谁生效）。
	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	// 工具裁剪：白名单非空才构造 Selection（nil 子项 = 该维度全量）。
	var sel *session.CapabilitySelection
	if req.AllowedSkills != nil || req.AllowedMcps != nil {
		sel = buildSelection(req.AllowedSkills, req.AllowedMcps)
	}

	res, err := e.orch.startSessionOn(runCtx, session.StartOptions{
		Cwd:       e.cwd,
		Model:     runtime.ModelConfig{Provider: req.Provider, Model: model},
		Origin:    internalSessionOrigin,
		Selection: sel,
	})
	if err != nil {
		return corellm.Result{}, err
	}
	if _, err := e.orch.Prompt(runCtx, res.SessionID, prompt); err != nil {
		return corellm.Result{}, err
	}

	// 等待本轮结束，期间按预算观察实时事件。WaitRun 放 goroutine：主流程 select 事件累计预算，
	// 超限则 CancelRun（WaitRun 会随之返回），正常则由 WaitRun 返回收尾。
	runErrCh := make(chan error, 1)
	go func() {
		_, err := e.orch.WaitRun(runCtx, res.SessionID)
		runErrCh <- err
	}()

	budgetErr := e.waitWithBudget(runCtx, res.SessionID, req.MaxSteps, req.TokenBudget, runErrCh)
	if budgetErr != nil {
		// 中止会话（复用既有 CancelRun，不硬杀进程），尽量读回已产出的正文与 token。
		_ = e.orch.CancelRun(context.WithoutCancel(ctx), res.SessionID)
		// 等 WaitRun 收尾（取消后应立即返回；用调用方 ctx 兜底）。
		select {
		case <-runErrCh:
		case <-ctx.Done():
		}
	}

	out, cErr := e.collectResult(ctx, res.SessionID)
	if cErr != nil {
		return corellm.Result{}, cErr
	}
	if budgetErr != nil {
		out.Outcome = corellm.OutcomeFailed
		return out, budgetErr
	}
	return out, nil
}

// waitWithBudget 订阅会话实时事件流，累计 `session.tool_call` 次数与 token，达上限则返回错误；
// run 结束（runErrCh 收到 WaitRun 的返回）时返回 nil。
//
// 语义近似（见执行计划 §7 G1）：底座不回传真实推理步数，此处以**可见的 tool_call 事件数**作 MaxSteps。
// 无总线（测试/最小装配）时退化为纯等待。
func (e *StatefulExecutor) waitWithBudget(ctx context.Context, sessionID string, maxSteps int, tokenBudget int64, runErrCh <-chan error) error {
	bus := e.orch.svc.bus
	if bus == nil {
		// 无总线：只能等 run 结束（由调用方 ctx 施加超时）。
		select {
		case <-runErrCh:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
	if maxSteps <= 0 && tokenBudget <= 0 {
		// 无预算约束：等 run 结束即可，不必订阅。
		select {
		case <-runErrCh:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
	// 从"当前水位之后"订阅：用当前最后位次作起点，避免回放历史 tool_call 重复计数。
	start := bus.LastSeq(sessionID)
	sub := bus.SubscribeAfter(map[string]int64{sessionID: start})
	defer bus.Unsubscribe(sub)

	steps := 0
	var tokens int64
	for {
		select {
		case <-ctx.Done():
			return nil // 超时/取消由 ctx 与 WaitRun 处理，不算预算超限
		case <-runErrCh:
			return nil // run 已结束
		case ev, ok := <-sub.C:
			if !ok {
				return nil
			}
			if ev.AggregID != sessionID {
				continue
			}
			switch ev.Type {
			case session.BusToolCall:
				steps++
				if maxSteps > 0 && steps > maxSteps {
					return &corellm.StepExceededError{Limit: maxSteps, Actual: steps}
				}
			case session.BusUsage:
				tokens = tokensOfEvent(ev.Payload)
				if tokenBudget > 0 && tokens > tokenBudget {
					return &corellm.BudgetExceededError{Kind: "token", Limit: tokenBudget, Actual: tokens}
				}
			}
		case <-sub.Gap():
			// 丢帧（慢订阅）：预算已无法精确，退化为不限制，交回正常等待，避免误判。
			return nil
		}
	}
}

// tokensOfEvent 从 BusUsage 载荷里尽力取累计 token（形状可能因底座而异，取不到记 0）。
func tokensOfEvent(payload any) int64 {
	m, ok := payload.(map[string]any)
	if !ok {
		return 0
	}
	for _, k := range []string{"total_tokens", "totalTokens", "used"} {
		if v, ok := m[k]; ok {
			if n, ok := toInt64(v); ok {
				return n
			}
		}
	}
	return 0
}

// runStatusOf 从 BusRunStatus 载荷里取 status。
// runStatusOf 已不需要（run 结束改由 WaitRun 的返回信号判定，避免引用 BusRunStatus——
// 该常量只能在 domain/session 铸造点使用，见 TestRunStatusIsSingleSource）。

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// buildSelection 把白名单映射成领域 CapabilitySelection（nil 子项 = 该维度全量）。
func buildSelection(skills, mcps []string) *session.CapabilitySelection {
	sel := &session.CapabilitySelection{}
	if skills != nil {
		s := append([]string{}, skills...)
		sel.Skills = &s
	}
	if mcps != nil {
		m := append([]string{}, mcps...)
		sel.Mcps = &m
	}
	return sel
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
	return corellm.Result{Text: sb.String(), Usage: usage, Outcome: corellm.OutcomeOK}, nil
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
