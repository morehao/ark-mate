package sessionruntime

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/core/capability"
	pkgidentity "github.com/morehao/ark-mate/internal/core/identity"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 领域平面的**按面端口**（阶段 0.2）———
//
// 改造前 `Service` 直接持 `*registry.Planes`（L6 组合驱动），于是执行面既认识具体平面集合，
// 也在 `Planes()` 上把整包具体类型漏给装配与测试。现在按**面**拆开：
//
//	Providers / Capabilities / Skills / McpSecrets / Workspaces / Credentials / Automation / Identity / Gateway
//
// 每个接口只声明本层真正调用的方法（签名里的类型全是域/值层：`provider.*`、`capability.*`…）。
// 装配根用 `WithPlaneFacts` 把它们从 `*registry.Planes` 上折出来；测试夹具用
// `newServiceForTest` 构造同一套。**执行面因此不再 import `drivers/registry`。**
//
// 为什么不是"一个大接口"：按面拆之后，某一块将来换实现（例如 MCP 秘密面换成外部 vault）
// 只需要满足它自己那几张方法，不会被迫实现另外八个面。

// ProviderFacts 供应商/模型面的事实与接线。
type ProviderFacts interface {
	SetLiveConfigOptionsSource(src provider.LiveConfigOptionsSource)
	SetCatalogSampler(s provider.CatalogSampler)
	SetCatalogConnectionFacts(f provider.CatalogConnectionFacts)
	SetEffect(fn provider.EffectFn)
	SetDefaultModel(fn provider.DefaultModelProvider)
	ModelValueFor(runtimeID, providerID, model string) (string, error)
	DefaultModelRow(ctx context.Context) (*model.ModelEntity, bool, error)
	ProviderEnvFor(runtimeID, providerID string) []string
	InjectedProviderID(ctx context.Context) string
	CredentialProblem(ctx context.Context) error
	ProviderModelRefsReadOnly(ctx context.Context, providerID string) []string
	ResolveSessionModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error)
	ProviderKeyReady(ctx context.Context, providerID string) (bool, string)
	PutConfigOptions(runtimeID string, options []acp.ConfigOption)
	MarkModelRejected(runtimeID, value, reason string)
	ClearModelRejection(runtimeID, value string)
	ModelCatalog(ctx context.Context) (*provider.CatalogView, error)
	ModelRow(ctx context.Context, id string) (*provider.ModelItem, bool, error)
}

// CapabilityFacts 能力注册表的读面（技能/MCP 服务器）。
type CapabilityFacts interface {
	ListSkills() []capability.Skill
	SkillByID(id string) (capability.Skill, bool)
	GetMcpServer(id string) (capability.McpServer, bool)
	ListMcpServers() []capability.McpServer
}

// SkillFacts 技能投影面（按 runtime 取投影）。
type SkillFacts interface {
	Projections() *skill.Projections
}

// McpSecretFacts MCP 密钥解析面。
type McpSecretFacts interface {
	Resolve(m capability.McpServer) (env, headers map[string]string, problems []string)
}

// WorkspaceFacts 工作空间面（解析、登记查询、展示与策略来源）。
type WorkspaceFacts interface {
	ResolveTargetWorkspace(reqWorkspaceID string) (target *workspace.DefaultWorkspaceRef, assignment string, err error)
	WorkspaceIDByPath(ctx context.Context, path string) string
	WorkspaceDefaultsOf(id string) (workspace.WorkspaceDefaults, bool)
	WorkspaceDisplayOf(workspaceID string) (string, string)
	SetSessionSource(src workspace.SessionSource)
}

// CredentialFacts 凭据面的读事实（cwd 等）。
type CredentialFacts interface {
	Cwd() string
}

// AutomationFacts 自动化面的接线（宿主端口）。
type AutomationFacts interface {
	SetHost(h automation.Host)
}

// IdentityFacts 产品身份交付面的读事实。
type IdentityFacts interface {
	Spec() pkgidentity.Spec
}

// GatewayFacts 进程级网关开关。
type GatewayFacts interface {
	McpGatewayEnabled() bool
}

// PlaneFacts 一次装配的领域平面端口集合（装配根或测试夹具提供）。
//
// nil 字段 = 该面不可用：读面按"缺省语义"处理（例如 `Gateway` nil → 网关默认开启），
// 写面（`SetX`）直接跳过——这比 panic 更接近"该面未装配"的真相。
type PlaneFacts struct {
	Providers    ProviderFacts
	Capabilities CapabilityFacts
	Skills       SkillFacts
	McpSecrets   McpSecretFacts
	Workspaces   WorkspaceFacts
	Credentials  CredentialFacts
	Automation   AutomationFacts
	Identity     IdentityFacts
	Gateway      GatewayFacts
}

// errPlaneFactsMissing 该面未装配（纯列表/纯状态路径上访问了它）。
var errPlaneFactsMissing = errors.New("该领域面未装配：装配根应通过 WithPlaneFacts 注入")

// ResourceStore 资源同步面的**窄库口**（会话元数据/工作空间行/会话表读取）。
//
// 它此前经 `s.resourceStore` 拿 `*store.Store`（L5 具体句柄）；本层实际只用到
// 这三条，于是收成端口，由装配根把同一个库句柄交进来（`EnableStore`）。
type ResourceStore interface {
	GetWorkspace(ctx context.Context, id string) (model.WorkspaceEntity, bool, error)
	UpdateSessionMeta(ctx context.Context, id string, meta model.SessionMetaPatch) error
	ListSessions(ctx context.Context, archived *bool) ([]model.SessionEntity, error)
}
