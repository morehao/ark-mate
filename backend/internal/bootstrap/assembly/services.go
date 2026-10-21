package assembly

import (
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/adapters/outbound/models"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly/planes"
	"github.com/morehao/ark-mate/internal/adapters/outbound/runtimeconn"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/app/automation"
	"github.com/morehao/ark-mate/internal/app/capability"
	"github.com/morehao/ark-mate/internal/app/commandpolicy"
	"github.com/morehao/ark-mate/internal/app/library"
	"github.com/morehao/ark-mate/internal/app/model"
	appruntime "github.com/morehao/ark-mate/internal/app/runtime"
	"github.com/morehao/ark-mate/internal/app/search"
	"github.com/morehao/ark-mate/internal/app/session"
	"github.com/morehao/ark-mate/internal/app/stream"
	"github.com/morehao/ark-mate/internal/app/system"
	"github.com/morehao/ark-mate/internal/app/usage"
	"github.com/morehao/ark-mate/internal/app/workspace"
	apprun "github.com/morehao/ark-mate/internal/app/run"
)

// Services 装配根构造出的全部用例（唯一构造点）。
//
// 字段是各 `app/<领域>` 包内声明的**接口**；控制器只收这些接口，`app`/`cli` 一个都不碰。
// 需要"事实来源"的用例（导出的事实源、模型读面的 `ModelFace`）也在这里建一次——同一次装配
// 只该有一份状态，否则"两处各自记账"会让诊断与真实行为分叉。
type Services struct {
	// ——— 会话面 ———
	Session            session.SessionSvc
	SessionHistory     session.HistorySvc
	SessionDetail      session.DetailSvc
	SessionInteraction session.InteractionSvc
	SessionQueue       session.QueueSvc
	SessionMeta        session.MetaSvc
	SessionRead        session.ReadSvc
	Search             search.SearchSvc
	Usage              usage.UsageSvc
	Export             system.SystemSvc

	// ——— 交付与设置 ———
	Workspace     workspace.WorkspaceSvc
	CommandPolicy commandpolicy.PolicySvc

	// ——— 底座 / 模型 / 供应商 ———
	Runtime       appruntime.RuntimeSvc
	Model         model.ModelSvc
	ModelDeclared model.DeclaredSvc
	Provider      model.ProviderSvc
	ProviderProbe model.Prober

	// ——— 能力 / 自动化 / Library ———
	Capability capability.CapabilitySvc
	Automation automation.AutomationSvc
	Library    library.LibrarySvc

	// ——— 系统面与事件流 ———
	Meta         system.MetaSvc
	Capabilities system.CapabilitySvc
	Stream       stream.Stream
}

// Facts 构造用例所需的事实集合（装配根在启动序列里填好；测试也可以直接给一份）。
//
// 单独成类型是为了让"用例依赖哪些事实"可读、可测：装配根与路由级测试用**同一套**构造，
// 不会出现"测试里手搭一份、生产里另一份"的漂移。
type Facts struct {
	Cfg          config.Config
	Store        *store.Store
	Planes       *planes.Planes
	Session      *apprun.Service
	Orchestrator *apprun.Orchestrator
	Bus          *eventbus.Bus
	Runtime      *runtime.Registry
	Execution    *runtimeconn.Plane
	// MetaSource 诊断面的值来源（nil = 空来源，测试与 CLI 最小装配用）。
	MetaSource func() system.MetaSource
}

// facts 把装配根的可变状态折成构造用例所需的事实。
func (as *assembly) facts() Facts {
	return Facts{
		Cfg:          as.cfg,
		Store:        as.st,
		Planes:       as.planes,
		Session:      as.svc,
		Orchestrator: as.orch,
		Bus:          as.bus,
		Runtime:      as.reg,
		Execution:    as.rconn,
		MetaSource:   as.metaSource,
	}
}

// buildServices 构造全部用例（`startControllers` 的第一步）。
func (as *assembly) buildServices() Services { return as.facts().BuildServices() }

// BuildServices 构造全部用例（`startControllers` 的第一步）。
func (f Facts) BuildServices() Services {
	as := f
	plane := as.Session.SessionPlane()
	modelFace := apprun.NewModelFace(as.Session, models.NewModelViews(as.Planes.Resources, as.Planes.Providers))
	return Services{
		// P5：会话写面用例在 `svcsession`，执行面只交能力（Starter/Prompt/Canceller 由编排器实现，
		// DeliverQueued 由 Service 实现）。漏接任一个的表现都是"用例 503/静默 no-op"。
		Session: session.NewSession(session.SessionDeps{
			Workspaces: as.Planes.Workspaces,
			Store:      workspaceStore(as.Store),
			Starter:    as.Orchestrator,
			Prompt:     as.Orchestrator,
			Canceller:  as.Orchestrator,
			Queued:     as.Session,
		}),
		SessionHistory:     session.NewHistory(eventReader(as.Store), plane, as.Bus),
		SessionDetail:      session.NewDetail(plane, as.Session),
		SessionInteraction: session.NewInteraction(plane, as.Session),
		SessionQueue:       session.NewQueue(plane, as.Session),
		SessionMeta:        session.NewMeta(plane, as.Session),
		SessionRead:        session.NewRead(plane),
		Search:             search.New(plane),
		Usage:              usage.New(plane),
		Export:             system.New(system.NewSource(sourceFactsReader(as.Store), plane, as.Bus)),

		Workspace:     workspace.New(as.Planes.Workspaces, as.Planes.Picker),
		CommandPolicy: commandpolicy.New(as.Cfg.CommandPolicyPath, f.orchestratorApply()),

		Runtime:       appruntime.New(as.Execution),
		Model:         modelFace,
		ModelDeclared: model.NewDeclared(modelStore(as.Store), as.Planes.Providers, modelFace),
		Provider:      model.NewProvider(as.Planes.Providers),
		ProviderProbe: model.NewProbe(as.Planes.Providers, as.Planes.Providers),

		Capability: capability.NewCapability(
			as.Planes.Skills, as.Planes.Mcp,
			capability.NewTools(as.Planes.Capabilities),
			capability.NewServers(as.Planes.Capabilities, as.Planes.Mcp, as.Planes.McpSecrets),
		),
		Automation: automation.New(as.Planes.Automation),
		Library: library.NewLibrary(as.Planes.Capabilities, as.Planes.Skills, as.Runtime,
			as.Planes.McpSecrets, as.Planes.McpGatewayEnabled),

		Meta:         system.NewMeta(f.metaSource),
		Capabilities: system.NewCapabilities(),
		Stream:       stream.New(as.Bus),
	}
}

// orchestratorApply 命令门禁的"立即生效"回调。
//
// 编排器在 `用例与控制器` 之前就已构造（步骤「编排器」在前），这里是显式判空而不是
// "反正一定有"：装配顺序若被改动，nil 会变成一次静默的规则丢弃。
func (f Facts) orchestratorApply() func([]interaction.PrefixRule) {
	if f.Orchestrator == nil {
		return func([]interaction.PrefixRule) {}
	}
	return f.Orchestrator.SetCommandPolicy
}

// metaSource 诊断面的值来源（未注入时给一份空来源，而不是 nil 函数）。
func (f Facts) metaSource() system.MetaSource {
	if f.MetaSource == nil {
		return system.MetaSource{}
	}
	return f.MetaSource()
}

// ——— 端口转换：把 `*store.Store` 当接口交出去（v3.1 P4）———
//
// 必须现判 nil：typed-nil（`var st *store.Store = nil` 装进接口）在接口里**不是 nil**，
// 用例里那句 `if m.st == nil { 返回 40501 }` 会失效——memory 模式下会变成一次对 nil 的方法调用。
func eventReader(st *store.Store) session.EventReader {
	if st == nil {
		return nil
	}
	return st
}

func sourceFactsReader(st *store.Store) system.SourceFactsReader {
	if st == nil {
		return nil
	}
	return st
}

func workspaceStore(st *store.Store) session.WorkspaceStore {
	if st == nil {
		return nil
	}
	return st
}

func modelStore(st *store.Store) model.ModelStore {
	if st == nil {
		return nil
	}
	return st
}
