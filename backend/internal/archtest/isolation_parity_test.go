package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morehao/ark-forge/pkg/runtime"
	"github.com/morehao/ark-forge/pkg/runtime/deepseekharness"
	"github.com/morehao/ark-forge/pkg/runtime/opencode"
)

// launcherIsolationRE 从 `gen-runtime-bin.mjs` 的一个启动器条目里抠出 `isolation: { … }` 的键名。
var (
	launcherIsolationRE = regexp.MustCompile(`(?s)isolation:\s*\{([^}]*)\}`)
	launcherEnvKeyRE    = regexp.MustCompile(`([A-Z][A-Z0-9_]*)\s*:`)
	launcherRuntimeOf   = map[string]string{"dsh": deepseekharness.ID, "opencode": opencode.ID}
)

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
// 允许的（那些由核心显式注入，例如 XDG 那批"全局改会连累用户其它工具"的变量）。
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
		allowed := map[string]bool{}
		for _, kv := range runtime.IsolationEnv(runtimeID, home) {
			allowed[strings.SplitN(kv, "=", 2)[0]] = true
		}
		for _, k := range keys {
			if !allowed[k] {
				t.Errorf("桌面启动器 %s 兜底了 %s，但核心的隔离表（%s）不再注入它——两侧手抄同一张表时分叉的典型形态。"+
					"`DSH_AGENTS_HOME` 这类「根变量」一旦只存在于一侧，就会让底座的读根与平台写根悄悄分叉、skill 静默不投放；"+
					"要么两侧一起加，要么两侧一起删", name, k, runtimeID)
			}
		}
	}
}
