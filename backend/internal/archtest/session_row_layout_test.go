package archtest

// 本文件钉住**会话行是两行式**（O21 / P1-h，2026-10-17）。
//
// 原型 `.dsh/docs/specs/2026-10-16-workbuddy-session-prototype.html` 的 `.sess` 结构是：
//
//	第 1 行 = 标题（单行省略）+ 置顶标记
//	第 2 行 = 状态点 + 阶段文案 + 来源标记 + 相对时间
//
// 挤成一行的后果是 236px 的侧栏里标题被削成「把 hov…」，而状态/时间也不是标题。当前实现用
// `data-sess-line="title" | "meta"` 两个标记把这条结构显式化；判据直接读这两个标记之间的片段。
//
// 判据（fail-closed：标记缺失 / 重复都失败）：
//
//	① 两个标记各恰好出现一次；
//	② 标题行含 `titleFor(` 与置顶标记（`PushpinFilled`），**不含** `WebkitLineClamp`；
//	③ 元信息行含 `<Dot`、`narr.label`、`relTime(`；
//	④ 行内不再有运行时胶囊（`runtimeLabelOf(`）——原型第 2 行只有状态/来源/时间。
//
// 反证（实跑）：删掉 `data-sess-line="meta"` → ① 红；把 `WebkitLineClamp: 2` 加回标题行 → ② 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionRowIsTwoLine 见文件头。
func TestSessionRowIsTwoLine(t *testing.T) {
	root := filepath.Dir(backendRoot(t))
	path := filepath.Join(root, "frontend", "packages", "console", "src", "components", "SessionPane.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 SessionPane.tsx 失败: %v（判据失去对象）", err)
	}
	src := stripTSComments(string(raw))

	const titleMark = `data-sess-line="title"`
	const metaMark = `data-sess-line="meta"`
	if n := strings.Count(src, titleMark); n != 1 {
		t.Fatalf("`%s` 出现 %d 次（want 1）——两行式的锚点没了，判据会空转", titleMark, n)
	}
	if n := strings.Count(src, metaMark); n != 1 {
		t.Fatalf("`%s` 出现 %d 次（want 1）——元信息行必须有且只有一个锚点", metaMark, n)
	}

	titleIdx := strings.Index(src, titleMark)
	metaIdx := strings.Index(src, metaMark)
	if titleIdx >= metaIdx {
		t.Fatalf("标题行标记在元信息行之后——两行的顺序反了")
	}
	titleSeg := src[titleIdx:metaIdx]
	metaEnd := strings.Index(src[metaIdx:], "</button>")
	if metaEnd < 0 {
		t.Fatalf("元信息行之后找不到 </button>——行结构变了")
	}
	metaSeg := src[metaIdx : metaIdx+metaEnd]

	if !strings.Contains(titleSeg, "titleFor(") {
		t.Errorf("标题行没有 titleFor(——那行不是标题")
	}
	if !strings.Contains(titleSeg, "PushpinFilled") {
		t.Errorf("标题行没有置顶标记（PushpinFilled）——原型把图钉放在第 1 行")
	}
	if strings.Contains(titleSeg, "WebkitLineClamp") {
		t.Errorf("标题行又用上了 WebkitLineClamp——原型是单行 nowrap 省略，两行 clamp 会抬高空高")
	}
	if !strings.Contains(metaSeg, "<Dot") {
		t.Errorf("元信息行没有状态点（<Dot）")
	}
	if !strings.Contains(metaSeg, "narr.label") {
		t.Errorf("元信息行没有阶段文案（narr.label）——状态点旁边必须说得出「在干什么」")
	}
	if !strings.Contains(metaSeg, "relTime(") {
		t.Errorf("元信息行没有相对时间（relTime）")
	}

	// ④ 运行时胶囊：只允许出现在搜索结果行，不允许出现在会话行里。
	rowStart := strings.Index(src, "key={sm.id}")
	rowEnd := strings.Index(src[rowStart:], "</Dropdown>")
	if rowStart < 0 || rowEnd < 0 {
		t.Fatalf("找不到会话行片段（key={sm.id} / </Dropdown>）——判据失去对象")
	}
	if row := src[rowStart : rowStart+rowEnd]; strings.Contains(row, "runtimeLabelOf(") {
		t.Errorf("会话行里仍有运行时胶囊（runtimeLabelOf）——原型第 2 行只有状态/来源/时间")
	}
}
