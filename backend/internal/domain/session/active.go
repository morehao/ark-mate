package session

import (
	"context"
	"sync"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 会话的**运行期句柄**（P4a 正文第五十五刀：从 `internal/manager` 搬入）———
//
// 这一族描述「一个平台会话此刻挂在哪条 ACP 会话上、这一轮在不在跑」：
//
//	ActiveSession —— 一个持久会话的运行期句柄（连接 + 原生会话 + 代际 + 轮次状态）
//	Run           —— 一轮 prompt 的运行状态（身份 / 取消 / 终态）
//	Handles       —— 进程内的活跃句柄表（平台 id ↔ 原生 id 双索引）
//
// **为什么它属于会话域**：这些字段是「会话此刻在做什么」的事实，与 `SessionRec`（会话是什么）
// 同属一个聚合；把它们留在编排层会让「会话状态」有两个持有者。执行面（`runtimeconn`）只持有
// **连接**，它不拥有会话——拆连时它回调编排侧（`DropSink`），由这边把句柄作废。

// ACP 是 acp.Session 的最小接口（测试可注入桩；真实实现为 protocol.Session）。
type ACP interface {
	Prompt(ctx context.Context, text string) (string, error)
	Cancel(ctx context.Context) error
	// ConfigOptions 会话配置项（模型目录等，唯一来源：运行时自报）。
	ConfigOptions() []protocol.ConfigOption
}

// ActiveSession 是持久会话句柄：绑定一条 ACP 会话与其所属连接，
// 支持多轮 prompt（每轮单飞）。事件经 EventSink 入 eventbus（agg = session id）。
type ActiveSession struct {
	ID string
	// Runtime 本句柄所属的 runtime（= 构建期固定的那个；v19 起它是唯一身份）。
	Runtime string
	// Fingerprint 该会话钉住的能力指纹（代际）。空 = 未声明能力选择。恢复路径据此回到**同一代际**
	// （漏了它就会在重启后悄悄换一套能力集）。
	Fingerprint string
	// NativeMode 该会话钉住的原生权限模式（v29）。它是**暖进程键的一维**：模式只能以进程 env
	// 下发，所以两个模式不同的会话不能共用一个暖进程（见 `runtimeconn.Pool.KeyFor`）。
	NativeMode string

	client protocol.Conn
	sess   ACP
	// providerID 底座侧的原生会话 id（`sess.ID()`）。存一份是因为换句柄（代际迁移）时要拿旧值
	// 摘 `byProvider` 映射与关掉旧会话，而 `sess` 是接口（`ACP` 上没有 ID()）。
	providerID string
	// proj 会话的可执行投影（§4.2 会话边界版；design doc §16 Orchestrator 采用 Runtime）。
	// 由 startSessionOn 依 spec 构造；测试注入的假会话为 nil → 走 sess 直连路径。
	proj runtime.Runtime

	mu   sync.Mutex
	busy bool
	run  *Run
	dead bool // 连接已断开，本句柄作废（下次 prompt 经 ensureSession 按需重建）
	// identity 产品身份的首轮块（每个执行上下文一次）。
	//
	// 它与 memo 分开、但同一种消费语义（首个 BeginRun 取走）：身份是**产品级、稳定**的，
	// memo 是历史摘要。分成两个字段而不是拼成一个串，是为了让"谁先谁后"成为可读的判据
	// （identity 在前——先立产品的规矩，再讲上一段历史）。
	identity string
	// memo bounded handoff memo（P3 F3）：跨 runtime / 无原生 resume 兜底建的 fresh session
	// 首条 prompt 注入（Prompt 消费后清空，仅生效一次）。
	memo string
	// expertRole 专家角色设定块（P1）：会话级专家 `instructions` 构造的隐藏 user-context，
	// **只在首轮**注入一次（首个 BeginRun 取走），随执行上下文一次性交付，次轮不重复。
	// 与 identity 的区别：identity 每个执行上下文都声明，expertRole 只在**初始**上下文声明
	//（resume/迁移不重注入——首轮已经发过，历史里已有人格）。
	expertRole string
	// expertPersona 专家角色块的**惰性提供者**（P1）：首轮 BeginRun 时才调用，使"创建后、
	// 首轮前改专家定义"按发送时最新 instructions 注入（设计沉默清单 #6）。一次性消费。
	expertPersona func() string
}

// HandleParams 造一个运行期句柄所需的全部事实（装配侧持有连接与投影，域只持有结果）。
type HandleParams struct {
	ID          string
	Runtime     string
	Fingerprint string
	NativeMode  string
	ProviderID  string
	Memo        string
	// Identity 产品身份的首轮块（空 = 本次执行上下文不注入；见 LifecycleDeps.Identity）。
	//
	// 它随**句柄**交付（而不是随会话记录）：每建一次执行上下文（新建、原生恢复、handoff、
	// 代际迁移）都重新声明一次身份——那是刻意的"重新锚定"，代价是每个上下文多 ~100 tokens。
	Identity string
	// ExpertRole 专家角色设定块（P1，空 = 无专家人格；只在首轮注入一次）。
	ExpertRole string
	// ExpertPersona 专家角色块的惰性提供者（nil = 用静态 ExpertRole）。首轮 BeginRun 时才求值。
	ExpertPersona func() string
	Client        protocol.Conn
	Session       ACP
	Projection    runtime.Runtime
}

// NewActiveSession 造一个句柄。字段在构造后**不可变**（除轮次/存活/memo 这些由方法改的状态），
// 所以索引与身份不会在运行中被换掉。
func NewActiveSession(p HandleParams) *ActiveSession {
	return &ActiveSession{
		ID: p.ID, Runtime: p.Runtime, Fingerprint: p.Fingerprint, NativeMode: p.NativeMode,
		client: p.Client, sess: p.Session, providerID: p.ProviderID, proj: p.Projection,
		memo: p.Memo, identity: p.Identity, expertRole: p.ExpertRole, expertPersona: p.ExpertPersona,
	}
}

// Client 所属连接（nil = 未拨号/测试桩）。
func (as *ActiveSession) Client() protocol.Conn { return as.client }

// Native 原生 ACP 会话（nil = 尚未建立）。
func (as *ActiveSession) Native() ACP { return as.sess }

// Projection 可执行投影（nil = 该 runtime 无投影/测试桩）。
func (as *ActiveSession) Projection() runtime.Runtime { return as.proj }

// ProviderID 底座侧原生会话 id（原文，未加 `rt_` 壳；它是 byProvider 的键）。
func (as *ActiveSession) ProviderID() string { return as.providerID }

// Dead 句柄是否已作废（连接断开/被新句柄顶替）。
func (as *ActiveSession) Dead() bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.dead
}

// Busy 是否有在途轮次。
func (as *ActiveSession) Busy() bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.busy
}

// RunID 当前在途轮次的 id（无在途 → 空串）。
func (as *ActiveSession) RunID() string {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.run == nil {
		return ""
	}
	return as.run.id
}

// ActiveRun 当前在途轮次（无在途 → nil）。
func (as *ActiveSession) ActiveRun() *Run {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.run
}

// SetMemo 挂上 handoff memo（仅恢复路径用；消费在 BeginRun）。
func (as *ActiveSession) SetMemo(memo string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.memo = memo
}

// RequestCancel 记下「用户主动取消」并返回当前在途轮次（无在途 → nil）。
func (as *ActiveSession) RequestCancel() *Run {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.run != nil {
		as.run.cancelReq = true
	}
	return as.run
}

// PromptACP 发一轮 prompt：有投影走投影，否则直连原生会话。
func (as *ActiveSession) PromptACP(ctx context.Context, text string) (string, error) {
	if as.proj != nil {
		return as.proj.Prompt(ctx, runtime.PromptRequest{Text: text})
	}
	return as.sess.Prompt(ctx, text)
}

// CancelACP 取消底层会话（投影优先，测试桩直连）。
func (as *ActiveSession) CancelACP(ctx context.Context) error {
	if as.proj != nil {
		return as.proj.Cancel(ctx)
	}
	return as.sess.Cancel(ctx)
}

// RunPrefix 一轮开跑时**一次性**取走的前缀（身份 → memo）。
//
// 它只进发给底座的 prompt 正文，**不进事件**：`session.user_message` 与 transcript 保存的是
// 用户原文（与 memo 的既有口径一致）。把它收成一个值对象而不是两个 string 返回值，是因为
// 拼接顺序本身是判据（identity 在前），而顺序写在调用点就会被下一次改动悄悄对调。
type RunPrefix struct {
	// Identity 产品身份首轮块（空 = 本上下文不注入）。
	Identity string
	// ExpertRole 专家角色设定块（P1；空 = 无专家人格）。与 identity 一起同属"隐藏 user-context"，
	// 由平台在**发送给底座前**拼进首条 prompt，不进事件/transcript。
	ExpertRole string
	// Memo bounded handoff memo（空 = 无历史摘要）。
	Memo string
}

// Empty 三个面都为空（调用方可据此跳过拼接）。
func (p RunPrefix) Empty() bool { return p.Identity == "" && p.ExpertRole == "" && p.Memo == "" }

// Compose 把前缀按 **identity → expertRole → memo → 用户原文** 的顺序拼成实际发给底座的文本。
//
// 顺序理由：identity 是产品级稳定身份，先立规矩；expertRole 是 `Role Override`，语义上要**压过**
// 产品身份（WorkBuddy 的专家模式即如此），所以排在 identity 之后；memo 是可能含旧自述的历史摘要，
// 排在最后（离用户原文最近）。反过来会把"角色覆盖"落到产品身份之前或之后错误的一侧。
func (p RunPrefix) Compose(text string) string {
	// 三次都是"往前面插"，所以**后插的在前**：memo → expertRole → identity。
	if p.Memo != "" {
		text = p.Memo + "\n\n" + text
	}
	if p.ExpertRole != "" {
		text = p.ExpertRole + "\n\n" + text
	}
	if p.Identity != "" {
		text = p.Identity + "\n\n" + text
	}
	return text
}

// BeginRun 开一轮：忙时返回 run_in_progress；成功时把身份块与 memo 一次性取走。
//
// 返回的前缀只给调用方拼进**首条 prompt**（事件里的 user_message 保持用户原文，不含前缀）。
// 「一次性」是刻意的：身份每个**执行上下文**声明一次（不是每轮一次）——每轮重复会让
// 系统提示之外又多出一份每轮 token 成本，而它想解决的问题（锚定产品身份）首轮就解决了。
func (as *ActiveSession) BeginRun(ctx context.Context) (*Run, RunPrefix, error) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.busy {
		return nil, RunPrefix{}, failure.NewError(failure.ClassLimit, "run_in_progress", "该会话正有 run 在执行，请等待结束", nil)
	}
	// 首轮才求值角色块：提前到构造期会把"创建后、首轮前改专家定义"的改动丢掉。
	// 静态 ExpertRole 优先（测试/直连注入），否则再走惰性提供者。
	expertRole := as.expertRole
	if expertRole == "" && as.expertPersona != nil {
		expertRole = as.expertPersona()
	}
	prefix := RunPrefix{Identity: as.identity, ExpertRole: expertRole, Memo: as.memo}
	as.identity, as.expertRole, as.memo, as.expertPersona = "", "", "", nil
	tctx, cancel := context.WithCancel(ctx)
	t := &Run{id: objid.New(objid.Run), done: make(chan struct{}), cancel: cancel, ctx: tctx}
	as.run = t
	as.busy = true
	return t, prefix, nil
}

// FinishRun 收尾一轮：清 busy/run 并把所属连接一并交回（取消在途登记要用它）。
func (as *ActiveSession) FinishRun() (*Run, protocol.Conn) {
	as.mu.Lock()
	defer as.mu.Unlock()
	t := as.run
	as.busy = false
	as.run = nil
	return t, as.client
}

// MarkDead 作废句柄（连接断开/被新句柄顶替）。intentional = 我方主动拆除。
//
// 返回在途轮次（没有则 nil）与「本次是否真的从活变死」：重复调用是幂等的，
// 拆连路径据此避免对同一句柄跑两遍收尾。
func (as *ActiveSession) MarkDead(intentional bool) (*Run, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.dead {
		return nil, false
	}
	as.dead = true
	if as.run != nil && intentional {
		as.run.intentional = true
	}
	return as.run, true
}

// Run 一轮 prompt 的运行状态。
type Run struct {
	// id 本轮标识（平台侧生成）。用途：§6 排队输入的 target_run_id 台账（「排在哪一轮后面」），
	// 以及日志/事件里指认具体某轮——不做投递判据（那一轮必然先结束）。
	id        string
	done      chan struct{}
	cancel    context.CancelFunc
	cancelReq bool // 用户主动 cancel（区别于父 ctx 超时/外部取消）
	// intentional 本轮所属连接是被**我方主动拆除**的（daemon 停机），
	// 区别于对端或子进程意外断开：主动拆除不是故障，收尾归 interrupted（无终态、可续聊重发），
	// 不能当失败处理（见 disconnectSession 的状态语义）。受 as.mu 保护。
	intentional bool
	ctx         context.Context
	stop        string
	err         error
}

// ID 本轮标识。
func (t *Run) ID() string { return t.id }

// Context 本轮的执行上下文。
func (t *Run) Context() context.Context { return t.ctx }

// Cancel 解除 SDK 阻塞（不等待 RPC 送达）。
func (t *Run) Cancel() { t.cancel() }

// Done 本轮收尾信号。
func (t *Run) Done() <-chan struct{} { return t.done }

// CloseDone 宣告收尾（只许单写者调用）。
func (t *Run) CloseDone() { close(t.done) }

// RequestCancel 记下「用户主动取消」（执行侧的收尾据此走 cancelled 分支）。
//
// **必须经句柄调用**（`ActiveSession.RequestCancel`）：cancelReq 与 run 同受 as.mu 保护，
// 直接写字段会与 executeRun 的收尾读形成数据竞争。

// CancelRequested 用户是否主动取消。
func (t *Run) CancelRequested() bool { return t.cancelReq }

// MarkIntentional 标记本轮因主动拆除而中断。
func (t *Run) MarkIntentional() { t.intentional = true }

// Intentional 本轮是否因主动拆除而中断。
func (t *Run) Intentional() bool { return t.intentional }

// Result 本轮终态（stop reason 与错误；收尾后才有意义）。
func (t *Run) Result() (string, error) { return t.stop, t.err }

// Handles 进程内的活跃句柄表（平台 id 与原生 id 双索引）。
//
// 两张表**同生共死**：只摘一张的表现是「事件还能路由到会话，但句柄已经没人认」——
// 那是两份索引最典型的静默分叉，所以它们由同一把锁、同一组方法维护。
type Handles struct {
	mu         sync.Mutex
	byID       map[string]*ActiveSession
	byProvider map[string]string
}

// NewHandles 造一张空表。
func NewHandles() *Handles {
	return &Handles{byID: make(map[string]*ActiveSession), byProvider: make(map[string]string)}
}

// Get 按平台会话 id 取句柄（不存在 → nil）。
func (h *Handles) Get(id string) *ActiveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[id]
}

// Put 装入句柄（替换同 id 的旧句柄），返回被替换的旧句柄。
//
// 旧句柄的原生映射只在「旧句柄的原生 id 与新句柄不同」时摘除：同一条 ACP 会话被重新挂上时
// 映射仍然成立，摘掉反而会让迟到事件失去归属。
func (h *Handles) Put(as *ActiveSession) *ActiveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.byID[as.ID]
	h.byID[as.ID] = as
	if as.providerID != "" {
		h.byProvider[as.providerID] = as.ID
	}
	if old != nil && old != as && old.providerID != as.providerID &&
		h.byProvider[old.providerID] == old.ID {
		delete(h.byProvider, old.providerID)
	}
	return old
}

// RemoveIf 仅当表中的当前句柄仍是 as 时摘除（被新句柄顶替时不动）。
func (h *Handles) RemoveIf(id string, as *ActiveSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byID[id] != as {
		return false
	}
	delete(h.byID, id)
	return true
}

// Remove 按平台会话 id 摘除（装配/测试用；生产路径用 RemoveIf 防顶替竞态）。
func (h *Handles) Remove(id string) *ActiveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.byID[id]
	delete(h.byID, id)
	return old
}

// BindProvider 登记原生 id → 平台 id（生产由 Put 自动完成；此处供装配/测试显式构造映射）。
func (h *Handles) BindProvider(providerID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byProvider[providerID] = sessionID
}

// ResolveProvider 把原生 provider session id 解析为平台会话 uuid（未登记 → 空串）。
func (h *Handles) ResolveProvider(providerID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byProvider[providerID]
}

// ForgetSession 移除某会话的全部原生映射（连接失活/会话清理时调用）。
func (h *Handles) ForgetSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.byProvider {
		if v == sessionID {
			delete(h.byProvider, k)
		}
	}
}

// ForClient 该连接上挂着哪些句柄（含已完成态）。
func (h *Handles) ForClient(client protocol.Conn) []*ActiveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*ActiveSession, 0, 1)
	for _, as := range h.byID {
		if as.client == client {
			out = append(out, as)
		}
	}
	return out
}

// AnyLive 任取一条仍有原生会话的活句柄的 ACP 会话（无 → nil）。
func (h *Handles) AnyLive() ACP {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, as := range h.byID {
		as.mu.Lock()
		dead, sess := as.dead, as.sess
		as.mu.Unlock()
		if dead || sess == nil {
			continue
		}
		return sess
	}
	return nil
}

// Len 活跃句柄数。
func (h *Handles) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byID)
}
