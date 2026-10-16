package manager

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表（构建期固定，见 runtime_binding.go）；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	reg  *runtime.Registry
	bus  *eventbus.Bus
	orch *Orchestrator
	st   *store.Store // SQLite 持久层（nil = 未启用；启用后见 EnableStore）
	// runtimeCfgDigests 各 runtime 已物化的模型配置指纹（T5）。它**并进代际键**
	// （见 connection.go 的 generationKey）：不并进键，暖进程会继续用旧清单而界面显示"已保存"。
	runtimeCfgDigests map[string]string
	// runtimeSyncOutcomes 各 runtime **最后一次同步的结论**（v12：读面直接读它，不在读路径上重跑同步）。
	runtimeSyncOutcomes map[string]resource.Outcome

	// providerDir 定义目录（v19 起只装 `providers.json`；空 = 不落盘，仅内存）。
	// v20 起它与凭据面无关：密钥在库里，托管文档锚在 `MATE_HOME`（见 SetProviderDir）。
	providerDir string
	// planner 工作空间域（起始点偏好策略：代建根 + 生效名单）。P2b 起策略字段归它自持锁；
	// `Service` 上**不再复刻**一份（两份 = 两个事实源，表现是面板改了名单、校验却按旧的走）。
	planner *workspace.Plane
	catalog *catalogStore        // 运行时派生模型目录缓存 + 拒绝值反馈（见 model_catalog.go）
	caps    *capability.Registry // 能力数据面（P1：Skill/MCP/Tool 三表；LoadCapabilityDefinitions 装载）
	// capabilityDir 能力定义目录。**语义按持久层模式分叉**（M6-2）：
	//   - memory 模式：装载源 + 启停写回位置（文件是唯一持久形态）；
	//   - sqlite 模式：**只作导入/导出目录**——事实源是 DB（v8 的 skills/mcp_servers），
	//     装载走 LoadCapabilityDefinitionsFromStore，写面走 store 的 CAS（M6-3）。
	// 两种模式都由 LoadCapabilityDefinitions / LoadCapabilityDefinitionsFromStore 登记。
	// automationPlane 自动化域（定义注册表 + 排期 + 调度循环；P2b 起状态归它自持锁）。
	automationPlane *automation.Plane
	// skillPlane 技能平面（读面 + 重校；与 mcp 平面共用同一份 caps 注册表）。
	skillPlane *skill.Plane
	// mcpSecrets 连接器平面的密钥解析面（引用 → 明文；与 creds 共用同一份凭据机制）。
	mcpSecrets *mcp.Secrets
	// mcpPlane 连接器平面（实时调用 + 探测；与 skill 平面共用同一份 caps 注册表）。
	// 探测状态（预算 + 最近一次探测结果）自持在平面内，不再挂在 Service 上。
	mcpPlane *mcp.Plane
	// sessionPlane 会话域（自持一把只保护域内字段的锁；P3b 分批搬入）。
	sessionPlane *session.Plane
	// providerPlane 供应商域（内存档案表 + 载体 + 凭据面；自持一把只保护域内字段的锁）。
	providerPlane *provider.Plane
	// picker 原生目录选择器（宿主事实判定 + 命令配方 + spawn）。自持"监听地址"一个事实。
	picker *workspace.Picker

	mu   sync.Mutex
	defs *definition.Mechanism
	// creds 凭据机制的状态（四层凭据面 + 凭据 cwd；见 mechanism/credential）。
	creds *credential.Store
	// runtimePlane 执行面（P3a-2 分批搬入）：本批装"本进程认的那个底座"的绑定，
	// 诊断组装面随后一批跟进。它自持锁（`bindingLocked` 那类"须持 s.mu"的约定随之消失）。
	runtimePlane *runtimeconn.Plane
	autoVer      map[string]int64 // 自动化事件的版本计数（`automation:<id>` 聚合）；会话侧那张表在 domain/session

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
	coalescer *session.Coalescer

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
	// 工作空间域：策略状态在它自己的锁下（P2b 起不再是 Service 的字段）。
	s.planner = workspace.NewPlane()
	// 自动化域：定义注册表与调度态都在它自己的锁下（P2b 起不再是 Service 的字段）。
	// 它需要两样家当（库句柄 + 宿主端口），分别在 `attachStore` 与这里接上——**早一行是 nil**，
	// 而表现是"该走库的走了内存"或"触发时找不到宿主"（静默）。
	s.automationPlane = automation.NewPlane()
	s.automationPlane.SetHost(automationHostAdapter{svc: s})
	// 会话事实端口（P2b）：workspace 域不拥有会话，只消费这里的两个动作。
	s.planner.SetSessionSource(s)
	s.providerPlane = provider.NewPlane(reg, s.creds)
	// 会话域：本批只装库句柄与用量端口（其余端口随 P3b-1b/2 接上）。
	s.sessionPlane = session.NewPlane()
	// 会话读面的端口：归属展示（工作空间登记面）P2b 起也在域内（`Plane.WorkspaceDisplayOf`）。
	// 能力选定回显与指纹装配点 P3c 起同样在域内，不再需要回显端口。
	s.sessionPlane.SetWorkspaceDisplay(s.planner.WorkspaceDisplayOf)
	// 能力事实提供者（P3c）：指纹的装配点在域，事实（注册表 / skill 投影 / MCP 注入计划）在这边。
	// **必须紧跟 `caps`/`reg` 的就位**：早一行登记会让指纹少算一段——静默，且只在"代际该不该切"
	// 这种慢变量上现形。
	s.sessionPlane.SetCapabilityFacts(s)
	s.sessionPlane.SetCapabilityLookup(s)
	// 会话域的其余端口：事件总线（恢复时按库位次注回）、模型行↔底座运行时值的换算、
	// 以及恢复期"作废重启前排队输入"的钩子（**临时**：输入队列 P3b-2 搬进来后就地调用）。
	s.sessionPlane.SetBus(bus)
	s.sessionPlane.SetModelValueSource(func(runtimeID, providerID, model string) (string, error) {
		return modelValueFor(s.reg, runtimeID, providerID, model)
	})
	s.sessionPlane.SetStaleInputsOnRestore(s.staleQueuedInputsOnRestore)
	// 两个端口都读会话/模型面（编排侧），在装配期接上；`demo`/`migrate` 这类没有编排器的
	// 场景下 `effect` 恒 nil（回执为 nil，与拆分前逐字同义）。
	s.providerPlane.SetEffect(s.providerEffect)
	s.providerPlane.SetDefaultModel(func(ctx context.Context) (string, bool) {
		row, ok, err := s.defaultModelRow(ctx)
		if err != nil || !ok {
			return "", false
		}
		return strings.TrimSpace(row.ProviderID), true
	})

	// 网关默认开启（config 的 auto）。放在构造末尾而不是结构体字面量里：atomic.Bool 没法写成
	// 复合字面量字段，且这里"默认值"的意图要一眼可见（off 由装配期显式关）。
	s.mcpGatewayEnabled.Store(true)
	// ③ 流式增量合并：flushFn 直连 publishRaw（绕过合并入口，避免自递归）。
	s.coalescer = session.NewCoalescer(func(typ, sessionID string, payload any) {
		s.publishRaw(typ, sessionID, payload)
	})
	// **必须紧跟创建点**：会话域持有的是合并器本身，早一行它还是 nil——表现是"文本增量不再
	// 合并"（每条增量各占一个位次），而编译、vet、构造期都看不出来（§3.1.38 的同一课）。
	s.sessionPlane.SetCoalescer(s.coalescer)
	// 绑定构建期选定的那个底座（v19：单实例）。`specOK=false`（未注册）不是这里报错——
	// 装配期的 `runtime.ProductRegistry` 已经 fail-closed，这里只是把事实记下来供诊断如实外露。
	spec, specOK := reg.Get(runtime.SelectedRuntime())
	s.runtimePlane = runtimeconn.NewPlane(reg, spec, specOK)
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
//
// P3b-2b：recorder 与装载/检索面整体落 `domain/session`，这一层只剩**装配转发**
// （域自己持有目录、文件句柄与启动回读，manager 不再认识 transcript 的文件布局）。
//
// TODO(P4b): 删转发（装配侧直接依赖 session.Plane）
func (s *Service) SetTranscriptDir(dir string) { s.sessionPlane.SetTranscriptDir(dir) }

// beginTranscript 为会话写 transcript header（首行）；失败只告警不阻断（域内实现自己判空）。
//
// TODO(P4b): 删转发（写面直接依赖 session.Plane）
func (s *Service) beginTranscript(sessionID, runtimeID, cwd string, workspaceTemporary bool) {
	s.sessionPlane.BeginTranscript(sessionID, runtimeID, cwd, workspaceTemporary)
}

// closeTranscripts 关闭全部 transcript 文件（daemon 退出/orchestrator.Close 收尾）。
// 先落掉合并器里的尾部文本增量，再关文件——否则长回复的最后一段会丢。
//
// TODO(P4b): 删转发（装配侧直接依赖 session.Plane）
func (s *Service) closeTranscripts() { s.sessionPlane.CloseTranscripts() }

// attachOrchestrator 由 NewOrchestrator 回挂；Service 的会话动作委托给它。
func (s *Service) attachOrchestrator(o *Orchestrator) {
	s.orch = o
	// 会话域的用量端口：读的是执行面内存里的"每会话最近一次上报"（`LatestUsage`）。
	if o != nil {
		s.sessionPlane.SetUsageSource(o.LatestUsage)
	}
}
