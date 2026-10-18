package session

import (
	"github.com/morehao/ark-mate/internal/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— P3c：能力集的事实面 ———
//
// 代际键（基础键 + **能力指纹** + 平台模型配置指纹）里的能力指纹归会话域：它回答"**这一代**会话
// 实际注入了什么"，而 `sessions.capability_json` 记的正是这一代会话钉住的选定。
//
// `skill` / `mcp` 两个平面只提供"我的定义如何进指纹"的**事实**。这不是洁癖：把指纹装配整个
// 交给能力域会让 `session` 与能力域双向依赖（设计文档 §2.4 G-2 已证），而同层零 import 守卫
// 正盯着（`TestDomainPackagesMustNotImportEachOther`）。
//
// 分界落在"**解析**在装配侧、**取舍**在本域"：
//
//	装配侧（manager 的适配器）读注册表、环境变量、网关开关，必要时**拼出网关条目**（它要拿
//	  本进程自己的可执行文件路径），最后交出平凡事实；
//	本域拿事实算指纹（`capability_fingerprint.go`），并决定"哪些内容进去"。
//
// 事实是平凡值对象，所以本域的单测可以直接喂，不必起真注册表、不必起子进程。

// ProjectedSkill 该 runtime 的 skill 投影**事实**（`skill.Projection` 的域侧视图）。
//
// `Err != ""` 表示该 runtime 声明了投影却没解析出落点（占位符变量未设置、落点模板非法…）。
// **仍然登记**它，是为了让审计视图能报"没配置"而不是让这个 runtime 从列表里彻底消失——
// 「我明明声明了，怎么哪儿都看不到」是最难排查的一种沉默。
type ProjectedSkill struct {
	Runtime    string
	Projection runtime.Projection
	Err        string
}

// InjectedMcp 一条**会真的被注入**的 MCP 条目事实（域侧视图）。
//
// 结构性约束：**只有键名与引用名**能过这道门（`EnvKeys` / `SecretRefs`）。§13.4 边界①说的是
// "密钥值绝不进指纹"——指纹会进日志、事件载荷与 DB 列，进去就等于泄漏。把明文挡在值对象之外
// 比"记得剔除"可靠：`SecretEnv`/`SecretHeaders` 在域内**不出现**（解析明文是注入前一刻的事）。
type InjectedMcp struct {
	ID        string
	Transport string
	Command   string
	Args      []string
	URL       string
	// EnvKeys 明文 env 的**键名**（值不入：值可能带凭据，且换值不该换进程）。
	EnvKeys []string
	// SecretRefs 密钥型 env/header 的**引用名**（`store:<ns>/<name>` / `env:<NAME>`），
	// **不是**解析后的值。改引用名（换一把钥匙）要产生新代际，改引用的**指向**（轮换密钥）
	// 不产生——轮换密钥不该让所有会话换底座进程。
	SecretRefs []string
}

// CapabilityFacts 该 runtime 上"按会话选定解析出的整套能力"的**平凡事实**。
//
// 由装配侧（`CapabilityFactsProvider` 的实现）一次算好：它是形状，不是判断。
type CapabilityFacts struct {
	// Spec 本进程认的底座形态（`SpecOK=false` = 未注册：指纹里只写 runtime id，与拆分前同义）。
	Spec   runtime.Spec
	SpecOK bool
	// Skills 该 runtime 上**真正会投放**的 skill（启用 + 声明投给它 + 本进程解析出了投影）。
	// 会话选定的白名单那一道**不在**这里收（它在本域的 `projectedSkillParts`）——两处收的话
	// "白名单为空数组"这条三态语义会出现两种解释。
	Skills []capability.Skill
	// Direct 直连注入的条目（含密钥引用，不含明文）。
	Direct []InjectedMcp
	// Gateway 平台网关条目（nil = 不需要）。
	//
	// 它天生无密钥（只有 scope 与会话钉住的白名单），故可以整体入指纹；而"加一个远程 server
	// 不换代际"这条 M7 收益，正是靠"远程定义在计划里被折叠成这一条常量条目"实现的。
	Gateway *InjectedMcp
	// Remote 交由网关代连的远程定义——**只用于日志/诊断**：它绝不进指纹。
	// （任何"把远程定义塞进指纹"的改动都会让"加一个远程 server 换代际"回潮，是 M7 的头号禁忌。）
	Remote []InjectedMcp
}

// CapabilityFactsProvider 装配侧登记的事实提供者（按 runtime + 会话选定算一次）。
//
// 它是**接口而不是一堆函数字段**：这一组事实必须同时来自三个平面（runtime 注册表 / skill 投影 /
// MCP 注入计划），拆成三个 setter 的后果是"漏接一个"表现为指纹少算一段——静默，且只在"代际该不该
// 切"这种慢变量上现形。一个接口 + 一个赋值点把那层风险收成一处。
type CapabilityFactsProvider interface {
	CapabilityFactsFor(runtimeID string, sel *CapabilitySelection) CapabilityFacts
}
