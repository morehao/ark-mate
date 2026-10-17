package sessionruntime

import (
	"context"
	"strings"
	"sync"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/registry"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// Service 实现 handler.Service（替换 mock），为控制面提供「真实」数据与动作：
// runtime 来自注册表（构建期固定，见 runtime_binding.go）；会话来自本进程内存存储；
// 会话动作（create/prompt/cancel）委托 Orchestrator（其内部持真实 ACP 连接）。
type Service struct {
	bus  *eventbus.Bus
	orch *Orchestrator
	// planes 本次进程的领域平面集合（P5a：**装配层 `internal/registry` 拥有**）。
	//
	// 本层**不再复刻**一份平面字段：所有读法都直接走 `s.planes.X`（一份实例、一条路径）。
	// P4b 之前它们是同名过渡视角，清理掉之后"这段实现在哪一层"重新可判定。
	planes *registry.Planes

	// sessionPlane 会话域（自持一把只保护域内字段的锁）。
	sessionPlane *session.Plane
	// runtimePlane 执行面（P3a-2 分批搬入）：本进程认的那个底座的绑定与诊断状态。
	runtimePlane *runtimeconn.Plane
	// runtimePool 连接池（P4a 正文第五十四刀搬进执行面）：连接表、代际上限与空闲回收。
	runtimePool *runtimeconn.Pool

	// coalescer ③ 文本增量合并器（NewService 构造时创建；见 chunk_coalescer.go）。
	coalescer *session.Coalescer

	// mcpGatewayWarned 网关条目组装失败"逐 key 只 WARN 一次"的记账（纯日志节流，
	// 不是网关开关本身——开关在 `registry.Planes`，那是进程级配置事实）。
	mcpGatewayWarnMu sync.Mutex
	mcpGatewayWarned map[string]bool
}

// NewService 构造 Service。bus 可为 nil（纯列表场景），Orchestrator 会覆盖注入。
// Option 装配期的可选注入。
//
// **为什么是"可选"而不是必填参数**：`NewService` 有 130+ 处调用点（几乎全在 manager 自己的
// 测试里，它们只需要"一个能跑的 Service"），而**装配层需要的是"同一个执行面实例"**
// （它要把这个实例也交给 API 面：P4a 起 `svcruntime.New` 由 `ctrruntime` 在构造函数里调，
// 用的就是这一份执行面）。必填参数会让 130+ 处跟着改，
// 换来的信息量与这里一样。P5a 的 `registry` 装配表会取代这个缝。
type Option func(*Service)

// WithRuntimePlane 用装配层提供的执行面（而不是本函数自己造一个）。
//
// **必须与 `svcruntime.New` 拿到同一个实例**（P4a 起那个调用在 `ctrruntime` 里）：
// 两个平面各自记账时，"诊断说 stopped、
// 连接却活着"这种分叉会立刻发生（`runtimeconn.Plane` 的进程状态是它自己的字段）。
func WithRuntimePlane(pl *runtimeconn.Plane) Option { return func(s *Service) { s.runtimePlane = pl } }

// WithPlanes 使用装配层已经造好的领域平面（核心与网关共用同一套事实的前提）。
//
// 不传时本函数自己造一套（130+ 处测试与 `mate demo` 走这条默认路径）；装配层传的是
// **同一个实例**——两份平面 = 两个事实源（"核心能注入、网关注入不了"就是那样长出来的）。
func WithPlanes(p *registry.Planes) Option { return func(s *Service) { s.planes = p } }

func NewService(reg *runtime.Registry, bus *eventbus.Bus, opts ...Option) *Service {
	s := &Service{bus: bus}
	// 可选注入**先行**：`WithPlanes` 决定的实例要在下面被引用，晚一步就会造出第二份平面。
	for _, opt := range opts {
		opt(s)
	}
	if s.planes == nil {
		s.planes = registry.NewPlanes(reg)
	}
	// 模型目录取样的三个窄端口（P5a）：活会话 / 历史会话 / 连接期事实。
	// **必须紧跟平面创建点**：早一行它们还是 nil，表现是目录恒空且说明文案说"没有执行面"。
	s.planes.Providers.SetLiveConfigOptionsSource(s)
	s.planes.Providers.SetCatalogSampler(s)
	s.planes.Providers.SetCatalogConnectionFacts(s)
	// 自动化域需要宿主端口（建会话/取消轮次）；它是编排面的事实，装配层造不了——
	// 与库句柄一样，漏接的表现是"触发时找不到宿主"（静默）。
	s.planes.Automation.SetHost(automationHostAdapter{svc: s})
	// 会话事实端口（P2b）：workspace 域不拥有会话，只消费这里的两个动作。
	s.planes.Workspaces.SetSessionSource(s)
	// 会话域：本批只装库句柄与用量端口（其余端口随 P3b-1b/2 接上）。
	s.sessionPlane = session.NewPlane()
	// 会话读面的端口：归属展示（工作空间登记面）P2b 起也在域内（`Plane.WorkspaceDisplayOf`）。
	// 能力选定回显与指纹装配点 P3c 起同样在域内，不再需要回显端口。
	s.sessionPlane.SetWorkspaceDisplay(s.planes.Workspaces.WorkspaceDisplayOf)
	// 能力事实提供者（P3c）：指纹的装配点在域，事实（注册表 / skill 投影 / MCP 注入计划）在这边。
	// **必须紧跟 `caps`/`reg` 的就位**：早一行登记会让指纹少算一段——静默，且只在"代际该不该切"
	// 这种慢变量上现形。
	s.sessionPlane.SetCapabilityFacts(s)
	s.sessionPlane.SetCapabilityLookup(s)
	// 会话域的其余端口：事件总线（恢复时按库位次注回）、模型行↔底座运行时值的换算、
	// 以及恢复期"作废重启前排队输入"的钩子（**临时**：输入队列 P3b-2 搬进来后就地调用）。
	s.sessionPlane.SetBus(bus)
	s.sessionPlane.SetModelValueSource(func(runtimeID, providerID, model string) (string, error) {
		return s.planes.Providers.ModelValueFor(runtimeID, providerID, model)
	})
	s.sessionPlane.SetStaleInputsOnRestore(s.sessionPlane.StaleQueuedInputsOnRestore)
	// 两个端口都读会话/模型面（编排侧），在装配期接上；`demo`/`migrate` 这类没有编排器的
	// 场景下 `effect` 恒 nil（回执为 nil，与拆分前逐字同义）。
	s.planes.Providers.SetEffect(s.providerEffect)
	s.planes.Providers.SetDefaultModel(func(ctx context.Context) (string, bool) {
		row, ok, err := s.planes.Providers.DefaultModelRow(ctx)
		if err != nil || !ok {
			return "", false
		}
		return strings.TrimSpace(row.ProviderID), true
	})

	// ③ 流式增量合并：flushFn 直连 publishRaw（绕过合并入口，避免自递归）。
	s.coalescer = session.NewCoalescer(func(typ, sessionID string, payload any) {
		s.sessionPlane.PublishRaw(typ, sessionID, payload)
	})
	// **必须紧跟创建点**：会话域持有的是合并器本身，早一行它还是 nil——表现是"文本增量不再
	// 合并"（每条增量各占一个位次），而编译、vet、构造期都看不出来（§3.1.38 的同一课）。
	s.sessionPlane.SetCoalescer(s.coalescer)
	// 绑定构建期选定的那个底座（v19：单实例）。`specOK=false`（未注册）不是这里报错——
	// 装配期的 `runtime.ProductRegistry` 已经 fail-closed，这里只是把事实记下来供诊断如实外露。
	spec, specOK := reg.Get(runtime.SelectedRuntime())
	if s.runtimePlane == nil {
		s.runtimePlane = runtimeconn.NewPlane(reg, spec, specOK)
	}
	// 执行面的口子：凭据面（供应商域）与连接池。池必须在这里造——它需要**最终那个**平面
	// （`WithRuntimePlane` 可能已经换过实例），且装配层（`app`）要用它设代际上限/空闲回收。
	s.runtimePlane.SetCredentialSource(s)
	s.runtimePool = runtimeconn.NewPool(s.runtimePlane)
	// env 与配置指纹都是**装配事实**，池只认端口（执行面不认识供应商档案与模型清单）。
	s.runtimePool.SetProviderEnvSource(s)
	// 指纹的来源是 `registry.Resources`：它是同一份 `runtimeCfgDigests`，不再经 Service 转一道。
	s.runtimePool.SetConfigDigestSource(s.planes.Resources)
	// 原生权限 env 的来源（P1-f 下半）：模式**进基础键**，所以这条端口不是"可选的注入"，
	// 而是"键的第三维"——不登记它，`DSH_PERMISSION_MODE` 既不会注入、也不会分进程。
	s.runtimePool.SetNativeEnvSource(s)
	// 连接池读面：诊断要问"此刻哪条连接活着"。**早一行拿到的是 nil**，表现是诊断永远报
	// 「未连接」、stderr 尾巴恒空——而它只在底座真的在跑时才该现形（开发态多半看不见）。
	s.runtimePlane.SetLiveClientSource(s.runtimePool)
	return s
}

// RuntimePool 连接池的事实出口（P4a 正文第五十四刀）。
//
// 装配期（`app` 的 serve）要用它设代际上限与空闲回收；这两个动作的实现只有
// `internal/runtimeconn` 那一份，manager 侧不再有一行转发（`SetGenerationLimit` /
// `StartIdleReaper` 已随本刀从 `Orchestrator` 删除）。
func (s *Service) RuntimePool() *runtimeconn.Pool { return s.runtimePool }

// Planes 交出本次装配的**领域平面集合**（同一实例；P5a）。
//
// 装配层（app）与网关子进程都用它拿事实：它们要的是平面本身，不是 Service 上的转发。
func (s *Service) Planes() *registry.Planes { return s.planes }

// SessionPlane 交会话域平面（**依赖事实**）：导出用例的事实来源（`svcsystem.NewSource`）
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
		s.runtimePool.SetDropSink(o)
	}
}
