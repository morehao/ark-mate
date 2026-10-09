package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/ark-mate/pkg/runtime/deepseekharness"
	"github.com/morehao/ark-mate/pkg/runtime/opencode"
)

// launcherIsolationRE 从 `gen-runtime-bin.mjs` 的一个启动器条目里抠出 `isolation: { … }` 的键名。
var (
	launcherIsolationRE = regexp.MustCompile(`(?s)isolation:\s*\{([^}]*)\}`)
	launcherEnvKeyRE    = regexp.MustCompile(`([A-Z][A-Z0-9_]*)\s*:`)
	launcherRuntimeOf   = map[string]string{"dsh": deepseekharness.ID, "opencode": opencode.ID}
)

// smokeKeyRE 抠出 V9 探针隔离表里的变量名：字面量里的 `KEY: value` 与后续的 `isolation.KEY = value`。
var (
	smokeAssignRE  = regexp.MustCompile(`isolation\.([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	smokeLiteralRE = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`)
)

// launcherCoreOnly 启动器**刻意**不兜底的变量（核心注入，启动器不兜底是有意的）。
//
// 为什么要有这张登记表而不是"核心有、启动器没有就放过"：那样核心一旦新增变量，JS 侧漏跟就是静默的
// ——壳直接跑随包载荷的路径上没人再兜底它，去读写用户真实目录，而两侧的守卫都绿。登记在这里意味着
// "这条路径不需要它，理由如下"，新增变量必须先二选一（补进启动器 / 在此登记），否则守卫红。
var launcherCoreOnly = map[string]map[string]bool{
	// npm 面：MCP server 常经 npx 拉起，那是**核心**拉子进程时要隔离的东西；壳直接跑载荷不经过 npx。
	deepseekharness.ID: {"npm_config_cache": true, "npm_config_prefix": true},
	opencode.ID: {
		"npm_config_cache": true, "npm_config_prefix": true,
		// 通用 XDG 三兄弟：改它们会连累用户其它工具，只在核心按 runtime 拉 ACP 时注入
		// （`gen-runtime-bin.mjs` 里那条注释写了同样的理由）。opencode **专属**的
		// `XDG_STATE_HOME`/`OPENCODE_CONFIG_DIR`/`OPENCODE_DB` 不在此列——那三条启动器必须兜底。
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	},
}

// TestDesktopSmokeIsolationMatchesCore V9 桌面冒烟探针自带的隔离表必须与**核心隔离表逐条对齐**（两侧相等）。
//
// 同一张表在仓库里存在**三份**（Go 核心 `pkg/runtime/isolation.go`、桌面私有启动器
// `apps/desktop/scripts/gen-runtime-bin.mjs`、桌面探针 `frontend/scripts/desktop-runtime-smoke.mjs`）。
// 第三份此前没有守卫——而它恰恰是**最会碰本机**的那一份：探针直接以子进程拉起随包载荷，
// 隔离表写漏一个变量的后果就是"验收脚本自己去读写用户的 `~/.dsh` / `~/.local/state/opencode`"，
// 表现为用户环境被改动，而探针自报通过。2026-10-09 opencode v2 的 `XDG_STATE_HOME` 就是这么被
// 找出来的（探针侧当时也漏了，同批补上）。
//
// 方向与启动器那条**刻意不同**：探针自己就是注入者，所以两边**相等**才是对的口径——只查"探针 ⊆ 核心"
// 会漏掉真正危险的那一向（核心新加了变量、探针没跟 ⇒ 探针去碰用户真实 home，而子集判定照样绿，
// 本守卫第一版就是这个毛病，反向验证时才发现）。
func TestDesktopSmokeIsolationMatchesCore(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "scripts", "desktop-runtime-smoke.mjs")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到桌面冒烟探针 %s: %v（路径改了请同步本守卫）", path, err)
	}
	src := string(raw)

	// 只取 `const isolation = { … }` 到"逐条落进 env"那行之间的块——块外的同名变量不属于这张表。
	start := strings.Index(src, "const isolation = {")
	end := strings.Index(src, "Object.entries(isolation)")
	if start < 0 || end <= start {
		t.Fatalf("探针隔离表的结构变了（找不到 `const isolation = {` 或 `Object.entries(isolation)`），"+
			"请修本守卫而不是放宽它：%s", path)
	}
	block := src[start:end]

	// 以 runtime 分支切段：公共段（所有本地 runtime 都给）+ dsh 段 + opencode（else）段。
	dshAt := strings.Index(block, "if (runtimeId === 'dsh')")
	elseAt := strings.Index(block, "} else {")
	if dshAt < 0 || elseAt <= dshAt {
		t.Fatalf("探针隔离表的 runtime 分支结构变了（找不到 `if (runtimeId === 'dsh')` / `} else {`），"+
			"请修本守卫而不是放宽它：%s", path)
	}
	common := block[:dshAt]
	dshPart := block[dshAt:elseAt]
	ocPart := block[elseAt:]

	keysOf := func(s string) []string {
		seen := map[string]bool{}
		out := []string{}
		for _, km := range smokeAssignRE.FindAllStringSubmatch(s, -1) {
			if !seen[km[1]] {
				seen[km[1]] = true
				out = append(out, km[1])
			}
		}
		for _, km := range smokeLiteralRE.FindAllStringSubmatch(s, -1) {
			if !seen[km[1]] {
				seen[km[1]] = true
				out = append(out, km[1])
			}
		}
		return out
	}

	home := t.TempDir()
	allowedOf := func(runtimeID string) map[string]bool {
		allowed := map[string]bool{}
		for _, kv := range runtime.IsolationEnv(runtimeID, home) {
			allowed[strings.SplitN(kv, "=", 2)[0]] = true
		}
		return allowed
	}
	dshCore, ocCore := allowedOf(deepseekharness.ID), allowedOf(opencode.ID)

	// 探针的**有效**隔离表 = 公共面 ∪ 该 runtime 的分支（脚本里就是这两段合起来落进 env 的）。
	commonKeys := keysOf(common)
	if len(commonKeys) == 0 {
		t.Fatalf("探针隔离表的公共面解析出 0 个变量（表格式变了，请修本守卫而不是放宽它）：%s", path)
	}
	// 公共面必须两边都认（npm 面是所有本地 runtime 共用的那一层）。
	for _, k := range commonKeys {
		if !dshCore[k] || !ocCore[k] {
			t.Errorf("探针的公共面变量 %s 只在部分 runtime 的核心隔离表里（dsh=%v opencode=%v）："+
				"公共面要么两边都给，要么挪进对应分支", k, dshCore[k], ocCore[k])
		}
	}
	for _, c := range []struct {
		label     string
		part      string
		core      map[string]bool
		runtimeID string
	}{
		{"dsh 分支", dshPart, dshCore, deepseekharness.ID},
		{"opencode 分支", ocPart, ocCore, opencode.ID},
	} {
		branchKeys := keysOf(c.part)
		if len(branchKeys) == 0 {
			t.Errorf("探针隔离表的%s解析出 0 个变量（表格式变了，请修本守卫而不是放宽它）：%s", c.label, path)
			continue
		}
		probe := map[string]bool{}
		for _, k := range append(append([]string{}, commonKeys...), branchKeys...) {
			probe[k] = true
		}
		// ① 探针多出来的：核心不认的变量 —— 探针会在生产没有的隔离下通过，把差异藏起来。
		for _, k := range append(commonKeys, branchKeys...) {
			if !c.core[k] {
				t.Errorf("V9 探针的%s设了 %s，但核心的隔离表（%s）不注入它——探针跑在比生产更隔离的环境里，"+
					"会把「生产其实没隔离」这件事盖住。要么两侧一起加，要么两侧一起删", c.label, k, c.runtimeID)
			}
		}
		// ② 核心有、探针漏的：**危险的那一向**——探针直接以子进程拉起随包载荷，漏一个 home 变量就是
		// 「验收脚本自己动了用户环境」而探针自报通过（2026-10-09 的 XDG_STATE_HOME 就是这个形态）。
		for _, kv := range runtime.IsolationEnv(c.runtimeID, home) {
			k := strings.SplitN(kv, "=", 2)[0]
			if !probe[k] {
				t.Errorf("核心的隔离表（%s）注入了 %s，但 V9 探针的%s没有——探针直接以子进程拉起随包载荷，"+
					"漏掉它就意味着**验收脚本自己去读写用户的真实 home**（`~/.dsh` / `~/.local/state/opencode` 这类），"+
					"而探针自报通过。探针侧必须逐条对齐 IsolationEnv", c.runtimeID, k, c.label)
			}
		}
	}
}

// TestDesktopLauncherIsolationMatchesCore 桌面私有启动器的隔离表必须是**核心隔离表的子集**。
//
// 为什么要有这条守卫：同一张"哪个底座认哪些 home 变量"的表在仓库里存在**两份**——
//   - Go 侧 `pkg/runtime/isolation.go`：核心拉 ACP runtime 时注入（IsolationEnv）；
//   - JS 侧 `frontend/apps/desktop/scripts/gen-runtime-bin.mjs`：壳直接跑随包载荷时在启动器里兜底
//     （`${VAR:=…}`，显式值优先，所以两条路径本应得到同一份 home）。
//
// 两处手抄同一张表，改了 A 忘了 B 的后果是**静默**的：2026-10-09 就是核心侧按"agents 面不是底座
// 私有配置、不该隔离"修掉 `DSH_AGENTS_HOME` 之后，私有启动器仍在兜底它，于是**桌面态** dsh 的 skill
// 读根继续与平台写根（`${HOME}/.agents/skills`）分叉、skill 不投放，而平台侧一切自报正常——直到
// 桌面态验收拿真请求体才看见。
//
// 方向刻意是**单向**的（启动器 ⊆ 核心）：启动器只许兜底核心也认的变量。核心认而启动器不兜底是
// 允许的（那些由核心显式注入，例如 XDG 那批"全局改会连累用户其它工具"的变量）——但**必须显式登记**
// 在 `launcherCoreOnly` 里：核心新增变量时"启动器没跟"同样是静默漏点（壳直接跑载荷的路径没人兜底，
// 去读写用户真实目录），所以新增变量要么补进启动器、要么登记豁免，没有第三条路。
func TestDesktopLauncherIsolationMatchesCore(t *testing.T) {
	path := filepath.Join(frontendRoot(t), "apps", "desktop", "scripts", "gen-runtime-bin.mjs")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到桌面私有启动器生成器 %s: %v（路径改了请同步本守卫）", path, err)
	}

	// 按 `name: '<launcher>'` 切分条目：每个条目里最多一个 isolation 表。
	parsed := map[string][]string{}
	for _, part := range strings.Split(string(raw), "name: '")[1:] {
		end := strings.Index(part, "'")
		if end < 0 {
			t.Fatalf("启动器条目缺 name 结尾引号：%q", part[:min(len(part), 40)])
		}
		name := part[:end]
		m := launcherIsolationRE.FindStringSubmatch(part)
		if m == nil {
			continue
		}
		keys := []string{}
		for _, km := range launcherEnvKeyRE.FindAllStringSubmatch(m[1], -1) {
			keys = append(keys, km[1])
		}
		if len(keys) == 0 {
			t.Fatalf("启动器 %s 的 isolation 表解析出 0 个变量（表格式变了，请修本守卫而不是放宽它）", name)
		}
		parsed[name] = keys
	}

	// fail-closed：条目或隔离表数量变了，说明生成器结构动了，本守卫的解析前提不再成立。
	if len(parsed) != len(launcherRuntimeOf) {
		t.Fatalf("解析出 %d 个带隔离表的启动器 %v，期望 %d 个（新增/删除启动器请在此登记它的 runtime 归属）",
			len(parsed), parsed, len(launcherRuntimeOf))
	}

	home := t.TempDir()
	for name, keys := range parsed {
		runtimeID, ok := launcherRuntimeOf[name]
		if !ok {
			t.Fatalf("启动器 %q 未登记 runtime 归属：新增启动器必须在这里登记，否则无法判断它兜底的变量是否与核心一致", name)
		}
		coreKeys := map[string]bool{}
		for _, kv := range runtime.IsolationEnv(runtimeID, home) {
			coreKeys[strings.SplitN(kv, "=", 2)[0]] = true
		}
		launcherKeys := map[string]bool{}
		for _, k := range keys {
			launcherKeys[k] = true
			if !coreKeys[k] {
				t.Errorf("桌面启动器 %s 兜底了 %s，但核心的隔离表（%s）不再注入它——两侧手抄同一张表时分叉的典型形态。"+
					"`DSH_AGENTS_HOME` 这类「根变量」一旦只存在于一侧，就会让底座的读根与平台写根悄悄分叉、skill 静默不投放；"+
					"要么两侧一起加，要么两侧一起删", name, k, runtimeID)
			}
		}
		// 反方向：核心新加了变量、JS 侧没跟。对启动器这条路径来说这一向同样是"动本机"（壳直接跑载荷时
		// 没人再兜底它），所以核心新增的变量必须**显式二选一**：补进启动器，或在 launcherCoreOnly 里
		// 登记"这条路径刻意不兜底"（登记处写清理由）。
		for _, kv := range runtime.IsolationEnv(runtimeID, home) {
			k := strings.SplitN(kv, "=", 2)[0]
			if launcherKeys[k] || launcherCoreOnly[runtimeID][k] {
				continue
			}
			t.Errorf("核心的隔离表（%s）注入了 %s，但桌面启动器 %s 既没兜底、也没在 launcherCoreOnly 里登记豁免："+
				"壳直接跑随包载荷时这条变量就没人兜底（写漏 home 变量 = 去读写用户真实目录）。"+
				"要么补进启动器，要么在 launcherCoreOnly 里写明为什么这条路径不需要它", runtimeID, k, name)
		}
	}
}
