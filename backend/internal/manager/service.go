package manager

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/vault"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// AgentRec 是进程内 Agent 定义（P0：Agent 不再绑定单一 runtime，路由候选见 Runtimes）。
type AgentRec struct {
	ID       string
	Name     string
	Runtimes []string // 路由候选 runtime（P0 取首候选；P2 升级为路由引擎）
	Provider string   // provider 档案 id（P2 provider 注入面）；空 = 默认
	// Mcps 启用的 MCP server id 白名单（P2 §5.2）。nil = 未声明（默认：注册表全量按
	// runtime 传输过滤注入）；非 nil（可为空数组）= 白名单（空 = 本 agent 禁用 MCP 注入）。
	Mcps *[]string
	// RoutingPolicy 路由策略声明（P2 §7）：first_available | capability_routed；空 = 默认。
	RoutingPolicy string
	// PermissionPolicy 默认权限策略（M2 #7）：nil = 全 ask（历史行为）。
	// 注：B5 起该策略还会投影到底座原生权限开关（见 native_permission.go）。
	PermissionPolicy *PermissionPolicy
	State            string
}

// pickRuntime 返回 Agent 的路由首选 runtime（P0 取首候选；P2 升级为路由引擎）。
func (a *AgentRec) pickRuntime() string {
	if len(a.Runtimes) == 0 {
		return ""
	}
	return a.Runtimes[0]
}

// ConversationRec 是进程内会话终态记录（内存镜像；sqlite 模式下与 conversations 行同源）。
// P0 起不再记录 runtime 身份；RuntimeHint 仅为最近/首选 runtime 的执行提示（非身份，用户不可见）。
type ConversationRec struct {
	ID      string
	AgentID string
	// Cwd 会话工作空间（canonical 绝对路径）。它同时是 ACP session/new 的 cwd 与
	// 原生权限 workspace-write 的边界（见 workspace.go）。
	Cwd string
	// WorkspaceTemporary 该工作空间是否由平台代建（未指定 cwd 时在 workspaceDir 下建）。
	// 落库 + 写 transcript header：**权威标志**，前端与离线回放都不得从路径形状反推。
	WorkspaceTemporary bool
	Provider           string // provider 档案 id（会话级模型选择；空 = 跟随 agent）
	Model              string // 会话级模型 id（空 = 底座默认）
	RuntimeHint        string // 最近/首选 runtime（执行提示，非身份）
	// Generation 会话**执行代际**：每次重建执行上下文（连接断开后重连 / 会话被重开）自增。
	// §6 排队输入据此判断「排队时依赖的那一轮上下文还在不在」——不等即作废（见 inputqueue.go）。
	// 只在内存：重启本身就让所有排队行作废，没有跨重启比对的语义。
	Generation int64
	// ProviderSessionID 原生底座会话 id（P3 双 id；conversation id = 平台 uuid，本字段存
	// 原生 id 供 load/resume 恢复承载。memory 模式下仅在活跃集合期间可用）。
	ProviderSessionID string
	// Fingerprint 该会话钉住的**能力指纹**（代际；空 = 未声明能力选择）。
	// 恢复路径据此回到同一代际。落库列 `capability_fingerprint` 与显式能力选择一起在 M3 补齐
	// （M1 只有本字段与代际键，值恒为空 → connKey 与代际机制之前逐字相同，见 connection.go）。
	Fingerprint string
	Status      string
	Title       string // 会话标题（首条用户消息截断生成；可被 PATCH 覆盖）
	Pinned            bool   // 置顶（侧栏/管理面用）
	Archived          bool   // 归档（软删除；会话列表默认不展示）
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表；agent 为每 runtime 的默认播种；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	reg  *runtime.Registry
	bus  *eventbus.Bus
	orch *Orchestrator
	tx   *transcriptRecorder // 会话 wire transcript（nil = 未启用，见 SetTranscriptDir）
	st   *store.Store        // SQLite 持久层（nil = 未启用；启用后见 EnableStore）

	agentDir  string                  // Agent definitions 目录（P2；空 = 不落盘，仅内存）
	providers map[string]*providerDef // provider 档案（P2；同一目录 providers.json 持久化）
	// workspaceDir 代建工作空间的根目录（未指定 cwd 时在 <root>/<会话id>/ 建）。
	// 空 = 关闭代建（未指定 cwd 直接报 workspace_required，不回落 os.Getwd()）。
	// 见 SetWorkspacePolicy / ResolveWorkspace。
	workspaceDir string
	// workspaceRoots 允许作为工作空间的根白名单（FORGE_WORKSPACE_ROOTS；空 = 不限制）。
	// 非空时显式 cwd 与目录浏览都被收在内，浏览也不允许越过根向上。
	// 这里存的是**显式配置**的那一份；对外生效名单还要并上代建根（effectiveWorkspaceRootsLocked）。
	// 见 SetWorkspacePolicy / withinRoots。
	workspaceRoots []string
	// workspaceRootsStore 白名单的持久化端口（面板 PUT 时写回配置目录；nil = 只读）。
	// 端口而非直接实现：manager 不碰配置文件，优先级/锁定判定由 infra 报告（见 workspace_settings.go）。
	workspaceRootsStore WorkspaceRootsStore
	// currentAgent 「当前 Agent」的显式选择（P2 起可切换；空 = 未设置，走回落链）。
	// 落盘 <AgentDir>/default.json（见 default_agent.go）；读写一律持 s.mu。
	currentAgent string
	catalog      *catalogStore        // 运行时派生模型目录缓存 + 拒绝值反馈（见 model_catalog.go）
	caps         *capability.Registry // 能力数据面（P1：Skill/MCP/Tool 三表；LoadCapabilityDefinitions 装载）
	// capabilityDir 能力数据面目录（启停写回定义文件用；LoadCapabilityDefinitions 登记）。
	capabilityDir string
	// skillTargets skill 物化目标（app 启动重校时登记；启停后立刻重校同一组目标）。
	skillProjections []SkillProjection

	mu            sync.Mutex
	agents        map[string]*AgentRec
	conversations map[string]*ConversationRec
	// inputs 会话待投递输入队列（**仅 memory 模式**；sqlite 模式的事实源是 inputs 表）。
	inputs    map[string][]QueuedInput
	ver       map[string]int64 // 每 agg(conversation) 的版本计数（域事件递增）
	restoring bool             // RestoreFromStore 进行中：registerConversation 跳过 DB 回写

	// MCP live 探测状态（P2 §5.2）：独立锁——探测可能耗时，不阻塞会话面。
	mcpMu           sync.Mutex
	mcpProbes       map[string]mcpProbeStatus
	mcpProbeTimeout time.Duration

	// A3 诊断：每 agent 最近一次失败（含 stderr 尾巴）。独立锁，避免与 s.mu 交叉。
	diagMu   sync.Mutex
	lastErrs map[string]agentLastErr
	// scopeNotes 每 agent 最近一条「归属歧义」留痕（elicitation 无法归属会话时写入）。
	scopeNotes map[string]scopeNote
	// coalescer ③ 文本增量合并器（NewService 构造时创建；见 chunk_coalescer.go）。
	coalescer *chunkCoalescer
	// vaultStore §5 provider 密钥加密落盘句柄（惰性创建 + 缓存；见 providers.go providerVault）。
	vaultStore *vault.Store
	vaultTried bool
	vaultErr   error

	// B5 原生权限映射开关（FORGE_NATIVE_PERMISSION=off 关闭；默认开启）。
	nativePermMu      sync.Mutex
	nativePermEnabled bool
	nativePermSet     bool
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
func NewService(reg *runtime.Registry, bus *eventbus.Bus) *Service {
	s := &Service{
		reg:             reg,
		bus:             bus,
		agents:          make(map[string]*AgentRec),
		conversations:   make(map[string]*ConversationRec),
		inputs:          make(map[string][]QueuedInput),
		ver:             make(map[string]int64),
		providers:       make(map[string]*providerDef),
		catalog:         newCatalogStore(),
		caps:            capability.NewRegistry(),
		mcpProbes:       make(map[string]mcpProbeStatus),
		mcpProbeTimeout: defaultMCPProbeTimeout,
		lastErrs:        make(map[string]agentLastErr),
		scopeNotes:      make(map[string]scopeNote),
		// 默认开启（nil 策略仍不注入 → 未声明策略的 agent 行为不变）。
		nativePermEnabled: true,
	}
	// ③ 流式增量合并：flushFn 直连 publishRaw（绕过合并入口，避免自递归）。
	s.coalescer = newChunkCoalescer(func(typ, conversationID string, payload any) {
		s.publishRaw(typ, conversationID, payload)
	}, slog.Default())
	// 播种内置 Agent：dsh（默认）/ opencode，仅当其 runtime 已注册时播种。
	for _, b := range builtinAgents {
		if _, ok := reg.Get(b.runtime); !ok {
			continue
		}
		s.agents[b.id] = &AgentRec{
			ID:       b.id,
			Name:     b.name,
			Runtimes: []string{b.runtime},
			State:    AgentStateStopped,
		}
	}
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
func (s *Service) beginTranscript(conversationID, agentID, runtimeID, cwd string, workspaceTemporary bool) {
	if s.tx == nil {
		return
	}
	s.tx.begin(conversationID, agentID, runtimeID, cwd, workspaceTemporary)
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

// registerConversation 登记一个新会话（已存在则覆盖）；未提供的时间落为当前 UTC。
// loadTranscripts 回灌时保留原 created_at/updated_at。
// sqlite 模式下（st != nil 且非恢复过程）同步写库。
func (s *Service) registerConversation(rec *ConversationRec) {
	s.mu.Lock()
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	s.conversations[rec.ID] = rec
	restoring := s.restoring
	s.mu.Unlock()
	if !restoring {
		s.persistConversation(rec)
	}
}

// bumpGeneration 会话执行代际自增（执行上下文重建时调用；见 ConversationRec.Generation）。
func (s *Service) bumpGeneration(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.conversations[id]
	if !ok {
		return 0
	}
	rec.Generation++
	return rec.Generation
}

// conversationGeneration 读会话当前执行代际（会话不存在 → 0，此时任何排队项都不匹配 → 作废）。
func (s *Service) conversationGeneration(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.conversations[id]
	if !ok {
		return 0
	}
	return rec.Generation
}

// conversationRec 返回某会话的进程内记录副本（断点恢复/内部查询用；不存在返回 nil）。
func (s *Service) conversationRec(id string) *ConversationRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.conversations[id]
	if !ok {
		return nil
	}
	cp := *rec
	return &cp
}

// markConversation 更新会话终态（sqlite 模式下同步写库 status）。
func (s *Service) markConversation(id, status string) {
	s.mu.Lock()
	if r, ok := s.conversations[id]; ok {
		r.Status = status
		r.UpdatedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	if s.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.st.UpdateConversationMeta(ctx, id, store.ConversationMetaPatch{Status: &status}); err != nil {
			slog.Default().Warn("store: mark conversation failed", "conversation", id, "err", err)
		}
	}
}

// attachOrchestrator 由 NewOrchestrator 回挂；Service 的会话动作委托给它。
func (s *Service) attachOrchestrator(o *Orchestrator) { s.orch = o }

// hasConversation 判断会话是否在本进程登记。
func (s *Service) hasConversation(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.conversations[id]
	return ok
}
