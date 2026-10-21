package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/runtime"
)

// launcherIsolationRE 从 `gen-runtime-bin.mjs` 的一个启动器条目里抠出 `isolation: { … }` 的键名。
var (
	launcherIsolationRE = regexp.MustCompile(`(?s)isolation:\s*\{([^}]*)\}`)
	launcherEnvKeyRE    = regexp.MustCompile(`([A-Z][A-Z0-9_]*)\s*:`)
	// 启动器名 → runtime id。**启动器名（私有 bin 里的文件名）与 Spec.ID 是两个东西**：
	// 它们今天恰好同名，但这层映射刻意留着不省——省掉之后"启动器叫什么"就只能靠两边猜。
	// 新增一个随包底座时在这里登记，`len(parsed) != len(launcherRuntimeOf)` 那条 fail-closed 会逼你登记。
	launcherRuntimeOf = map[string]string{"dsh": dsh.ID}
)

// smokeKeyRE 抠出 V9 探针隔离表里的变量名：字面量里的 `KEY: value` 与后续的 `isolation.KEY = value`。
var (
	smokeAssignRE  = regexp.MustCompile(`isolation\.([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	smokeLiteralRE = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`)
)

// reDanglingAssign 抓"赋值右侧为空"这一形态（`isolation.KEY =` 后直接换行）。
var reDanglingAssign = regexp.MustCompile(`isolation\.[A-Za-z_]+ *= *\n`)

// 注：这里曾有一张手工维护的 `launcherCoreOnly` 豁免表（"核心有、启动器刻意不兜底"的变量清单）。
// 它已随"隔离声明进 Spec"删除：那张表是同一事实的**第二份手抄副本**，漏登记就是静默漏点。
// 现在那份集合由 adapter 在 `Spec.Isolation` 里用 `LauncherFallback` 声明，
// 本文件只消费 `runtime.IsolationLauncherFallbackKeys`——判据也从"子集 + 豁免"升级为**逐条相等**。

// TestDesktopSmokeIsolationMatchesCore V9 桌面冒烟探针自带的隔离表必须与**核心隔离表逐条对齐**（两侧相等）。
//
// 同一张表在仓库里存在**三份**（Go 核心 `internal/runtime/isolation.go`、桌面私有启动器
// `apps/desktop/scripts/gen-runtime-bin.mjs`、桌面探针 `frontend/scripts/desktop-runtime-smoke.mjs`）。
// 第三份此前没有守卫——而它恰恰是**最会碰本机**的那一份：探针直接以子进程拉起随包载荷，
// 隔离表写漏一个变量的后果就是"验收脚本自己去读写用户的 `~/.dsh`"，表现为用户环境被改动，
// 而探针自报通过（2026-10-09 的 `XDG_STATE_HOME` 就是这么被找出来的，探针侧当时也漏了）。
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

	// 悬空赋值（与启动器那条同一个事故）：本守卫按**文本**解析这张表，所以"某一行只写了
	// `isolation.KEY =` 而值被截掉"这种语法错误能一路绿到最后，直到真的去跑探针才炸
	// （V9 冒烟探针 2026-10-10 就是这么红的，而它在 DoD 链的最末一环）。
	if reDanglingAssign.MatchString(src) {
		t.Fatalf("%s 里有悬空赋值（`isolation.KEY =` 后面没有值）——三副本同步时最容易被截断的正是最后一行，"+
			"而这类错只在**运行期**才现形", path)
	}

	// 以 runtime 分支切段：公共段（所有本地 runtime 都给）+ dsh 段。
	//
	// 这里曾按 `if (runtimeId === 'dsh') … } else { … }` 切成三段（else = 当时那个第二底座）。
	// 第二底座下线后 else 分支随之删除，探针只剩"公共面 + dsh 分支"。**刻意断言 else 不存在**：
	// 下一节的两条判据靠"未知 id 只拿公共面"才能与核心的 fail-safe 对上，若有人把某个底座的
	// 专属变量塞回一个 else 里，这里的结构断言会先红。
	dshAt := strings.Index(block, "if (runtimeId === 'dsh')")
	if dshAt < 0 {
		t.Fatalf("探针隔离表的分支结构变了（找不到 `if (runtimeId === 'dsh')`），"+
			"请修本守卫而不是放宽它：%s", path)
	}
	if strings.Contains(block, "} else {") {
		t.Fatalf("探针隔离表长回了 `} else {` 分支：只有**声明过隔离面的** runtime 才配专属变量，"+
			"未知 id 必须只拿公共面（与核心 IsolationEnv 的 fail-safe 逐条一致）。"+
			"真加了第二个底座，就把它登记进本守卫的 launcherRuntimeOf/用例表，而不是塞一个 else：%s", path)
	}
	common := block[:dshAt]
	dshPart := block[dshAt:]

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
	// v3.1：隔离声明是唯一事实源（`internal/runtime` 不再按 id 回查内置清单），
	// 所以"核心给某个 id 的隔离表"要按 adapter 的**声明**算。
	allowedOf := func(runtimeID string) map[string]bool {
		allowed := map[string]bool{}
		for _, kv := range runtime.IsolationEnvFor(specOf(runtimeID), home) {
			allowed[strings.SplitN(kv, "=", 2)[0]] = true
		}
		return allowed
	}
	dshCore := allowedOf(dsh.ID)
	// 拿一个**没在任何地方声明过**的 id 当"未知 runtime"的样本：核心对它只给公共 npm 面。
	// 用它而不是写死 npm 两个键名——核心那张公共表将来加一条，这里自动跟上。
	unknownCore := allowedOf("rt-unregistered-probe")

	// 探针的**有效**隔离表 = 公共面 ∪ 该 runtime 的分支（脚本里就是这两段合起来落进 env 的）。
	commonKeys := keysOf(common)
	if len(commonKeys) == 0 {
		t.Fatalf("探针隔离表的公共面解析出 0 个变量（表格式变了，请修本守卫而不是放宽它）：%s", path)
	}
	// 公共面必须与"核心给未知 runtime 的那一份"**逐条相等**：
	// 少一条 = 探针在比生产更少隔离的环境里跑；多一条 = 探针凭空隔离了一个核心不管的变量，
	// 于是"生产其实没隔离"这件事被盖住。
	commonSet := map[string]bool{}
	for _, k := range commonKeys {
		commonSet[k] = true
	}
	for _, k := range commonKeys {
		if !unknownCore[k] {
			t.Errorf("探针的公共面设了 %s，但核心对未知 runtime 不注入它——探针跑在比生产更隔离的环境里，"+
				"会把「生产其实没隔离」这件事盖住。要么两侧一起加，要么两侧一起从公共面删", k)
		}
	}
	for k := range unknownCore {
		if !commonSet[k] {
			t.Errorf("核心对未知 runtime 注入了 %s，但探针的公共面没有——公共面是**所有**本地 runtime 共用的那一层，"+
				"漏掉它就等于探针在某些 runtime 上跑在更少隔离的环境里", k)
		}
	}
	for _, c := range []struct {
		label     string
		part      string
		core      map[string]bool
		runtimeID string
	}{
		{"dsh 分支", dshPart, dshCore, dsh.ID},
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
		for _, kv := range runtime.IsolationEnvFor(specOf(c.runtimeID), home) {
			k := strings.SplitN(kv, "=", 2)[0]
			if !probe[k] {
				t.Errorf("核心的隔离表（%s）注入了 %s，但 V9 探针的%s没有——探针直接以子进程拉起随包载荷，"+
					"漏掉它就意味着**验收脚本自己去读写用户的真实 home**（`~/.dsh` 这类），"+
					"而探针自报通过。探针侧必须逐条对齐 IsolationEnv", c.runtimeID, k, c.label)
			}
		}
	}
}

// TestDesktopLauncherIsolationMatchesCore 桌面私有启动器的隔离表必须是**核心隔离表的子集**。
//
// 为什么要有这条守卫：同一张"哪个底座认哪些 home 变量"的表在仓库里存在**两份**——
//   - Go 侧 `internal/runtime/isolation.go`：核心拉 ACP runtime 时注入（IsolationEnv）；
//   - JS 侧 `frontend/apps/desktop/scripts/gen-runtime-bin.mjs`：壳直接跑随包载荷时在启动器里兜底
//     （`${VAR:=…}`，显式值优先，所以两条路径本应得到同一份 home）。
//
// 两处手抄同一张表，改了 A 忘了 B 的后果是**静默**的：2026-10-09 就是核心侧按"agents 面不是底座
// 私有配置、不该隔离"修掉 `DSH_AGENTS_HOME` 之后，私有启动器仍在兜底它，于是**桌面态** dsh 的 skill
// 读根继续与平台写根（`${HOME}/.agents/skills`）分叉、skill 不投放，而平台侧一切自报正常——直到
// 桌面态验收拿真请求体才看见。
//
// 方向刻意是**单向**的（启动器 ⊆ 核心）：启动器只许兜底核心也认的变量。核心认而启动器不兜底是
// 允许的（那些由核心显式注入，例如 XDG 那批"全局改会连累用户其它工具"的变量）——但**必须显式声明**
// 在 `Spec.Isolation` 的 `LauncherFallback: false` 上（声明处写明理由）。
//
// 2026-10-12 收口：原先那半是手抄的 `launcherCoreOnly` 豁免表，方向是"子集 + 豁免"，漏登记即静默漏点。
// 现在两侧都由 adapter 的 `LauncherFallback` 声明驱动，判据升级为**逐条相等**：
// 启动器解析出的键集合必须恰好等于 `runtime.IsolationLauncherFallbackKeys(id)`。
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

	// ——— 空值语义（2026-10-10 的真事故）———
	//
	// 只比**键名**会漏掉一类很坏的错：把「清掉用户同名变量」（Go 侧那条的取值是**空串**）机械地
	// 翻译成「未设置就兜底成 $MATE_HOME/」。产出的启动器照常 exec，底座才在内部报一个看不出因果的
	// 错——当年那个底座把目录当配置文件读（`<变量>=$MATE_HOME/` 这种形态 → EISDIR），桌面态的
	// skill 验收就是这么抓到的（真请求一个都没发出去）。
	//
	// 所以这里钉一条：生成器对空值必须走**单独一条分支**，且那条分支里不许出现 `MATE_HOME`。
	src := string(raw)
	if !strings.Contains(src, "if (sub === '')") {
		t.Fatalf("生成器没有对「空值」单独分支：空的语义是**清除**用户同名变量，不是兜底成 `$MATE_HOME/…`。" +
			"把它并进普通分支就会产出「<变量>=$MATE_HOME/」这种把目录当配置文件读的启动器（EISDIR）")
	}
	emptyBranch := src[strings.Index(src, "if (sub === '')"):]
	if cut := strings.Index(emptyBranch, "return style ==="); cut > 0 {
		emptyBranch = emptyBranch[:cut]
	}
	if strings.Contains(emptyBranch, "MATE_HOME") {
		t.Fatalf("空值分支里出现了 MATE_HOME：空值只能「清除」（unset / set \"KEY=\"），" +
			"拿 $MATE_HOME 兜底会让底座把目录当配置文件读（实测 EISDIR）")
	}

	// 悬空赋值（2026-10-10 的真事故之二）：本守卫按**文本**解析这三份表，所以"某一行只写了
	// `isolation.KEY =` 而值被截掉"这种**语法错误**能一路绿到最后——直到真的去跑那个脚本才炸
	// （V9 冒烟探针就这么红在 `SyntaxError: Unexpected token`，而那一步在 DoD 链的最后一环）。
	// 这里用一条正则把"赋值右侧为空"挡在静态阶段：它挡不了所有语法错，但挡得住"手抄一行时截断"。
	if reDanglingAssign.MatchString(string(raw)) {
		t.Fatalf("%s 里有悬空赋值（`isolation.KEY =` 后面没有值）——这类语法错会让脚本在**运行期**才炸，"+
			"而本草稿式的三副本同步最容易被截断的正是最后一行", path)
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
		// ① 启动器兜底的每一条都必须是核心也认的（没有"凭空多出来"的兜底）。
		coreKeys := map[string]bool{}
		for _, kv := range runtime.IsolationEnvFor(specOf(runtimeID), home) {
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
		// ② 双向相等：启动器的兜底集合必须**恰好**等于该 adapter 用 `LauncherFallback` 声明的那一份。
		// 少一条 = 壳直接跑随包载荷时没人兜底它（去读写用户真实目录，而两侧守卫都绿）；
		// 多一条 = 声明说"核心专供"、启动器却擅自兜底（通用 XDG 被改写会连累用户其它工具）。
		wantFallback := runtime.IsolationLauncherFallbackKeys(specOf(runtimeID))
		if len(wantFallback) == 0 {
			t.Fatalf("adapter %q 没有声明任何 LauncherFallback 变量，但启动器 %s 兜底了 %d 条：两侧的事实源已经分叉",
				runtimeID, name, len(launcherKeys))
		}
		for _, k := range wantFallback {
			if !launcherKeys[k] {
				t.Errorf("adapter %q 声明了 %s 应由桌面启动器兜底（LauncherFallback: true），但 %s 里没有它——"+
					"壳直接跑随包载荷时这条变量就没人兜底（写漏 home 变量 = 去读写用户真实目录）",
					runtimeID, k, name)
			}
		}
		for _, k := range keys {
			found := false
			for _, w := range wantFallback {
				if w == k {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("桌面启动器 %s 兜底了 %s，但 adapter %q 没把它声明为 LauncherFallback（= 核心专供）——"+
					"要么在声明里放开它（并写清为什么壳里兜底是安全的），要么从启动器里删掉",
					name, k, runtimeID)
			}
		}
	}
}

// specOf 按 id 取内置 adapter 的声明（未知 id 给一份空声明：核心对它只给公共 npm 面）。
func specOf(runtimeID string) runtime.Spec {
	for _, sp := range dsh.Specs() {
		if sp.ID == runtimeID {
			return sp
		}
	}
	return runtime.Spec{ID: runtimeID}
}
