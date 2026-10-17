package archtest

// G2 · 工作区行不得有「复制路径」悬停卡或路径文本（十项修正第 2 条）
// G3 · 工作区行不得有实心三角（十项修正第 3 条）
//
// 这两条同属"行内噪声"，共用同一批文件与读取方式，所以放在一个文件里。
//
// 背景（G2）：`fg-wsgroup-tip*` 这四个类在仓库里**从来没有 CSS**，所以那段 JSX 不是浮层，
// 而是常驻在 `display:flex` 行里的内联子节点——它正是横向溢出的结构性来源。判据同时钉 TSX
// 与 CSS 两侧：删了 TSX 又把样式补回来，等于把同一张卡换一种方式复活。
//
// 背景（G3）：用户明确不要"展开工作空间下的操作"旁边的小三角。本批把整个图标位去掉了，
// 展开态改由**行底色 + 组内会话缩进**表达。判据因此钉三件事：零 `CaretRightOutlined`、
// 行上有 `aria-expanded`（可访问性事实）、CSS 里有 `.fg-wsgroup-row.fg-open`（视觉事实）。
//
// 反证（实跑）：把 `复制路径` 加回 TSX → G2 红；把 `CaretRightOutlined` 加回 → G3 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readConsoleFile(t *testing.T, rel ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{filepath.Dir(backendRoot(t)), "frontend", "packages", "console", "src"}, rel...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v（判据失去对象）", filepath.Join(rel...), err)
	}
	return stripTSComments(string(raw))
}

func TestWorkspaceRowHasNoCopyOrPathCard(t *testing.T) {
	pane := readConsoleFile(t, "components", "WorkspaceGroupPane.tsx")
	for _, gone := range []string{"fg-wsgroup-tip", "复制路径", "copyText"} {
		if strings.Contains(pane, gone) {
			t.Errorf("WorkspaceGroupPane.tsx 里仍有 %q——工作区行不是复制/路径的展示位（需求 2）", gone)
		}
	}

	css := readConsoleFile(t, "console.css.ts")
	for _, gone := range []string{"fg-wsgroup-tip", "fg-wsgroup-count"} {
		if strings.Contains(css, gone) {
			t.Errorf("console.css.ts 里仍有 %q——TSX 已不渲染，留一份样式只会让下一个人以为它还在用", gone)
		}
	}
}

func TestWorkspaceRowHasNoFilledCaret(t *testing.T) {
	pane := readConsoleFile(t, "components", "WorkspaceGroupPane.tsx")
	for _, gone := range []string{"CaretRightOutlined", "chevron"} {
		if strings.Contains(pane, gone) {
			t.Errorf("WorkspaceGroupPane.tsx 里仍有 %q——需求 3 不要方向图标（展开态靠底色 + 缩进）", gone)
		}
	}
	if !strings.Contains(pane, "aria-expanded={isOpen}") {
		t.Errorf("工作区行没有 aria-expanded={isOpen}——去掉图标后，折叠态的可访问性事实只剩这一处")
	}

	css := readConsoleFile(t, "console.css.ts")
	if !strings.Contains(css, ".fg-wsgroup-row.fg-open") {
		t.Errorf("console.css.ts 里没有 .fg-wsgroup-row.fg-open——展开态不再有可见表达")
	}
}
