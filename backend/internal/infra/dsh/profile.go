package dsh

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ——— 自建 ACP profile：mate-acp ———
//
// 背景（源码依据，见 packages/bundle/{acp-app,sdk-app}/cordis.patch.yml @ 0.2.2-rc.1）：
//   - dsh 的 `acp` 模板 = `dsh-base + dsh-acp-app`，**只含 ACP 传输，不含 office/依赖能力**；
//   - 但 `sdk` 模板的 `dsh-sdk-app` 里**本来就有一段 office 三件套的 insert block**
//     （workspace-dependencies / skill-office / office-to-pdf），并带官方 `disabled: !!js` 门控；
//     那段只依赖 `DSH_PRIMARY_RUNTIME`/`DSH_BUNDLED_OFFICE_*`，与 sdk 传输无耦合。
//   - `skill-office` 插件只 `inject: ['skills']`，而 dsh-base（acp 已含）已提供 skill 注册表 →
//     在 acp 组合上零耦合可挂。
//
// 所以平台不再「代码里手抄 cordis 条目 + 每次 --patch 叠加」（那会随上游漂移、且改坏了 disabled
// 语义），而是：**自建一个持久 profile `mate-acp`（基于 acp 模板），把 office block 声明式地写进
// 它自己的 cordis.patch.yml 覆写层**。组合随 dsh 版本走、声明干净。

// ProfileName 平台自建的 dsh profile 名（基于 acp 模板 + office 能力覆写）。
//
// 必须是独立名字（不能用 `acp`）：`acp` 是随包模板名，app-boot 会按模板初始化、平台不该也改不动它；
// 平台需要自己的一份可写 profile 才能声明 office 能力。
const ProfileName = "mate-acp"

// profilePackageJSON mate-acp 的 package.json：与 acp 模板同款 bundles（base + acp-app），
// 只是 profile 名是 mate-acp。office 能力不在这里加 bundle（它不是独立 bundle），而在 patch 层。
const profilePackageJSON = `{
  "name": "dsh-profile-mate-acp",
  "private": true,
  "dependencies": {},
  "dsh": {
    "profile": {
      "bundles": [
        "@deepseek-ai/dsh-base",
        "@deepseek-ai/dsh-acp-app"
      ]
    }
  }
}
`

// profileCordisYML dsh profile 根（空 entry 列表；树的组合靠 package.json bundles + cordis.patch.yml）。
// 内容逐字取自 dsh 模板生成物。
const profileCordisYML = `# dsh profile root — an empty entry list. The tree is composed as patches:
# each bundle in package.json's dsh.profile.bundles, then cordis.patch.yml, then any
# --patch overlays. Edit cordis.patch.yml, not this file.
[]
`

// profilePnpmWorkspaceYML 逐字取自 dsh 模板生成物。
const profilePnpmWorkspaceYML = `packages:
  - .

nodeLinker: hoisted
autoInstallPeers: false
`

// profilePatchYML mate-acp 的覆写层：把 sdk-app 里那段 office insert block 原样搬来，
// 并补上 sdk-app 里同样挂着的 present / deliverable-file-references（acp 基座缺、ark-mate 也要）。
//
// 三条 `disabled: !!js ...` 门控**逐字照抄官方**（不改成 disabled:false）：平台在 build.go 里
// 保证 `DSH_PRIMARY_RUNTIME`/`DSH_BUNDLED_OFFICE_*` env 就位，门控自然为 enabled；保留官方语义可
// 在 env 缺失时如实降级（skill-office 检查 cli、workspace-dependencies 检查 runtime）而非硬开。
//
// present / deliverable-file-references 在 sdk-app 里**都没有 inject / config**（全用插件自身默认），
// 也**没有 env 门控**（一直启用）：ark-mate 逐字照抄这两个 name，不额外加 disabled。
//   - present：模型产出文件后声明交付物（dsh 侧 `present` 工具），是「office 用 Python exec 写文件、
//     平台只认 write/edit 而漏抽 .pptx/.docx/... 拿不到成品」这条 P0 链的收口——present 上报后平台
//     侧按 rawInput.files 逐个登记（见 infra/workartifact + assembly/artifactextract）。
//   - deliverable-file-references：给模型注入文件链接/交付物提示词（systemPrompt 段）。
//
// 另补 standard preset 独有的两项「上层面工具」（acp 路径无 preset，base 也没有；ark-mate 桌面端
// 能力要与 dsh 桌面端对齐）：
//   - time-context：每步往模型可见历史追加带来源的时钟读数（当前时间/浏览器时区/经过时长），解决
//     "今天几号/本周哪天/几天后"这类相对时间的瞎猜。inject `['agents','sessionProjections']` 均由
//     dsh-base 提供（base/cordis.patch.yml 的 dsh-agent、dsh-session-projection）。
//   - tool-ask-user：给模型 `ask_user_question` 工具主动结构化提问（单选/多选/补充）。它是平台
//     elicitation 卡片通道的模型侧入口；inject `['tools','userQuestions']`——`tools` 与
//     `userQuestions`（`ctx.userQuestions` seam，@deepseek-ai/dsh-user-questions）均由 dsh-base 提供
//     （base/cordis.patch.yml 的 dsh-tools、dsh-user-questions）。缺该工具则平台 elicitation 卡片永远不弹。
//     这两项不依赖随包 env，故与 present/deliverable 一样**不加 disabled 门控**。
//
// 升级 dsh 时：office block + present/deliverable 与 `packages/bundle/sdk-app/cordis.patch.yml` 对齐；
// time-context / tool-ask-user 与 `packages/bundle/web-app/presets/standard.patch.yml` 对齐（字段改了要同步）。
const profilePatchYML = `# ark-mate 生成并维护的 dsh profile 覆写层（平台自有；经 --profile mate-acp 生效）。
# 作用：在 acp 组合（dsh-base + dsh-acp-app）上补 office / workspace-dependencies 内置能力，
#       并补 present / deliverable-file-references（sdk-app 有、acp 基座无），
#       以及 time-context / tool-ask-user（standard preset 独有、acp 基座无）。
# 来源：office block + present/deliverable 逐字搬自 dsh 0.2.2-rc.1 的 packages/bundle/sdk-app/cordis.patch.yml；
#       time-context / tool-ask-user 取自 packages/bundle/web-app/presets/standard.patch.yml。
# 归属标记：本文件只有平台会写。升级 dsh 时与上述 bundle/preset 对齐。
- insert:
    - id: workspace-dependencies
      name: '@deepseek-ai/dsh-tool-workspace-dependencies'
      disabled: !!js "!(process.env.DSH_PRIMARY_RUNTIME ?? process.env.DSH_BUNDLED_PRIMARY_RUNTIME)"
      config:
        source: !!js "process.getBuiltinModule('node:path').resolve(process.env.DSH_PRIMARY_RUNTIME ?? process.env.DSH_BUNDLED_PRIMARY_RUNTIME ?? '')"

    - id: skill-office
      name: '@deepseek-ai/dsh-skill-office'
      disabled: !!js "!(process.env.DSH_PRIMARY_RUNTIME ?? process.env.DSH_BUNDLED_PRIMARY_RUNTIME) && !process.env.DSH_BUNDLED_OFFICE_CLI"
      config:
        assetRoot: !!js "process.env.DSH_BUNDLED_OFFICE_SKILLS ?? process.getBuiltinModule('node:path').resolve(process.env.DSH_PRIMARY_RUNTIME ?? process.env.DSH_BUNDLED_PRIMARY_RUNTIME ?? '', '..', 'office-skills')"
        cli: !!js "process.env.DSH_BUNDLED_OFFICE_CLI === '' ? false : process.env.DSH_BUNDLED_OFFICE_CLI"
        node: !!js "process.env.DSH_BUNDLED_OFFICE_NODE ?? process.getBuiltinModule('node:path').resolve(process.env.DSH_PRIMARY_RUNTIME ?? process.env.DSH_BUNDLED_PRIMARY_RUNTIME ?? '', 'dependencies', 'node', 'bin', process.platform === 'win32' ? 'node.exe' : 'node')"

    - id: office-to-pdf
      name: '@deepseek-ai/dsh-office-to-pdf'

    # present / deliverable-file-references：照 sdk-app 原样（无 inject / config / env 门控，一直启用）。
    - id: present
      name: '@deepseek-ai/dsh-tool-present'

    - id: deliverable-file-references
      name: '@deepseek-ai/dsh-deliverable-file-references'

    # time-context / tool-ask-user：standard preset 独有的上层面工具（acp 无 preset、base 也没有）。
    # 两项 inject 依赖均由 dsh-base 满足，不依赖随包 env，故不加 disabled 门控（同 present/deliverable）。
    - id: time-context
      name: '@deepseek-ai/dsh-time-context'

    - id: tool-ask-user
      name: '@deepseek-ai/dsh-tool-ask-user'
`

// answererPatchEntry 生成 answerer 插件的 patch 条目（追加在覆写层末尾）。
//
// 为什么 `name` 用**绝对路径**而不是包名：profile 目录没有 node_modules，loader 的 ESM 解析器
// 锚在 profile 的 package scope 上，按包名会解析不到 ark-mate 自带的插件；而绝对路径
// （`file://` URL 或绝对文件路径）被 `import.meta.resolve` 原样接受，绕过 node_modules。
// 这与「answerer 作为 ark-mate 自有资产、不改 dsh 载荷」的定稿（设计 §8.2）一致。
//
// 门控：`disabled: !!js` 沿用同一套 env 判定（沿用 office 已证的写法）——插件路径为空或
// 端点未注入时如实关闭，dsh 自然回到 NO_PROVIDER（不误开、不拒启）。路径与端点都由平台在
// 拉起底座前经 `ProvisionProfile` + `PrepareRuntimeEnv` 注入/写入。
func answererPatchEntry(pluginPath string) string {
	if strings.TrimSpace(pluginPath) == "" {
		// 路径未解析到：不写条目（与 office 的「缺资产如实关闭」同口径——少一个能力，不毁会话）。
		return ""
	}
	// YAML 单引号转义：路径里的单引号加倍。Windows 反斜杠在单引号标量里不需要转义。
	quoted := "'" + strings.ReplaceAll(pluginPath, "'", "''") + "'"
	return `
    # answerer：把 dsh ask_user_question 的 userQuestions 请求桥接到平台 elicitation 卡片通道
    # （answerer 桥接；设计 2026-11-11-ask-user-answerer-bridge-design.md）。name 用绝对路径
    # 绕过 profile 无 node_modules 的解析问题；端点/令牌经 env 注入，缺任一则 disabled。
    - id: ark-mate-answerer
      name: ` + quoted + `
      disabled: !!js "!process.env.MATE_ANSWERER_ENDPOINT"
`
}

// buildProfilePatchYML 拼出完整覆写层：静态块 + 动态 answerer 条目。
func buildProfilePatchYML(pluginPath string) string {
	return profilePatchYML + answererPatchEntry(pluginPath)
}

// profileFiles 是 mate-acp profile 的四份静态文件（逐字与 dsh 模板生成物一致 + 覆写层）。
func profileFiles(pluginPath string) map[string]string {
	return map[string]string{
		"package.json":        profilePackageJSON,
		"cordis.yml":          profileCordisYML,
		"cordis.patch.yml":    buildProfilePatchYML(pluginPath),
		"pnpm-workspace.yaml": profilePnpmWorkspaceYML,
	}
}

// AnswererEnvKeys answerer 桥接插件读取的环境变量（注入 dsh Launch.Env）。
var AnswererEnvKeys = []string{
	"MATE_ANSWERER_ENDPOINT",
	"MATE_ANSWERER_TOKEN",
	"MATE_ANSWERER_PLUGIN",
}

// AnswererEnv 由平台自身监听端点与 answerer 插件路径派生 3 个 KEY=VALUE，供注入 dsh 启动环境：
//   - MATE_ANSWERER_ENDPOINT：平台回环 base URL（answerer 插件 POST /v1/mate/sessions/-/elicitations/ask）；
//   - MATE_ANSWERER_TOKEN：每进程随机令牌（平台侧校验；缺则插件拒绝桥接，如实失败）；
//   - MATE_ANSWERER_PLUGIN：插件入口绝对路径（同时供 ProvisionProfile 写进覆写层 answerer 条目）。
//
// 插件路径解析不到 → 返回 nil（不注入，覆写层 disabled，dsh 自然 NO_PROVIDER，不误开）。
// 端点为空（如测试/无地址场景）→ 也返回 nil（answerer 如实关闭，不误开）。
func AnswererEnv(selfEndpoint string) []string {
	env, _ := AnswererEnvWithToken(selfEndpoint)
	return env
}

// AnswererEnvWithToken 同 AnswererEnv，但额外把生成的令牌原样返回，供**服务端保存并比对**。
//
// 令牌此前只活在注入给 dsh 的 env 里，平台自己没留副本，于是服务端做不了强校验；这里把
// 「注入的那一份」与「服务端保存的那一份」收成同一处生成（同一次 AnswererEnv 调用产出的
// 唯一值），杜绝"两边各生成一个、永远对不上"的静默失效。返回的 token 在未启用时为空串。
func AnswererEnvWithToken(selfEndpoint string) (env []string, token string) {
	ep := strings.TrimSpace(selfEndpoint)
	plugin := ResolveAnswererPlugin()
	if ep == "" || plugin == "" {
		return nil, ""
	}
	token = genAnswererToken()
	return []string{
		"MATE_ANSWERER_ENDPOINT=" + ep,
		"MATE_ANSWERER_TOKEN=" + token,
		"MATE_ANSWERER_PLUGIN=" + plugin,
	}, token
}

// genAnswererToken 生成每进程一次性令牌（16 字节 hex）。仅本机回环、进程生命周期内有效，
// 是 OriginPolicy 之外的一层 defense-in-depth。
func genAnswererToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 极罕见：随机源失败时退回时间派生（仍本机、仍短生命周期），绝不返回空串（空串=不校验）。
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// 优先 `MATE_ANSWERER_PLUGIN`（桌面壳随包资产路径，生产路径）；否则由 `MATE_DSH_ENTRY` 上溯到
// payloads 根下 `ark-mate-answerer/lib/index.js`（给自装配/测试用）。都不成立返回空串 →
// profile 不写 answerer 条目（如实关闭，不误开）。校验文件存在。
func ResolveAnswererPlugin() string {
	if p := strings.TrimSpace(os.Getenv("MATE_ANSWERER_PLUGIN")); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
		return ""
	}
	entry := strings.TrimSpace(os.Getenv("MATE_DSH_ENTRY"))
	if entry == "" {
		return ""
	}
	// entry = <payloads>/dsh/<ver>/node_modules/@deepseek-ai/dsh/lib/bin.js
	// 上溯 6 层到 <payloads>/dsh → 上一级 <payloads>，追问 ark-mate-answerer/lib/index.js。
	p := entry
	for i := 0; i < 6; i++ {
		p = filepath.Dir(p)
	}
	payloadsRoot := filepath.Dir(p)
	candidate := filepath.Join(payloadsRoot, "ark-mate-answerer", "lib", "index.js")
	if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
		return candidate
	}
	return ""
}

// ProfileDir 返回 mate-acp profile 的落点：`<DSH_HOME>/profiles/mate-acp`。
//
// DSH_HOME 由平台的隔离声明注入为 `<HomeDir>/runtime/dsh`（见 adapter.go 的 Isolation），
// 所以这里由 homeDir（平台的 MATE_HOME）拼出来，不自己猜环境变量。
func ProfileDir(homeDir string) string {
	return filepath.Join(homeDir, "runtime", "dsh", "profiles", ProfileName)
}

// ProvisionProfile 幂等地把 mate-acp profile 的四份文件写到 `<DSH_HOME>/profiles/mate-acp`。
// 语义：
//   - 文件内容与期望逐字相同 → 不重写（幂等；不制造无谓 mtime 变化）；
//   - 缺失或不同 → 写（平台独占这份 profile，以平台内容为准；用户手改会被覆盖——这是"平台自有"的
//     代价，与 mate-models/mate-identity overlay 同款归属纪律）。
//
// 返回实际写了几个文件（诊断/日志用）。任何 IO 失败都返回错误，由调用方决定是否致命——按项目约定
// "缺能力不该拒绝启动"，调用方通常只记 WARN。
func ProvisionProfile(homeDir string) (int, error) {
	home := strings.TrimSpace(homeDir)
	if home == "" {
		return 0, fmt.Errorf("provision mate-acp profile: homeDir 为空")
	}
	return provisionProfileAtDshHome(filepath.Join(home, "runtime", "dsh"))
}

// provisionProfileAtDshHome 把 mate-acp 四份文件写到 `<dshHome>/profiles/mate-acp`。
//
// 与 ProvisionProfile 的区别只在「根」：ProvisionProfile 的根是 MATE_HOME（DSH_HOME 由
// `<MATE_HOME>/runtime/dsh` 拼出）；本函数直接吃 DSH_HOME。抽出来是为了让**底座级校验门**
// 能把同一份 profile 预置到它自己的 staging `DSH_HOME`（见 config.go 的 validate）——校验门在
// 全新临时 DSH_HOME 上跑，若那里没有 mate-acp profile，dsh 会以「profile 不存在」拒启（exit 1），
// 于是每份候选都被误判为「无法校验」。这是 mate-acp（非内置模板名）引入后必须配套的一步。
func provisionProfileAtDshHome(dshHome string) (int, error) {
	dir := filepath.Join(dshHome, "profiles", ProfileName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("创建 mate-acp profile 目录失败（%s）：%w", dir, err)
	}
	written := 0
	pluginPath := ResolveAnswererPlugin()
	for name, content := range profileFiles(pluginPath) {
		path := filepath.Join(dir, name)
		changed, err := writeIfChanged(path, []byte(content))
		if err != nil {
			return written, fmt.Errorf("写 mate-acp profile 文件失败（%s）：%w", path, err)
		}
		if changed {
			written++
		}
	}
	return written, nil
}

// BundledRuntimeEnvKeys mate-acp 覆写层里 office 三件套依赖的 4 个随包 runtime env。
var BundledRuntimeEnvKeys = []string{
	"DSH_PRIMARY_RUNTIME",
	"DSH_BUNDLED_OFFICE_SKILLS",
	"DSH_BUNDLED_OFFICE_CLI",
	"DSH_BUNDLED_OFFICE_NODE",
}

// resolveOfficeCLI 由 office skills assets 目录推导随包 libreoffice-kit 的 cli.js 绝对路径。
//
// 目录布局（见 ResolveBundledDirs 里 officeSkills = <dsh载荷>/node_modules/@deepseek-ai/dsh-skill-office/assets）：
//
//	officeSkills                        = <载荷>/node_modules/@deepseek-ai/dsh-skill-office/assets
//	filepath.Dir(officeSkills)          = .../dsh-skill-office
//	filepath.Dir ×2                     = .../@deepseek-ai
//	filepath.Dir ×3                     = .../node_modules
//	=> <载荷>/node_modules/@deepseek-ai/libreoffice-kit/lib/cli.js
//
// dsh 的 skill-office 插件用 statSync(cli).isFile() 校验该值，**必须是文件**（目录会让 isFile() 为
// false 而抛错，导致 office-render/office-recalculate/office-export-pdf 三个 skill 加载失败）。
// 解析不到（文件不在场）时返回空串：由 BundledEnv 显式置空 = 显式关闭 office CLI（语义正确，不误抛错），
// office skills 本身仍可加载（assetRoot 照旧指向 skills 目录）。
func resolveOfficeCLI(officeSkills string) string {
	nmDir := filepath.Dir(filepath.Dir(filepath.Dir(officeSkills))) // .../node_modules
	cli := filepath.Join(nmDir, "@deepseek-ai", "libreoffice-kit", "lib", "cli.js")
	if fi, err := os.Stat(cli); err != nil || fi.IsDir() {
		return ""
	}
	return cli
}

// ResolveOfficeCLI 是 resolveOfficeCLI 的**导出版**：由 office skills assets 目录推导随包
// libreoffice-kit 的 cli.js 绝对路径，供「预览时把 office 转 PDF」的服务端转换复用同一处解析
// （不再复制一份推导逻辑，避免升级 dsh 时两处漂移）。解析不到（文件不在场）时返回空串。
func ResolveOfficeCLI(officeSkills string) string {
	osDir := strings.TrimSpace(officeSkills)
	if osDir == "" {
		return ""
	}
	return resolveOfficeCLI(osDir)
}

// ResolveOfficeNode 由 primary-runtime 载荷目录推导随包 node 可执行文件绝对路径，供服务端调用
// libreoffice-kit CLI 用（与 mate-acp 覆写层里 `DSH_BUNDLED_OFFICE_NODE` 的推导口径一致：
// `<primaryRuntime>/dependencies/node/bin/node`）。文件不在场时返回空串。
func ResolveOfficeNode(primaryRuntime string) string {
	pr := strings.TrimSpace(primaryRuntime)
	if pr == "" {
		return ""
	}
	node := filepath.Join(pr, "dependencies", "node", "bin", "node")
	if fi, err := os.Stat(node); err != nil || fi.IsDir() {
		return ""
	}
	return node
}

// BundledEnv 由 ark-mate **自有**的两处随包资产目录派生 4 个 KEY=VALUE，供注入 dsh 启动环境——
// mate-acp 覆写层的 `!!js` 门控与 config 的 getter 读它们。
//
//   - primaryRuntime：ark-mate 自带的 primary-runtime 载荷目录（含 `runtime.json` 与 `dependencies/`，
//     内 bundled Python/Node），对应 `DSH_PRIMARY_RUNTIME`；
//   - officeSkills：ark-mate 自带 payload 里的 `@deepseek-ai/dsh-skill-office/assets` 目录（含 office
//     七个 skill 的 SKILL.md），对应 `DSH_BUNDLED_OFFICE_SKILLS`；
//   - `DSH_BUNDLED_OFFICE_CLI` 由 officeSkills 同级 @deepseek-ai 下的 libreoffice-kit cli.js 推导
//     （必须是**文件**，见 resolveOfficeCLI）；解析不到 → 显式置空串（关闭 office CLI）；
//   - `DSH_BUNDLED_OFFICE_NODE` 从 primaryRuntime 下的 node 推导。
//
// **绝不从 DSH 桌面端取**：两个目录都必须由 ark-mate 自己的 payload 提供；任一为空 → 返回 nil
// （覆写层门控顺带把 office 关掉 = 如实降级，不借用外部桌面应用）。
func BundledEnv(primaryRuntime, officeSkills string) []string {
	pr := strings.TrimSpace(primaryRuntime)
	osDir := strings.TrimSpace(officeSkills)
	if pr == "" || osDir == "" {
		return nil
	}
	node := filepath.Join(pr, "dependencies", "node", "bin", "node")
	return []string{
		"DSH_PRIMARY_RUNTIME=" + pr,
		"DSH_BUNDLED_OFFICE_SKILLS=" + osDir,
		"DSH_BUNDLED_OFFICE_CLI=" + resolveOfficeCLI(osDir),
		"DSH_BUNDLED_OFFICE_NODE=" + node,
	}
}

// ResolveBundledDirs 解析 ark-mate **自有**的两处随包资产目录（primary-runtime 与 office skill assets）。
//
// 来源优先级（全部限定在 ark-mate 自己的安装面，**绝不回落到 DSH 桌面端**）：
//  1. 显式 env：`MATE_DSH_BUNDLED_RUNTIME`（primary-runtime 目录）、`MATE_DSH_BUNDLED_OFFICE_SKILLS`
//     （office skills 目录）——给运维/测试精确指定；
//  2. 由 `MATE_DSH_ENTRY`（Electron 壳传给核心的 dsh 载荷入口 `<payloads>/dsh/<ver>/node_modules/
//     @deepseek-ai/dsh/lib/bin.js`）推导：
//     - office skills = 同级 `@deepseek-ai/dsh-skill-office/assets`（在 dsh 载荷里，ark-mate 自带）；
//     - primary-runtime = `<payloads>/primary-runtime/<ver>/`（ark-mate 自带的独立载荷）。
//
// 任一目录解析不到（且没有显式 env）→ 该目录返回空；BundledEnv 会据此跳过注入（office 关闭）。
func ResolveBundledDirs() (primaryRuntime, officeSkills string) {
	primaryRuntime = strings.TrimSpace(os.Getenv("MATE_DSH_BUNDLED_RUNTIME"))
	officeSkills = strings.TrimSpace(os.Getenv("MATE_DSH_BUNDLED_OFFICE_SKILLS"))
	entry := strings.TrimSpace(os.Getenv("MATE_DSH_ENTRY"))
	if entry == "" {
		return primaryRuntime, officeSkills
	}
	// entry = <payloads>/dsh/<ver>/node_modules/@deepseek-ai/dsh/lib/bin.js
	dshPkgDir := filepath.Dir(filepath.Dir(entry)) // .../@deepseek-ai/dsh
	aiDir := filepath.Dir(dshPkgDir)               // .../@deepseek-ai
	if officeSkills == "" {
		officeSkills = filepath.Join(aiDir, "dsh-skill-office", "assets")
	}
	if primaryRuntime == "" {
		// <payloads>/dsh/<ver>/node_modules/@deepseek-ai/dsh → 上溯到 <payloads>/dsh/<ver> → <payloads>
		dshPayloadVer := filepath.Dir(filepath.Dir(aiDir)) // .../@deepseek-ai/dsh → skip: aiDir is @deepseek-ai
		_ = dshPayloadVer
		// 从 entry 找到 `.../payloads/dsh/<ver>/`：node_modules/@deepseek-ai/dsh/lib/bin.js
		// 向上: lib → dsh → @deepseek-ai → node_modules → <ver> → dsh → payloads
		p := entry
		for i := 0; i < 6; i++ {
			p = filepath.Dir(p)
		}
		// p 现在是 <payloads>/dsh
		payloadsRoot := filepath.Dir(p)
		primaryRuntime = filepath.Join(payloadsRoot, "primary-runtime")
	}
	// 校验存在性：不存在则清空，避免注入错路径（底座会 ENOENT）。
	if primaryRuntime != "" {
		if fi, err := os.Stat(filepath.Join(primaryRuntime, "runtime.json")); err != nil || fi.IsDir() {
			primaryRuntime = ""
		}
	}
	if officeSkills != "" {
		if fi, err := os.Stat(filepath.Join(officeSkills, "office-docx", "SKILL.md")); err != nil || fi.IsDir() {
			officeSkills = ""
		}
	}
	return primaryRuntime, officeSkills
}
