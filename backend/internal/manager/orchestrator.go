package manager

import (
	"context"
	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/session"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objusage"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// agentCancelTimeout session/cancel RPC 的上限等待：底座无响应（如模型限流重试挂起）时
// 不阻塞本地解除，Prompt 的 ctx 取消总是会立刻返回。
const agentCancelTimeout = 3 * time.Second

// DefaultHandoffMaxChars bounded handoff memo 默认字符预算（§18 D-3；env MATE_HANDOFF_MAX_CHARS 覆盖）。
const DefaultHandoffMaxChars = 6000

// DefaultGenerationLimit 同一配置下的能力代际上限默认值（风险表第 12 行；env MATE_GENERATION_LIMIT）。
const DefaultGenerationLimit = 4

// acpSession 是 acl.Session 的最小接口（测试可注入桩；真实实现为 *acl.Session）。
type acpSession interface {
	Prompt(ctx context.Context, text string) (string, error)
	Cancel(ctx context.Context) error
	// ConfigOptions 会话配置项（模型目录等，唯一来源：运行时自报）。
	ConfigOptions() []acl.ConfigOption
}

// ActiveSession 是 manager 层持久会话句柄：绑定一条 ACP 会话与其所属连接，
// 支持多轮 prompt（每轮单飞）。事件经 eventSink 入 eventbus（agg = session id）。
type ActiveSession struct {
	ID string
	// Runtime 本句柄所属的 runtime（= 构建期固定的那个；v19 起它是唯一身份）。
	Runtime string
	// Fingerprint 该会话钉住的能力指纹（代际）。空 = 未声明能力选择。恢复路径据此回到**同一代际**
	// （漏了它就会在重启后悄悄换一套能力集）。
	Fingerprint string

	client *acl.Client
	sess   acpSession
	// providerID 底座侧的原生会话 id（`sess.ID()`）。存一份是因为换句柄（代际迁移）时要拿旧值
	// 摘 `byProvider` 映射与关掉旧会话，而 `sess` 是接口（`acpSession` 上没有 ID()）。
	providerID string
	// proj 会话的可执行投影（§4.2 会话边界版；design doc §16 Orchestrator 采用 Runtime）。
	// 由 startSessionOn 依 spec 构造；测试注入的假会话为 nil → 走 sess 直连路径。
	proj runtime.Runtime

	mu   sync.Mutex
	busy bool
	run  *Run
	dead bool // 连接已断开，本句柄作废（下次 prompt 经 ensureSession 按需重建）
	// memo bounded handoff memo（P3 F3）：跨 runtime / 无原生 resume 兜底建的 fresh session
	// 首条 prompt 注入（Prompt 消费后清空，仅生效一次）。
	memo string
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

// CreateResult 是 StartSession 的结果。
type CreateResult struct {
	SessionID string
	Runtime   string
	Status    string
	EventSeq  int64 // session.created 事件的 bus 水位（供 WS attach 续传）
}

// Orchestrator 编排 runtime 连接生命周期与会话执行（连接获取→session/new→多轮 prompt→cancel）。
type Orchestrator struct {
	svc *Service
	reg *runtime.Registry

	mu       sync.Mutex
	conns    map[string]*runtimeConn // key = connKey(runtime, 原生权限 env)；同配置共享暖进程
	sessions map[string]*ActiveSession
	// byProvider：原生 provider session id → 平台会话 uuid（P3 双 id）。事件/权限等底层
	// 回调只携带原生 id（acl.Event.SessionID / PermissionRequest.SessionID），host 侧经此
	// 解析为平台会话 id 再发布/键控；会话失活（dropConn）时随 victim 一并清除。
	byProvider map[string]string
	reapStop   context.CancelFunc // 空闲回收 goroutine 停止（Close 时调用）

	// inflight 记录每连接上正在执行 run 的会话（session.InFlightTracker：§17 E1 显式化簿记，
	// 语义同 §1.5 决策修订——连接上唯一在途才归属，多会话并发在途不猜测）。
	inflight *session.InFlightTracker
	// migMu 串行化代际迁移（M3-6）：罕见操作，一把全局锁足够；不设它会两个并发轮次各迁一次。
	migMu    sync.Mutex
	allowAll bool // 调试/冒烟用：放行一切权限请求（默认拒绝）

	// perm 管理挂起权限请求（允许/拒绝 × once/always + 会话级始终允许记忆 + 超时自动拒）。
	perm *interaction.PermissionManager
	// elic 管理挂起结构化提问（elicitation/create：表单/URL + 超时自动取消）。
	elic *interaction.ElicitationManager

	// usageMu/usage 保护「每平台会话最近一次 ACP 用量快照」（M3 用量统计：run 收口时
	// 随 runs.usage_json 落库；底座不上报时无条目）。
	usageMu sync.Mutex
	usage   map[string]objusage.UsageInfo

	// runTimeout 单轮 prompt 看门狗（0 = 关闭；见 SetRunTimeout / MATE_RUN_TIMEOUT）。
	runTimeout time.Duration

	// handoffMaxChars bounded handoff memo 字符预算（§5.6/§18 D-3；见 SetHandoffMaxChars /
	// MATE_HANDOFF_MAX_CHARS）。0 = 关闭 memo 兜底。
	handoffMaxChars int

	// generationLimit 同一基础键（runtime + 原生权限 + provider）下允许并存的代际上限
	// （见 SetGenerationLimit / MATE_GENERATION_LIMIT）。0 = 不限制。
	generationLimit int
}

type runtimeConn struct {
	client *acl.Client
	spec   runtime.Spec
	// key 连接缓存键（B5：runtime ID + 原生权限 env 签名 + 代际维度）。同一 runtime 但原生权限
	// 配置不同的会话必须各自独立进程（权限开关是进程级），配置相同则共享暖进程。
	key string
	// baseKey 该连接的基础键（= 不含代际维度的 key）。停机用它一次性找到**该配置的
	// 全部代际**——只按 key 找的话，代际机制一旦真的产生多代际就会漏关。
	baseKey string
	// configDigest 该连接建立时的平台模型配置指纹（T5）。代际裁决用它判断"清单变了没有"。
	configDigest string
	started      time.Time // 连接建立时刻（空闲回收计时起点）
	pid          int       // 子进程 pid（进程注册表登记用；0 = 未登记）
	// intentional 该连接是**我方主动关闭**的（daemon 停机 / 空闲回收），
	// 而非对端或子进程意外断开。拆除方在 Close 之前置位，dropConn 据此判定拆除原因；
	// 主动拆除不得把会话写成失败态（见 disconnectSession 的状态语义）。
	intentional atomic.Bool
}

// RunOptions 一次 RunOnce（demo）的入参。
//
// **没有 runtime 字段**：底座是构建期固定的（pkg/runtime/build.go），不是这次运行的入参。
type RunOptions struct {
	Prompt     string
	Cwd        string   // 空则用当前工作目录
	Env        []string // 附加环境
	ConfigPath string   // 自定义 adapter 的 {config} 占位替换（内置 runtime 无占位，预留扩展）
}

// StartOptions 一次 StartSession 的入参。
//
// Cwd 为空时回落当前工作目录，**仅为 demo/RunOnce 路径保留**：会话创建路径
// （Service.CreateSession）必须先经 ResolveWorkspace 定出工作空间再传进来，
// 绝不能让「平台站在哪」成为用户会话的沙箱边界（见 workspace.go）。
type StartOptions struct {
	// SessionID 平台会话 uuid；空 = 由编排器生成。
	// 该 id 在 session/new **之前**定号，代建工作空间的目录名以它为准。
	SessionID string
	// Cwd 会话工作空间（规范化后的绝对路径）；空 = 用当前工作目录（demo 语义）。
	Cwd string
	// WorkspaceTemporary 该 cwd 是否由**平台**给出（= 默认工作空间；随响应透出，
	// 前端不得从路径形状反推）。v14 起语义从"平台按会话代建"变成"这是默认工作空间"。
	WorkspaceTemporary bool
	// WorkspaceID 归属的工作空间登记 id（空 = 未登记 → 读面归默认工作空间）。
	//
	// 与 Cwd 的分工见 SessionRec.WorkspaceID：Cwd 是执行事实（ACP cwd、沙箱边界），
	// WorkspaceID 是归属事实（左栏分组）。这里两个字段一起传，是因为**定稿发生在同一处**
	// （CreateSession 解析出 ws 之后），分开传会让两条来源有机会不一致。
	WorkspaceID string
	// Model 会话级模型。**`Model` 字段装的是平台模型行 id**（`mdl_…`；空 = 用平台默认那一行），
	// 由 `startSessionOn` 经 `resolveSessionModel` 解析成"行 + 运行时值"——
	// v26 起库里/wire 上流通的是**身份**，底座认的不透明值由 adapter 现算（设计 §5.4 定案「辰」）。
	Model runtime.ModelConfig
	// Origin 会话来源（平台判定，非用户输入）：'' = 人工，'automation:<id>' = 该 automation 触发。
	// 只用它给会话列表打标与做"某 automation 历次生效运行"的快速路径；不参与任何鉴权判断。
	Origin string
	// Selection 该会话声明的能力选定（M3；nil = 未声明 → 全量按 runtime 过滤）。
	//
	// **这里刻意没有独立的 `Fingerprint` 字段**：指纹在 startSessionOn 里由本字段
	// 现算（`capabilityFingerprint`）。两者分开传的后果是"指纹按 A 算、注入按 B 做"，
	// 而那种分裂不会报错——只会让会话拿到与代际台账不符的能力集，且只在重启后显现。
	Selection *CapabilitySelection
}

// RunResult 一次 RunOnce（demo）的结果。
type RunResult struct {
	SessionID  string
	Runtime    string
	StopReason string
}

// NewOrchestrator 构造编排器并回挂到 Service（Service 负责 bus/存储/REST 方法）。
func NewOrchestrator(svc *Service, reg *runtime.Registry) *Orchestrator {
	o := &Orchestrator{
		svc:             svc,
		reg:             reg,
		conns:           make(map[string]*runtimeConn),
		sessions:        make(map[string]*ActiveSession),
		byProvider:      make(map[string]string),
		inflight:        session.NewInFlightTracker(),
		perm:            interaction.NewPermissionManager(interaction.DefaultPermissionTimeout),
		elic:            interaction.NewElicitationManager(interaction.DefaultElicitationTimeout),
		usage:           make(map[string]objusage.UsageInfo),
		handoffMaxChars: DefaultHandoffMaxChars,
		generationLimit: DefaultGenerationLimit,
	}
	// 挂起权限一旦有终局（用户决策 / 超时自动拒绝 / 会话取消），发布收敛事件
	// （session.permission_resolved），前端据此收敛权限卡——服务端事件是唯一事实，不再依赖前端时钟。
	o.perm.SetOnResolved(func(sessionID, callID, reason string, d acl.PermissionDecision) {
		outcome := "cancelled"
		if d.Allowed || (d.Rejected && d.OptionID != "") {
			outcome = "selected"
		}
		o.svc.publish(session.BusPermissionResolved, sessionID, map[string]any{
			"session_id": sessionID,
			"call_id":    callID,
			"outcome":    outcome,
			"reason":     reason,
			"allowed":    d.Allowed,
			"rejected":   d.Rejected,
			"option_id":  d.OptionID,
			"always":     d.Always,
		})
	})
	// 会话级记忆持久化挂钩（sqlite 模式写 permission_memories 表；st 为 nil 时 no-op）。
	o.perm.SetMemoryHooks(o.svc.persistMemoryAdd, o.svc.persistMemoryDel, o.svc.persistMemoryDelAll)
	// 挂起提问一旦有终局（accept/decline/cancel/超时自动取消/会话取消），发布收敛事件
	// （session.elicitation_resolved），前端据此收敛提问卡——服务端事件是唯一事实。
	o.elic.SetOnResolved(func(sessionID, id, reason string, d acl.ElicitationOutcome) {
		o.svc.publish(session.BusElicitationResolved, sessionID, map[string]any{
			"session_id":     sessionID,
			"elicitation_id": id,
			"action":         d.Action,
			"reason":         reason,
		})
	})
	svc.attachOrchestrator(o)
	return o
}

// SetAllowAll 放开权限请求（仅调试/冒烟；默认一律拒绝）。
func (o *Orchestrator) SetAllowAll(v bool) { o.allowAll = v }

// RestoreMemoriesFromStore 启动恢复会话级权限记忆（sqlite 模式：
// permission_memories 全库装载进 perm.mem——allow/reject 双侧、跨重启不丢）。
func (o *Orchestrator) RestoreMemoriesFromStore(ctx context.Context) error {
	rows, err := o.svc.memoryRows(ctx)
	if err != nil {
		return err
	}
	items := make([]interaction.MemoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, interaction.NewMemoryItem(
			r.SessionID, r.Title, r.Kind, r.Decision, r.CreatedAt))
	}
	o.perm.LoadMemories(items)
	glog.Infow(ctx, "permission memories restored", "count", len(items))
	return nil
}

// SetPermissionTimeout 覆盖挂起权限请求的自动拒绝超时（0 → 默认 30min）。
func (o *Orchestrator) SetPermissionTimeout(d time.Duration) { o.perm.SetTimeout(d) }

// SetElicitationTimeout 覆盖挂起结构化提问的自动取消超时（0 → 默认 30min）。
func (o *Orchestrator) SetElicitationTimeout(d time.Duration) { o.elic.SetTimeout(d) }

// SetRunTimeout 设置单轮 run 看门狗超时（0 = 关闭）。见 watchdogRun。
func (o *Orchestrator) SetRunTimeout(d time.Duration) { o.runTimeout = d }

// SetHandoffMaxChars 设置 bounded handoff memo 字符预算（0 = 关闭 memo 兜底）。见 ensureSession。
func (o *Orchestrator) SetHandoffMaxChars(n int) { o.handoffMaxChars = n }

// SetGenerationLimit 设置同一配置下的能力代际上限（0 = 不限制）。见 admitGeneration。
//
// 是**软上限**：达到上限时先显式淘汰「无会话引用且空闲」的代际，只有全都还在用时才拒绝新建。
// 拒绝而不是复用：复用错的能力集是安全 bug（底座会给 A 会话的能力投给 B），宁可报错。
func (o *Orchestrator) SetGenerationLimit(n int) {
	if n < 0 {
		n = 0
	}
	o.mu.Lock()
	o.generationLimit = n
	o.mu.Unlock()
}

// GenerationLimit 当前上限（0 = 不限制；诊断/测试用）。
func (o *Orchestrator) GenerationLimit() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.generationLimit
}

// session 按 ID 取活跃会话（连接存活期内）。查找不存在（如进程重启后）按未找到处理。
func (o *Orchestrator) session(id string) *ActiveSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[id]
}

// Close 关闭全部连接并清理（daemon 退出/测试收尾）。
func (o *Orchestrator) Close() {
	o.mu.Lock()
	if o.reapStop != nil {
		o.reapStop()
		o.reapStop = nil
	}
	o.mu.Unlock()
	o.mu.Lock()
	conns := make([]*runtimeConn, 0, len(o.conns))
	for _, rc := range o.conns {
		conns = append(conns, rc)
	}
	o.conns = make(map[string]*runtimeConn)
	o.mu.Unlock()
	for _, rc := range conns {
		// 主动停机：先置原因再拆除 —— 停机不是连接故障，不能把会话写成失败态。
		rc.intentional.Store(true)
		o.disconnectSessionAll(rc)
		o.svc.setRuntimeState(session.AgentStateStopped)
		o.unregisterProc(rc)
		if rc.client != nil {
			_ = rc.client.Close()
		}
	}
	o.svc.closeTranscripts()
}
