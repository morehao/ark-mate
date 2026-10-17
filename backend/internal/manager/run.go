package manager

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/pkg/object/objusage"
)

// ——— 会话轮次的**编排侧入口**（P4a 正文第五十五刀）———
//
// 受理 / 事件出口 / 排队投递 / 收尾整套运行期状态机已进 `domain/session.Runner`；本文件只剩
// 三件**编排侧**的事：
//
//	① `acquireSession`：取（或重建）活句柄——重建要拨连接、恢复要 resume/handoff，属本层；
//	② 轮次边界的代际迁移（`maybeMigrateGeneration`）：要拿连接池与权限/提问的挂起状态；
//	③ 对外的四个入口（Prompt / PromptWithDelivery / CancelRun / WaitRun）——薄转发到 Runner，
//	   调用点（Service / demo / 测试）不用跟着改名。

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
func (o *Orchestrator) Prompt(ctx context.Context, sessionID, text string) (int64, error) {
	return o.runner.Prompt(ctx, sessionID, text)
}

// PromptWithDelivery 带投递模式的 Prompt（§6 busy input 队列）。
func (o *Orchestrator) PromptWithDelivery(ctx context.Context, sessionID, text, delivery string) (session.PromptOutcome, error) {
	return o.runner.PromptWithDelivery(ctx, sessionID, text, delivery)
}

// CancelRun 取消会话进行中的 run（无 run 时为幂等 no-op）。
func (o *Orchestrator) CancelRun(ctx context.Context, sessionID string) error {
	return o.runner.CancelRun(ctx, sessionID)
}

// WaitRun 阻塞至当前 run 结束（demo/同步路径用）；无 run 立即返回。
func (o *Orchestrator) WaitRun(ctx context.Context, sessionID string) (string, error) {
	return o.runner.WaitRun(ctx, sessionID)
}

// LatestUsage 取会话最近一次用量快照（无上报 → false）。run 收口落库用。
func (o *Orchestrator) LatestUsage(sessionID string) (objusage.UsageInfo, bool) {
	return o.runner.LatestUsage(sessionID)
}

// acquireSession 取（或重建）会话的活句柄。Prompt 与 §6 排队投递共用：
// 两者都必须在「会话忙不忙」之前先把句柄拿到手——队列入队要记当前执行代际，
// 代际只在活句柄存在时有意义（无句柄 = 需要重建，重建即换代）。
func (o *Orchestrator) acquireSession(ctx context.Context, sessionID string) (*session.ActiveSession, error) {
	as := o.session(sessionID)
	if as != nil && as.Dead() {
		// 连接已断的死句柄（拆连后理应已被摘除；这里兜住拿到旧指针的竞态）：摘除并按需重建。
		o.evictSession(as)
		as = nil
	}
	if as == nil {
		var err error
		as, err = o.ensureSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	// M3-6/M3-7：把"这一轮该用哪个代际"的裁决放在**每一个轮次边界**上（本函数就是那个边界；
	// Prompt 与排队投递都经它）。迁移失败分两种（见 generation.go 文件头的不对称规则）：
	// 纯能力定义变化 → 回退旧代际继续跑（纪律④，不返回错误）；缩权面（权限/provider）变化
	// → 带码拒绝这一轮，绝不用已被收回的权限面继续跑。
	return o.maybeMigrateGeneration(ctx, as)
}

// RunOnce 在**构建期选定的** runtime 上跑一次 fresh session + prompt 并等待结束（mate demo 用）。
func (o *Orchestrator) RunOnce(ctx context.Context, opt RunOptions) (*RunResult, error) {
	res, err := o.startSessionOn(ctx, session.StartOptions{Cwd: opt.Cwd})
	if err != nil {
		return nil, err
	}
	if _, err := o.Prompt(ctx, res.SessionID, opt.Prompt); err != nil {
		return nil, err
	}
	stop, err := o.WaitRun(ctx, res.SessionID)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return &RunResult{SessionID: res.SessionID, Runtime: res.Runtime}, err
	}
	if err != nil {
		// 父 ctx 超时/取消：executeRun 已按其 err 归为 failed/cancelled
		return &RunResult{SessionID: res.SessionID, Runtime: res.Runtime, StopReason: stop}, err
	}
	return &RunResult{SessionID: res.SessionID, Runtime: res.Runtime, StopReason: stop}, nil
}

// resolveArgs 替换 Launch.Args 中的 {config} 占位（保留给自定义 adapter 模板；
// 当前内置 runtime 的 Args 均已不含占位，此函数为幂等空操作）。
func resolveArgs(args []string, opt RunOptions) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, "{config}", opt.ConfigPath)
	}
	return out
}
