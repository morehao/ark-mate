package llm

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objid"
)

// Gateway 是内部模型调用的**唯一入口**。业务只依赖它，不关心底层执行器。
type Gateway interface {
	Call(ctx context.Context, req Request) (Result, error)
}

// Executor 是一种执行策略（会话 / 直连）。可替换、可扩；`Gateway` 默认实现按 policy 选一个。
type Executor interface {
	Call(ctx context.Context, req Request) (Result, error)
}

// Recorder 记录一条调用流水。**由网关在每次调用后调用**（调用方不接触它）。
//
// 为什么记账由网关而非各 Executor 做：Executor 可换、可加，但"漏记"必须不可能——网关是
// 唯一咽喉。失败调用同样记一条（可能已计费，且失败率本身是运维信号）。
type Recorder interface {
	Record(ctx context.Context, c LLMCall) error
}

// ProviderInfo 一个供应商的**解析结果**（供执行器发请求；`Key` 是明文，绝不是落档物）。
type ProviderInfo struct {
	// Protocol 协议（如 "openai"）。执行器据此选适配；未识别 → 显式错误，不猜。
	Protocol string
	// BaseURL 上游端点（如 https://api.deepseek.com/v1）。
	BaseURL string
	// Key 生效的明文密钥（空 = 没有密钥；执行器应显式报错，不静默发空 key）。
	Key string
}

// ProviderSource 解析"用哪个供应商怎么拨号"（**端口**：由装配根接上平台 provider 数据面）。
//
// 执行器只认这个窄端口，不直接 import 领域面——这样 `internal/infra/llm` 与 provider 域解耦，
// 也让单测能注入假实现而不碰真库/真密钥。
type ProviderSource interface {
	// Resolve 按 provider 档案 id 取解析后的拨号信息。不存在 → 显式错误。
	Resolve(ctx context.Context, providerID string) (ProviderInfo, error)
}

// Policy 用途 → 默认执行器的裁决（显式策略表，不做启发式——启发式在成本/延迟上是双输）。
type Policy func(purpose Purpose) ExecutorKind

// GatewayConfig 构造网关的依赖。
type GatewayConfig struct {
	// Executors 已就绪的执行器（键为类别）。
	Executors map[ExecutorKind]Executor
	// Policy 用途 → 默认执行器；nil = 退回 defaultPolicy。
	Policy Policy
	// Recorder 流水记录器；nil = 不记账（仅测试/演示；生产必须注入）。
	Recorder Recorder
	// Now 时钟（nil = time.Now；测试可注入）。
	Now func() time.Time
	// NewID 铸号（nil = objid.New(objid.Call)；测试可注入）。
	NewID func() string
}

// NewGateway 用配置构造默认网关（纯编排：选执行器 → 调用 → 记账，零 IO）。
func NewGateway(cfg GatewayConfig) Gateway {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	newID := cfg.NewID
	if newID == nil {
		newID = func() string { return objid.New(objid.Call) }
	}
	policy := cfg.Policy
	if policy == nil {
		policy = defaultPolicy
	}
	return &gateway{executors: cfg.Executors, policy: policy, rec: cfg.Recorder, now: now, newID: newID}
}

// defaultPolicy 各用途的默认执行器：需要工具/多轮/上下文的走 Stateful，其余走轻量直连。
func defaultPolicy(p Purpose) ExecutorKind {
	switch p {
	case PurposeAgentTurn, PurposeSummary:
		return ExecutorStateful
	case PurposeEmbed, PurposeJudge, PurposeRewrite, PurposeTitle, PurposeExtract:
		return ExecutorStateless
	default:
		// 未知用途不猜：退回 Stateful（能力最全）。宁可重，不可静默失败。
		return ExecutorStateful
	}
}

type gateway struct {
	executors map[ExecutorKind]Executor
	policy    Policy
	rec       Recorder
	now       func() time.Time
	newID     func() string
}

// Call 选执行器、执行、记一条流水（成败都记）。执行器缺失 → 记 error 流水并返回明确错误。
func (g *gateway) Call(ctx context.Context, req Request) (Result, error) {
	kind := req.Prefer
	if kind == "" {
		kind = g.policy(req.Purpose)
	}
	start := g.now()

	exec, ok := g.executors[kind]
	if !ok {
		err := &executorMissingError{kind: kind, purpose: req.Purpose}
		g.record(ctx, req, Result{}, start, StatusError, kind)
		return Result{}, err
	}

	res, err := exec.Call(ctx, req)
	// 归一 Outcome：执行器没显式给时按 err 兜底（无 err 即 OK，有 err 即 Failed）。
	if res.Outcome == "" {
		if err == nil {
			res.Outcome = OutcomeOK
		} else {
			res.Outcome = OutcomeFailed
		}
	}
	status := statusFor(ctx, res.Outcome, err)
	g.record(ctx, req, res, start, status, kind)
	return res, err
}

// record 组装并落一条流水。Recorder 为 nil 时跳过（测试/演示）。记账失败不覆盖主调用的结果
// （按设计记账是旁路：它不能把一次成功的模型调用变成失败）。
func (g *gateway) record(ctx context.Context, req Request, res Result, start time.Time, status string, _ ExecutorKind) {
	if g.rec == nil {
		return
	}
	call := LLMCall{
		ID:           g.newID(),
		At:           start,
		Purpose:      req.Purpose,
		SessionID:    req.SessionID,
		RunID:        req.RunID,
		Provider:     req.Provider,
		Model:        req.Model,
		Usage:        res.Usage,
		CostAmount:   res.CostAmount,
		CostCurrency: res.CostCurrency,
		LatencyMS:    g.now().Sub(start).Milliseconds(),
		Status:       status,
	}
	_ = g.rec.Record(ctx, call)
}

// statusFor 依 Outcome 与 err/ctx 判流水状态。
//
// 优先按 Outcome：Degraded → StatusDegraded；Failed → 再看 ctx/err 区分 timeout/cancelled/error；
// Outcome 为空/OK 时回落 classifyStatus 的 err 逻辑。
func statusFor(ctx context.Context, outcome Outcome, err error) string {
	switch outcome {
	case OutcomeDegraded:
		return StatusDegraded
	case OutcomeFailed:
		// 业务失败：有 ctx 取消/超时按更精确的 status，否则记 error。
		if ctx.Err() == context.Canceled {
			return StatusCancelled
		}
		if ctx.Err() == context.DeadlineExceeded {
			return StatusTimeout
		}
		return StatusError
	}
	return classifyStatus(ctx, err)
}

// classifyStatus 依 err/ctx 判流水状态。执行器未给状态时用这个兜底。
func classifyStatus(ctx context.Context, err error) string {
	if err == nil {
		return StatusOK
	}
	if ctx.Err() == context.Canceled {
		return StatusCancelled
	}
	if ctx.Err() == context.DeadlineExceeded {
		return StatusTimeout
	}
	return StatusError
}

// executorMissingError 执行器未装配（编程/装配错误，不是用户输入问题）。
type executorMissingError struct {
	kind    ExecutorKind
	purpose Purpose
}

func (e *executorMissingError) Error() string {
	return "llm: 未装配执行器 " + string(e.kind) + "（purpose=" + string(e.purpose) + "）"
}
