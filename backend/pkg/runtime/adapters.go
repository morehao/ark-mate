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
// Tested=true 的依据（两段，缺一不可）：
//  1. **形态**：`<root>/<id>/SKILL.md` + 强制 frontmatter，于 2026-09-09 用真实实现验证过
//     （`opencode debug skill`，隔离 XDG_CONFIG_HOME，见 pkg/capability/skill_materialize.go 顶部实测记录）。
//  2. **端到端**：「平台默认根下写进去、底座开箱即读到」，于 2026-10-08 用 opencode **v1.18.35** 补测通过
//     （报告：.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md）。关键取证：平台在**私有 HOME**
//     下**不设任何 override**（不设 FORGE_PROJECTION_DIRS / 不写底座配置 / 不设 XDG_CONFIG_HOME）写到
//     `${HOME}/.agents/skills/<id>/SKILL.md`，`opencode debug skill` 列出的 `location` **逐字等于**该文件路径；
//     **反证** `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` 下该条消失（只剩内置 skill）——证明读到的正是
//     「外部 skill 自动加载」这条路，而不是别的机制碰巧命中。
//
// 第 1 段不能顶替第 2 段：上次那次隔离 XDG_CONFIG_HOME 等价于显式配置 skills.paths，只能证明
// 「给了路径就能读」，证明不了「开箱即读」。
func opencodeSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: opencode.SkillFormat,
		Tested: true,
		Note: "全局扫描 `~/.agents/skills/**/SKILL.md`（递归，扁平落点也在范围内）；" +
			"内容形态于 2026-09-09 用 v1.18.29 实测通过；**默认根端到端于 2026-10-08 用 v1.18.35 实测通过**" +
			"（平台不设任何 override 写入 `~/.agents/skills/<id>/SKILL.md` → `opencode debug skill` 的 location 逐字命中；" +
			"`OPENCODE_DISABLE_EXTERNAL_SKILLS=1` 下消失）",
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
// Tested=true 的依据（两段，缺一不可）：
//  1. **形态**：`<root>/<id>/SKILL.md` + 强制 frontmatter，于 2026-09-09 用 `dsh-skill-filesystem`
//     真实 provider 实测过（无 frontmatter / 只有 name / 只有 description 三种都丢弃，嵌套不扫）
//     ——与上面的「只扫根下一层」互相印证（见 pkg/capability/skill_materialize.go 顶部实测记录）。
//  2. **端到端**：「平台默认根下写进去、底座开箱即读到」，于 2026-10-08 用 dsh **0.2.0-rc.2** 确认
//     （报告：.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md §B-6）。取证分两路：
//     · **源码链**（读本机安装版本 app.asar 内的实际代码）：`PROFILE_TEMPLATES.acp = [dsh-base, dsh-acp-app]`
//     → `dsh-base@0.2.0-rc.2` 的 `cordis.patch.yml` 常态启用 `id: skill-filesystem` 与 `id: tool-skill`
//     → provider 的 `includeDefaultRoots` 默认 true，rank 500 根 = `join(agentsHome,"skills")`，
//     而 `agentsHome = resolve(config.agentsHome ?? process.env.DSH_AGENTS_HOME ?? join(homedir(),".agents"))`
//     ⇒ 默认落点正是 `${HOME}/.agents/skills`。注意这条链对 **acp / web / headless 三个 profile 同款**
//     （都含 dsh-base），故 acp 与实测用的 desktop 走的是同一份 provider 与同一个工具。
//     · **运行级**：`~/.agents/skills/officecli`（第三方手工放置、无 `.forge-*` 标记、布局与 frontmatter
//     均合规）**只存在于该根**——它在 asar 内 0 命中（非内置，而内置的 office-docx 有 6 命中），
//     却出现在真实 dsh 会话投给模型的 skill 目录里 ⇒ 该根不是"被配置"而是**真被枚举并上图**。
func deepseekharnessSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: deepseekharness.SkillFormat,
		Tested: true,
		Note: "dsh-skill-filesystem 源码确认 user 级根为 `$DSH_HOME/skills` 与 `~/.agents/skills`，取后者以与 DSH_HOME 解耦；" +
			"**只扫根下一层**（`<n>.md` 或 `<n>/SKILL.md`），故落点必须扁平；frontmatter 必填 name+description 且 " +
			"**name 强制 `^[a-z0-9]+(?:-[a-z0-9]+)*$`（不合规静默丢弃）**；" +
			"形态于 2026-09-09 用真实 provider 实测通过，**默认根端到端于 2026-10-08 用 v0.2.0-rc.2 确认通过**" +
			"（源码链：acp 模板含 dsh-base，其 patch 常态启用 skill-filesystem/tool-skill，agentsHome 默认 `<HOME>/.agents`；" +
			"运行级：仅存在于该根的 officecli 被真实会话投给模型，且非内置）。" +
			"两条边界：① 该根是**最低优先**（rank 500），项目级 `<cwd>/.agents/skills`(200) 与 `~/.dsh/skills`(400) 会遮蔽它；" +
			"② 底座按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析该根，故**任何**注入 `DSH_AGENTS_HOME` 的地方都会让读根与本投影分叉；" +
			"平台的配置隔离**刻意不注入它**（2026-10-09 修：曾注入，E5 实测导致 dsh 会话的 `<available_skills>` 看不到本投影根下的 skill；" +
			"护栏见 `pkg/runtime/isolation_test.go` 的 TestIsolationKeepsSkillRootAlignedWithProjection），但用户若在平台进程环境里设了它仍会静默分叉",
	}
}
