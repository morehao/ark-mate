package dsh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// profilePatchYML mate-acp 的覆写层：把 sdk-app 里那段 office insert block 原样搬来。
//
// 三条 `disabled: !!js ...` 门控**逐字照抄官方**（不改成 disabled:false）：平台在 build.go 里
// 保证 `DSH_PRIMARY_RUNTIME`/`DSH_BUNDLED_OFFICE_*` env 就位，门控自然为 enabled；保留官方语义可
// 在 env 缺失时如实降级（skill-office 检查 cli、workspace-dependencies 检查 runtime）而非硬开。
//
// 升级 dsh 时：与 `packages/bundle/sdk-app/cordis.patch.yml` 那段对齐（字段改了要同步）。
const profilePatchYML = `# ark-mate 生成并维护的 dsh profile 覆写层（平台自有；经 --profile mate-acp 生效）。
# 作用：在 acp 组合（dsh-base + dsh-acp-app）上补 office / workspace-dependencies 内置能力。
# 来源：逐字搬自 dsh 0.2.2-rc.1 的 packages/bundle/sdk-app/cordis.patch.yml（office block）。
# 归属标记：本文件只有平台会写。升级 dsh 时与该 bundle 的 office block 对齐。
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
`

// profileFiles 是 mate-acp profile 的四份静态文件（逐字与 dsh 模板生成物一致 + 覆写层）。
func profileFiles() map[string]string {
	return map[string]string{
		"package.json":        profilePackageJSON,
		"cordis.yml":          profileCordisYML,
		"cordis.patch.yml":    profilePatchYML,
		"pnpm-workspace.yaml": profilePnpmWorkspaceYML,
	}
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
	dir := ProfileDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("创建 mate-acp profile 目录失败（%s）：%w", dir, err)
	}
	written := 0
	for name, content := range profileFiles() {
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

// BundledEnv 由「随包 runtime 根」（即 dsh 的 `Resources/runtime`）派生 4 个 KEY=VALUE，供注入
// 该底座的启动环境——mate-acp 覆写层的 `!!js` 门控与 config 的 getter 读它们。
//
// 约定目录布局（与 DeepSeek Harness 随包一致）：
//
//	<runtimeRoot>/primary-runtime                              —— DSH_PRIMARY_RUNTIME
//	<runtimeRoot>/office-skills                               —— DSH_BUNDLED_OFFICE_SKILLS
//	<runtimeRoot>/office-skills                               —— DSH_BUNDLED_OFFICE_CLI（office CLI 资产根）
//	<runtimeRoot>/primary-runtime/dependencies/node/bin/node   —— DSH_BUNDLED_OFFICE_NODE
//
// runtimeRoot 为空 → 返回 nil（调用方据此跳过注入，覆盖层门控随之把 office 关掉=如实降级）。
func BundledEnv(runtimeRoot string) []string {
	root := strings.TrimSpace(runtimeRoot)
	if root == "" {
		return nil
	}
	primary := filepath.Join(root, "primary-runtime")
	officeSkills := filepath.Join(root, "office-skills")
	node := filepath.Join(primary, "dependencies", "node", "bin", "node")
	return []string{
		"DSH_PRIMARY_RUNTIME=" + primary,
		"DSH_BUNDLED_OFFICE_SKILLS=" + officeSkills,
		"DSH_BUNDLED_OFFICE_CLI=" + officeSkills,
		"DSH_BUNDLED_OFFICE_NODE=" + node,
	}
}
