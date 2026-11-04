package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sync"
	"time"

	interaction "github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/session"

	pkgidentity "github.com/morehao/ark-mate/internal/core/identity"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// Orchestrator 编排 runtime 连接生命周期与会话执行（连接获取→session/new→多轮 prompt→cancel）。
type Orchestrator struct {
	svc *Service
	reg *runtime.Registry
	// pool 连接池（P4a 正文第五十四刀起归执行面 `internal/drivers/runtimeconn`）。
	//
	// **它与会话表不共用锁**：池自持一把只保护连接表的锁，句柄表在会话域自己的锁下。
	// 此前三样东西挤在一把锁下，于是"持锁请求会话快照"这类锁序反转只能靠人眼防
	// （§3.1.49 踩过，表现是 `go test ./...` 挂死而不是红）。
	pool *runtimeconn.Pool

	// handles 运行期句柄表（P4a 正文第五十五刀起归 `domain/session`）：平台 id ↔ 原生 id
	// 双索引与句柄状态都在域里。编排侧只通过它取句柄，不再自己持一份 map。
	handles *session.Handles
	// runner 轮次运行期状态机（受理 / 事件出口 / 排队投递 / 收尾）。它持有在途轮次状态，
	// 与 handles 同属会话域；编排侧只负责「怎么拿到连接」与「怎么把决定投给底座」。
	runner *session.Runner

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

	// life 会话的启动/恢复/换句柄（P4a 第五十六刀起归 `domain/session`）：建会话、按需重建、
	// 代际重开与模型应用都在域里；编排侧只提供拨号、模型解析与 MCP 注入三个事实。
	life *session.Lifecycle

	// commandPolicy 命令门禁规则表（A2；`domain/interaction/command_policy.go`）。
	// 空 = 未启用：`GateCommand` 返回 isShell=false，行为与改前逐字相同。
	commandPolicy []interaction.PrefixRule

	// tools 按 toolCallId 记住工具调用详情（P4a 第六十一刀起归 `domain/session`）：
	// dsh 的权限请求只带 toolCallId，命令行在同 id 的 tool_call 更新里。
	tools *session.ToolCallMemory
}

// RunOptions 一次 RunOnce（demo）的入参。
//
// **没有 runtime 字段**：底座是构建期固定的（internal/runtime/build.go），不是这次运行的入参。
type RunOptions struct {
	Prompt     string
	Cwd        string   // 空则用当前工作目录
	Env        []string // 附加环境
	ConfigPath string   // 自定义 adapter 的 {config} 占位替换（内置 runtime 无占位，预留扩展）
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
		svc:      svc,
		reg:      reg,
		pool:     svc.runtimePool,
		handles:  session.NewHandles(),
		inflight: session.NewInFlightTracker(),
		perm:     interaction.NewPermissionManager(interaction.DefaultPermissionTimeout),
		elic:     interaction.NewElicitationManager(interaction.DefaultElicitationTimeout),
		tools:    session.NewToolCallMemory(),
	}
	// 产品身份是**装配面**的事实（模式算完才定稿），这里只交出读取口：会话在第一个 prompt
	// 时才问它，因此不需要"先塞空值再补一个 setter"（那会变成两个持有者）。
	//
	// `planes.Identity` 在 `NewPlanes` 里必造；nil 判据只服务"自造 Planes 的测试夹具"。
	var identityOf func() pkgidentity.Spec
	if svc.planes.Identity != nil {
		identityOf = svc.planes.Identity.Spec
	}
	// 轮次运行期状态机的四张窄端口：句柄取自本结构体、在途登记沿用同一份 tracker，
	// 其余三样（诊断 / 模型目录 / 工具记忆 / 交互取消）都是编排侧的事实，用适配器交出。
	o.life = session.NewLifecycle(session.LifecycleDeps{
		Plane:            svc.sessionPlane,
		Handles:          o.handles,
		Registry:         reg,
		SelectedSpec:     svc.runtimePlane.SelectedSpec,
		Acquire:          o.acquireClient,
		ResolveModel:     svc.planes.Providers.ResolveSessionModel,
		ProviderKeyReady: svc.planes.Providers.ProviderKeyReady,
		McpServers:       svc.McpServersFor,
		Catalog:          runnerCatalog{svc: svc},
		Diagnostics:      runnerDiagnostics{o: o},
		SetRuntimeState:  svc.runtimePlane.SetState,
		ApplyReason:      provider.ModelApplyReason,
		Identity:         identityOf,
	})
	o.runner = session.NewRunner(session.RunnerDeps{
		Plane:        svc.sessionPlane,
		Handles:      o.handles,
		Acquire:      o.acquireSession,
		Busy:         o.inflight,
		Diagnostics:  runnerDiagnostics{o: o},
		Catalog:      runnerCatalog{svc: svc},
		ToolMemory:   o.tools,
		ToolFailures: reg,
		Interactions: runnerInteractions{perm: o.perm, elic: o.elic},
	})
	// 挂起权限一旦有终局（用户决策 / 超时自动拒绝 / 会话取消），发布收敛事件
	// （session.permission_resolved），前端据此收敛权限卡——服务端事件是唯一事实，不再依赖前端时钟。
	o.perm.SetOnResolved(func(sessionID, callID, reason string, d protocol.PermissionDecision) {
		outcome := "cancelled"
		if d.Allowed || (d.Rejected && d.OptionID != "") {
			outcome = "selected"
		}
		o.svc.sessionPlane.Publish(session.BusPermissionResolved, sessionID, map[string]any{
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
	o.perm.SetMemoryHooks(
		func(it interaction.MemoryItem) {
			o.svc.sessionPlane.PersistMemoryAdd(it.SessionID(), it.Title, it.Kind, it.Decision,
				it.Scope, it.GrantInputDigest, it.Since)
		},
		o.svc.sessionPlane.PersistMemoryDel,
		o.svc.sessionPlane.PersistMemoryDelAll,
	)
	// 挂起提问一旦有终局（accept/decline/cancel/超时自动取消/会话取消），发布收敛事件
	// （session.elicitation_resolved），前端据此收敛提问卡——服务端事件是唯一事实。
	// `action` 由域给出，**不是**回给底座的那个 outcome：超时那一支要发 `pending`（卡片保留可答），
	// 而回给底座的仍是 `cancel`（那一轮的 ACP 调用必须有人应答）。这个不对称是 ADR-11 的核心。
	o.elic.SetOnResolved(func(sessionID, id, action, reason string) {
		o.svc.sessionPlane.Publish(session.BusElicitationResolved, sessionID, map[string]any{
			"session_id":     sessionID,
			"elicitation_id": id,
			"action":         action,
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
	rows, err := o.svc.sessionPlane.MemoryRows(ctx)
	if err != nil {
		return err
	}
	items := make([]interaction.MemoryItem, 0, len(rows))
	for _, r := range rows {
		// 恢复时把**授权形态**一并带回来（v28）：不带就等于重启后丢掉了"当初那句话是在哪次
		// 具体调用上说的"。旧库里两列是默认值（`session` / 空串），所以老数据天然兼容。
		items = append(items, interaction.NewMemoryItem(
			r.SessionID, r.Title, r.Kind, r.Decision, r.CreatedAt).
			WithGrantShape(r.Scope, r.GrantInputDigest))
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
func (o *Orchestrator) SetRunTimeout(d time.Duration) { o.runner.SetRunTimeout(d) }

// SetArtifactExtractor 装配产物自动抽取端口（nil = 关闭）。用例层构造完成后由装配根注入
// （抽取实现需要 store/attachStore/artifactSvc，晚于编排器构造，见 assembly.BuildServices）。
func (o *Orchestrator) SetArtifactExtractor(ex session.ArtifactExtractor) {
	o.runner.SetArtifactExtractor(ex)
}

// SetSettleTimeout 覆盖取消后的结算等待窗口（0 = 默认 10s）。
//
// 生产装配不调用它；测试用它把窗口压到毫秒级，避免每个取消用例都等满 10s。
func (o *Orchestrator) SetSettleTimeout(d time.Duration) { o.runner.SetSettleTimeout(d) }

// SetCommandPolicy 装配期登记命令门禁规则（nil = 未启用）。规则表由 app 从
// `<MATE_HOME>/command_policy.json` 加载并**加载期校验**；这里只持有，不再校验。
func (o *Orchestrator) SetCommandPolicy(rules []interaction.PrefixRule) { o.commandPolicy = rules }

// SetHandoffMaxChars 设置 bounded handoff memo 字符预算（0 = 关闭 memo 兜底）。见 ensureSession。
func (o *Orchestrator) SetHandoffMaxChars(n int) { o.life.SetHandoffMaxChars(n) }

// session 按 ID 取活跃会话（连接存活期内）。查找不存在（如进程重启后）按未找到处理。
func (o *Orchestrator) session(id string) *session.ActiveSession {
	return o.handles.Get(id)
}

// Close 关闭全部连接并清理（daemon 退出/测试收尾）。
//
// 连接的关闭（含空闲回收停止、进程登记摘除、进程状态回落、会话收尾）整体归执行面：
// 这里只剩"叫它关" + transcript 收尾。会话收尾仍回到本层的 `OnConnDropped`（`DropSink`）。
func (o *Orchestrator) Close() {
	o.pool.CloseAll()
	o.svc.sessionPlane.CloseTranscripts()
}
