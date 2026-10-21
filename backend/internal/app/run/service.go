package run

import (
	"context"
	"strings"
	"sync"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/contract/dtosession"
	"github.com/morehao/ark-mate/internal/runtime"
)

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表（构建期固定，见 runtime_binding.go）；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	bus  *eventbus.Bus
	orch *Orchestrator
	// facts 本次装配的领域平面**按面端口**（阶段 0.2）：本层不再认识平面容器（`bootstrap/assembly/planes.Planes`）。
	// 装配根从具体平面上折出来交进（`WithPlaneFacts`），测试夹具构造同一套。
	facts PlaneFacts
	// reg 构建期选定的底座注册表（L1 值对象）：`Get` / `NativePermission` / `McpTransportsFor`。
	reg *runtime.Registry
	// resourceStore 资源同步面的窄库口（`EnableStore` 注入；nil = 未接库）。
	resourceStore ResourceStore
	// sessionUC 自动化宿主需要的两条会话写面能力（P5 起用例在 `svcsession`，
	// 由装配根在它造好之后注入；nil = 未装配，宿主动作带码失败而不是 panic）。
	sessionUC sessionUseCase

	// sessionPlane 会话域（自持一把只保护域内字段的锁）。
	sessionPlane *session.Plane
	// runtimePlane 执行面（P3a-2 分批搬入）：本进程认的那个底座的绑定与诊断状态。
	//
	// 阶段 0.4：字段是**端口**（`session.RuntimePlane`），具体实现留在 `adapters/outbound/runtimeconn`。
	// 本层**不自造**执行面：装配根注入；未注入时退到 no-op（见 `ports_nop.go`）。
	runtimePlane session.RuntimePlane
	// runtimePool 连接池（P4a 正文第五十四刀搬进执行面）：连接表、代际上限与空闲回收。
	//
	// 阶段 0.4：字段是**端口**（`session.ConnPool`）。取连接与生命周期都在端口上；
	// env / 凭据 / 配置指纹的来源登记是**装配接线**（`bootstrap/assembly`），不在本层。
	runtimePool session.ConnPool

	// coalescer ③ 文本增量合并器（NewService 构造时创建；见 chunk_coalescer.go）。
	coalescer *session.Coalescer

	// mcpGatewayWarned 网关条目组装失败"逐 key 只 WARN 一次"的记账（纯日志节流，
	// 不是网关开关本身——开关在平面容器（`bootstrap/assembly/planes.Planes`），那是进程级配置事实）。
	mcpGatewayWarnMu sync.Mutex
	mcpGatewayWarned map[string]bool
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
// Option 装配期的可选注入。
//
// **为什么是"可选"而不是必填参数**：`NewService` 有 130+ 处调用点（几乎全在 manager 自己的
// 测试里，它们只需要"一个能跑的 Service"），而**装配层需要的是"同一个执行面实例"**
// （它要把这个实例也交给 API 面：P4a 起 `runtime.New` 由 `ctrruntime` 在构造函数里调，
// 用的就是这一份执行面）。必填参数会让 130+ 处跟着改，
// 换来的信息量与这里一样。P5a 的 `registry` 装配表会取代这个缝。
type Option func(*Service)

// WithRuntimePlane 用装配层提供的执行面（而不是本函数自己造一个）。
//
// **必须与 `runtime.New` 拿到同一个实例**（P4a 起那个调用在 `ctrruntime` 里）：
// 两个平面各自记账时，"诊断说 stopped、
// 连接却活着"这种分叉会立刻发生（`runtimeconn.Plane` 的进程状态是它自己的字段）。
func WithRuntimePlane(pl session.RuntimePlane) Option {
	return func(s *Service) { s.runtimePlane = pl }
}

// sessionUseCase 自动化宿主需要的两条会话写面能力（**只声明用到的那两条**）。
//
// 它不叫 `session.SessionSvc`：执行面不该 import 用例层；装配根把用例实例交进来即可。
type sessionUseCase interface {
	CreateSession(ctx context.Context, req dtosession.SessionCreateReq) (*dtosession.SessionCreateResp, error)
	CancelRun(ctx context.Context, id string) error
}

// SetSessionUseCase 装配根在会话用例造好之后注入（自动化的 `Host` 端口要用）。
func (s *Service) SetSessionUseCase(u sessionUseCase) { s.sessionUC = u }

// WithConnPool 用装配层提供的连接池（与 `WithRuntimePlane` 是**同一份执行面**的两半）。
//
// 两半必须一起注：池与平面各自记账会让"诊断说 stopped、连接却活着"立刻发生。
func WithConnPool(pool session.ConnPool) Option {
	return func(s *Service) { s.runtimePool = pool }
}

// WithPlaneFacts 使用装配层从领域平面上折出来的**按面端口**（阶段 0.2）。
//
// 不传时各面为 nil：读面走缺省语义、写面跳过（纯列表/纯状态路径不需要平面）。
// 装配根传的是**同一份实例**折出来的端口——两份平面 = 两个事实源。
func WithPlaneFacts(f PlaneFacts) Option { return func(s *Service) { s.facts = f } }

func NewService(reg *runtime.Registry, bus *eventbus.Bus, opts ...Option) *Service {
	s := &Service{bus: bus}
	// 可选注入**先行**：`WithPlanes` 决定的实例要在下面被引用，晚一步就会造出第二份平面。
	for _, opt := range opts {
		opt(s)
	}
	s.reg = reg
	// 模型目录取样的三个窄端口（P5a）：活会话 / 历史会话 / 连接期事实。
	// **必须紧跟平面创建点**：早一行它们还是 nil，表现是目录恒空且说明文案说"没有执行面"。
	s.writeProviderSources()

	// 自动化域需要宿主端口（建会话/取消轮次）；它是编排面的事实，装配层造不了——
	// 与库句柄一样，漏接的表现是"触发时找不到宿主"（静默）。
	if s.facts.Automation != nil {
		s.facts.Automation.SetHost(automationHostAdapter{svc: s})
	}
	// 会话事实端口（P2b）：workspace 域不拥有会话，只消费这里的两个动作。
	if s.facts.Workspaces != nil {
		s.facts.Workspaces.SetSessionSource(s)
	}
	// 会话域：本批只装库句柄与用量端口（其余端口随 P3b-1b/2 接上）。
	s.sessionPlane = session.NewPlane()
	// 会话读面的端口：归属展示（工作空间登记面）P2b 起也在域内（`Plane.WorkspaceDisplayOf`）。
	// 能力选定回显与指纹装配点 P3c 起同样在域内，不再需要回显端口。
	if s.facts.Workspaces != nil {
		s.sessionPlane.SetWorkspaceDisplay(s.facts.Workspaces.WorkspaceDisplayOf)
	}
	// 能力事实提供者（P3c）：指纹的装配点在域，事实（注册表 / skill 投影 / MCP 注入计划）在这边。
	// **必须紧跟 `caps`/`reg` 的就位**：早一行登记会让指纹少算一段——静默，且只在"代际该不该切"
	// 这种慢变量上现形。
	s.sessionPlane.SetCapabilityFacts(s)
	s.sessionPlane.SetCapabilityLookup(s)
	// 会话域的其余端口：事件总线（恢复时按库位次注回）、模型行↔底座运行时值的换算、
	// 以及恢复期"作废重启前排队输入"的钩子（**临时**：输入队列 P3b-2 搬进来后就地调用）。
	s.sessionPlane.SetBus(bus)
	s.sessionPlane.SetModelValueSource(func(runtimeID, providerID, model string) (string, error) {
		return s.modelValueFor(runtimeID, providerID, model)
	})
	s.sessionPlane.SetStaleInputsOnRestore(s.sessionPlane.StaleQueuedInputsOnRestore)
	// 两个端口都读会话/模型面（编排侧），在装配期接上；`demo`/`migrate` 这类没有编排器的
	// 场景下 `effect` 恒 nil（回执为 nil，与拆分前逐字同义）。
	if s.facts.Providers != nil {
		s.facts.Providers.SetEffect(s.providerEffect)
	}
	if s.facts.Providers != nil {
		s.facts.Providers.SetDefaultModel(func(ctx context.Context) (string, bool) {
			row, ok, err := s.facts.Providers.DefaultModelRow(ctx)
			if err != nil || !ok {
				return "", false
			}
			return strings.TrimSpace(row.ProviderID), true
		})
	}

	// ③ 流式增量合并：flushFn 直连 publishRaw（绕过合并入口，避免自递归）。
	s.coalescer = session.NewCoalescer(func(typ, sessionID string, payload any) {
		s.sessionPlane.PublishRaw(typ, sessionID, payload)
	})
	// **必须紧跟创建点**：会话域持有的是合并器本身，早一行它还是 nil——表现是"文本增量不再
	// 合并"（每条增量各占一个位次），而编译、vet、构造期都看不出来（§3.1.38 的同一课）。
	s.sessionPlane.SetCoalescer(s.coalescer)
	// 执行面两半：装配根注入（`WithRuntimePlane` / `WithConnPool`）；未注入时退到 no-op ——
	// 本层**不再自造** `*runtimeconn.Plane/Pool`（那会让 L7 import L6 的具体适配器，
	// 阶段 0.6 的 deny 不成立）。纯列表/纯状态用例不需要执行面，no-op 让它们照常跑。
	if s.runtimePlane == nil {
		s.runtimePlane = nopRuntimePlane{}
	}
	if s.runtimePool == nil {
		s.runtimePool = nopConnPool{}
	}
	return s
}

// RuntimePool 连接池的事实出口（P4a 正文第五十四刀）。
//
// 装配期（`app` 的 serve）要用它设代际上限与空闲回收；这两个动作的实现只有
// `internal/adapters/outbound/runtimeconn` 那一份，manager 侧不再有一行转发（`SetGenerationLimit` /
// `StartIdleReaper` 已随本刀从 `Orchestrator` 删除）。
func (s *Service) RuntimePool() session.ConnPool { return s.runtimePool }

// SessionPlane 交会话域平面（**依赖事实**）：导出用例的事实来源（`system.NewSource`）
// 由 `ctrexport` 用它与 Store/Bus 一起在自己的构造函数里造（P4a 正文第八刀）。
func (s *Service) SessionPlane() *session.Plane { return s.sessionPlane }

// attachOrchestrator 由 NewOrchestrator 回挂；Service 的会话动作委托给它。
func (s *Service) attachOrchestrator(o *Orchestrator) {
	s.orch = o
	// 会话域的用量端口：读的是轮次状态机内存里的"每会话最近一次上报"（`Runner.LatestUsage`）。
	if o != nil {
		s.sessionPlane.SetUsageSource(o.LatestUsage)
		// 连接拆除的**应用**面：池只负责摘除与通知，会话/在途/诊断怎么收尾由编排侧决定
		// （`DropSink`）。接在这一刻而不是 `NewService`：编排器在此之前还不存在。
		//
		// 判空是**装配不完整**的显式出口：注入了非 `*runtimeconn.Plane` 的平面时本层建不出池
		// （端口没有构造能力），装配层必须两半一起给；这里不 panic，让诊断在别处如实报。
		if s.runtimePool != nil {
			s.runtimePool.SetDropSink(o)
		}
	}
}

// writeProviderSources 三个目录取样端口的一次性接线（阶段 0.2：Providers 面可缺省）。
func (s *Service) writeProviderSources() {
	if s.facts.Providers == nil {
		return
	}
	s.facts.Providers.SetLiveConfigOptionsSource(s)
	s.facts.Providers.SetCatalogSampler(s)
	s.facts.Providers.SetCatalogConnectionFacts(s)
}

// modelValueFor 模型行 ↔ 底座运行时值的换算（Providers 面缺省时返回明确错误）。
func (s *Service) modelValueFor(runtimeID, providerID, model string) (string, error) {
	if s.facts.Providers == nil {
		return "", errPlaneFactsMissing
	}
	return s.facts.Providers.ModelValueFor(runtimeID, providerID, model)
}

// mcpGatewayEnabled 进程级网关开关（未装配 → 默认开启，与 `planes.New` 的默认一致）。
func (s *Service) mcpGatewayEnabled() bool {
	if s.facts.Gateway == nil {
		return true
	}
	return s.facts.Gateway.McpGatewayEnabled()
}
