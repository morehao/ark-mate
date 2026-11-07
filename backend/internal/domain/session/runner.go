package session

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

const (
	// AgentCancelSendTimeout session/cancel **通知发送**的上限等待：底座无响应（如模型限流
	// 重试挂起）时，至少不要把「发通知」这一步拖住。
	AgentCancelSendTimeout = 3 * time.Second
	// AgentSettleTimeout 取消/关闭后等待底座**真正结算**的上限。
	//
	// 为什么需要它：dsh 0.2.1 起 `session/cancel` 会等当前根活动与已排队输出 drain 完才让
	// `session/prompt` 返回（`packages/acp/acp/README.zh.md`）。若平台按旧口径「发完通知就
	// 本地解除」，迟到的 `session/update` 会落进下一轮（ACP 没有 run id，界面无从分辨）。
	// 这里给一个**有界**窗口：正常结算通常远小于它；超时才强制取消本地 Prompt（保留
	// 「底座彻底无响应也要解除」的兜底），并留一条可读 WARN。
	AgentSettleTimeout = 10 * time.Second
)

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
	Mark(client protocol.Conn, sessionID string)
	Unmark(client protocol.Conn, sessionID string)
	Clear(client protocol.Conn)
	Len(client protocol.Conn) int
}

// DiagnosticsSink 运行期诊断的写口（错误分类 + stderr 尾巴）。
type DiagnosticsSink interface {
	RecordRuntimeError(sessionID string, ae *failure.Error, tail []string)
	StderrTail(client protocol.Conn) []string
}

// ConfigOptionsSink 运行时配置项（模型目录）的缓存写口。
type ConfigOptionsSink interface {
	PutConfigOptions(runtimeID string, options []protocol.ConfigOption)
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
	Remember(info protocol.ToolCallInfo)
	Forget(id string)
}

// InteractionCanceller 会话取消时把挂起的权限/提问一并收敛（权限面的事实）。
type InteractionCanceller interface {
	CancelSession(sessionID string)
}

// ArtifactExtractor 产物自动抽取的**窄端口**（产物面的事实）。
//
// 触发时机由 Runner 决定（工具**成功完成**的终态帧）；**是否是一次 write/edit 落文件、路径怎么
// 解析、快照怎么落**一律在实现侧（装配层）判定——本层只递递原始事件，不 import 抽取实现
// （domain 不认识 infra，零侵入 C1/C3：识别不了就由实现侧忽略）。
//
// **必须非阻塞**：实现在**同步返回前**完成「读工作区字节」（C5：必须在此刻捕获，否则会读到 dsh
// 后续步骤的覆盖内容），随后「算哈希 + 落 filestore + 建行」由实现内部异步完成；同步段要快
// （限读大小，见 MaxSnapshotBytes）。任何失败都不上抛（C4：抽取失败绝不影响会话）。nil = 不抽取。
type ArtifactExtractor interface {
	// ExtractToolWrite 在工具**成功完成**时尝试登记一条产物（同步捕获字节、内部异步落盘；失败静默）。
	ExtractToolWrite(ctx context.Context, sessionID, runID string, info protocol.ToolCallInfo)
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
	// Extractor 产物自动抽取端口（nil = 不抽取；工具完成时旁路触发，不影响会话）。
	Extractor ArtifactExtractor
	// DeliverQueuedFn 投递队首时**带附件重建本轮**的端口（v4 P2）：解析附件 id → parts 需要
	// 附件解析器，而解析器在编排层（domain 不 import 附件链路）。nil = 退化为纯文本投递
	// （旧行为；无附件的队项本就走这条）。有附件时必须走它，否则排队会丢附件。
	DeliverQueuedFn func(ctx context.Context, sessionID, text string, attachments []string) error
}

// Runner 会话轮次的运行期状态机：受理、事件出口、排队投递、收尾。
type Runner struct {
	plane         *Plane
	handles       *Handles
	acquire       func(ctx context.Context, sessionID string) (*ActiveSession, error)
	busy          BusyTracker
	diagnostics   DiagnosticsSink
	catalog       ConfigOptionsSink
	toolMemory    ToolMemory
	toolFailed    ToolFailureClassifier
	interacts     InteractionCanceller
	extractor     ArtifactExtractor
	deliverQueued func(ctx context.Context, sessionID, text string, attachments []string) error

	// toolWriteMu/toolWrites 是「在途工具调用的写文件线索」：in_progress 帧的 rawInput 带路径
	// （终态帧不带，见 R1 探针），故在 tool_call 帧缓存、终态帧取出后清掉。仅承接 write/edit 线索时用
	// 得着的字段（这里存整条 info 由实现侧自行判断，本层不解析）。上限见 maxPendingToolWrites。
	toolWriteMu    sync.Mutex
	toolWrites     map[string]pendingToolWrite
	toolWriteOrder []string

	// runTimeout 单轮 prompt 看门狗（0 = 关闭；见 SetRunTimeout / MATE_RUN_TIMEOUT）。
	runTimeout time.Duration
	// settleTimeout 取消后等待 prompt/close 真正结算的上限（见 AgentSettleTimeout）。
	settleTimeout time.Duration

	// usageMu/usage 保护「每平台会话最近一次 ACP 用量快照」（M3 用量统计：run 收口时
	// 随 runs.usage_json 落库；底座不上报时无条目）。
	usageMu sync.Mutex
	usage   map[string]objusage.UsageInfo
	// tokenUsageMu/tokenUsage 保护「每轮 token 明细流水」（键 = run id；executeRun 在 prompt
	// 返回后写入，run 收口时随 runs.token_usage_json 落库）。与上面的快照表**分开**：
	// 快照按会话、后到覆盖先到；流水按轮、可逐轮保留。底座不上报时无条目。
	tokenUsageMu sync.Mutex
	tokenUsage   map[string]objusage.TokenUsage
}

// NewRunner 造一个运行期状态机。所有端口在装配期一次接上（nil 端口的语义见各方法）。
func NewRunner(d RunnerDeps) *Runner {
	return &Runner{
		plane: d.Plane, handles: d.Handles, acquire: d.Acquire, busy: d.Busy,
		diagnostics: d.Diagnostics, catalog: d.Catalog, toolMemory: d.ToolMemory,
		toolFailed: d.ToolFailures, interacts: d.Interactions,
		extractor:     d.Extractor,
		deliverQueued: d.DeliverQueuedFn,
		usage:         make(map[string]objusage.UsageInfo),
		tokenUsage:    make(map[string]objusage.TokenUsage),
		settleTimeout: AgentSettleTimeout,
	}
}

// SetRunTimeout 设置单轮 run 看门狗超时（0 = 关闭）。
func (r *Runner) SetRunTimeout(d time.Duration) { r.runTimeout = d }

// SetArtifactExtractor 装配产物抽取端口（nil = 关闭抽取）。装配根在用例层构造完成后注入
// （Runner 早于 svc 构造，故走 setter 而非构造参数；与 SetRunTimeout 等同处）。
func (r *Runner) SetArtifactExtractor(ex ArtifactExtractor) { r.extractor = ex }

// SetSettleTimeout 覆盖取消后的结算等待窗口（0 = 用默认值；测试用它把窗口压到毫秒级）。
func (r *Runner) SetSettleTimeout(d time.Duration) {
	if d <= 0 {
		d = AgentSettleTimeout
	}
	r.settleTimeout = d
}

// Prompt 向会话追加一轮 prompt（异步；事件经 bus/WS 出流）。
// 返回 user_message 事件后的 bus 水位（客户端 attach 续传用）。会话正忙时返回 run_in_progress。
// 会话无活句柄（进程刚起、或连接已断）时按需重建：持久层有记录 → 由 Acquire 走
// native resume / handoff memo（ADR-4：执行可丢弃、会话平台拥有）；无记录才 session_not_found。
func (r *Runner) Prompt(ctx context.Context, sessionID, text string) (int64, error) {
	return r.PromptParts(ctx, sessionID, text, nil, nil, "")
}

// PromptParts 同 Prompt，但额外携带内容块 parts（v32 附件链路：text + image）与附件元数据
// attachMeta（进 `session.user_message.attachments[]` 载荷）。
//
// text 仍是**用户原文**（进事件/落库/开轮行）；parts 是**发给底座的**内容块（text 段的文本
// 会再叠上首轮前缀，image 段带字节）。parts 为空时按 TextParts(text) 兜底（行为与旧 Prompt 一致）；
// attachMeta 为空时 user_message 载荷不含 attachments 键（与无附件行为逐字一致）。
func (r *Runner) PromptParts(ctx context.Context, sessionID, text string, parts []protocol.PromptPart, attachMeta []AttachmentMeta, skillContract string) (int64, error) {
	as, err := r.acquire(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	t, prefix, err := as.BeginRun(ctx, skillContract)
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
	ue := r.plane.Publish(BusUserMessage, as.ID, func() map[string]any {
		payload := map[string]any{"session_id": as.ID, "runtime": as.Runtime, "text": text}
		// v32：附件元数据挂 user 消息（只元数据，无字节/无 path；G6）。
		if len(attachMeta) > 0 {
			payload["attachments"] = attachMeta
		}
		return payload
	}())
	// 首轮前缀（P3 F3 memo + 产品身份）：只作为发给 runtime 的首条 prompt 引导——上面那条
	// `session.user_message` 发布的是**用户原文**，前缀不进事件、不进 transcript。
	//
	// 顺序固定 identity → memo → 用户原文（见 `RunPrefix.Compose`）：身份是产品级、稳定的，
	// memo 是可能含旧自述的历史摘要，先立产品的规矩再讲历史，否则历史会覆盖身份。
	// SDK v0.13.5 无 provider handoff 通道 → 文本前缀承载；provider 侧拒绝重试语义随 SDK 能力后置。
	if !prefix.Empty() {
		text = prefix.Compose(text)
	}
	// 组装发给底座的内容块：parts 为空 → 单 text part；非空 → 把 text 段的文本换成**已叠前缀**
	// 的 text（image 段原样保留）。事件/落库用的仍是上面的 `text` 变量（用户原文+前缀不进事件）。
	deliverParts := composeDeliverParts(parts, text)
	go r.executeRun(as, t, deliverParts)
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
	return r.PromptWithDeliveryParts(ctx, sessionID, text, nil, nil, delivery, "")
}

// PromptWithDeliveryParts 同 PromptWithDelivery，但额外携带内容块 parts（v32 附件链路）。
//
// 带 image 的 parts **只走立即执行的两条路径**（空闲 / reject）；会排队的路径（after_run /
// interrupt_then_prompt 且会话忙）**显式拒绝**带 image 的附件（fail-closed，不静默丢图）——
// 队列只存文本，把图灌进去等于丢附件。
func (r *Runner) PromptWithDeliveryParts(ctx context.Context, sessionID, text string, parts []protocol.PromptPart, attachMeta []AttachmentMeta, delivery string, skillContract string) (PromptOutcome, error) {
	mode, err := NormalizeDelivery(delivery)
	if err != nil {
		return PromptOutcome{}, err
	}
	if mode == DeliveryReject {
		seq, err := r.PromptParts(ctx, sessionID, text, parts, attachMeta, skillContract)
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
		seq, err := r.PromptParts(ctx, sessionID, text, parts, attachMeta, skillContract)
		return PromptOutcome{EventSeq: seq}, err
	}
	// 忙 → 入队。附件（v4 P2）随队项落库（存 id，投递时经 DeliverQueuedFn 重建）。但若本次带着
	// 附件而**投递端口未装配**（无法在投递时重建 parts），则显式拒绝而不是静默丢附件——fail-closed。
	attIDs := attachmentIDs(attachMeta)
	if len(attIDs) > 0 && r.deliverQueued == nil {
		return PromptOutcome{}, failure.NewError(failure.ClassRequest, "attachment_queue_unsupported",
			"会话正忙时暂不支持排队携带附件，请等本轮结束后再发", nil)
	}
	// 技能契约不随队列行持久化（`inputs` 表无 skill 列），排队会把技能丢掉——
	// 显式拒绝而不是静默降级，避免"我选了技能、排队后却没生效"的错觉。
	if skillContract != "" {
		return PromptOutcome{}, failure.NewError(failure.ClassRequest, "skill_queue_unsupported",
			"会话正忙时技能暂不能排队携带，请等本轮结束后再发，或用「打断并发送」", nil)
	}
	q, err := r.plane.EnqueueInput(ctx, sessionID, text, mode, runID, r.plane.SessionGeneration(sessionID), attIDs)
	if err != nil {
		return PromptOutcome{}, err
	}
	evt := r.plane.Publish(BusInputQueued, sessionID, func() map[string]any {
		payload := map[string]any{
			"session_id": sessionID, "runtime": as.Runtime, "input_id": q.ID, "text": q.Text,
			"delivery": q.Delivery, "target_run_id": q.TargetRunID, "generation": q.Generation,
		}
		if len(q.Attachments) > 0 {
			payload["attachments"] = q.Attachments
		}
		return payload
	}())
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
// composeDeliverParts 组装发给底座的内容块：parts 为空 → 单 text part（composedText）；
// 非空 → 把其中的 text 段文本换成 composedText（叠了首轮前缀），image 段原样保留。
func composeDeliverParts(parts []protocol.PromptPart, composedText string) []protocol.PromptPart {
	if len(parts) == 0 {
		return protocol.TextParts(composedText)
	}
	out := make([]protocol.PromptPart, 0, len(parts))
	textReplaced := false
	for _, p := range parts {
		if (p.Kind == protocol.PromptPartText || p.Kind == "") && !textReplaced {
			p.Text = composedText
			textReplaced = true
		}
		out = append(out, p)
	}
	if !textReplaced {
		// parts 里没有 text 段（纯图）：把 composedText 作为首个 text part 补上（保序：文本在前）。
		out = append([]protocol.PromptPart{{Kind: protocol.PromptPartText, Text: composedText}}, out...)
	}
	return out
}

// attachmentIDs 从附件元数据里取 id 列表（供队项落库与投递重建用；保序）。
func attachmentIDs(metas []AttachmentMeta) []string {
	if len(metas) == 0 {
		return nil
	}
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func (r *Runner) executeRun(as *ActiveSession, t *Run, parts []protocol.PromptPart) {
	// panic 兜底：本函数是 `go r.executeRun(...)` 起的常驻 goroutine，其 panic 不会被 gin.Recovery
	// 覆盖，会直接杀掉整个 daemon（桌面形态下丢掉当前所有会话与在途轮次）。更要命的是：正常收尾
	// 里 `as.busy=false` 与 `t.CloseDone()` 都在函数尾部，一旦中途 panic，这两件事都不会发生——
	// 会话将永久停在 busy、done 永不关闭，此后每个 prompt 都返回 run_in_progress（用户侧表现为
	// "这个会话彻底卡死"）。
	//
	// 因此在这里 defer 一个 recover：把"本轮失败"如实收口（MarkSession failed + run.error），
	// 并保证 busy 释放、run 清空、done 关闭这三件收尾一定发生。
	defer func() {
		if rec := recover(); rec != nil {
			as.mu.Lock()
			client := as.client
			as.busy = false
			as.run = nil
			as.mu.Unlock()
			r.busy.Unmark(client, as.ID)
			r.CloseRunStatus(as.ID, t.id)
			r.plane.MarkSession(as.ID, SessionStatusFailed)
			r.PublishError(as.ID, t.id, fmt.Errorf("run 执行 panic: %v", rec), client)
			// CloseDone 已改成幂等（sync.Once）：panic 落在正常收尾之后（如 DeliverQueued）时，
			// 这里重复调用安全，不会二次 close 再 panic。
			t.CloseDone()
			glog.Errorw(context.Background(), "run 执行 panic（已兜底收尾：置 failed、释放 busy、关闭 done）",
				"session", as.ID, "run", t.id, "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	var (
		stop  string
		usage objusage.TokenUsage
		err   error
	)
	// 「请求已发出、首个增量未到」——它覆盖从发请求到第一个 token 之间的那段空白，
	// 而那正是用户最容易以为「没反应」的时候（今天这段时间与「已卡住」长得一模一样）。
	// 用 `t.id` 而不是重查：这一支里 `as.run` 就是 `t`，重查只多一个可能拿到 nil 的窗口。
	r.plane.PublishRunStatus(as.ID, t.id, RunStatusModelRequesting, RunStatusOpts{})
	// 内容块（P3 起含 image part；文档/大图的 handle 文本已在 sessionruntime 拼进 text 段）。
	stop, usage, err = as.PromptACP(t.ctx, parts)
	// 本轮 token 明细：写进 Run（收口用）并登记到按轮流水表（run 收口时随 runs.token_usage_json
	// 落库）。零值 = 底座未上报，落库时按"未上报"处理，不臆造。
	t.usage = usage
	if usage.Reported() {
		r.tokenUsageMu.Lock()
		r.tokenUsage[t.id] = usage
		r.tokenUsageMu.Unlock()
	}

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
	//
	// v4 P2：队项带附件时经 DeliverQueuedFn 重建 parts 投递（解析附件需要编排层的解析器）；
	// 否则走纯文本 Prompt（无附件的旧路径）。两条路都先起轮、失败即发 cancelled。
	var derr error
	if len(q.Attachments) > 0 && r.deliverQueued != nil {
		derr = r.deliverQueued(context.Background(), as.ID, q.Text, q.Attachments)
	} else {
		_, derr = r.Prompt(context.Background(), as.ID, q.Text)
	}
	if err := derr; err != nil {
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
		return failure.NewError(failure.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
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
	// agent 侧取消（session/cancel）有界发送：runtime 无响应时不阻塞「发通知」这一步。
	cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), AgentCancelSendTimeout)
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
		glog.Debugw(ctx, "session/cancel RPC 未在限期内响应（继续等结算窗口）",
			"session", sessionID, "timeout", AgentCancelSendTimeout.String())
	}
	// **不立刻 t.Cancel()**：0.2.1 的 prompt 要等 drain 完才返回；提前本地解除会把迟到
	// update 甩到下一轮。改为有界等待（见 awaitSettle），超时才强制解除。
	go r.awaitSettle(sessionID, t)
	if r.interacts != nil {
		r.interacts.CancelSession(sessionID) // 按 ACP 规范回 Cancelled 全部挂起权限/提问
	}
	return nil
}

// awaitSettle 等一轮在取消后**真正收尾**；窗口外仍未结束才强制取消本地 Prompt。
//
// 它只动 `t`（本轮自己的 ctx/done），不碰会话表：收尾的唯一写者仍是 executeRun。
func (r *Runner) awaitSettle(sessionID string, t *Run) {
	timer := time.NewTimer(r.settleTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
		return
	case <-timer.C:
	}
	select {
	case <-t.done:
		return
	default:
	}
	glog.Warnw(context.Background(), "取消后底座未在结算窗口内收尾，本地强制解除",
		"session", sessionID, "timeout", r.settleTimeout.String())
	t.Cancel()
}

// WaitRun 阻塞至当前 run 结束（demo/同步路径用）；无 run 立即返回。
func (r *Runner) WaitRun(ctx context.Context, sessionID string) (string, error) {
	as := r.handles.Get(sessionID)
	if as == nil {
		return "", failure.NewError(failure.ClassRequest, "session_not_found", fmt.Sprintf("未知会话 %q", sessionID), nil)
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
	var ae *failure.Error
	if errors.As(err, &ae) {
		s := strings.ToLower(ae.Code + " " + ae.Details)
		return strings.Contains(s, "cancel") || strings.Contains(s, "abort")
	}
	return false
}

// DropConn 一条连接刚被池摘除（池锁已释放）：作废其上的会话句柄、清在途登记与原生 id 映射。
//
// 拆除原因决定会话状态怎么收尾（`disconnectSession` 的注释是本仓对这条语义的唯一说明）：
// 把停机当成故障，会让上一轮已 completed 的会话在每次重启后被改写成 failed。
func (r *Runner) DropConn(client protocol.Conn, intentional bool) {
	// 连接断开：在途登记一并作废（executeRun 的 unmark 对已清键为 no-op）。
	r.busy.Clear(client)
	victims := r.handles.ForClient(client)
	for _, as := range victims {
		r.handles.ForgetSession(as.ID) // 连接断开：原生→平台映射一并清除（迟到事件丢弃）
	}
	for _, as := range victims {
		r.disconnectSession(as, intentional)
	}
	if len(victims) > 0 {
		glog.Infow(context.Background(), "连接拆除：会话句柄作废（会话保留，下次 prompt 按需重建）",
			"sessions", len(victims), "intentional", intentional)
	}
}

// InUse 这条连接还有人用吗：有在途轮次、或仍挂着会话（含已完成态）。
//
// 这是空闲回收与代际回收的**唯一判据**；池在池锁之外问它，句柄表的锁与池锁不互指。
func (r *Runner) InUse(client protocol.Conn) bool {
	if r.busy.Len(client) > 0 {
		// 有在途 run：绝不打断。（挂起权限/提问只在某一轮的执行期间存在，而那一轮就是在途 run。）
		return true
	}
	return len(r.handles.ForClient(client)) > 0 // 仍有会话引用（含已完成态），不回收
}

// Evict 把句柄从活跃集合摘除（仅当仍是当前句柄；已被新句柄顶替时不动）。
func (r *Runner) Evict(as *ActiveSession) { r.handles.RemoveIf(as.ID, as) }

// Disconnect 作废单个句柄（拆连路径按连接批量调用它；测试/运维也可显式停用一条）。
func (r *Runner) Disconnect(as *ActiveSession, intentional bool) {
	r.disconnectSession(as, intentional)
}

// disconnectSession 把某断连连接上的会话句柄作废并摘出活跃集合。
//
// 本函数是「连接生命周期」写会话状态的**唯一入口**，因此必须区分拆除原因——把停机当成故障，
// 会让上一轮已 completed 的会话在每次重启后被改写成 failed（并往历史里追加假的 run.error）：
//
//   - 在途 run：主动拆除（daemon 停机 / 显式停 agent）→ 收尾归 interrupted（无终态、可续聊重发，
//     与崩溃恢复同义，不发错误事件）；意外断开 → 解除阻塞，由 executeRun 按错误收尾。
//   - 空闲会话：**不改写状态**。上一轮的终态是轮次结果的事实，连接掉了并不改变它；意外断开
//     只落 agent 级诊断（last_error + stderr 尾巴）与日志，不伪造会话失败。
//
// 会话记录本身（Plane/store）不动：执行可丢弃、会话平台拥有（ADR-4），下次 prompt 经
// `Lifecycle.Ensure` 走 native resume / handoff memo 重建句柄。
func (r *Runner) disconnectSession(as *ActiveSession, intentional bool) {
	t, newly := as.MarkDead(intentional)
	if !newly {
		return
	}
	r.handles.RemoveIf(as.ID, as)

	// 连接没了，挂起的权限/提问永远等不到决策：随拆除一并收敛，不留「待确认」幽灵卡。
	if r.interacts != nil {
		r.interacts.CancelSession(as.ID)
	}

	if t != nil {
		t.Cancel()
		return
	}
	if intentional {
		return
	}
	ae := failure.NewError(failure.ClassConnection, "connection_lost",
		"runtime 连接断开（会话句柄已作废；下次 prompt 按需重建）", nil)
	if r.diagnostics != nil {
		tail := r.diagnostics.StderrTail(as.Client())
		ae.StderrTail = tail
		r.diagnostics.RecordRuntimeError(as.ID, ae, tail)
	}
	glog.Warnw(context.Background(), "连接意外断开：空闲会话保持原状态（会话可续聊）",
		"session", as.ID, "runtime", as.Runtime)
}

// LiveConfigOptions 返回任一活跃会话的配置项（模型目录的**首选来源**：运行时此刻
// 的真实状态，零额外开销）。无活跃会话返回 false。
//
// 注：有活跃会话但配置项为空时同样返回 ok=true（空即运行时的真实答案：未配 provider/未登录），
// 不走 resume 兜底去「凑一份」目录。
func (r *Runner) LiveConfigOptions() ([]protocol.ConfigOption, bool) {
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

// TokenUsageOf 取某轮（run id）的 token 明细流水（无上报 → false）。run 收口落库用。
// 取到即删（一轮只收口一次；避免按轮表随会话增长无界堆积）。
func (r *Runner) TokenUsageOf(runID string) (objusage.TokenUsage, bool) {
	r.tokenUsageMu.Lock()
	defer r.tokenUsageMu.Unlock()
	u, ok := r.tokenUsage[runID]
	if ok {
		delete(r.tokenUsage, runID)
	}
	return u, ok
}

// ——— 产物自动抽取：在途工具写文件线索的缓存（P2-b） ———
//
// 为什么要有这张小表：R1 探针实测 dsh 的 write/edit **只在 in_progress 的 tool_call 帧**带
// `rawInput.file_path`，终态 tool_call_update 帧**不带**。而「产物登记」只能在**确认成功**的终态
// 那一刻做。所以：in_progress 帧把整条 info 缓存下来，终态 completed 帧取出交给 extractor。
//
// 本层**不判断**它是不是 write/edit、也不解析路径——那是装配层实现的事（domain 不认识 infra）。
// 这里只做「缓存住、终态取出、不无限增长」三件事；extractor 为 nil 时整条路径零开销。

// pendingToolWrite 一条在途工具调用的缓存（含平台会话/轮次 id 与原始事件）。
type pendingToolWrite struct {
	sessionID string
	runID     string
	info      protocol.ToolCallInfo
	// runtimeID 归一化事件所属底座（失败归类/诊断用；抽取本身不依赖它，但保留便于实现侧判断）。
	runtimeID string
}

// maxPendingToolWrites 在途线索上限（防长跑进程里 map 只增不减）。256 与 toolMemory 同量级：
// 覆盖「同会话并发工具 + 权限等待」窗口绰绰有余；超出按插入序淘汰最旧。
const maxPendingToolWrites = 256

// rememberToolWrite 缓存一条工具调用的写文件线索（空 id 忽略；extractor 为 nil 时不缓存，零开销）。
func (r *Runner) rememberToolWrite(sessionID, runID, runtimeID string, info protocol.ToolCallInfo) {
	if r.extractor == nil || info.ID == "" {
		return
	}
	r.toolWriteMu.Lock()
	defer r.toolWriteMu.Unlock()
	if r.toolWrites == nil {
		r.toolWrites = make(map[string]pendingToolWrite)
	}
	if _, seen := r.toolWrites[info.ID]; !seen {
		r.toolWriteOrder = append(r.toolWriteOrder, info.ID)
	}
	r.toolWrites[info.ID] = pendingToolWrite{
		sessionID: sessionID, runID: runID, runtimeID: runtimeID, info: info,
	}
	// 淘汰最旧（order 里可能含已取出的 id，循环到找到一个还在的为止）。
	for len(r.toolWriteOrder) > maxPendingToolWrites {
		old := r.toolWriteOrder[0]
		r.toolWriteOrder = r.toolWriteOrder[1:]
		if _, ok := r.toolWrites[old]; ok {
			delete(r.toolWrites, old)
			break
		}
	}
}

// takeToolWrite 取出并清除一条在途线索（不存在 → false）。
func (r *Runner) takeToolWrite(id string) (pendingToolWrite, bool) {
	if r.extractor == nil || id == "" {
		return pendingToolWrite{}, false
	}
	r.toolWriteMu.Lock()
	defer r.toolWriteMu.Unlock()
	p, ok := r.toolWrites[id]
	if ok {
		delete(r.toolWrites, id)
	}
	return p, ok
}
