package assembly

import (
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	corellm "github.com/morehao/ark-mate/internal/core/llm"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/service/svcartifact"
	"github.com/morehao/ark-mate/internal/service/svcattachment"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
	"github.com/morehao/ark-mate/internal/service/svccapability"
	"github.com/morehao/ark-mate/internal/service/svccommandpolicy"
	"github.com/morehao/ark-mate/internal/service/svclibrary"
	"github.com/morehao/ark-mate/internal/service/svcmaterial"
	"github.com/morehao/ark-mate/internal/service/svcmodel"
	"github.com/morehao/ark-mate/internal/service/svcruntime"
	"github.com/morehao/ark-mate/internal/service/svcsearch"
	"github.com/morehao/ark-mate/internal/service/svcsession"
	"github.com/morehao/ark-mate/internal/service/svcstream"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/service/svcusage"
	"github.com/morehao/ark-mate/internal/service/svcworkspace"
	"github.com/morehao/ark-mate/internal/sessionruntime"
)

// Services 装配根构造出的全部用例（唯一构造点）。
//
// 字段是各 `service/svc<领域>` 包内声明的**接口**；控制器只收这些接口，`app`/`cli` 一个都不碰。
// 需要"事实来源"的用例（导出的事实源、模型读面的 `ModelFace`）也在这里建一次——同一次装配
// 只该有一份状态，否则"两处各自记账"会让诊断与真实行为分叉。
type Services struct {
	// ——— 会话面 ———
	Session            svcsession.SessionSvc
	SessionHistory     svcsession.HistorySvc
	SessionDetail      svcsession.DetailSvc
	SessionInteraction svcsession.InteractionSvc
	SessionQueue       svcsession.QueueSvc
	SessionMeta        svcsession.MetaSvc
	SessionCapability  svcsession.CapabilityEditSvc
	SessionRead        svcsession.ReadSvc
	Search             svcsearch.SearchSvc
	Usage              svcusage.UsageSvc
	Export             svcsystem.SystemSvc

	// ——— 交付与设置 ———
	Workspace     svcworkspace.WorkspaceSvc
	CommandPolicy svccommandpolicy.PolicySvc

	// ——— 底座 / 模型 / 供应商 ———
	Runtime       svcruntime.RuntimeSvc
	Model         svcmodel.ModelSvc
	ModelDeclared svcmodel.DeclaredSvc
	Provider      svcmodel.ProviderSvc
	ProviderProbe svcmodel.Prober

	// ——— 能力 / 自动化 / Library ———
	Capability svccapability.CapabilitySvc
	Automation svcautomation.AutomationSvc
	Library    svclibrary.LibrarySvc

	// ——— 系统面与事件流 ———
	Meta         svcsystem.MetaSvc
	Capabilities svcsystem.CapabilitySvc
	Stream       svcstream.Stream

	// ——— 附件（v32）———
	Attachment svcattachment.AttachmentSvc

	// ——— 资料库（v33）———
	Material svcmaterial.MaterialSvc

	// ——— 产物溯源（v34）———
	Artifact svcartifact.ArtifactSvc

	// ——— 内部模型调用网关（v37）———
	// 平台内部"不经用户会话"的模型调用的唯一入口；后续用例按需消费（当前主要供旁路辅助调用）。
	LLM corellm.Gateway
}

// Facts 构造用例所需的事实集合（装配根在启动序列里填好；测试也可以直接给一份）。
//
// 单独成类型是为了让"用例依赖哪些事实"可读、可测：装配根与路由级测试用**同一套**构造，
// 不会出现"测试里手搭一份、生产里另一份"的漂移。
type Facts struct {
	Cfg          config.Config
	Store        *store.Store
	Planes       *registry.Planes
	Session      *sessionruntime.Service
	Orchestrator *sessionruntime.Orchestrator
	Bus          *eventbus.Bus
	Runtime      *runtime.Registry
	Execution    *runtimeconn.Plane
	// MetaSource 诊断面的值来源（nil = 空来源，测试与 CLI 最小装配用）。
	MetaSource func() svcsystem.MetaSource
	// AttachStore 附件字节存储（v32）；nil = memory 模式（无库无字节存储），附件面 40501/不可用。
	AttachStore *attachmentstore.Store
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
		AttachStore:  as.attachStore,
	}
}

// buildServices 构造全部用例（`startControllers` 的第一步）。
func (as *assembly) buildServices() Services { return as.facts().BuildServices() }

// BuildServices 构造全部用例（`startControllers` 的第一步）。
func (f Facts) BuildServices() Services {
	as := f
	plane := as.Session.SessionPlane()
	modelFace := sessionruntime.NewModelFace(as.Session)
	attachmentSvc := f.buildAttachmentSvc()
	artifactSvc := f.buildArtifactSvc()
	// 资料库用例先建好：产物抽取成功后要顺手把产物收录进资料库（自动收录，见 artifactextract.go）。
	materialSvc := f.buildMaterialSvc(attachmentSvc, artifactSvc)
	// 产物自动抽取（P2-c）：把「工具写工作区文件」旁路成 artifacts 行。依赖 store/attachStore/artifactSvc，
	// 故在用例层构造完成后、Runner 已存在时经 setter 注入（memory 模式三者缺一 → 返回 nil，抽取自然关闭）。
	if as.Orchestrator != nil {
		as.Orchestrator.SetArtifactExtractor(newArtifactExtractor(as.Store, as.AttachStore, artifactSvc, materialSvc))
	}
	return Services{
		Session:            as.Session,
		SessionHistory:     svcsession.NewHistory(eventReader(as.Store), plane, as.Bus),
		SessionDetail:      svcsession.NewDetail(plane, as.Session),
		SessionInteraction: svcsession.NewInteraction(plane, as.Session),
		SessionQueue:       svcsession.NewQueue(plane, as.Session),
		SessionMeta:        svcsession.NewMeta(plane, as.Session),
		SessionCapability:  svcsession.NewCapabilityEdit(plane, as.Session),
		SessionRead:        svcsession.NewRead(plane),
		Search:             svcsearch.New(plane),
		Usage:              svcusage.New(plane),
		Export:             svcsystem.New(svcsystem.NewSource(sourceFactsReader(as.Store), plane, as.Bus)),

		Workspace:     svcworkspace.New(as.Planes.Workspaces, as.Planes.Picker),
		CommandPolicy: svccommandpolicy.New(as.Cfg.CommandPolicyPath, f.orchestratorApply()),

		Runtime:       svcruntime.New(as.Execution),
		Model:         modelFace,
		ModelDeclared: svcmodel.NewDeclared(modelStore(as.Store), as.Planes.Providers, modelFace),
		Provider:      svcmodel.NewProvider(as.Planes.Providers),
		ProviderProbe: svcmodel.NewProbe(as.Planes.Providers, as.Planes.Providers),

		Capability: svccapability.NewCapability(
			as.Planes.Skills, as.Planes.Mcp, as.Planes.Experts,
			svccapability.NewTools(as.Planes.Capabilities),
			svccapability.NewServers(as.Planes.Capabilities, as.Planes.Mcp, as.Planes.McpSecrets),
		),
		Automation: svcautomation.New(as.Planes.Automation),
		Library: svclibrary.NewLibrary(as.Planes.Capabilities, as.Planes.Skills, as.Runtime,
			as.Planes.McpSecrets, as.Planes.McpGatewayEnabled),

		Meta:         svcsystem.NewMeta(f.metaSource),
		Capabilities: svcsystem.NewCapabilities(),
		Stream:       svcstream.New(as.Bus),

		Attachment: attachmentSvc,
		Material:   materialSvc,
		Artifact:   artifactSvc,

		LLM: buildLLMGateway(f),
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
func (f Facts) metaSource() svcsystem.MetaSource {
	if f.MetaSource == nil {
		return svcsystem.MetaSource{}
	}
	return f.MetaSource()
}

// ——— 端口转换：把 `*store.Store` 当接口交出去（v3.1 P4）———
//
// 必须现判 nil：typed-nil（`var st *store.Store = nil` 装进接口）在接口里**不是 nil**，
// 用例里那句 `if m.st == nil { 返回 40501 }` 会失效——memory 模式下会变成一次对 nil 的方法调用。
func eventReader(st *store.Store) svcsession.EventReader {
	if st == nil {
		return nil
	}
	return st
}

func sourceFactsReader(st *store.Store) svcsystem.SourceFactsReader {
	if st == nil {
		return nil
	}
	return st
}

func modelStore(st *store.Store) svcmodel.ModelStore {
	if st == nil {
		return nil
	}
	return st
}
