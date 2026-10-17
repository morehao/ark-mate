package archtest

// G1 · 左栏不得有与「新建会话」重复的入口（十项修正第 1 条）。
//
// `/sessions` 曾经同时是一级导航项「问答」与左栏顶部「新建会话」的目的地——两个入口做同一件事。
// 判据落在**事实源** `NAV_ENTRIES` 上，而不是某一段渲染代码：
//
//	① `export const NAV_ENTRIES` 必须能解析出来（改名/搬迁即失败，fail-closed）；
//	② 该数组块内不得出现 `/sessions`（它就是"以会话页为目的地的一级导航项"的唯一形态）；
//	③ `NavRail` 仍保留 `.fg-rail-new`（唯一入口必须存在），且不再有 `entry.id === 'chat'` 计数分支。
//
// 反证（实跑）：把 chat 条目加回 NAV_ENTRIES → ② 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavHasNoDuplicateNewSessionEntry(t *testing.T) {
	consoleSrc := filepath.Join(filepath.Dir(backendRoot(t)), "frontend", "packages", "console", "src")

	navRaw, err := os.ReadFile(filepath.Join(consoleSrc, "console", "navView.tsx"))
	if err != nil {
		t.Fatalf("读 navView.tsx 失败: %v（判据失去对象）", err)
	}
	navSrc := stripTSComments(string(navRaw))

	const decl = "export const NAV_ENTRIES"
	start := strings.Index(navSrc, decl)
	if start < 0 {
		t.Fatalf("解析不到 %q——导航条目的唯一事实源改名/搬迁了，本判据会静默空转", decl)
	}
	tail := navSrc[start:]
	end := strings.Index(tail, "\n]")
	if end < 0 {
		t.Fatalf("NAV_ENTRIES 数组的结尾（行首 ]）解析不到——判据无法切片")
	}
	block := tail[:end]
	if strings.Contains(block, "/sessions") {
		t.Errorf("NAV_ENTRIES 里仍有以 /sessions 为目的地的条目——它与「新建会话」重复（需求 1）")
	}

	railRaw, err := os.ReadFile(filepath.Join(consoleSrc, "components", "NavRail.tsx"))
	if err != nil {
		t.Fatalf("读 NavRail.tsx 失败: %v", err)
	}
	rail := stripTSComments(string(railRaw))
	if !strings.Contains(rail, "fg-rail-new") {
		t.Errorf("NavRail 里找不到 fg-rail-new——唯一的「新建会话」入口不见了")
	}
	if strings.Contains(rail, "entry.id === 'chat'") {
		t.Errorf("NavRail 里仍有 entry.id === 'chat' 的分支——问答条目的连带死代码（计数徽标）又回来了")
	}
}
