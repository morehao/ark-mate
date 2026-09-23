package runtime

import (
	"github.com/morehao/ark-forge/pkg/runtime/deepseekharness"
	"github.com/morehao/ark-forge/pkg/runtime/opencode"
)

// opencodeSpec 汇入 opencode adapter 的启动/能力描述。
//
// 模型面（决策记录 D1，源码级结论）：opencode 的 ACP server **实现**会话级模型选择——
// packages/opencode/src/acp/service.ts 处理 configId=="model"（parseSelectedModel → session.setModel），
// 且其自带 live 测试断言 currentValue 随之变化（test/cli/acp/config-options.test.ts）。
// 因此不再用 OPENCODE_MODEL 注入 env（那样会「一个模型一个进程」），改为走 ACP 会话配置项；
// 其 ACP 目录本身就是「已配置凭证/显式声明的 provider」的模型集合。
func opencodeSpec() Spec {
	return Spec{
		ID:      opencode.ID,
		Name:    opencode.Name,
		Version: opencode.Version,
		Launch: LaunchSpec{
			Command: opencode.Command,
			Args:    opencode.Args,
		},
		Caps: Capabilities{
			SessionsPersistent:     opencode.Caps.SessionsPersistent,
			MultiSessionConcurrent: opencode.Caps.MultiSessionConcurrent,
			Images:                 opencode.Caps.Images,
			EmbeddedContext:        opencode.Caps.EmbeddedContext,
		},
		Description:       "opencode（terminal AI）ACP 执行后端",
		McpTransports:     []string{"stdio", "sse", "http"}, // §6.2：opencode 支持 http/sse/stdio
		ManagesOwnContext: true,                             // §6.2：后端自管上下文/工具
		Notes:             opencode.Notes,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 /agents/:id/diagnostics 外露为 remediation）。
		SetupSteps: []string{
			"安装 opencode CLI（ACP 子命令由它提供），确认 `opencode --version` 可执行且 opencode 在 PATH 中",
			"在底座内完成 provider 登录，或给该 agent 绑定 provider 档案以注入密钥 env",
		},
		Projections: []Projection{opencodeSkillProjection()},
		// 未实测：P-2 探针尚无结论 → 保守 process（EffectiveCapabilityScope 也会强制回落）。
		CapabilityScope: CapabilityScopeProcess,
		Tested:          false,
		TestedOnVersion: "",
	}
}

// opencodeSkillProjection opencode 的 skill 投影（原生形态见下）。
//
// 根的选择：opencode v1.18.32 的 skill 发现（二进制内 `SkillDiscovery`）扫的全局根是
// `~/.claude` 与 `~/.agents` 下的 `skills/**/SKILL.md`（`GA="skills/**/SKILL.md"`），
// **递归**。这里取 `~/.agents/skills`（跨工具约定，dsh 同款），不用 `~/.config/opencode/skills`
// ——后者是 openwork 自己的约定，opencode 本体不扫它。
//
// 不用 `${XDG_CONFIG_HOME}` 的理由：opencode 的 skill 面与 XDG_CONFIG_HOME 无关，且该变量在
// macOS 上普遍未设置，用它当默认根只会让默认形态在半个平台上直接报错。
//
// Tested=false 的理由（**刻意不虚报**）：`<root>/<id>/SKILL.md` + 强制 frontmatter 这个**形态**
// 于 2026-09-09 用真实实现验证过（`opencode debug skill`，隔离 XDG_CONFIG_HOME，见
// pkg/capability/skill_materialize.go 顶部实测记录），但「平台默认根下写进去、底座开箱即读到」
// 这条**端到端**链尚未实测（那次的隔离 XDG_CONFIG_HOME 等价于显式配置 skills.paths）。
// P-2 探针会一并测它，届时置 true 并把版本写进 Note。
func opencodeSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: opencode.SkillFormat,
		Tested: false,
		Note: "opencode v1.18.32 源码级确认全局扫描 `~/.agents/skills/**/SKILL.md`（递归，故扁平落点也在扫描范围内）；" +
			"内容形态（SKILL.md + 强制 YAML frontmatter）于 2026-09-09 用 opencode v1.18.29 的 `opencode debug skill` 实测通过，" +
			"但「默认根开箱即读」这条端到端链待 P-2 探针确认",
	}
}

// deepseekharnessSpec 汇入 deepseek-harness adapter 的启动/能力描述。
func deepseekharnessSpec() Spec {
	return Spec{
		ID:      deepseekharness.ID,
		Name:    deepseekharness.Name,
		Version: deepseekharness.Version,
		Launch: LaunchSpec{
			Command: deepseekharness.Command,
			Args:    deepseekharness.Args,
		},
		Caps: Capabilities{
			SessionsPersistent:     deepseekharness.Caps.SessionsPersistent,
			MultiSessionConcurrent: deepseekharness.Caps.MultiSessionConcurrent,
			Images:                 deepseekharness.Caps.Images,
			EmbeddedContext:        deepseekharness.Caps.EmbeddedContext,
		},
		Description:       "deepseek-harness ACP 执行后端",
		McpTransports:     []string{"stdio", "streamable_http"}, // §6.2：dsh 拒 sse
		ManagesOwnContext: true,                                 // §6.2：后端自管上下文/工具
		// 模型走 ACP 会话配置项（session/set_config_option，会话级、可共享连接）：
		// 目录与取值全部由 dsh 自报（option id "model"、值 JSON.stringify([provider,model])），
		// 平台只按 category 消费，不在此声明任何 id/格式。
		Notes: deepseekharness.Notes,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 remediation 外露）。
		SetupSteps: []string{
			"安装含 acp profile 模板的 dsh（npm @next 0.1.2-rc.1+；latest 0.1.1-rc.2 缺该模板），确认 `dsh --profile acp` 可启动且 dsh 在 PATH 中",
			"准备 DEEPSEEK_API_KEY（子进程环境透传）：给该 agent 绑定 provider 档案，或让 daemon 进程环境带上该变量",
		},
		Projections: []Projection{deepseekharnessSkillProjection()},
		// 未实测：P-2 探针尚无结论 → 保守 process。
		CapabilityScope: CapabilityScopeProcess,
		Tested:          false,
		TestedOnVersion: "",
	}
}

// deepseekharnessSkillProjection dsh 的 skill 投影（原生形态见下）。
//
// 根的选择：dsh 的 `dsh-skill-filesystem` provider 声明的 user 级根有两个——
// `$DSH_HOME/skills`（"user-dsh"）与 `~/.agents/skills`（"user-agents"）。
// 这里取 `~/.agents/skills`：它与 `$DSH_HOME` 解耦，容器里把 DSH_HOME 改到 `/data/dsh` 时
// 投影根不会跟着漂移（否则默认根在容器形态下必然写错地方）。
//
// **落点必须扁平**：`isPotentialSkillPath` 只认根下 1~2 段——`<root>/<name>.md`（扁平文件）
// 或 `<root>/<name>/SKILL.md`（目录包）；更深的 `**/SKILL.md` **不扫**（与 opencode 相反）。
// 故 Layout 只能是 `<id>/SKILL.md`，且内容必须自带 YAML frontmatter（缺 name/description 整条丢弃）。
//
// Tested=false 的理由同 opencode：形态 2026-09-09 用 `dsh-skill-filesystem` 真实 provider 实测过
// （无 frontmatter / 只有 name / 只有 description 三种都丢弃，嵌套不扫），但默认根端到端待 P-2。
func deepseekharnessSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: deepseekharness.SkillFormat,
		Tested: false,
		Note: "dsh-skill-filesystem 源码确认 user 级根为 `$DSH_HOME/skills` 与 `~/.agents/skills`，取后者以与 DSH_HOME 解耦；" +
			"**只扫根下一层**（`<n>.md` 或 `<n>/SKILL.md`），故落点必须扁平；内容形态（frontmatter 必填 name+description）" +
			"于 2026-09-09 用真实 provider 实测通过，默认根端到端待 P-2 探针确认",
	}
}
