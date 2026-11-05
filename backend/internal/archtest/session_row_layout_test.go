package archtest

// 本文件钉住**会话行是单行式**（O21 / 十项修正 P4，2026-10-17 改写；2026-11-05 原型重设计 §15 再改口径）。
//
// 历史：
//  - 最先钉「两行式」：第 1 行标题 + 第 2 行（状态点 / 阶段文案 / 来源 / 时间）。
//  - 再改成一行的「标题 + 右端相对时间」，行内不要状态与来源。
//  - 2026-11-05 原型重设计（design §15）把行右端从**相对时间**换成**状态点 + 状态文案**：
//    标题（省略）+ 状态（运行中/待授权/失败/已完成）/ hover 让位给 ⋯；时间挪进会话信息浮层。
//    判据必须同步改口径，否则它会为了守住旧形态（relTime）而红——这正是本文件被改写而不是新增的原因。
//
// 目标形态：
//
//	[标题（省略号，title 全名） … 置顶]   [● 运行中]   [⋯]
//
// 结构锚点保留 `data-sess-line="title"`（且**只允许这一个**）；`meta` 锚点必须消失。
//
// 判据（fail-closed：锚点缺失 / 重复 / 片段切不出来都失败）：
//
//	① `title` 锚点恰好 1 个，且片段内含 `titleFor(`、`PushpinFilled`、`sessionBadge(`、`fg-sess-status`；
//	② 片段内 `relTime(` **零命中**（相对时间已挪进浮层，行内不再显示时间）；
//	③ `data-sess-line="meta"` 零命中（第二行早已整块删除）；
//	④ 会话行片段内 `<Dot`、`narr.label`、`isAutomationOrigin`、`runtimeLabelOf(` 零命中
//	   （旧的 narration 组件不回来；行内状态点用本项目自己的 `fg-sess-dot`，不是 `<Dot`）。
//	⑤ 工作区行片段内零 `fg-wsgroup-count`（计数已从 TSX 撤掉，别让它回来）。
//
// 反证（实跑）：把行内状态换回 `relTime(` → ① 红 / ② 红；塞一个 `<Dot …>` → ④ 红。

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

	for _, want := range []string{"titleFor(", "PushpinFilled", "fg-sess-status"} {
		if !strings.Contains(row, want) {
			t.Errorf("单行会话行缺少 %q——标题 + 置顶 + 右端状态徽标是这一行的全部内容", want)
		}
	}
	// 状态口径的唯一实现是 railView.sessionBadge（纯函数），行内引用它算出的 badge。
	if !strings.Contains(src, "sessionBadge(") {
		t.Errorf("SessionPane 里找不到 sessionBadge(——状态徽标必须走 railView.sessionBadge 唯一口径")
	}
	// ② 相对时间已挪进会话信息浮层，行内不应再有 relTime(。
	if strings.Contains(row, "relTime(") {
		t.Errorf("会话行里仍有 relTime(——相对时间已挪进会话信息浮层，行右端改显示状态徽标")
	}
	for _, gone := range []string{"<Dot", "narr.label", "isAutomationOrigin", "runtimeLabelOf("} {
		if strings.Contains(row, gone) {
			t.Errorf("会话行里仍有 %q——旧的 narration 状态/来源标记不回来（行内状态点用 fg-sess-dot）", gone)
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
