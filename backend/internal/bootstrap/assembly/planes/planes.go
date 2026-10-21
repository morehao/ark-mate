// Package planes 是**领域平面容器**：本次进程里跨域的一份共享事实集合。
//
// 为什么收在一个结构体而不是继续散在各处装配：核心进程、`mate mcp bridge` 网关子进程与
// `mate doctor` 用的是**同一套**能力/供应商/凭据事实。三处各自装配一遍，等于三套装配代码；
// 分叉的表现是"核心能注入的服务器，网关注入不了"（用户只看到工具少了一半，没有任何报错）。
//
// 这些平面的依赖是**构造期封闭的**（注册表 + 机制），没有一样来自会话/编排。把它们与
// "要拨号、要会话"的那一半分开，能力面才可能在会话解散后继续服务所有入口。
//
// 落点为什么是 `bootstrap/assembly/planes` 而不是 `bootstrap/assembly` 包本体（C3 4.5 后半）：
// 装配根本身 import 用例环（`app/run` 等），而**用例环的包内测试**要用这套平面做夹具
// （`internal/app/run/testrig_test.go`）。夹具 import 装配根会得到
// `import cycle not allowed in test`——实测过。容器单独成子包是为了让"同一套事实"既有唯一
// 构造点、又能被夹具直接取用；装配根通过 `New` 构造它，方向仍然只有"装配根 → 平面容器"。
//
// 字段一律导出：消费方拿的是**同一个实例**（不是方法转发的副本），两份平面 = 两个事实源。
package planes

import (
	"context"
	"sync"
	"sync/atomic"

	credmech "github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/credential"
	"github.com/morehao/ark-mate/internal/adapters/outbound/definitions"
	"github.com/morehao/ark-mate/internal/adapters/outbound/models"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/adapters/outbound/workspaces"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// Planes 本次进程的**领域平面集合**（装配层唯一构造点）。
type Planes struct {
	Registry *runtime.Registry

	// Definitions 能力定义装载面（机制 + 注册表 + 库）。
	Definitions  *definitions.Face
	Credentials  *credmech.Store
	Capabilities *capability.Registry
	Skills       *skill.Plane
	McpSecrets   *mcp.Secrets
	Mcp          *mcp.Plane
	Picker       *workspace.Picker
	Workspaces   *workspace.Plane
	Automation   *automation.Plane
	Providers    *provider.Plane

	// Resources 资源同步面（模型清单向各底座交付 + 同步结论/指纹）。
	Resources *models.Resources

	// Identity 产品身份的交付面（部署级常量；不落库、不读库）。
	//
	// 它与会话域的分工：本面负责"把身份交给底座"与"本进程生效的是哪一份"，
	// 会话域只读它的 Spec 去拼首轮块。两边共用同一份事实 ⇒ 系统提示与首轮块不会各说一套。
	Identity *models.IdentityDelivery

	// B5 原生权限映射开关（MATE_NATIVE_PERMISSION=off 关闭；默认开启）。
	//
	// 它今天**不改变任何注入结果**（v19 起平台没有策略来源），但它是进程级配置事实：
	// 放在装配面而不是编排面，网关与核心读同一个值；保留读取口是为了让"部署脚本设了它"
	// 这件事至少可观测（静默失效比读一次并如实说出当前没有策略来源更糟）。
	nativePermMu      sync.Mutex
	nativePermEnabled bool

	// mcpGatewayEnabled 平台侧 MCP 网关开关（M7-3；MATE_MCP_GATEWAY=off 回退）。
	// 放在装配面而不是编排面：它是**进程级配置事实**，网关子进程与核心必须读同一个值。
	// 零值 false 会被 New 显式置成 true——"忘了设"不该等于"静默关掉网关"。
	mcpGatewayEnabled atomic.Bool
}

// New 构造一套领域平面（只做内存装配，不读盘、不拨号）。
//
// 顺序敏感的两处：
//   - `skill` 与 `mcp` 平面共用**同一份**能力注册表（两份注册表 = 两个事实源）；
//   - `mcp.Secrets` 与 `provider.Plane` 共用**同一份**凭据机制（同一引用解析出两个结果
//     是 v13–v19 那类分叉的来源）。
func New(reg *runtime.Registry) *Planes {
	caps := capability.NewRegistry()
	p := &Planes{
		Registry:     reg,
		Definitions:  definitions.New(caps),
		Credentials:  credmech.New(reg),
		Capabilities: caps,
		Picker:       workspace.NewPicker(),
		Workspaces:   workspace.NewPlane(),
		Automation:   automation.NewPlane(),
	}
	// 定义**文件**载体与库句柄无关：memory 模式走文件，sqlite 模式才用行。
	// 所以登记在构造处，而不是 `EnableStore`（那一步只在接了库时才跑——挂在它上面，
	// memory 模式的写面会直接空指针）。
	p.Automation.SetDefinitionFiles(definitions.AutomationDefinitionFiles{})
	p.Skills = definitions.NewSkillPlane(p.Capabilities, p.Definitions.Mechanism)
	p.McpSecrets = mcp.NewSecrets(p.Credentials)
	p.Mcp = definitions.NewMcpPlane(p.Capabilities, p.Definitions.Mechanism, p.McpSecrets)
	p.Providers = provider.NewPlane(reg, models.NewProviderSecrets(p.Credentials))
	p.Resources = models.NewResources(reg, p.Providers)
	p.Identity = models.NewIdentityDelivery(reg)
	// 默认开启（config 的 auto）：显式置位，避免 atomic.Bool 零值把默认关掉。
	p.mcpGatewayEnabled.Store(true)
	// 原生权限映射默认开启（nil 策略仍不注入 → 未声明策略的 agent 行为不变）。
	p.nativePermEnabled = true
	return p
}

// EnableStore 把库句柄接到**所有**持有它的平面，然后装载同步指纹并做一次装配期同步。
//
// 为什么必须是一个入口：这几处曾经都各自读同一个字段，"设一次就够了"；拆层之后它们各持一个
// 引用，**漏掉任一个的表现都是静默的**（该走库的走了文件、或反过来——定义只写进内存、凭据读
// 不到库）。核心的 serve、网关子进程与只读的 doctor 都经这里，**不要各自赋值**。
//
// 装配期同步（`Resources.EnableStore`）也在这里：重启后平台必须重新把清单交付给底座，
// 否则上一轮的交付物与新声明可能分叉，而界面显示的是"已同步"。
func (p *Planes) EnableStore(st *store.Store) {
	p.Definitions.EnableStore(st)
	p.Credentials.SetStore(st)
	p.Skills.SetPersistence(st != nil)
	p.Providers.SetStore(st)
	p.Workspaces.SetStore(workspaces.NewStore(st))
	p.Automation.SetStore(st)
	p.Resources.EnableStore(st)
}

// SetNativePermission 开关 B5 原生权限映射（false = 不向底座注入权限配置）。
func (p *Planes) SetNativePermission(enabled bool) {
	p.nativePermMu.Lock()
	defer p.nativePermMu.Unlock()
	p.nativePermEnabled = enabled
}

// NativePermissionEnabled 读取 B5 开关（默认 true）。
func (p *Planes) NativePermissionEnabled() bool {
	p.nativePermMu.Lock()
	defer p.nativePermMu.Unlock()
	return p.nativePermEnabled
}

// SetMcpGatewayEnabled 设置平台 MCP 网关开关（装配期与测试用）。
func (p *Planes) SetMcpGatewayEnabled(enabled bool) { p.mcpGatewayEnabled.Store(enabled) }

// McpGatewayEnabled 读取网关开关（默认 true）。
func (p *Planes) McpGatewayEnabled() bool { return p.mcpGatewayEnabled.Load() }

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐定义装载的容错口径）。
func (p *Planes) LoadCapabilityDefinitions(dir string) int { return p.Definitions.LoadDir(dir) }

// LoadCapabilityDefinitionsFromStore 从持久层装载能力定义（sqlite 模式的事实源，M6-2）。
func (p *Planes) LoadCapabilityDefinitionsFromStore(ctx context.Context, importDir string) int {
	return p.Definitions.LoadFromStore(ctx, importDir)
}

// ReloadCapabilityDefinitions 重新装载定义（**网关每次取数前**调用；M7-2）。
func (p *Planes) ReloadCapabilityDefinitions(ctx context.Context) (int, error) {
	return p.Definitions.Reload(ctx)
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / MCP live）。
func (p *Planes) RegisterTool(t capability.Tool) error { return p.Definitions.RegisterTool(t) }

// ——— 窄端口访问器 ———
//
// 网关子进程（L6 入站适配）不能 import 本包（装配根的子包）。它需要的是"取数要用的那几个面"
// 而不是整个容器，所以这里把字段暴露成**方法**供端口接口匹配；容器仍然只有一个构造点
// （`New`），且由装配根注入给网关进程。

// CredentialStore 凭据机制（网关要接分层凭据源）。
func (p *Planes) CredentialStore() *credmech.Store { return p.Credentials }

// ProviderPlane 供应商平面（网关要装载供应商定义）。
func (p *Planes) ProviderPlane() *provider.Plane { return p.Providers }

// McpPlane MCP 平面（网关要取网关面与解析密钥）。
func (p *Planes) McpPlane() *mcp.Plane { return p.Mcp }
