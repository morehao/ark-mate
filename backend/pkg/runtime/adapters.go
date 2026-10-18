package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime/dsh"
)

// dshSpec 汇入 deepseek-harness adapter 的启动/能力描述。
func dshSpec() Spec {
	return Spec{
		ID:      dsh.ID,
		Name:    dsh.Name,
		Version: dsh.Version,
		Launch: LaunchSpec{
			Command: dsh.Command,
			Args:    dsh.Args,
		},
		Caps: Capabilities{
			SessionsPersistent:     dsh.Caps.SessionsPersistent,
			MultiSessionConcurrent: dsh.Caps.MultiSessionConcurrent,
			Images:                 dsh.Caps.Images,
			EmbeddedContext:        dsh.Caps.EmbeddedContext,
		},
		// 配置隔离（M3-3 / 设计 §6.6.1）：dsh 只认 DSH_HOME 这一个 home 变量。
		//
		// 它**必须让桌面私有启动器也兜底**（LauncherFallback: true）：dsh 连 `--version` 都会写自己的
		// home（实测：不隔离时它往用户的 `~/.dsh` 里写日志），而壳直接跑随包载荷的那条路径上没有核心
		// 注入，只有启动器兜底。
		//
		// **刻意不给 `DSH_AGENTS_HOME`**：dsh 的 skill provider 按
		// `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析 agents 根，而平台把 skill 投射到
		// `${HOME}/.agents/skills`（见 dshSkillProjection）。一旦注入 DSH_AGENTS_HOME，**读根就与写根
		// 分叉**，skill 静默不生效（2026-10-09 E5 实测：注入后 dsh 会话的 `<available_skills>` 里没有
		// 投影根下那条 skill，只有注入根下的）。历史：本表曾按"agents 面也要隔离"注入它，那是把
		// "隔离用户既有配置"错用到了**平台自己的投放面**上——这条根是**用户全局共享**的
		// （`~/.agents` 是**跨工具约定**，不是某个底座的私有配置）。护栏见
		// `TestIsolationKeepsSkillRootAlignedWithProjection`。
		Isolation: []IsolationVar{
			{Key: "DSH_HOME", Kind: IsolationPath, Path: []string{"runtime", "dsh"}, LauncherFallback: true},
		},
		Description: "dsh（deepseek-harness）ACP 执行后端",
		// dsh 侧**总是**写 `models[].input`（ADR-12）：不写时 `declaredInput()` 会"问下一层"
		// （catalog 认识的 id 保留 catalog、平台新造 id 落到 defaultInput=["text"]），
		// 于是"平台声明的模型能不能收图"变成一件取决于底座目录版本、用户改不动也看不见的事。
		DeclaresModelInput: true,
		// 0.2.1 起 dsh 的模型目录按凭据过滤：apiKeyEnv 缺失 → 该 route 不列模型。
		ModelCatalogNeedsCredential: true,
		McpTransports:               []string{"stdio", "streamable_http"}, // §6.2：dsh 拒 sse
		ManagesOwnContext:           true,                                 // §6.2：后端自管上下文/工具
		// 模型走 ACP 会话配置项（session/set_config_option，会话级、可共享连接）：
		// 目录与取值全部由 dsh 自报（option id "model"、值 JSON.stringify([provider,model])），
		// 平台只按 category 消费，不在此声明任何 id/格式。
		Notes: dsh.Notes + "\n\n" + dshModelConfigEvidence,
		// SetupSteps 该底座未就绪时的可执行修复步骤（诊断面 remediation 外露）。
		SetupSteps: []string{
			"安装含 `acp` profile 模板的 dsh（缺模板时报 profile \"acp\" does not exist；随包载荷 pin 的版本满足这条），确认 `dsh --profile acp` 可启动且 dsh 在 PATH 中",
			"准备 DEEPSEEK_API_KEY（子进程环境透传）：给该 agent 绑定 provider 档案，或让 daemon 进程环境带上该变量",
		},
		Projections: []Projection{dshSkillProjection()},
		// 模型清单的**落点**（M3）：平台的 overlay + 启动参数 `--patch <绝对路径>`。
		//
		// 为什么不用 `$DSH_HOME/cordis.patch.yml`（home 级 patch）：那份是 dsh 自己的持久层，
		// 平台写它会把用户从底座设置界面里的权利挤掉，且写坏就是**拒启**、只能靠修复文件恢复。
		// 平台的 overlay + 启动参数坏了把参数去掉即可（回到出厂配置），且归属干净（那个文件只有平台写）。
		// 根取隔离表注入的 `DSH_HOME`（= <MATE_HOME>/runtime/dsh，平台独占）；文件名带 `mate-` 前缀
		// 表明归属，且**不覆盖** dsh 自己的 `cordis.patch.yml`。
		//
		// 格式（patch 的 `- id:` 条目、`--patch` 参数、两级校验门）全部住在
		// `pkg/runtime/dsh`——内核只知道落点，因为占位符只能靠该 runtime 的隔离 env 解出来。
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
			TestedOnVersion: "0.2.1-rc.2",
			Note: "原生凭据文档**已端到端实测**（H6 探针 7/7 PASS，2026-10-18，dsh 0.2.1-rc.2）：" +
				"① **热生效成立**——同一进程内把文档的 refs 从 key-A 改成 key-B（平台式临时文件 + 原子 rename），" +
				"下一轮请求的 `Authorization` 就是 key-B，**未重启**；" +
				"② **文档赢**（0.2.1 起；0.2.0 是 env 赢）——文档与子进程 env 同时给时实际用的是文档。" +
				"所以同一 runtime 的 env 与文档**绝不能同时启用**：另一条会变成静默死值，轮换后没有任何报错；" +
				"③ **0644 是拒用不是告警**——凭据插件在启动期 `loadInitial` 抛错、该 route 报 `no credential`，" +
				"文案指向 `chmod 600`；" +
				"④ **dsh 从不回写该文件**——平台读-改-写（只替换 refs）逐字保住注释与 `records:` 分节，" +
				"文件 sha 前后相同；" +
				"⑤ 官方文档逐字：「只有你的 OS 用户能读取该文件，但 agent 的工具进程以同一用户身份运行，" +
				"因此**该存储无法向 agent 隔离机密**」——所以这是**用持久化换热生效**，不是安全改进（B10）。" +
				"五条纪律（§安全）：① 权限 0600（父目录 0700）且**落盘后读回校验**，不满足即中止（dsh 拒启）；" +
				"② 密钥绝不进日志/错误文案/事件/API/doctor/导出（诊断只许出现**键名**）；" +
				"③ 同一 runtime 的 env 与文档不得同时启用（`FallbackEnv:false` 就是这条的声明；0.2.1 起文档赢，注入 env 只会是死值）；" +
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

// dshModelConfigEvidence dsh 清单通道（`--patch` overlay）的实测结论与三条已知坑
// （原先写在已删除的 `ConfigProjection.Note` 上；写入权是用实测换的，依据不能丢）。
const dshModelConfigEvidence = "清单通道端到端实测（2026-10-10，随包 0.2.0-rc.2；2026-10-18 在 0.2.1-rc.2 复测通过）。" +
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

// dshSkillProjection dsh 的 skill 投影（原生形态见下）。
//
// 根的选择：dsh 的 `dsh-skill-filesystem` provider 声明的 user 级根有两个——
// `$DSH_HOME/skills`（"user-dsh"）与 `~/.agents/skills`（"user-agents"）。
// 这里取 `~/.agents/skills`：它与 `$DSH_HOME` 解耦，容器里把 DSH_HOME 改到 `/data/dsh` 时
// 投影根不会跟着漂移（否则默认根在容器形态下必然写错地方）。
//
// **落点必须扁平**：`isPotentialSkillPath` 只认根下 1~2 段——`<root>/<name>.md`（扁平文件）
// 或 `<root>/<name>/SKILL.md`（目录包）；更深的 `**/SKILL.md` **不扫**。
// 故 Layout 只能是 `<id>/SKILL.md`，且内容必须自带 YAML frontmatter（缺 name/description 整条丢弃）。
//
// Tested=true 的依据（两段，缺一不可）：
//  1. **形态**：`<root>/<id>/SKILL.md` + 强制 frontmatter，于 2026-09-09 用 `dsh-skill-filesystem`
//     真实 provider 实测过（无 frontmatter / 只有 name / 只有 description 三种都丢弃，嵌套不扫）
//     ——与上面的「只扫根下一层」互相印证（见 pkg/capability/skill_materialize.go 顶部实测记录）。
//  2. **端到端**：「平台默认根下写进去、底座开箱即读到」，于 2026-10-08 用 dsh **0.2.0-rc.2** 确认（2026-10-18 在 **0.2.1-rc.2** 复测通过）
//     （报告：.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md §B-6）。取证分两路：
//     · **源码链**（读本机安装版本 app.asar 内的实际代码）：`PROFILE_TEMPLATES.acp = [dsh-base, dsh-acp-app]`
//     → `dsh-base@0.2.1-rc.2` 的 `cordis.patch.yml` 常态启用 `id: skill-filesystem` 与 `id: tool-skill`
//     → provider 的 `includeDefaultRoots` 默认 true，rank 500 根 = `join(agentsHome,"skills")`，
//     而 `agentsHome = resolve(config.agentsHome ?? process.env.DSH_AGENTS_HOME ?? join(homedir(),".agents"))`
//     ⇒ 默认落点正是 `${HOME}/.agents/skills`。注意这条链对 **acp / web / headless 三个 profile 同款**
//     （都含 dsh-base），故 acp 与实测用的 desktop 走的是同一份 provider 与同一个工具。
//     · **运行级**：`~/.agents/skills/officecli`（第三方手工放置、无 `.mate-*` 标记、布局与 frontmatter
//     均合规）**只存在于该根**——它在 asar 内 0 命中（非内置，而内置的 office-docx 有 6 命中），
//     却出现在真实 dsh 会话投给模型的 skill 目录里 ⇒ 该根不是"被配置"而是**真被枚举并上图**。
func dshSkillProjection() Projection {
	return Projection{
		Kind:   ProjectionSkill,
		Scope:  ScopeGlobal,
		Root:   "${HOME}/.agents/skills",
		Layout: LayoutIDToken + "/SKILL.md",
		Format: dsh.SkillFormat,
		Tested: true,
		Note: "dsh-skill-filesystem 源码确认 user 级根为 `$DSH_HOME/skills` 与 `~/.agents/skills`，取后者以与 DSH_HOME 解耦；" +
			"**只扫根下一层**（`<n>.md` 或 `<n>/SKILL.md`），故落点必须扁平；frontmatter 必填 name+description 且 " +
			"**name 强制 `^[a-z0-9]+(?:-[a-z0-9]+)*$`（不合规静默丢弃）**；" +
			"形态于 2026-09-09 用真实 provider 实测通过，**默认根端到端于 2026-10-08 用 v0.2.0-rc.2 确认、2026-10-18 用 v0.2.1-rc.2 复测通过**" +
			"（源码链：acp 模板含 dsh-base，其 patch 常态启用 skill-filesystem/tool-skill，agentsHome 默认 `<HOME>/.agents`；" +
			"运行级：仅存在于该根的 officecli 被真实会话投给模型，且非内置）。" +
			"两条边界：① 该根是**最低优先**（rank 500），项目级 `<cwd>/.agents/skills`(200) 与 `~/.dsh/skills`(400) 会遮蔽它；" +
			"② 底座按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析该根，故**任何**注入 `DSH_AGENTS_HOME` 的地方都会让读根与本投影分叉；" +
			"平台的配置隔离**刻意不注入它**（2026-10-09 修：曾注入，E5 实测导致 dsh 会话的 `<available_skills>` 看不到本投影根下的 skill；" +
			"护栏见 `pkg/runtime/isolation_test.go` 的 TestIsolationKeepsSkillRootAlignedWithProjection），但用户若在平台进程环境里设了它仍会静默分叉",
	}
}

// RegisterBuiltinPorts 把各 adapter 的**可执行半场**登记进 `Registry` 的五张执行表
// （syncer + 原生权限投影 + 模型值投影 + 工具失败归类 + 产品身份交付）。
// ADR-2 的分工：`Spec` 只装纯数据，行为一律走这几张表。
//
// **只登记 `r` 里已经在册的 id**（按 id 查表，不新增 Spec）。产品装配由 `ProductRegistry` /
// `AllBuiltinRegistry` 调用；导出它是给"自造 Spec 但要求与产品同形"的调用方（测试夹具、诊断）用的——
// 少了它，自造注册表会**静默少掉执行半场**，表现为"这个底座没有原生权限开关 / 清单交付 unsupported"，
// 而不是报错。同形取舍见 `AllBuiltinRegistry` 的存在理由。
//
// 这是「父包汇入」这条既有约定的延申：adapter 子包提供实现，父包决定它在注册表里的键、**并把已解析的
// 落点交给它**（路径的 `${...}` 占位符只能靠该 runtime 自己的隔离 env 解出来，而 adapter 拿不到注册表
// ——约束⑦）。所以 syncer 的装配形态是"先 `ResolveConfigLocation` → 再把 `Path` 传进去"，
// adapter 永远不自己拼 HOME。
//
// 原生权限、模型值与工具失败归类都不需要落点（只产出 env / 只做字符串投影），因此不受"隔离 env
// 还没应用"的时序影响；身份交付与清单同步共用同一个落点（平台独占根），所以在同一处一起登记。
// 五条登记都做成幂等（同 ID 覆盖），这样装机期重跑一次不会翻倍。
func RegisterBuiltinPorts(r *Registry) {
	if r == nil {
		return
	}
	registerBuiltinSyncers(r)
	registerBuiltinNativePermissions(r)
	registerBuiltinModelValues(r)
	registerBuiltinToolFailures(r)
}

// registerBuiltinModelValues 登记各 adapter 的模型值投影实现（第三张可执行端口表，见 model_value.go）。
//
// 这张表承担的是**同一份生成规则**：`DefaultModelValueFor`（T2 下发）与清单物化 / 对账（T5）
// 都经它取值，所以"平台下发的默认"与"写进底座配置的那条"逐字同源。
//
// 未登记的 id 不回落到任何猜测：调用方据此如实报 `model_value_unsupported` 或"该底座不消费
// 平台清单"——两态都由调用点按语义选，这张表只回答"有没有"。
func registerBuiltinModelValues(r *Registry) {
	if r == nil {
		return
	}
	for _, id := range []string{dsh.ID} {
		if _, ok := r.Get(id); !ok {
			continue
		}
		// 同前两条：保留 `switch id` 形态，**接下一个 adapter = 加一个 case**。
		switch id {
		case dsh.ID:
			r.RegisterModelValue(id, dsh.ModelValue{})
		}
	}
}

// registerBuiltinNativePermissions 登记各 adapter 的原生权限投影实现（第二张可执行端口表）。
//
// 只登记**在册**的那些 id：注册表里没有它（例如 `ProductRegistry` 只装配构建期选中的那一个）时
// 跳过，于是内核那句「该 runtime 无原生权限开关（仅平台拦截面）」是**如实**的——
// 而不是"adapter 有实现但没被登记上"这种假阴性。
func registerBuiltinNativePermissions(r *Registry) {
	if r == nil {
		return
	}
	for _, id := range []string{dsh.ID} {
		if _, ok := r.Get(id); !ok {
			continue
		}
		// 刻意保留 `switch id` 的形态（而不是塌成一条直路）：**接下一个 adapter = 加一个 case**。
		// 塌平之后，下一个底座接进来时改的是控制流而不是加一分支，回归面会大得多。
		switch id {
		case dsh.ID:
			r.RegisterNativePermission(id, dsh.NativePermission{})
		}
	}
}

// registerBuiltinToolFailures 登记各 adapter 的工具失败签名归类（第四张可执行端口表）。
//
// 与前三张同一条讲究：只登记**在册**的 id；刻意保留 `switch id` 形态（接下一个 adapter = 加一个 case）。
func registerBuiltinToolFailures(r *Registry) {
	if r == nil {
		return
	}
	for _, id := range []string{dsh.ID} {
		if _, ok := r.Get(id); !ok {
			continue
		}
		switch id {
		case dsh.ID:
			r.RegisterToolFailureClassifier(id, dsh.ToolFailure{})
		}
	}
}

// registerBuiltinSyncers 把各 adapter 的同步实现登记进 `Registry` 的第一张可执行端口表，
// 同时登记**产品身份交付**（第五张表；两者共用平台独占根与同一个 `--patch` 列表）。
//
// 落点解析失败（声明非法 / 隔离 env 没应用）时**不登记**：此时该 runtime 的 `sync[]` 会如实报
// `unsupported`——比登记一个会写错地方的实现好得多（写错落点等于去动用户自己的目录，隔离纪律的硬红线）。
func registerBuiltinSyncers(r *Registry) {
	if r == nil {
		return
	}
	for _, id := range []string{dsh.ID} {
		spec, ok := r.Get(id)
		if !ok {
			continue
		}
		loc, declared, err := ResolveConfigLocation(spec)
		if err != nil || !declared {
			continue
		}
		// `--patch` 是**可重复**参数，而模型清单与产品身份是两个独立写者。它们必须共用**同一个**
		// `PatchArgs`：各造一个的话，后装的那一方会把另一方从启动参数里挤掉，而两边都报 applied
		// （用户看到"清单已同步、身份已交付"，底座只拿到一层）。
		patches := dsh.NewPatchArgs(r.DeliveryFor(id))
		// 同上：保留 switch 形态，接新底座 = 加一个 case（而不是重写循环体）。
		switch id {
		case dsh.ID:
			r.RegisterSyncer(id, dsh.NewSyncer(dsh.Options{
				PatchPath:         loc.Path,
				PatchArgs:         patches,
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
			// 身份交付与清单同步同住平台独占根（`loc.RootAbs`）：身份 patch 的文件名由 adapter 自己
			// 决定（`mate-identity.patch.yml`），内核只知道"这个 runtime 有交付面"。
			r.RegisterIdentity(id, dsh.NewIdentity(dsh.IdentityOptions{RootAbs: loc.RootAbs}, patches))
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
