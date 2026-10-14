package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// legacyProviderSubdir / providerFileBase v19 之前的定义目录与那个目录里唯一要保留的文件。
//
// v19 之前 `<MATE_HOME>/agents/` 装两件事：agent 定义（`<id>.json`、`default.json`）与
// `providers.json`。agent 概念并入 runtime 之后只剩后者，目录随之改名为 `providers/`
// （一词一目录，见 docs/design-and-roadmap.md「决策变更 v19」③）。
const (
	legacyProviderSubdir = "agents"
	providerFileBase     = "providers.json"
)

// ImportLegacyProviderDir 把 v19 之前的 `<MATE_HOME>/agents/providers.json` **一次性导入**
// 新的 `<MATE_HOME>/providers/providers.json`。
//
// 返回值：`imported` = 本次真的搬了一份；`warn` = 给人看的说明（非空即应由装配期打 WARN，
// 它同时是 mate doctor 该报的那条事实）；`err` = 复制失败（**不致命**：调用方 WARN，
// 平台照常起来——打不开界面反而让用户无从修）。
//
// 四条刻意的取舍：
//
//  1. **只在默认目录上做**：`MATE_PROVIDERS` 指向别处 = 用户显式选了另一个目录，
//     往那里塞一份"从旧目录搬来的"文件是替用户做决定。自定义目录的用户自己搬。
//  2. **新文件已存在就不动**：新目录里的那份是**事实源**（用户可能已经在新目录里改过），
//     拿旧文件覆盖它 = 用历史覆盖现在。
//  3. **不删旧文件**：它是退路（也是"这次搬了什么"的证据）。删掉等于把用户的配置收走，
//     而 v19 的目录改名本来就不该有"删数据"这一步。
//  4. **只搬 `providers.json`**：同目录下的 agent 定义（`<id>.json`）与 `default.json`
//     随 agent 概念一并撤销，搬过来只会让"这些文件还在，但没人读"变成新的困惑；
//     warn 文案里如实说明这一点。
func ImportLegacyProviderDir(providerDir, homeDir string) (imported bool, warn string, err error) {
	providerDir = filepath.Clean(providerDir)
	homeDir = filepath.Clean(homeDir)
	if homeDir == "" || homeDir == "." || providerDir != filepath.Join(homeDir, "providers") {
		return false, "", nil
	}
	dst := filepath.Join(providerDir, providerFileBase)
	if _, statErr := os.Stat(dst); statErr == nil {
		return false, "", nil
	} else if !os.IsNotExist(statErr) {
		return false, "", fmt.Errorf("检查 %s: %w", dst, statErr)
	}
	src := filepath.Join(homeDir, legacyProviderSubdir, providerFileBase)
	raw, readErr := os.ReadFile(src)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, "", nil // 老布局不存在：绝大多数新装场景，静默
		}
		return false, "", fmt.Errorf("读旧定义目录 %s: %w", src, readErr)
	}
	if mkErr := os.MkdirAll(providerDir, 0o700); mkErr != nil {
		return false, "", fmt.Errorf("建目录 %s: %w", providerDir, mkErr)
	}
	// 临时文件 + rename：读到半个 providers.json 会让定义加载器把用户的供应商判成坏行。
	tmp := dst + ".importing"
	if wErr := os.WriteFile(tmp, raw, 0o600); wErr != nil {
		return false, "", fmt.Errorf("写 %s: %w", tmp, wErr)
	}
	if rErr := os.Rename(tmp, dst); rErr != nil {
		_ = os.Remove(tmp)
		return false, "", fmt.Errorf("落位 %s: %w", dst, rErr)
	}
	return true, fmt.Sprintf(
		"定义目录已随 v19 改名为 providers/：已把 %s 导入 %s（**旧文件保留**，确认无误后可自行删除）。"+
			"同目录下的 agent 定义（`<id>.json`）与 `default.json` 不再被读取——agent 概念已整个并入 runtime"+
			"（底座由构建期固定，用户不可自定义）",
		src, dst), nil
}
