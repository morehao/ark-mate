package runtime

import (
	"github.com/morehao/ark-mate/pkg/runtime/deepseekharness"
	"github.com/morehao/ark-mate/pkg/runtime/opencode"
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
		// 模型清单的落点（T5/ADR-15）：主通道是**内联 content**（`OPENCODE_CONFIG_CONTENT`，
		// 配置优先级 ⑥，压过项目级发现 ④⑤），文件仍写（对账的读回锚点 + 没有内联通道时的兜底）。
		// 根取隔离表注入的 `OPENCODE_CONFIG_DIR`（平台独占；用户的 ~/.config/opencode 一个字节不碰）。
		//
		// Tested 的取值见下面的长注释。
		ConfigProjection: ConfigProjection{
			Format:      ConfigFormatOpencodeJSON,
			Root:        "${OPENCODE_CONFIG_DIR}",
			Filename:    "opencode.json",
			Channel:     ConfigChannelEnvContent,
			FileChannel: ConfigChannelDirRead,
			// Tested=true 的**范围**要说清（这是"用实测结论换写入权"的那条纪律）：
			// 本仓已有端到端实证"平台写这份文件、真 opencode 读它并按其中的 settings.baseURL 发请求"
			// （frontend/scripts/desktop-skill-projection-acceptance.mjs 写、断言、真会话）。
			// H6 补的是**另一半**：写完去读 ACP 目录、断言声明的 model id 真的出现在候选里。
			Tested:          true,
			TestedOnVersion: "2.0.26",
			Effect:          ConfigEffectNextProcess,
			Note: "端到端实测（2026-10-10，随包 2.0.26，私有 HOME/MATE_HOME，探针 .dsh/tmp/gate1-opencode-probe.mjs）：" +
				"① 平台声明一条底座原本不知道的 model id（`arkprobe/ark-probe-*`）→ 新进程的 ACP model 候选里" +
				"**逐字出现**它，且 `current_value` 就是它（慢路径默认模型生效）；" +
				"② 会话 cwd 放一份声明了不同 model 的 `opencode.json` → 候选里**没有**那条敌意模型；" +
				"③ 父进程 env 预设 `OPENCODE_CONFIG_CONTENT`（敌意目录）→ 候选里同样没有它。" +
				"两条反证也跑了（同一脚本的 --falsify / --falsify-env）：拿掉 `OPENCODE_CONFIG_PROJECT_DISABLE` 时" +
				"cwd 的项目级文件**确实**抢占成功（甚至抢走了默认模型），拿掉平台对 `OPENCODE_CONFIG_CONTENT` 的" +
				"同键替换时父 env 的内联内容**确实**抢先 —— 所以上面三条 PASS 是修复带来的，不是本来就不会发生。" +
				"已知边界：① 内联内容走 env，同机同用户 ps eww 可见（清单不是密钥，可接受；" +
				"**因此绝不能把 apiKey 放进内联内容**，密钥仍走 T1 的 env 引用）；" +
				"② 管理员面（managed 配置 / macOS MDM）仍然压过平台，受管机器上「平台清单 = 最终生效清单」不成立；" +
				"③ 空清单会让 opencode 起不了会话（loadCatalog 重试 5s 后抛 No models are available）——" +
				"所以生成器在清单为空时**什么都不写**（实测中反证档正是这个形态：不声明模型时底座自己也没有我们的条目）；" +
				"④ `is_default` 指向不存在/被 `disabled:true` 的条目时，底座会**静默退到第一个可用模型**（事实 #46）——" +
				"平台的对账（T8）负责把这个事实报出来，而不是在配置里保证它一定生效。",
		},
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
// **递归**；v2.0.26 的发现表（上游 docs/skills「Discovery」）仍是 `~/.config/opencode/skills` +
// 兼容根 `~/.claude/skills`、`~/.agents/skills`，且实测仍读后者（见下 Tested 第 2 段）。
// 这里取 `~/.agents/skills`（跨工具约定，dsh 同款），不用 `~/.config/opencode/skills`
// ——后者是 openwork 自己的约定，opencode 本体不扫它。
//
// 不用 `${XDG_CONFIG_HOME}` 的理由：opencode 的 skill 面与 XDG_CONFIG_HOME 无关，且该变量在
// macOS 上普遍未设置，用它当默认根只会让默认形态在半个平台上直接报错。
//
// Tested=true 的依据（两段，缺一不可）：
//  1. **形态**：`<root>/<id>/SKILL.md` + 强制 frontmatter，于 2026-09-09 用真实实现验证过
//     （`opencode debug skill`，隔离 XDG_CONFIG_HOME，见 pkg/capability/skill_materialize.go 顶部实测记录），
//     并于 2026-10-09 在 **v2.0.26** 上复验（v2 的模型请求体里出现
//     `<skill><id>…</id><name>…</name><description>…</description></skill>`）。
//  2. **端到端**：「平台默认根下写进去、底座开箱即读到」，两代底座各有一段取证：
//     v1.18.35（2026-10-08）：平台在**私有 HOME** 下**不设任何 override**（不设 MATE_PROJECTION_DIRS /
//     不写底座配置 / 不设 XDG_CONFIG_HOME）写到 `${HOME}/.agents/skills/<id>/SKILL.md`，
//     `opencode debug skill` 列出的 `location` **逐字等于**该文件路径；反证 `OPENCODE_DISABLE_EXTERNAL_SKILLS=1`
//     下该条消失（只剩内置 skill）——证明读到的正是「外部 skill 自动加载」这条路。
//     v2.0.26（2026-10-09）：同样隔离、同样不设 override，**有**该文件时模型请求体的 skill 目录块里出现
//     这条 skill；**把文件拿掉**后同一位置不再出现（负对照）。注意 v2 的两处口径变化：`debug` 子命令只剩
//     agents/config/paths（`debug skill` 没了），`OPENCODE_DISABLE_EXTERNAL_SKILLS=1` 在 v2 上**已不生效**
//     ——v1 那套反证法在 v2 上不可用，现在的反证只能是「拿掉文件看它消失」。
//     报告：.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md（v1）、
//     .dsh/docs/notes/2026-10-09-opencode-v2-payload-verification-notes.md（v2 全量）。
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
		// 模型清单的落点（T5/ADR-14）：平台写**自己的** overlay，由 `--patch <绝对路径>` 递入。
		//
		// 为什么不用 `$DSH_HOME/cordis.patch.yml`（home 级 patch）：那份是 dsh 自己的持久层，
		// 平台写它会把用户从底座设置界面里的权利挤掉，且写坏就是**拒启**、只能靠修复文件恢复。
		// 平台的 overlay + 启动参数坏了把参数去掉即可（回到出厂配置），且归属干净（那个文件只有平台写）。
		// 根取隔离表注入的 `DSH_HOME`（= <MATE_HOME>/runtime/dsh，平台独占）；文件名带 `mate-` 前缀
		// 表明归属，且**不覆盖** dsh 自己的 `cordis.patch.yml`。
		ConfigProjection: ConfigProjection{
			Format:          ConfigFormatDshPatchYAML,
			Root:            "${DSH_HOME}",
			Filename:        "mate-models.patch.yml",
			Channel:         ConfigChannelCLIPatchArg,
			Tested:          false,
			TestedOnVersion: "",
			Effect:          ConfigEffectNextProcess,
			Note: "H6 的 dsh 组尚未跑：机制上成立（`--patch` 是层序最高的一层、与 --dump-config 共用同一份 " +
				"patch 算法），但本仓没有实测记录，故**只读不写**。" +
				"三条已知坑：① 空 patch / 只有注释的文件会让 dsh **拒启**（README 逐字：「an empty or " +
				"comments-only file fails boot — disable the layer with [] instead」）；" +
				"② `--patch` 的路径必须是**绝对**路径（dsh 侧按调用目录解析，而平台拉底座的 cwd 是用户会话目录）；" +
				"③ 目标条目是 `id: acp`（ACP 平面唯一消费模型的行），**不是** agent-default-model" +
				"（ACP 平面不消费它，写它等于空操作）。",
		},
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
//     · **运行级**：`~/.agents/skills/officecli`（第三方手工放置、无 `.mate-*` 标记、布局与 frontmatter
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
