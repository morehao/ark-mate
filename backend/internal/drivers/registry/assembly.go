package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/morehao/golib/glog"
)

// Phase 启动阶段（只用于对照表与日志分组，**不参与排序**——顺序以 Sequence 的声明为准）。
type Phase string

const (
	PhasePlatform    Phase = "平台"
	PhasePersistence Phase = "持久层"
	PhaseDomain      Phase = "领域"
	PhaseSettings    Phase = "参数"
	PhaseBackground  Phase = "后台"
	PhaseDelivery    Phase = "交付"
)

// defaultStopTimeout 单个步骤的默认关闭预算。
//
// 为什么给每一个 Step 独立的超时：关闭路径上任何一步挂住，后面的步骤（尤其是关库）就永远
// 轮不到——表现是进程"退出了"但 transcript 尾部没落盘。给每步一个上界，最坏情况也有界。
const defaultStopTimeout = 10 * time.Second

// Step 启动序列里的一步。
//
// Start 与 Stop 都是可选的：纯构造/纯读步骤没有 Stop；长命 goroutine 的步骤必须给 Stop，
// 否则关闭就只能靠 ctx（而 ctx 的时序在信号路径上不可控）。
//
// Critical 的口径**只描述失败后果**：失败是否让整个 Init 失败。与"重要程度"无关——
// 一条只 WARN 的导入步骤同样重要，但它的失败不该拒绝启动（用户目录缺文件是常态）。
// 这个区分在改造前是散落在"这里 return err、那里 glog.Warnw"里的，现在是一条显式声明。
type Step struct {
	Name    string
	Phase   Phase
	Start   func(context.Context) error
	Stop    func(context.Context) error
	Timeout time.Duration // Start 的超时（0 = 不设）
	// StopTimeout Stop 的超时（0 = defaultStopTimeout）。
	StopTimeout time.Duration
	// Critical 失败是否终止启动并回滚已启动步骤。
	Critical bool
}

// Sequence 一条声明好的启动/关闭序列。
//
// 它是有状态的（记着哪些步骤真的启动了），所以**必须由同一个实例**执行 Start 与 Stop；
// 不要在关闭时另造一条序列——那等于把"启动了哪些"重新猜一遍。
type Sequence struct {
	steps   []Step
	started []int
}

// New 按声明顺序构造序列。
func New(steps ...Step) *Sequence { return &Sequence{steps: steps} }

// Steps 返回声明本身（对照表与测试用；返回副本，调用方改不到内部状态）。
func (s *Sequence) Steps() []Step {
	out := make([]Step, len(s.steps))
	copy(out, s.steps)
	return out
}

// Start 顺序执行全部步骤。
//
// critical 步骤失败 → 对**已启动**的步骤按逆序执行 Stop（回滚），然后返回带步骤名的错误
// （用 %w 包裹，`*app.ExitError` 的退出码不被吃掉）。
// 非 critical 步骤失败 → 只记 WARN 并继续（与原代码"这里只告警"的控制流逐条对应）。
func (s *Sequence) Start(ctx context.Context) error {
	for i, st := range s.steps {
		if st.Start == nil {
			s.started = append(s.started, i)
			continue
		}
		if err := runStep(ctx, st.Name, st.Timeout, st.Start); err != nil {
			if st.Critical {
				s.rollback()
				return fmt.Errorf("启动步骤 %s 失败: %w", st.Name, err)
			}
			glog.Warnw(ctx, "装配: 非致命步骤失败，继续启动", "step", st.Name, "phase", string(st.Phase), "err", err)
		}
		s.started = append(s.started, i)
	}
	return nil
}

// Stop 按启动的**逆序**执行已启动步骤的 Stop，返回全部错误（调用方决定记日志还是忽略）。
//
// 幂等：执行后清空 started，重复调用不会二次关闭（关闭动作本身也应当幂等，这是双保险）。
func (s *Sequence) Stop(ctx context.Context) []error {
	var errs []error
	for n := len(s.started) - 1; n >= 0; n-- {
		st := s.steps[s.started[n]]
		if st.Stop == nil {
			continue
		}
		timeout := st.StopTimeout
		if timeout <= 0 {
			timeout = defaultStopTimeout
		}
		stopCtx, cancel := context.WithTimeout(ctx, timeout)
		err := st.Stop(stopCtx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("关闭步骤 %s: %w", st.Name, err))
			glog.Warnw(ctx, "装配: 关闭步骤失败（继续关闭其余步骤）", "step", st.Name, "err", err)
		}
	}
	s.started = nil
	return errs
}

// rollback 对已启动步骤逆序执行 Stop（失败只记日志；回滚本身不能再失败成第二个错误）。
func (s *Sequence) rollback() {
	for _, err := range s.Stop(context.Background()) {
		glog.Warnw(context.Background(), "装配: 启动失败后的回滚中出现错误（已继续回滚其余步骤）", "err", err)
	}
}

// runStep 给单步套一个超时，并在返回后**复核** ctx：步骤自己不观察 ctx 时（例如 store.Open），
// 超时无法中断它，但至少能在它返回后把"超了"如实报出来，而不是当成成功继续往下走。
func runStep(ctx context.Context, name string, timeout time.Duration, fn func(context.Context) error) error {
	if timeout <= 0 {
		return fn(ctx)
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := fn(stepCtx)
	if err == nil && stepCtx.Err() != nil {
		return fmt.Errorf("%w（步骤 %s 未在 %s 内完成）", stepCtx.Err(), name, timeout)
	}
	return err
}

// IsTimeout 判断错误是否来自步骤超时（装配层与测试共用同一判据）。
func IsTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
