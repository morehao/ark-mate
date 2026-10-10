package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/pkg/resource"
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
		// Notes 里带上**清单通道**的实测结论（原先挂在已删除的 `ConfigProjection.Note` 上，
		// 那些知识不能随字段一起消失——见 const 的注释）。
		Notes: opencode.Notes + "\n\n" + opencodeModelConfigEvidence,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 /agents/:id/diagnostics 外露为 remediation）。
		SetupSteps: []string{
			"安装 opencode CLI（ACP 子命令由它提供），确认 `opencode --version` 可执行且 opencode 在 PATH 中",
			"在底座内完成 provider 登录，或给该 agent 绑定 provider 档案以注入密钥 env",
		},
		Projections: []Projection{opencodeSkillProjection()},
		// 模型清单的**落点**（M2）：主通道是内联 content（`opencode.json` 仍写，`OPENCODE_CONFIG_CONTENT`
		// 是压过项目级发现的那条注入），根取隔离表注入的 `OPENCODE_CONFIG_DIR`（平台独占；用户的
		// `~/.config/opencode` 一个字节不碰）。
		//
		// **格式与通道的细节不在这里**（T1/A9 由 archtest 的 TestInternalHasNoRuntimeConfigLiterals
		// 钉住）：`providers` 复数键、官方原生 package 字符串、走哪个环境变量内联——这些是 opencode
		// 的私有知识，全部住在 `pkg/runtime/opencode`。内核只知道"这个底座把清单落在哪个文件"，
		// 因为路径的 `${...}` 占位符只能靠它自己的隔离 env 解出来。
		ModelConfig: ModelConfigLocation{
			Root:     "${OPENCODE_CONFIG_DIR}",
			Filename: "opencode.json",
		},
		// 凭据面（ADR-4 / Q1'）：用**官方字段** `providers.<route>.env` 引用 + 平台子进程 env 注入，
		// **这是它的终局形态而非欠账**。
		//
		// 为什么 opencode 不走原生凭据文档（有证据，不是省事）：2.0.26 的通用凭据面已经不是文件——
		// 二进制里它的凭据存储是自己的 SQLite 表（`CREATE TABLE workspace (id, provider, binding,
		// created_at, last_used_at)`），而 `auth.json` 全程只有 4 处引用，其中一处是名为
		// `20260805200742_import_legacy_credentials` 的**迁移**（读 `<data>/auth.json`，失败文案逐字
		// 「Legacy credential file must contain an object」）。也就是说 `auth.json` 是**迁移输入（legacy）**，
		// 而写它的私有 DB 属越界（别人的 schema、别人的迁移、`binding` 语义不明）。
		// 2026-10-10 用户拍板**接受 opencode 换代际生效**，所以 env 通道不是"等着换掉的东西"：
		// 只有将来官方给出受支持的非交互凭据面（`auth.json` 恢复成文件面、或官方接口），才需要重新取证
		// ——那是**增量**，不是补债。
		Credential: resource.CredentialSpec{
			// 逐供应商派生（ADR-16），`MATE_` 前缀同时是平台在凭据文档里的命名空间（R13：
			// 用 `OPENAI_API_KEY` 这类"知名"名会被用户 shell 里的同名变量静默遮蔽）。
			RefPattern:  "MATE_%s_API_KEY",
			FallbackEnv: true,
			// Tested=false 指的是**原生凭据文档**这条通道（它为空、不存在），不是清单通道：
			// opencode 从来不走原生文档，所以它永远不该被"实测通过"。
			Tested: false,
			Note: "opencode 的原生凭据面在 2.0.26 已是它自己的 SQLite 表，auth.json 只剩 legacy 迁移输入" +
				"——本期用官方 env 字段 + 平台 env 注入，这是它的终局形态而非欠账（ADR-4/Q1'）。" +
				"两条与密钥面直接相关的已知边界：① 内联内容走 env，同机同用户 `ps eww` 可见（清单不是密钥，" +
				"可接受；**因此绝不能把 apiKey 放进内联内容**，密钥只以逐供应商引用名的形式出现在配置里）；" +
				"② `is_default` 指向不存在/被 `disabled:true` 的条目时，底座会**静默退到第一个可用模型**" +
				"（事实 #46）——平台的对账负责把这个事实报出来，而不是在配置里保证它一定生效。",
		},
		// 未实测：P-2 探针尚无结论 → 保守 process（EffectiveCapabilityScope 也会强制回落）。
		CapabilityScope: CapabilityScopeProcess,
		Tested:          false,
		TestedOnVersion: "",
	}
}

// opencodeModelConfigEvidence opencode 清单通道的端到端实测结论与已知边界（原先写在
// `ConfigProjection.Note` 上；字段随 ADR-7 删除，但**结论不能丢**——它正是"平台写的清单真的被
// 底座读到并生效"这条写入权的依据，下一个人排查"我加了模型为什么选不到"时要能直接读到它）。
const opencodeModelConfigEvidence = "清单通道端到端实测（2026-10-10，随包 2.0.26，私有 HOME/MATE_HOME，" +
	"探针 .dsh/tmp/gate1-opencode-probe.mjs）：" +
	"① 平台声明一条底座原本不知道的 model id（`arkprobe/ark-probe-*`）→ 新进程的 ACP model 候选里" +
	"**逐字出现**它，且 `current_value` 就是它（慢路径默认模型生效）；" +
	"② 会话 cwd 放一份声明了不同 model 的 `opencode.json` → 候选里**没有**那条敌意模型；" +
	"③ 父进程 env 预设 `OPENCODE_CONFIG_CONTENT`（敌意目录）→ 候选里同样没有它。" +
	"两条反证也跑了（同一脚本的 --falsify / --falsify-env）：拿掉 `OPENCODE_CONFIG_PROJECT_DISABLE` 时" +
	"cwd 的项目级文件**确实**抢占成功（甚至抢走了默认模型），拿掉平台对 `OPENCODE_CONFIG_CONTENT` 的" +
	"同键替换时父 env 的内联内容**确实**抢先 —— 所以上面三条 PASS 是修复带来的，不是本来就不会发生。" +
	"已知边界：① 内联内容走 env，同机同用户 ps eww 可见（清单不是密钥，可接受；" +
	"**因此绝不能把 apiKey 放进内联内容**，密钥仍走 env 引用）；" +
	"② 管理员面（managed 配置 / macOS MDM）仍然压过平台，受管机器上「平台清单 = 最终生效清单」不成立；" +
	"③ 空清单会让 opencode 起不了会话（loadCatalog 重试 5s 后抛 No models are available）——" +
	"所以清单为空时**撤回**：删掉平台写的文件并撤销内联注入，绝不留下空的注入内容（H4 的 Plan B）；" +
	"④ `is_default` 指向不存在/被 `disabled:true` 的条目时，底座会**静默退到第一个可用模型**（事实 #46）——" +
	"平台的对账负责把这个事实报出来，而不是在配置里保证它一定生效。"

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
		Notes: deepseekharness.Notes + "\n\n" + deepseekharnessModelConfigEvidence,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 remediation 外露）。
		SetupSteps: []string{
			"安装含 acp profile 模板的 dsh（npm @next 0.1.2-rc.1+；latest 0.1.1-rc.2 缺该模板），确认 `dsh --profile acp` 可启动且 dsh 在 PATH 中",
			"准备 DEEPSEEK_API_KEY（子进程环境透传）：给该 agent 绑定 provider 档案，或让 daemon 进程环境带上该变量",
		},
		Projections: []Projection{deepseekharnessSkillProjection()},
		// 模型清单的**落点**（M3）：平台的 overlay + 启动参数 `--patch <绝对路径>`。
		//
		// 为什么不用 `$DSH_HOME/cordis.patch.yml`（home 级 patch）：那份是 dsh 自己的持久层，
		// 平台写它会把用户从底座设置界面里的权利挤掉，且写坏就是**拒启**、只能靠修复文件恢复。
		// 平台的 overlay + 启动参数坏了把参数去掉即可（回到出厂配置），且归属干净（那个文件只有平台写）。
		// 根取隔离表注入的 `DSH_HOME`（= <MATE_HOME>/runtime/dsh，平台独占）；文件名带 `mate-` 前缀
		// 表明归属，且**不覆盖** dsh 自己的 `cordis.patch.yml`。
		//
		// 格式（patch 的 `- id:` 条目、`--patch` 参数、两级校验门）全部住在
		// `pkg/runtime/deepseekharness`——内核只知道落点，因为占位符只能靠该 runtime 的隔离 env 解出来。
		ModelConfig: ModelConfigLocation{
			Root:     "${DSH_HOME}",
			Filename: "mate-models.patch.yml",
			// 原生凭据文档与清单配置同住这个平台独占根（解析由 ResolveConfigLocation 一起做）。
			CredentialDoc: ".credentials.yaml",
		},
		// 凭据面（ADR-4）：写**底座的原生凭据文档**，换来热生效（B9）。
		Credential: resource.CredentialSpec{
			NativeDoc:       "${DSH_HOME}/.credentials.yaml",
			RefPattern:      "MATE_%s_API_KEY",
			FallbackEnv:     false,
			Tested:          true,
			TestedOnVersion: "0.2.0-rc.2",
			Note: "原生凭据文档**已端到端实测**（H6 探针 7/7 PASS，2026-10-10，dsh 0.2.0-rc.2）：" +
				"① **热生效成立**——同一进程内把文档的 refs 从 key-A 改成 key-B（平台式临时文件 + 原子 rename），" +
				"下一轮请求的 `Authorization` 就是 key-B，**未重启**；" +
				"② **env 赢**——文档与子进程 env 同时给时实际用的是 env（所以同一 runtime 的 env 与文档" +
				"**绝不能同时启用**，否则轮换后的旧值被陈旧 env 永久遮蔽且无任何报错）；" +
				"③ **0644 是拒用不是告警**——凭据插件在启动期 `loadInitial` 抛错、该 route 报 `no credential`，" +
				"文案指向 `chmod 600`；" +
				"④ **dsh 从不回写该文件**——平台读-改-写（只替换 refs）逐字保住注释与 `records:` 分节，" +
				"文件 sha 前后相同；" +
				"⑤ 官方文档逐字：「只有你的 OS 用户能读取该文件，但 agent 的工具进程以同一用户身份运行，" +
				"因此**该存储无法向 agent 隔离机密**」——所以这是**用持久化换热生效**，不是安全改进（B10）。" +
				"五条纪律（§安全）：① 权限 0600（父目录 0700）且**落盘后读回校验**，不满足即中止（dsh 拒启）；" +
				"② 密钥绝不进日志/错误文案/事件/API/doctor/导出（诊断只许出现**键名**）；" +
				"③ 同一 runtime 的 env 与文档不得同时启用（`FallbackEnv:false` 就是这条的声明）；" +
				"④ 只写平台独占根，绝不写用户的 `~/.dsh`；" +
				"⑤ 空集撤回只移除**平台自己写过的** refs 条目，用户自己的 refs 与 `records:`/注释逐字节保留。" +
				"残余未取证：平台与 dsh 自身写路径「同一瞬间都写」未被压测（由 dsh 的 watch 对账与平台侧" +
				"读-改-写兜住）；Windows 无 mode 可查、权限门未验；**版本敏感**——载荷升级时先置回 `false` 再重跑探针。",
		},
		// 未实测：P-2 探针尚无结论 → 保守 process。
		CapabilityScope: CapabilityScopeProcess,
		Tested:          false,
		TestedOnVersion: "",
	}
}

// deepseekharnessModelConfigEvidence dsh 清单通道（`--patch` overlay）的实测结论与三条已知坑
// （原先写在已删除的 `ConfigProjection.Note` 上；写入权是用实测换的，依据不能丢）。
const deepseekharnessModelConfigEvidence = "清单通道端到端实测（2026-10-10，随包 0.2.0-rc.2）。" +
	"① 机制（最小 ACP 客户端直接问底座，见 .dsh/docs/notes/probes/dsh-acp-catalog.mjs）：" +
	"在 `llm-pi-ai.config.providers.<route>` 上声明一条自定义 route（api + baseURL + apiKeyEnv + models 齐备）" +
	"**确实**会以一组出现在 ACP 的 `options[category=model]` 里（实测候选 `[\"arkprobe\",\"ark-probe-x\"]` " +
	"与内置三条并列）；**当前选中值由 `id: acp` 那一行决定**（只改 acp → currentValue 变成自定义那条；" +
	"只改 agent-default-model → 纹丝不动）。② 平台侧完整链路也实测过（生成 → 自校验 → 底座级校验门 → " +
	"落盘 → `--patch` → 新进程 → 候选里出现该条、且对账差集为空，见 " +
	".dsh/docs/notes/probes/gate1-dsh-probe.mjs --platform-writes）。写入权的**代价**必须说清：" +
	"这条链路写坏会让 dsh **拒启**，所以落盘前两级校验门（自校验 + `--dump-config-schema`）一个都不能省。" +
	"三条已知坑：① 空 patch / 只有注释的文件会让 dsh **拒启**（README 逐字：「an empty or comments-only " +
	"file fails boot — disable the layer with [] instead」），所以清单为空时**撤回**（删文件 + 摘参数）；" +
	"② `--patch` 的路径必须是**绝对**路径（dsh 侧按调用目录解析，而平台拉底座的 cwd 是用户会话目录）；" +
	"③ 想覆盖**已安装 route**（如 `deepseek-official`）的目录会撞 `configurable provider … is already " +
	"declared`（它同时被 llm-deepseek 与 pi-ai 声明）——平台给 dsh 加模型必须**新开一条自己的 route** " +
	"并在 `acp` 行把 provider 指向它。"

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

// registerBuiltinSyncers 把各 adapter 的同步实现登记进 `Registry` 的第二张表。
//
// 这是「父包汇入」这条既有约定的延申：adapter 子包提供实现，父包决定它在注册表里的键、**并把已解析的
// 落点交给它**（路径的 `${...}` 占位符只能靠该 runtime 自己的隔离 env 解出来，而 adapter 拿不到注册表
// ——约束⑦）。所以装配形态是"先 `ResolveConfigLocation` → 再把 `Path` 传进去"，adapter 永远不自己拼 HOME。
//
// 落点解析失败（声明非法 / 隔离 env 没应用）时**不登记**：此时该 runtime 的 `sync[]` 会如实报
// `unsupported`——比登记一个会写错地方的实现好得多（写错落点等于去动用户自己的目录，隔离纪律的硬红线）。
func registerBuiltinSyncers(r *Registry) {
	if r == nil {
		return
	}
	for _, id := range []string{opencode.ID, deepseekharness.ID} {
		spec, ok := r.Get(id)
		if !ok {
			continue
		}
		loc, declared, err := ResolveConfigLocation(spec)
		if err != nil || !declared {
			continue
		}
		switch id {
		case opencode.ID:
			r.RegisterSyncer(id, opencode.NewSyncer(opencode.Options{ConfigPath: loc.Path}, r.DeliveryFor(id)))
		case deepseekharness.ID:
			r.RegisterSyncer(id, deepseekharness.NewSyncer(deepseekharness.Options{
				PatchPath:         loc.Path,
				CredentialDocPath: loc.CredentialDocPath,
				RefPattern:        spec.Credential.RefPattern,
				// 底座级校验门要用的那两个参数（T3.2）：
				//
				// `DshBin` 取**平台自己会拉起的那一个**——即该 spec 的 `Launch.Command` 经 PATH 解析
				// 的结果（与 `exec` 拉底座完全同一条解析链）。为什么不 `LookPath("dsh")` 写死名字：
				// 测试夹具会把 Command 换成桩名，那时解析必然失败 → 校验门**如实跳过**（adapter 在
				// DshBin=="" 时报"未跑底座级校验"），而不是去执行宿主上恰好装着的那份 dsh
				// ——全量 `go test ./...` 不依赖外部 runtime，这条是本仓的既有纪律。
				//
				// `StagingHome` 落在**平台自己的隔离根**里（`<MATE_HOME>/runtime/dsh/validate-staging`）：
				// 校验是在候选落盘**之前**跑的，必须用一个与真 home 分开的目录，否则半成品会先污染
				// 底座真正要读的那份 home。
				DshBin:      resolveLaunchBinary(spec.Launch.Command),
				StagingHome: filepath.Join(loc.RootAbs, "validate-staging"),
			}, r.DeliveryFor(id)))
		}
	}
}

// resolveLaunchBinary 把 spec 声明的启动命令解析成绝对路径（解析不到 → 空串 = 不跑底座级校验）。
//
// 显式、不探测：只看该 runtime 自己声明的 `Command`，找不到就返回空——**绝不**去猜别的位置。
// 这样"平台会执行哪个二进制"与"拿哪个二进制做校验"永远是同一个答案。
func resolveLaunchBinary(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if filepath.IsAbs(command) {
		if info, err := os.Stat(command); err == nil && !info.IsDir() {
			return command
		}
		return ""
	}
	abs, err := exec.LookPath(command)
	if err != nil {
		return ""
	}
	return abs
}
