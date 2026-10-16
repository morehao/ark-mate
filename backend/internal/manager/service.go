package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// SessionRec 是进程内会话终态记录（内存镜像；sqlite 模式下与 sessions 行同源）。
//
// **身份只有 runtime**（v19）：v17 时这里写着"P0 起不再记录 runtime 身份"，本轮反过来——
// 底座由构建期固定、用户不可自定义，所以 `runtime_id` 就是唯一身份事实（它同时是 DB 列
// `sessions.runtime_id`、事件 payload 的 `runtime`、诊断面的 `runtime`）。v19 之前那半个
// `agent_id` 与它指向同一个东西，收成一个。
type SessionRec struct {
	ID string
	// Cwd 会话工作空间（canonical 绝对路径）。它同时是 ACP session/new 的 cwd 与
	// 原生权限 workspace-write 的边界（见 workspace.go）。
	Cwd string
	// WorkspaceTemporary 该工作空间是否由**平台**给出（= 默认工作空间）。
	// 落库 + 写 transcript header：**权威标志**，前端与离线回放都不得从路径形状反推。
	WorkspaceTemporary bool
	// WorkspaceID 归属的工作空间登记 id（v14；'' = 未登记/登记已删 → 读面归默认工作空间）。
	//
	// 与 Cwd 的分工（设计 ADR-2，最容易踩的一处）：Cwd 是**执行事实**（底座 ACP session 的
	// cwd、原生权限 workspace-write 的边界、不可变），WorkspaceID 是**归属事实**（左栏分组）。
	// 两者不一致是允许的（工作空间登记被删、或目录后来不可用），执行**恒**用 Cwd。
	WorkspaceID string
	// ModelID 会话引用的**平台模型行 id**（`mdl_…`；空 = 底座默认）。
	//
	// v26 起语义是"存身份、运行时值现算"（定案「辰」，设计 §5.4）：库/内存里只留平台行 id，
	// 底座认的不透明值由 adapter 每次现算（`modelValueFor`）——路由键改名后旧会话照样活得下来。
	// 它**不是** `models.model`（那是发给上游的模型名，一个外部世界的字符串）。
	//
	// 这里曾有 `ProviderID`：v19 删 agent 面后它没有任何写入点（恒空），v26 删列——
	// 供应商由**模型行**给出（`models.provider_id`），再存一份就是第二事实源。
	ModelID string
	// RuntimeID 本会话的 runtime 身份（v17 起列 `runtime_id`；v19 起它是**唯一的**身份事实）。
	RuntimeID string
	// Generation 会话**执行代际**：每次重建执行上下文（连接断开后重连 / 会话被重开）自增。
	// §6 排队输入据此判断「排队时依赖的那一轮上下文还在不在」——不等即作废（见 inputqueue.go）。
	// 只在内存：重启本身就让所有排队行作废，没有跨重启比对的语义。
	Generation int64
	// RuntimeSessionID 原生底座会话 id（P3 双 id；session id = 平台 uuid，本字段存
	// 原生 id 供 load/resume 恢复承载，v17 前叫 ProviderSessionID / `provider_session_id`。
	// memory 模式下仅在活跃集合期间可用）。
	RuntimeSessionID string
	// Fingerprint 该会话钉住的**能力指纹**（代际；空 = 未声明能力选择）。
	// 恢复路径据此回到同一代际。M3-1 起落库列 `capability_fingerprint`（v7），
	// RestoreFromStore 原样读回——否则重启后指纹变空、会话悄悄换到默认能力集。
	Fingerprint string
	// Origin 会话来源（'' = 人工；'automation:<id>' = 该 automation 触发产生）。M4 写入，M3 只落列。
	Origin string
	// CapabilityJSON 创建时解析出的**能力选定明细**（JSON；'' = 未声明 → 全量按 runtime 过滤）。
	// 与 Fingerprint 的分工：指纹回答"是不是同一套"（比较/回收用），明细回答"到底是哪几个"
	// （回显、诊断、以及指纹算法变化后的一次性迁移判据）。
	CapabilityJSON string
	Status         string
	Title          string // 会话标题（首条用户消息截断生成；可被 PATCH 覆盖）
	Pinned         bool   // 置顶（侧栏/管理面用）
	Archived       bool   // 归档（软删除；会话列表默认不展示）
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表（构建期固定，见 runtime_binding.go）；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	reg  *runtime.Registry
	bus  *eventbus.Bus
	orch *Orchestrator
	tx   *transcriptRecorder // 会话 wire transcript（nil = 未启用，见 SetTranscriptDir）
	st   *store.Store        // SQLite 持久层（nil = 未启用；启用后见 EnableStore）
	// runtimeCfgDigests 各 runtime 已物化的模型配置指纹（T5）。它**并进代际键**
	// （见 connection.go 的 generationKey）：不并进键，暖进程会继续用旧清单而界面显示"已保存"。
	runtimeCfgDigests map[string]string
	// runtimeSyncOutcomes 各 runtime **最后一次同步的结论**（v12：读面直接读它，不在读路径上重跑同步）。
	runtimeSyncOutcomes map[string]resource.Outcome

	// providerDir 定义目录（v19 起只装 `providers.json`；空 = 不落盘，仅内存）。
	// v20 起它与凭据面无关：密钥在库里，托管文档锚在 `MATE_HOME`（见 SetProviderDir）。
	providerDir string
	providers   map[string]*providerDef // provider 档案（P2；同一目录 providers.json 持久化）
	// workspaceDir 代建工作空间的根目录（未指定 cwd 时在 <root>/<会话id>/ 建）。
	// 空 = 关闭代建（未指定 cwd 直接报 workspace_required，不回落 os.Getwd()）。
	// 见 SetWorkspacePolicy / ResolveWorkspace。
	workspaceDir string
	// workspaceRoots 允许作为工作空间的根白名单（MATE_WORKSPACE_ROOTS；空 = 不限制）。
	// 非空时显式 cwd 与目录浏览都被收在内，浏览也不允许越过根向上。
	// 这里存的是**显式配置**的那一份；对外生效名单还要并上代建根（effectiveWorkspaceRootsLocked）。
	// 见 SetWorkspacePolicy / withinRoots。
	workspaceRoots []string
	// workspaceRootsStore 白名单的持久化端口（面板 PUT 时写回配置目录；nil = 只读）。
	// 端口而非直接实现：manager 不碰配置文件，优先级/锁定判定由 infra 报告（见 workspace_settings.go）。
	workspaceRootsStore WorkspaceRootsStore
	catalog             *catalogStore        // 运行时派生模型目录缓存 + 拒绝值反馈（见 model_catalog.go）
	caps                *capability.Registry // 能力数据面（P1：Skill/MCP/Tool 三表；LoadCapabilityDefinitions 装载）
	// capabilityDir 能力定义目录。**语义按持久层模式分叉**（M6-2）：
	//   - memory 模式：装载源 + 启停写回位置（文件是唯一持久形态）；
	//   - sqlite 模式：**只作导入/导出目录**——事实源是 DB（v8 的 skills/mcp_servers），
	//     装载走 LoadCapabilityDefinitionsFromStore，写面走 store 的 CAS（M6-3）。
	// 两种模式都由 LoadCapabilityDefinitions / LoadCapabilityDefinitionsFromStore 登记。
	// automationDir / automations 自动化定义目录与注册表（M4-3；LoadAutomationDefinitions 登记）。
	// 运行态 overlay 写在定义文件里（last_fired_at/state/...），内存只留调度态（nextDue/startup）。
	automationDir string
	automations   map[string]*automationRec
	// automationStop 调度 goroutine 的停止函数（非 nil = 已启动；StartAutomationScheduler 幂等判据）。
	automationStop context.CancelFunc
	// automationDone 调度 goroutine 的退出信号（Stop 等它关闭再返回：停机后不该再有新触发起步）。
	automationDone chan struct{}
	// automationRunMu 把「认领判定 → 建会话 → 写回游标」串成临界区。
	//
	// 认领判定（`automation.Decide`）是纯函数，纯函数**不提供互斥**：tick 与
	// `POST /automations/:id/run` 若同时看到"这个 occurrence 还没认领过"，两边都会建会话——
	// 而 automation 的 prompt 常常有副作用（改代码、发通知），重复跑一次是不可接受的。
	// 临界区里唯一的慢动作是建会话（子进程握手），而 automation 触发本身稀疏，串行化代价可忽略。
	automationRunMu sync.Mutex
	// skillPlane 技能平面（读面 + 重校；与 mcp 平面共用同一份 caps 注册表）。
	skillPlane *skill.Plane
	// mcpSecrets 连接器平面的密钥解析面（引用 → 明文；与 creds 共用同一份凭据机制）。
	mcpSecrets *mcp.Secrets
	// mcpPlane 连接器平面（实时调用 + 探测；与 skill 平面共用同一份 caps 注册表）。
	// 探测状态（预算 + 最近一次探测结果）自持在平面内，不再挂在 Service 上。
	mcpPlane *mcp.Plane
	// picker 原生目录选择器（宿主事实判定 + 命令配方 + spawn）。自持"监听地址"一个事实。
	picker *workspace.Picker

	mu   sync.Mutex
	defs *definition.Mechanism
	// creds 凭据机制的状态（四层凭据面 + 凭据 cwd；见 mechanism/credential）。
	creds *credential.Store
	// binding 本进程认的那个底座（v19：单实例、构造期建好；见 runtime_binding.go）。
	binding  *runtimeBinding
	sessions map[string]*SessionRec
	// inputs 会话待投递输入队列（**仅 memory 模式**；sqlite 模式的事实源是 inputs 表）。
	inputs    map[string][]QueuedInput
	ver       map[string]int64 // 每 agg(session) 的版本计数（域事件递增）
	restoring bool             // RestoreFromStore 进行中：registerSession 跳过 DB 回写

	// A3 诊断：底座最近一次失败（含 stderr 尾巴）。独立锁，避免与 s.mu 交叉。
	// 单底座 ⇒ 一份记录（v19 之前是 per-agent 的 map）。
	diagMu    sync.Mutex
	lastErr   *runtimeLastErr
	ambiguous *ambiguityNote
	// memDefaultWorkspace memory 模式的**默认工作空间**（登记表不存在时的那一份）。
	//
	// 它不是"登记项的缓存"：默认工作空间是**平台代建**的东西（进程没起来时它是库里的第一行；
	// 库不存在时它就该由装配期现建一个），而登记表（用户显式产出的状态）在 memory 模式本来就
	// 无处可存。少了它，"未指定 cwd 就发一条消息"这条最普通的路在 `MATE_STORE=memory` 下
	// 直接报 `workspace_required` —— 用户什么都没做错。见 EnsureDefaultWorkspace。
	//
	// 由 s.mu 保护（与 sessions 同一把锁）。
	memDefaultWorkspace *model.WorkspaceEntity

	// coalescer ③ 文本增量合并器（NewService 构造时创建；见 chunk_coalescer.go）。
	coalescer *chunkCoalescer

	// defWarned 装载期"同一条定义问题只 WARN 一次"的记账（key = 域:id@revision）。
	// 装载会被反复调用（启动、导入、**网关每次取数**），不记账就会把一条坏定义刷成日志洪水，
	// 而洪水之后真问题也没人看。见 defWarnOnce。
	defWarned map[string]bool

	// B5 原生权限映射开关（MATE_NATIVE_PERMISSION=off 关闭；默认开启）。
	nativePermMu      sync.Mutex
	nativePermEnabled bool
	nativePermSet     bool

	// mcpGatewayEnabled 平台侧 MCP 网关开关（M7-3；MATE_MCP_GATEWAY=off 回退）。
	// 零值 false 会被 NewService 显式置成 true——"忘了设"不该等于"静默关掉网关"。
	mcpGatewayEnabled atomic.Bool
	mcpGatewayWarnMu  sync.Mutex
	mcpGatewayWarned  map[string]bool
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
func NewService(reg *runtime.Registry, bus *eventbus.Bus) *Service {
	s := &Service{
		reg:   reg,
		defs:  definition.New(),
		creds: credential.New(reg),
		bus:   bus,
		// 网关默认开启（config 的 auto）：显式在这里置位，避免 atomic.Bool 零值把默认关掉。
		mcpGatewayWarned: make(map[string]bool),
		sessions:         make(map[string]*SessionRec),
		inputs:           make(map[string][]QueuedInput),
		ver:              make(map[string]int64),
		providers:        make(map[string]*providerDef),
		catalog:          newCatalogStore(),
		caps:             capability.NewRegistry(),
		// 默认开启（nil 策略仍不注入 → 未声明策略的 agent 行为不变）。
		nativePermEnabled: true,
	}
	// 技能平面与 RegisterSkill 共用**同一份**注册表（两份注册表 = 两个事实源）。
	s.skillPlane = skill.NewPlane(s.caps, s.defs)
	// 密钥解析面与 provider 凭据面共用**同一份** `credential.Store`（两份 = 同一份引用解析出两个结果）。
	s.mcpSecrets = mcp.NewSecrets(s.creds)
	// 连接器平面与 RegisterMcpServer 共用**同一份**注册表与**同一份**密钥解析面。
	s.mcpPlane = mcp.NewPlane(s.caps, s.defs, s.mcpSecrets)
	// 选择器无外部依赖（宿主事实每次调用现采），构造期建一个即可。
	s.picker = workspace.NewPicker()

	// 网关默认开启（config 的 auto）。放在构造末尾而不是结构体字面量里：atomic.Bool 没法写成
	// 复合字面量字段，且这里"默认值"的意图要一眼可见（off 由装配期显式关）。
	s.mcpGatewayEnabled.Store(true)
	// ③ 流式增量合并：flushFn 直连 publishRaw（绕过合并入口，避免自递归）。
	s.coalescer = newChunkCoalescer(func(typ, sessionID string, payload any) {
		s.publishRaw(typ, sessionID, payload)
	})
	// 绑定构建期选定的那个底座（v19：单实例）。`specOK=false`（未注册）不是这里报错——
	// 装配期的 `runtime.ProductRegistry` 已经 fail-closed，这里只是把事实记下来供诊断如实外露。
	spec, specOK := reg.Get(runtime.SelectedRuntime())
	s.binding = newRuntimeBinding(spec, specOK)
	return s
}

// SetBus 注入事件总线（Orchestrator 构造时调用；与 handler.New 共用同一 bus）。
func (s *Service) SetBus(bus *eventbus.Bus) { s.bus = bus }

// SetNativePermission 开关 B5 原生权限映射（false = 不向底座注入权限配置）。
func (s *Service) SetNativePermission(enabled bool) {
	s.nativePermMu.Lock()
	defer s.nativePermMu.Unlock()
	s.nativePermEnabled = enabled
	s.nativePermSet = true
}

// NativePermissionEnabled 读取 B5 开关（默认 true）。
func (s *Service) NativePermissionEnabled() bool {
	s.nativePermMu.Lock()
	defer s.nativePermMu.Unlock()
	return s.nativePermEnabled
}

// SetTranscriptDir 启用会话 wire transcript 落盘（目录不存在会自动创建；
// dir 为空 = 禁用）。须在任何会话开始前调用（serve/demo 装配时）。
// 启用后立即回读已落盘 transcript，重建会话列表与历史（会话重启存活）。
func (s *Service) SetTranscriptDir(dir string) {
	if dir == "" {
		s.tx = nil
		return
	}
	s.tx = newTranscriptRecorder(dir)
	s.loadTranscripts()
}

// beginTranscript 为会话写 transcript header（首行）；失败只告警不阻断。
func (s *Service) beginTranscript(sessionID, runtimeID, cwd string, workspaceTemporary bool) {
	if s.tx == nil {
		return
	}
	s.tx.begin(sessionID, runtimeID, cwd, workspaceTemporary)
}

// closeTranscripts 关闭全部 transcript 文件（daemon 退出/orchestrator.Close 收尾）。
// 先落掉合并器里的尾部文本增量，再关文件——否则长回复的最后一段会丢。
func (s *Service) closeTranscripts() {
	s.flushChunks()
	if s.tx == nil {
		return
	}
	s.tx.closeAll()
	s.tx = nil
}

// registerSession 登记一个新会话（已存在则覆盖）；未提供的时间落为当前 UTC。
// loadTranscripts 回灌时保留原 created_at/updated_at。
// sqlite 模式下（st != nil 且非恢复过程）同步写库。
func (s *Service) registerSession(rec *SessionRec) {
	s.mu.Lock()
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	s.sessions[rec.ID] = rec
	restoring := s.restoring
	s.mu.Unlock()
	if !restoring {
		s.persistSession(rec)
	}
}

// bumpGeneration 会话执行代际自增（执行上下文重建时调用；见 SessionRec.Generation）。
func (s *Service) bumpGeneration(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[id]
	if !ok {
		return 0
	}
	rec.Generation++
	return rec.Generation
}

// sessionGeneration 读会话当前执行代际（会话不存在 → 0，此时任何排队项都不匹配 → 作废）。
func (s *Service) sessionGeneration(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[id]
	if !ok {
		return 0
	}
	return rec.Generation
}

// sessionRec 返回某会话的进程内记录副本（断点恢复/内部查询用；不存在返回 nil）。
func (s *Service) sessionRec(id string) *SessionRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[id]
	if !ok {
		return nil
	}
	cp := *rec
	return &cp
}

// markSession 更新会话终态（sqlite 模式下同步写库 status）。
func (s *Service) markSession(id, status string) {
	s.mu.Lock()
	if r, ok := s.sessions[id]; ok {
		r.Status = status
		r.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	if s.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateSessionMeta(ctx, id, model.SessionMetaPatch{Status: &status}); err != nil {
			glog.Warnw(context.Background(), "store: mark session failed", "session", id, "err", err)
		}
	}
}

// attachOrchestrator 由 NewOrchestrator 回挂；Service 的会话动作委托给它。
func (s *Service) attachOrchestrator(o *Orchestrator) { s.orch = o }

// hasSession 判断会话是否在本进程登记。
func (s *Service) hasSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}
