package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objusage"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// AgentCancelTimeout session/cancel RPC 的上限等待：底座无响应（如模型限流重试挂起）时
// 不阻塞本地解除，Prompt 的 ctx 取消总是会立刻返回。
const AgentCancelTimeout = 3 * time.Second

// ——— 会话轮次的**运行期状态机**（P4a 正文第五十五刀：从 `internal/manager` 搬入）———
//
// 这一族是「一轮 prompt 从受理到收尾」的全部事实：轮的受理（忙判据）、事件出口（ACP 事件 →
// bus）、输入队列投递、终态收尾（run.status / run.* / 会话状态 / 用量快照）。
//
// **为什么必须一次搬完**：它们共用同一份在途状态（`ActiveSession.run` / `busy`）。分两批各自
// 搬一半，就会出现「两份在途」「两份队列」——而那种分叉不会让任何编译错误出现，只会在并发下
// 表现为重复投递或丢投递。
//
// 本层不认识的依赖走三张窄端口：① 取句柄（`Acquire`，会话启动/恢复仍在编排侧）；
// ② 在途登记（`BusyTracker`，归执行面的连接台账）；③ 诊断 / 模型目录 / 工具记忆 / 交互取消
// （分别是执行面、模型面与权限面的事实）。端口签名只吃域/pkg 类型——实现侧因此能落在
// mismatch 的另一层，而不需要本层反向 import 它们。

// BusyTracker 在途轮次登记（每连接一份台账）。
type BusyTracker interface {
	Mark(client *acl.Client, sessionID string)
	Unmark(client *acl.Client, sessionID string)
}

// DiagnosticsSink 运行期诊断的写口（错误分类 + stderr 尾巴）。
type DiagnosticsSink interface {
	RecordRuntimeError(sessionID string, ae *acl.Error, tail []string)
	StderrTail(client *acl.Client) []string
}

// ConfigOptionsSink 运行时配置项（模型目录）的缓存写口。
type ConfigOptionsSink interface {
	PutConfigOptions(runtimeID string, options []acl.ConfigOption)
}

// ToolFailureClassifier 按 runtime 归类一条工具结果文本（底层是注册表的签名表）。
type ToolFailureClassifier interface {
	ClassifyToolFailure(runtimeID, text string) (runtime.ToolFailure, bool)
}

// ToolMemory 工具调用详情的短期记忆（权限门禁在权限请求到达时回查命令行）。
//
// 它**不是**本状态机的一部分（生命周期按 toolCallId，不按轮次），所以留在编排侧，
// 这里只认三个窄方法。
type ToolMemory interface {
	Remember(info acl.ToolCallInfo)
	Forget(id string)
}

// InteractionCanceller 会话取消时把挂起的权限/提问一并收敛（权限面的事实）。
type InteractionCanceller interface {
	CancelSession(sessionID string)
}

// RunnerDeps 造一个 Runner 所需的全部事实与端口。
type RunnerDeps struct {
	Plane       *Plane
	Handles     *Handles
	Acquire     func(ctx context.Context, sessionID string) (*ActiveSession, error)
	Busy        BusyTracker
	Diagnostics DiagnosticsSink
	Catalog     ConfigOptionsSink
	ToolMemory  ToolMemory
	// ToolFailures 底座的工具失败签名表（第四张可执行端口表）；nil = 不归类。
	ToolFailures ToolFailureClassifier
	// Interactions 权限/提问的取消面（nil = 没有交互状态可收敛）。
	Interactions InteractionCanceller
}

// Runner 会话轮次的运行期状态机：受理、事件出口、排队投递、收尾。
type Runner struct {
	plane       *Plane
	handles     *Handles
	acquire     func(ctx context.Context, sessionID string) (*ActiveSession, error)
	busy        BusyTracker
	diagnostics DiagnosticsSink
	catalog     ConfigOptionsSink
	toolMemory  ToolMemory
	toolFailed  ToolFailureClassifier
	interacts   InteractionCanceller

	// runTimeout 单轮 prompt 看门狗（0 = 关闭；见 SetRunTimeout / MATE_RUN_TIMEOUT）。
	runTimeout time.Duration

	// usageMu/usage 保护「每平台会话最近一次 ACP 用量快照」（M3 用量统计：run 收口时
	// 随 runs.usage_json 落库；底座不上报时无条目）。
	usageMu sync.Mutex
	usage   map[string]objusage.UsageInfo
}

// NewRunner 造一个运行期状态机。所有端口在装配期一次接上（nil 端口的语义见各方法）。
func NewRunner(d RunnerDeps) *Runner {
	return &Runner{
		plane: d.Plane, handles: d.Handles, acquire: d.Acquire, busy: d.Busy,
		diagnostics: d.Diagnostics, catalog: d.Catalog, toolMemory: d.ToolMemory,
		toolFailed: d.ToolFailures, interacts: d.Interactions,
		usage: make(map[string]objusage.UsageInfo),
	}
}

// SetRunTimeout 设置单轮 run 看门狗超时（0 = 关闭）。
func (r *Runner) SetRunTimeout(d time.Duration) { r.runTimeout = d }

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 run_in_progress。
// 会话无活句柄（进程刚起、或连接已断）时按需重建：持久层有记录 → 由 Acquire 走
// native resume / handoff memo（ADR-4：执行可丢弃、会话平台拥有）；无记录才 session_not_found。
func (r *Runner) Prompt(ctx context.Context, sessionID, text string) (int64, error) {
	as, err := r.acquire(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	t, memo, err := as.BeginRun(ctx)
	if err != nil {
		return 0, err
	}
	client := as.Client()
	r.busy.Mark(client, as.ID)

	r.plane.MarkSession(as.ID, SessionStatusInProgress)
	// 开轮三连（v22，设计 §D2）：① `run.started` 落库拿到本轮的**起点位次** → ② 开轮行
	// （`run_<ULID>` 身份与事件载荷同源、`first_seq` = 那个位次）→ ③ 用户消息。
	//
	// 顺序不能换：开轮行要写 first_seq，而 first_seq 就是本条事件的位次；用户消息在它之后，
	// 于是本轮窗口把"用户输入"整个包在里面（窗口起点早于输入，不再需要"轮界裁剪"）。
	se := r.plane.Publish(BusRunStarted, as.ID, map[string]any{
		"session_id": as.ID, "run_id": t.id, "runtime": as.Runtime, "text": text,
	})
	r.plane.OpenRun(as.ID, t.id, text, se.Seq)
	// 过程状态的第一帧（S1 / ADR-6）：一轮已经开始、还没向模型发请求。它落在 `run.started`
	// **之后**（本轮窗口从 run.started 起算，状态属于这一轮）、用户消息之前。
	r.RunStatusFor(as.ID, RunStatusPreparing, RunStatusOpts{})
	ue := r.plane.Publish(BusUserMessage, as.ID, map[string]any{
		"session_id": as.ID, "runtime": as.Runtime, "text": text,
	})
	// P3 F3 memo 注入：摘要仅作为发给 runtime 的首条 prompt 引导（user_message 事件保持用户
	// 原文，不含摘要）。SDK v0.13.5 无 provider handoff 通道 → 文本前缀承载；provider 侧拒绝
	// 重试语义随 SDK 能力后置，不臆造。
	if memo != "" {
		text = memo + "\n\n" + text
	}
	go r.executeRun(as, t, text)
	// 看门狗：runTimeout 内未收尾则自动取消（防 runtime 吞错/挂起导致 busy 永久占用）。
	if r.runTimeout > 0 {
		go r.watchdogRun(as, sessionID, t)
	}
	return ue.Seq, nil
}

// PromptWithDelivery 带投递模式的 Prompt（§6 busy input 队列）。
//
//   - reject（缺省）：忙时报 run_in_progress（历史行为，不排队）。
//   - after_run：忙时入队，当前轮结束后自动投递；空闲时与 reject 同路径（不产生队列行）。
//   - interrupt_then_prompt：入队**后**取消当前轮，随后由收尾路径自动投递。
//     顺序是刻意的——反过来（先取消再入队）会在「取消已生效、executeRun 已收尾取队」与
//     「本函数还没入队」之间留一个窗口，那一项将永远不被投递。入队后再复查一次空闲，
//     闭合「取消前旧轮已自然结束」的另一半窗口（认领是条件更新，两边都跑也只会投一次）。
func (r *Runner) PromptWithDelivery(ctx context.Context, sessionID, text, delivery string) (PromptOutcome, error) {
	mode, err := NormalizeDelivery(delivery)
	if err != nil {
		return PromptOutcome{}, err
	}
	if mode == DeliveryReject {
		seq, err := r.Prompt(ctx, sessionID, text)
		return PromptOutcome{EventSeq: seq}, err
	}
	as, err := r.acquire(ctx, sessionID)
	if err != nil {
		return PromptOutcome{}, err
	}
	busy := as.Busy()
	runID := as.RunID()
	if !busy {
		// 空闲：直接执行（与 reject 路径同语义——没有「等的那一轮」，不该留下队列行）。
		seq, err := r.Prompt(ctx, sessionID, text)
		return PromptOutcome{EventSeq: seq}, err
	}
	q, err := r.plane.EnqueueInput(ctx, sessionID, text, mode, runID, r.plane.SessionGeneration(sessionID))
	if err != nil {
		return PromptOutcome{}, err
	}
	evt := r.plane.Publish(BusInputQueued, sessionID, map[string]any{
		"session_id": sessionID, "runtime": as.Runtime, "input_id": q.ID, "text": q.Text,
		"delivery": q.Delivery, "target_run_id": q.TargetRunID, "generation": q.Generation,
	})
	if mode == DeliveryInterruptThenPrompt {
		if cerr := r.CancelRun(ctx, sessionID); cerr != nil {
			// 取消失败（会话/连接已变）：入队项留着没有意义——就地撤下，避免留下一条
			// 「谁也不等」的孤儿队列项。
			if _, derr := r.plane.DeleteQueuedInput(ctx, sessionID, q.ID); derr != nil {
				glog.Warnw(ctx, "取消当前轮失败且撤下排队项失败", "session", sessionID, "input_id", q.ID, "err", derr)
			}
			r.plane.Publish(BusInputCancelled, sessionID, map[string]any{
				"session_id": sessionID, "runtime": as.Runtime, "input_id": q.ID,
				"delivery": q.Delivery, "reason": ReasonDeliverFail,
			})
			return PromptOutcome{}, cerr
		}
	}
	r.DeliverQueued(as) // 空闲复查（见函数注释：闭合窗口；认领条件更新保证只投一次）
	return PromptOutcome{EventSeq: evt.Seq, Queued: true, InputID: q.ID}, nil
}

// watchdogRun 单轮超时兜底：超时后走 CancelRun 的有界取消路径（agent 无响应也本地解除），
// 终态事件由 executeRun 依取消语义发布（run.cancelled）。与用户手动取消幂等共存：run 已结束时
// select 到 t.done 直接退出；已取消则 CancelRun 对 nil run 为 no-op。
func (r *Runner) watchdogRun(as *ActiveSession, sessionID string, t *Run) {
	timer := time.NewTimer(r.runTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		glog.Warnw(context.Background(), "run 超过看门狗时限，自动取消", "session", sessionID, "timeout", r.runTimeout.String())
		_ = r.CancelRun(context.Background(), sessionID)
	}
}

// executeRun 执行一轮 prompt 并发布终态事件（单写者：仅本 goroutine 改动 run 终态）。
// Prompt 走 ActiveSession 的投影（Runtime 接口；§16 采用），proj 为 nil（测试假会话/
// 未投影 runtime）时回退底层会话直连——两路径语义一致。
func (r *Runner) executeRun(as *ActiveSession, t *Run, text string) {
	var (
		stop string
		err  error
	)
	// 「请求已发出、首个增量未到」——它覆盖从发请求到第一个 token 之间的那段空白，
	// 而那正是用户最容易以为「没反应」的时候（今天这段时间与「已卡住」长得一模一样）。
	// 用 `t.id` 而不是重查：这一支里 `as.run` 就是 `t`，重查只多一个可能拿到 nil 的窗口。
	r.plane.PublishRunStatus(as.ID, t.id, RunStatusModelRequesting, RunStatusOpts{})
	stop, err = as.PromptACP(t.ctx, text)

	as.mu.Lock()
	intentional := t.intentional
	userCancel := t.cancelReq // 用户显式取消：即使正在停机收尾，取消仍是用户事实（优先于 interrupted）
	cancelled := t.cancelReq || (err == nil && strings.EqualFold(stop, "cancelled"))
	// agent 侧返回「取消类」错误（如 dsh 的 "Request cancelled"、SDK ctx 取消）也归一为 cancelled。
	if !cancelled && err != nil && errLooksCancelled(err) {
		cancelled = true
	}
	as.busy = false
	as.run = nil
	client := as.client
	as.mu.Unlock()
	r.busy.Unmark(client, as.ID)
	t.stop = stop
	t.err = err

	// §6：本轮正常走完（含用户取消）→ 收尾后推进排队输入。**失败不自动续投**：
	// 底座刚报错就把队列一条条灌进去，等于把队列变成「连续失败重放」；停机（intentional）
	// 更不该投——那一轮注定不会被执行，重启时统一作废（见恢复期的作废路径）。
	deliverNext := false
	switch {
	case userCancel:
		// 用户取消优先：与停机撞车时，取消是用户意图，不能改写成 interrupted。
		r.CloseRunStatus(as.ID, t.id)
		r.plane.MarkSession(as.ID, SessionStatusCancelled)
		r.plane.Publish(BusRunCancelled, as.ID, map[string]any{
			"session_id": as.ID, "run_id": t.id, "stop_reason": stop,
		})
		deliverNext = true
	case intentional:
		// 主动拆除（daemon 停机 / 显式停 agent）打断的轮次：既不是用户取消也不是失败，
		// 归 interrupted —— 无终态、可续聊重发（与崩溃恢复同义）。刻意不发 run.error /
		// run.cancelled：伪终态会污染历史、失败轮列表与重试入口。
		//
		// **也不发收口 `run.status`**（ADR-8 的前半）：收口状态同样是「这一轮到此为止」的宣告，
		// 在这一支上它与伪终态同罪。崩溃残轮的收口由**恢复路径**补，两处行为相反且各有判据。
		r.plane.MarkSession(as.ID, SessionStatusInterrupted)
		glog.Infow(context.Background(), "run 因连接主动拆除而中断（会话保留，可续聊重发）",
			"session", as.ID, "runtime", as.Runtime)
	case cancelled:
		r.CloseRunStatus(as.ID, t.id)
		r.plane.MarkSession(as.ID, SessionStatusCancelled)
		r.plane.Publish(BusRunCancelled, as.ID, map[string]any{
			"session_id": as.ID, "run_id": t.id, "stop_reason": stop,
		})
		deliverNext = true
	case err != nil:
		r.CloseRunStatus(as.ID, t.id)
		r.plane.MarkSession(as.ID, SessionStatusFailed)
		r.PublishError(as.ID, t.id, err, client)
	default:
		r.CloseRunStatus(as.ID, t.id)
		r.plane.MarkSession(as.ID, SessionStatusCompleted)
		r.plane.Publish(BusRunEnded, as.ID, map[string]any{
			"session_id": as.ID, "run_id": t.id, "runtime": as.Runtime, "stop_reason": stop,
		})
		deliverNext = true
	}
	t.CloseDone()
	if deliverNext {
		// 在 executeRun 的 goroutine 里同步投递队首（DeliverQueued 经 Prompt 另起 goroutine
		// 跑新一轮，不会在本 goroutine 上递归执行）。
		r.DeliverQueued(as)
	}
}

// DeliverQueued 在会话空闲时投递队首排队输入（executeRun 收尾 / 入队后复查 / 立即发送调用）。
// 不投递：会话仍忙（下一轮自然会再触发）、句柄已死（重建后代际已变，队列项会作废）、队列空。
func (r *Runner) DeliverQueued(as *ActiveSession) {
	if as.Dead() || as.Busy() {
		return
	}
	gen := r.plane.SessionGeneration(as.ID)
	q, ok := r.plane.ClaimNextInput(context.Background(), as.ID, gen)
	if !ok {
		return
	}
	// 先启动新一轮、再发布 delivered：反过来（先发 delivered 再 Prompt）一旦启动失败，
	// 前端会看到队列项凭空消失而其文本从未作为 user_message 出现过。
	if _, err := r.Prompt(context.Background(), as.ID, q.Text); err != nil {
		glog.Warnw(context.Background(), "排队输入投递失败", "session", as.ID, "input_id", q.ID, "err", err)
		r.plane.Publish(BusInputCancelled, as.ID, map[string]any{
			"session_id": as.ID, "runtime": as.Runtime, "input_id": q.ID,
			"delivery": q.Delivery, "reason": ReasonDeliverFail,
		})
		return
	}
	r.plane.Publish(BusInputDelivered, as.ID, map[string]any{
		"session_id": as.ID, "runtime": as.Runtime, "input_id": q.ID,
		"delivery": q.Delivery, "reason": ReasonRunFinished,
	})
}

// CancelRun 取消会话进行中的 run（无 run 时为幂等 no-op）。
func (r *Runner) CancelRun(ctx context.Context, sessionID string) error {
	as := r.handles.Get(sessionID)
	if as == nil {
		return acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	t := as.RequestCancel()
	if t == nil {
		return nil
	}
	// 「已受理取消、收尾未完成」（S1）：它与终态**不是**同一件事——底座的 `session/cancel`
	// 可能没响应，而这一段时间里界面必须能说清「正在收尾」而不是「已经停了」。
	//
	// 这里直接用 `t.id` 而不是重查：`as.run` 随时可能被 executeRun 收尾清空，
	// 重查一次会拿到空 id，于是这条状态挂到一个「不存在的轮」上。
	r.plane.PublishRunStatus(sessionID, t.id, RunStatusCancelling, RunStatusOpts{})
	// agent 侧取消（session/cancel）有界执行：runtime 无响应时不阻塞本地解除。
	// 无论 RPC 是否送达，随后都 t.cancel() 让 Prompt 返回（executeRun 收尾不依赖 RPC）。
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), AgentCancelTimeout)
	defer ccancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// §17 E2：会话级取消走投影（Cancel 已入 Runtime 接口）；proj nil（测试注入假会话）
		// 回退直连 sess.Cancel，语义一致。
		_ = as.CancelACP(cctx)
	}()
	select {
	case <-done:
	case <-cctx.Done():
		glog.Debugw(ctx, "session/cancel RPC 未在限期内响应，本地强制解除",
			"session", sessionID, "timeout", AgentCancelTimeout.String())
	}
	t.Cancel() // 解除 SDK 阻塞
	if r.interacts != nil {
		r.interacts.CancelSession(sessionID) // 按 ACP 规范回 Cancelled 全部挂起权限/提问
	}
	return nil
}

// WaitRun 阻塞至当前 run 结束（demo/同步路径用）；无 run 立即返回。
func (r *Runner) WaitRun(ctx context.Context, sessionID string) (string, error) {
	as := r.handles.Get(sessionID)
	if as == nil {
		return "", acl.NewError(acl.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	t := as.ActiveRun()
	if t == nil {
		return "", nil
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.done:
	}
	return t.stop, t.err
}

// RunStatusFor 发一条过程状态（**只**调域里的唯一铸造点：调用方不拼载荷、不碰 `BusRunStatus`）。
func (r *Runner) RunStatusFor(sessionID, status string, opt RunStatusOpts) {
	if sessionID == "" {
		return
	}
	r.plane.PublishRunStatus(sessionID, r.ActiveRunID(sessionID), status, opt)
}

// ActiveRunID 取会话当前那一轮的 id（没有在跑的轮 → 空串）。
//
// 过程状态的载荷要带 `run_id`（那一轮的身份）：UI 与读面据此判断「这条状态属于哪一轮」，
// 而这正是"多轮会话里过程状态被上一轮残留"的唯一防线。
func (r *Runner) ActiveRunID(sessionID string) string {
	as := r.handles.Get(sessionID)
	if as == nil {
		return ""
	}
	return as.RunID()
}

// CloseRunStatus 终态事件**之前**的收口状态（ADR-6）：界面「停止」的判据必须是一个**状态**，
// 不能是「一段时间没事件」。
//
// 刻意只在**非 intentional** 的四个终态分支里调：那一支连收口状态都不发（ADR-8 的前半）。
func (r *Runner) CloseRunStatus(sessionID, runID string) {
	r.plane.PublishRunStatus(sessionID, runID, RunStatusIdle, RunStatusOpts{})
}

// errLooksCancelled 判断错误是否为「取消」语义（ctx 取消或 agent 取消类错误文本）。
func errLooksCancelled(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	var ae *acl.Error
	if errors.As(err, &ae) {
		s := strings.ToLower(ae.Code + " " + ae.Details)
		return strings.Contains(s, "cancel") || strings.Contains(s, "abort")
	}
	return false
}

// LiveConfigOptions 返回任一活跃会话的配置项（模型目录的**首选来源**：运行时此刻
// 的真实状态，零额外开销）。无活跃会话返回 false。
//
// 注：有活跃会话但配置项为空时同样返回 ok=true（空即运行时的真实答案：未配 provider/未登录），
// 不走 resume 兜底去「凑一份」目录。
func (r *Runner) LiveConfigOptions() ([]acl.ConfigOption, bool) {
	sess := r.handles.AnyLive()
	if sess == nil {
		return nil, false
	}
	return sess.ConfigOptions(), true
}

// RecordUsage 记录平台会话最近一次用量快照（同会话后到覆盖先到；空快照忽略）。
func (r *Runner) RecordUsage(sessionID string, u objusage.UsageInfo) {
	if sessionID == "" || !u.Reported() {
		return
	}
	r.usageMu.Lock()
	r.usage[sessionID] = u
	r.usageMu.Unlock()
}

// LatestUsage 取会话最近一次用量快照（无上报 → false）。run 收口落库用。
func (r *Runner) LatestUsage(sessionID string) (objusage.UsageInfo, bool) {
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	u, ok := r.usage[sessionID]
	return u, ok
}
