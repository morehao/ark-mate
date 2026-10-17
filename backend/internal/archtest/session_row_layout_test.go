package archtest

// 本文件钉住**会话行是单行式**（O21 / 十项修正 P4，2026-10-17 改写）。
//
// 前一版钉的是「两行式」：第 1 行标题 + 第 2 行（状态点 / 阶段文案 / 来源 / 时间）。本批按
// 用户第 9、10 条把两行收成一行——**时间贴行右，行内不再出现状态与来源**。判据必须同步改口径，
// 否则它会为了守住旧形态而红（这正是本文件被改写而不是新增的原因）。
//
// 目标形态：
//
//	[标题（省略号，title 全名） … 置顶]                    [2 分钟前]
//
// 结构锚点保留 `data-sess-line="title"`（且**只允许这一个**）；`meta` 锚点必须消失。
//
// 判据（fail-closed：锚点缺失 / 重复 / 片段切不出来都失败）：
//
//	① `title` 锚点恰好 1 个，且片段内含 `titleFor(`、`PushpinFilled`、`relTime(`；
//	② `data-sess-line="meta"` 零命中（第二行已整块删除）；
//	③ 会话行片段内 `<Dot`、`narr.label`、`isAutomationOrigin`、`runtimeLabelOf(` 零命中；
//	④ 工作区行片段内零 `fg-wsgroup-count`（计数已从 TSX 撤掉，别让它回来）。
//
// 反证（实跑）：把 `data-sess-line="meta"` 加回去 → ② 红；在标题行塞一个 `<Dot …>` → ③ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionRowIsSingleLine 见文件头。
func TestSessionRowIsSingleLine(t *testing.T) {
	consoleSrc := filepath.Join(filepath.Dir(backendRoot(t)), "frontend", "packages", "console", "src")

	raw, err := os.ReadFile(filepath.Join(consoleSrc, "components", "SessionPane.tsx"))
	if err != nil {
		t.Fatalf("读 SessionPane.tsx 失败: %v（判据失去对象）", err)
	}
	src := stripTSComments(string(raw))

	const titleMark = `data-sess-line="title"`
	const metaMark = `data-sess-line="meta"`
	if n := strings.Count(src, titleMark); n != 1 {
		t.Fatalf("%s 出现 %d 次（want 1）——单行式的锚点没了，判据会空转", titleMark, n)
	}
	if n := strings.Count(src, metaMark); n != 0 {
		t.Fatalf("%s 出现 %d 次（want 0）——元信息行已删，加回来就是「状态/来源又回到行里」", metaMark, n)
	}

	// 会话行片段：从标题行锚点到「更多」下拉结束。`</Dropdown>` 是行尾，且行内只有这一处。
	titleIdx := strings.Index(src, titleMark)
	rowEnd := strings.Index(src[titleIdx:], "</Dropdown>")
	if rowEnd < 0 {
		t.Fatalf("标题行之后找不到 </Dropdown>——会话行的结尾变了，判据无法切片")
	}
	row := src[titleIdx : titleIdx+rowEnd]

	for _, want := range []string{"titleFor(", "PushpinFilled", "relTime("} {
		if !strings.Contains(row, want) {
			t.Errorf("单行会话行缺少 %q——标题 + 置顶 + 右端时间是这一行的全部内容", want)
		}
	}
	for _, gone := range []string{"<Dot", "narr.label", "isAutomationOrigin", "runtimeLabelOf("} {
		if strings.Contains(row, gone) {
			t.Errorf("会话行里仍有 %q——需求 10 明确不要状态点/阶段文案/来源标记", gone)
		}
	}

	// ④ 工作区行：计数已从 TSX 撤掉，CSS 里也不该再有它的规则（G2 另钉 CSS）。
	wsRaw, err := os.ReadFile(filepath.Join(consoleSrc, "components", "WorkspaceGroupPane.tsx"))
	if err != nil {
		t.Fatalf("读 WorkspaceGroupPane.tsx 失败: %v（判据失去对象）", err)
	}
	if wsSrc := stripTSComments(string(wsRaw)); strings.Contains(wsSrc, "fg-wsgroup-count") {
		t.Errorf("工作区行里仍有 fg-wsgroup-count——会话计数已经撤掉，别再渲染")
	}
}
