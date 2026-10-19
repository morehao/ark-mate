package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/infra/logging"
	credmech "github.com/morehao/ark-mate/internal/mechanism/credential"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/golib/glog"
)

// Planes 本次进程的**领域平面集合**（装配层唯一构造点）。
//
// 为什么收在一个结构体而不是继续散在 `manager.Service` 上：
//
//   - 核心进程、`mate mcp bridge` 网关子进程与 `mate doctor` 用的是**同一套**能力/供应商/
//     凭据事实。三处各自 `manager.NewService` 一遍，等于三套装配代码；分叉的表现是
//     "核心能注入的服务器，网关注入不了"（用户只看到工具少了一半，没有任何报错）。
//   - 这些平面的依赖是**构造期封闭的**（注册表 + 机制），没有一样来自会话/编排。把它们与
//     "要拨号、要会话"的那一半分开，registry 才可能在 manager 解散后继续服务所有入口。
//
// 字段一律导出：消费方拿的是**同一个实例**（不是方法转发的副本），两份平面 = 两个事实源。
type Planes struct {
	Registry *runtime.Registry

	Definitions  *definition.Mechanism
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
	Resources *Resources

	// Identity 产品身份的交付面（部署级常量；不落库、不读库）。
	//
	// 它与会话域的分工：本面负责"把身份交给底座"与"本进程生效的是哪一份"，
	// 会话域只读它的 Spec 去拼首轮块。两边共用同一份事实 ⇒ 系统提示与首轮块不会各说一套。
	Identity *IdentityDelivery

	// mu/defWarned 装载期"同一条定义问题只 WARN 一次"的记账。
	// 装载会被反复调用（启动、导入、**网关每次取数**），不记账就会把一条坏定义刷成日志洪水。
	mu        sync.Mutex
	defWarned map[string]bool

	// B5 原生权限映射开关（MATE_NATIVE_PERMISSION=off 关闭；默认开启）。
	//
	// 它今天**不改变任何注入结果**（v19 起平台没有策略来源），但它是进程级配置事实：
	// 放在装配面而不是编排面，网关与核心读同一个值；保留读取口是为了让"部署脚本设了它"
	// 这件事至少可观测（静默失效比读一次并如实说出当前没有策略来源更糟）。
	nativePermMu      sync.Mutex
	nativePermEnabled bool

	// mcpGatewayEnabled 平台侧 MCP 网关开关（M7-3；MATE_MCP_GATEWAY=off 回退）。
	// 放在装配面而不是编排面：它是**进程级配置事实**，网关子进程与核心必须读同一个值。
	// 零值 false 会被 NewPlanes 显式置成 true——"忘了设"不该等于"静默关掉网关"。
	mcpGatewayEnabled atomic.Bool
}

// NewPlanes 构造一套领域平面（只做内存装配，不读盘、不拨号）。
//
// 顺序敏感的两处：
//   - `skill.NewPlane` 与 `mcp.NewPlane` 共用**同一份**能力注册表（两份注册表 = 两个事实源）；
//   - `mcp.NewSecrets` 与 `provider.NewPlane` 共用**同一份**凭据机制（同一引用解析出两个结果
//     是 v13–v19 那类分叉的来源）。
func NewPlanes(reg *runtime.Registry) *Planes {
	p := &Planes{
		Registry:     reg,
		Definitions:  definition.New(),
		Credentials:  credmech.New(reg),
		Capabilities: capability.NewRegistry(),
		Picker:       workspace.NewPicker(),
		Workspaces:   workspace.NewPlane(),
		Automation:   automation.NewPlane(),
		defWarned:    make(map[string]bool),
	}
	p.Skills = skill.NewPlane(p.Capabilities, skillDefs{p.Definitions})
	p.McpSecrets = mcp.NewSecrets(p.Credentials)
	p.Mcp = mcp.NewPlane(p.Capabilities, mcpDefs{p.Definitions}, p.McpSecrets)
	p.Providers = provider.NewPlane(reg, p.Credentials)
	p.Resources = NewResources(reg, p.Providers)
	p.Identity = NewIdentityDelivery(reg)
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
	p.Definitions.SetStore(st)
	p.Credentials.SetStore(st)
	p.Skills.SetPersistence(st != nil)
	p.Providers.SetStore(st)
	p.Workspaces.SetStore(workspaceStore{st})
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

// defWarnOnce 同一条定义问题在一个进程内只 WARN 一次（key 须含 id 与 revision）。
//
// key 里带 revision 是刻意的：**同一条定义改坏了两次要报两次**。
func (p *Planes) defWarnOnce(key, msg string, args ...any) {
	p.mu.Lock()
	seen := p.defWarned[key]
	p.defWarned[key] = true
	p.mu.Unlock()
	if seen {
		return
	}
	glog.Warnw(context.Background(), msg, args...)
}

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐定义装载的容错口径）。
//
// **语义降级（M6-2）**：自 v8 起，sqlite 模式下本方法**不再是装载源**——能力定义的事实源是
// DB（`skills`/`mcp_servers`），本方法只剩两个用途：memory 模式的装载路径，以及导入/导出路径
// （`mate capability import|export`）。
func (p *Planes) LoadCapabilityDefinitions(dir string) int {
	if p.Capabilities == nil {
		return 0
	}
	p.Definitions.SetDir(dir)
	return p.Capabilities.LoadDir(dir, skill.NormalizeRuntimeDecls).Total()
}

// LoadCapabilityDefinitionsFromStore 从持久层装载能力定义（sqlite 模式的事实源，M6-2）。
//
// 三步：**清空 → 逐行解码 → 过注册闸门**。顺序不能换：先整体换面是"库里删掉的行不得以幽灵
// 定义留下"的前提；过注册闸门是最后一道防线（库是可写的，一手坏定义不该让整个能力面起不来）。
//
// 坏行/非法定义**逐条 WARN 跳过、不阻断启动**。返回成功装载的条数。
func (p *Planes) LoadCapabilityDefinitionsFromStore(ctx context.Context, importDir string) int {
	n, _ := p.reloadCapabilityDefinitions(ctx, importDir, glog.InfoLevel)
	return n
}

// ReloadCapabilityDefinitions 重新装载定义（**网关每次取数前**调用；M7-2）。
//
// 与 FromStore 是同一份实现，只有日志档位不同：网关每次 tools/list / tools/call 都会触发，
// 用 Info 档会把日志刷成噪声——而噪声的代价是"真出问题时没人看日志"。
func (p *Planes) ReloadCapabilityDefinitions(ctx context.Context) (int, error) {
	return p.reloadCapabilityDefinitions(ctx, "", glog.DebugLevel)
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / MCP live）。
func (p *Planes) RegisterTool(t capability.Tool) error {
	return p.Capabilities.RegisterTool(&t)
}

// reloadCapabilityDefinitions 装载内核（公开入口与网关共用；level 只作用于汇总行）。
func (p *Planes) reloadCapabilityDefinitions(ctx context.Context, importDir string, level glog.Level) (int, error) {
	if p.Capabilities == nil || p.Resources.Store() == nil {
		return 0, fmt.Errorf("能力注册表或持久层未初始化")
	}
	st := p.Resources.Store()
	if importDir != "" {
		p.Definitions.SetDir(importDir)
	}
	// 装进**独立的**新注册表再整体换入（不是"先 Reset 再逐条注册"）：后者会在共享面上留下
	// 一个空的/半装的可观测中间态，而网关每次取数前都重装定义 ⇒ 并发调用必然撞上它。
	fresh := capability.NewRegistry()
	total := 0

	skills, err := st.ListSkillDefinitions(ctx)
	if err != nil {
		p.defWarnOnce("store:skills", "capability: 从持久层读 skill 定义失败（能力面将为空）", "err", err)
		return total, err
	}
	for i := range skills {
		row := skills[i]
		var sk capability.Skill
		if err := json.Unmarshal([]byte(row.Body), &sk); err != nil {
			p.defWarnOnce("skill:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		// id 以**行主键**为准：body 里的 id 与行主键不一致时，行主键是事实（它是 CAS 与审计的键）。
		if sk.ID != row.ID {
			if sk.ID != "" {
				p.defWarnOnce("skill-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", sk.ID)
			}
			sk.ID = row.ID
		}
		skill.NormalizeRuntimeDecls(&sk)
		// SourceFile 刻意**不填**：DB 化之后"文件来源"不再成立，库视图的来源口径在 M6-5/M6-6
		// 统一重定义为 db / builtin / imported。
		if err := fresh.RegisterSkill(&sk); err != nil {
			p.defWarnOnce("skill-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: skill 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	mcps, err := st.ListMcpServerDefinitions(ctx)
	if err != nil {
		p.defWarnOnce("store:mcps", "capability: 从持久层读 mcp server 定义失败", "err", err)
		return total, err
	}
	for i := range mcps {
		row := mcps[i]
		var m capability.McpServer
		if err := json.Unmarshal([]byte(row.Body), &m); err != nil {
			p.defWarnOnce("mcp:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp server 定义解码失败，跳过", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		if m.ID != row.ID {
			if m.ID != "" {
				p.defWarnOnce("mcp-id:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义 body 里的 id 与行主键不一致，以行主键为准", "row", row.ID, "body", m.ID)
			}
			m.ID = row.ID
		}
		if err := fresh.RegisterMcpServer(&m); err != nil {
			p.defWarnOnce("mcp-rej:"+row.ID+"@"+fmt.Sprint(row.Revision), "capability: mcp 定义未采纳", "id", row.ID, "revision", row.Revision, "err", err)
			continue
		}
		total++
	}

	// 一次换面：此后所有读者看到的是完整的新目录。
	p.Capabilities.ReplaceAll(fresh)
	logging.At(ctx, level, "capability: 定义已从持久层装载", "skills", len(skills), "mcps", len(mcps), "adopted", total)
	return total, nil
}
