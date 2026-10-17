package archtest

// G5 · 思考折叠只有一个出处（十项修正第 6 条下半）。
//
// 分组是**纯函数**（`foldThoughts`），渲染是**一个组件**（`ThoughtFoldView`）。腐化方式全是静默的：
//
//	① 有人在 `Transcript` 里把 `case 'thought'` 又渲染出来 ⇒ 同一段思考同时以折叠块和原始条目出现；
//	② 有人在 ChatPage 里再分一次组（或绕开纯函数另写一段）⇒ 两套分组口径慢慢分叉；
//	③ CSS 类被改名 ⇒ 折叠块失去样式（`maxHeight:200` 的内滚没了），但界面"看起来还在"。
//
// 判据（fail-closed：解析不到锚点即失败）：
//
//	① `transcriptView.ts` 里 `export function foldThoughts(` 恰好 1 处；
//	② `ChatPage.tsx` 里 `foldThoughts(` 作为**调用**恰好 1 处；
//	③ `Transcript.tsx` 的 `case 'thought'` 分支体只 `return null`，不含 `<Role` / `思考</span>`；
//	④ `console.css.ts` 有 `.fg-thought-head`。
//
// 反证（实跑）：在 Transcript 的 thought 分支写回一个 `<Role>` → ③ 红。

import (
	"strings"
	"testing"
)

func TestThoughtFoldIsSingleSourced(t *testing.T) {
	view := readConsoleFile(t, "console", "transcriptView.ts")
	if n := strings.Count(view, "export function foldThoughts("); n != 1 {
		t.Fatalf("foldThoughts 声明了 %d 次（want 1）——分组口径必须只有一个出处", n)
	}

	page := readConsoleFile(t, "pages", "ChatPage.tsx")
	if n := strings.Count(page, "foldThoughts("); n != 1 {
		t.Errorf("ChatPage 里 foldThoughts( 出现 %d 次（want 1，且是调用而不是第二份实现）", n)
	}

	transcript := readConsoleFile(t, "components", "Transcript.tsx")
	caseStart := strings.Index(transcript, "case 'thought':")
	if caseStart < 0 {
		t.Fatalf("Transcript.tsx 里找不到 case 'thought':——判据失去对象")
	}
	body := transcript[caseStart:]
	if next := strings.Index(body[len("case 'thought':"):], "case "); next >= 0 {
		body = body[:len("case 'thought':")+next]
	}
	if !strings.Contains(body, "return null") {
		t.Errorf("Transcript 的 thought 分支不再 return null——单条思考会被直接渲染（与折叠块重复）")
	}
	for _, gone := range []string{"<Role", "思考</span>"} {
		if strings.Contains(body, gone) {
			t.Errorf("Transcript 的 thought 分支里仍有 %q——旧的「assistant · thinking / 思考」标签回来了", gone)
		}
	}

	css := readConsoleFile(t, "console.css.ts")
	if !strings.Contains(css, ".fg-thought-head") {
		t.Errorf("console.css.ts 里没有 .fg-thought-head——折叠块的标题行失去样式")
	}
}
