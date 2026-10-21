package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件是 **队列项离队原因词表**的守卫（ADR-10，P1-c）。
//
// 队列只补三件（重排 / 立即发送 / 取出编辑），其中第三件会引入一个新的离队原因 `user_edit`。
// 两种腐化都不会让任何既有测试变红：
//
//	① **后端多加一个前端不认的值**：前端的队列**只认事件**、不做本地推断（`applyFrame` 按
//	   `input_cancelled` 收敛那一行）。多出来的 reason 在界面上要么被渲染成"未知原因"，要么
//	   干脆不收敛——于是那一行永远挂着，直到刷新页面。契约那条边（后端 events.go ↔ 前端 `Ev`）
//	   只比对**事件 type**，reason 的取值集此前没有任何机制维护；
//	② **第二处写死原因字面量**：`"user_cancel"` 这种串在别处再写一遍，两份取值集从此各自演化，
//	   而两边都能编译、都能跑。
//
// 判据三段，都 fail-closed（锚找不到即失败）。

// TestQueuedInputReasonVocabularyIsClosed 见文件头。
func TestQueuedInputReasonVocabularyIsClosed(t *testing.T) {
	root := backendRoot(t)

	// ① 取值集的唯一声明在会话域，且五个值逐个在（少一个 = 前端少收敛一类离队）。
	queuePath := filepath.Join(root, "internal", "domain", "session", "inputqueue.go")
	queue := stripLineComments(readFileOrFatal(t, queuePath))
	if !strings.Contains(queue, "var QueuedInputReasons = []string{") {
		t.Fatalf("%s 里找不到 `var QueuedInputReasons = []string{`：离队原因的取值集必须有一个单点声明", queuePath)
	}
	declared := 0
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		if strings.HasSuffix(p, "_test.go") || strings.Contains(filepath.ToSlash(p), "/archtest/") {
			continue
		}
		if strings.Contains(stripLineComments(readFileOrFatal(t, p)), "var QueuedInputReasons = []string{") {
			declared++
		}
	}
	if declared != 1 {
		t.Fatalf("`QueuedInputReasons` 在 internal/ 里声明了 %d 处（want 1）：取值集必须单点", declared)
	}
	// 取值集**从后端源码里读**，不在守卫里手抄一份。
	//
	// 手抄那一份有个洞（本守卫第一版就是这么漏的）：后端新增一个原因时，守卫的 want 不会跟着变，
	// 于是"后端多发一个前端不认的值"这条**最该拦的方向**照样绿——它根本没看见那个新值。
	// 这正是 `TestEventTypeContractFrozen` 当初拒绝手抄的同一个理由。
	varBlock := tsUnionBlock(queue, "var QueuedInputReasons = []string{")
	if varBlock == "" {
		t.Fatalf("%s 里解析不到 `var QueuedInputReasons = []string{`（fail-closed）", queuePath)
	}
	want := make([]string, 0, 8)
	for _, m := range regexp.MustCompile(`\bReason[A-Z][A-Za-z]*`).FindAllString(varBlock, -1) {
		want = append(want, reasonLiteral(queue, m))
	}
	if len(want) < 5 {
		t.Fatalf("从 %s 的 `QueuedInputReasons` 里只解析出 %d 个原因 %v（解析失效，守卫形同虚设）",
			queuePath, len(want), want)
	}
	for _, v := range want {
		if v == "" {
			t.Fatalf("`QueuedInputReasons` 里有一个取不到字面量的常量（fail-closed）：%v", want)
		}
	}

	// ② 前端联合类型与它**逐条相等**（Go 读 TS，不引前端测试框架）。
	fePath := filepath.Join(root, "..", "frontend", "packages", "types", "src", "events.ts")
	fe := stripTSComments(readFileOrFatal(t, fePath))
	block := tsUnionBlock(fe, "export type QueuedInputReason =")
	if block == "" {
		t.Fatalf("%s 里解析不到 `export type QueuedInputReason =`（改名了？本守卫 fail-closed）", fePath)
	}
	got := make([]string, 0, len(want))
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(block, -1) {
		got = append(got, m[1])
	}
	if len(got) != len(want) {
		t.Fatalf("前端 `QueuedInputReason` 有 %d 个取值 %v，后端有 %d 个 %v：两侧必须逐条相等",
			len(got), got, len(want), want)
	}
	set := map[string]bool{}
	for _, v := range want {
		set[v] = true
	}
	for _, v := range got {
		if !set[v] {
			t.Errorf("前端多了 %q：后端不会发它（前端只认事件，多一个值等于一段永远不会走到的分支）", v)
		}
		delete(set, v)
	}
	for v := range set {
		t.Errorf("前端少了 %q：后端会发它，而界面不认——那一行会永远挂在队列里", v)
	}

	// ③ 域之外**零字面量**：谁要在别处再写一个原因串，取值集就有第二份了。
	//
	// **`stale` 刻意不在扫描名单里**：它是本仓的**重载词**（`internal/adapters/outbound/sqlite/model/enum.go` 的定义状态、
	// `domain/skill` 的物化状态都用它），按字面量扫会把那些正当用法算进来——那条判据唯一的产物
	// 就是逼着下一个人加白名单，而白名单一加，这条守卫对它真正要拦的东西也就松了。
	// 队列自己那一处走 `ReasonStale` 常量，取值集里仍然有它（上面 ② 逐条比对过）。
	scanned := []string{"run_finished", "user_cancel", "deliver_failed", "user_edit"}
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal")) {
		relSlash := filepath.ToSlash(rel(root, p))
		if strings.HasSuffix(p, "_test.go") ||
			strings.HasPrefix(relSlash, "internal/archtest/") ||
			strings.HasPrefix(relSlash, "internal/domain/session/") {
			continue
		}
		src := stripLineComments(readFileOrFatal(t, p))
		for _, v := range scanned {
			if strings.Contains(src, `"`+v+`"`) {
				t.Errorf("%s 里出现了离队原因字面量 %q：取值集唯一在 `domain/session` 的 "+
					"`QueuedInputReasons`。在别处再写一遍，两份就各自演化了——而两边都能编译、都能跑", relSlash, v)
			}
		}
	}
}

// tsUnionBlock 取一个 TS 类型别名（以 decl 开头）的右值块（到分号为止）。
func tsUnionBlock(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// reasonLiteral 从 `inputqueue.go` 里取某个 `ReasonXxx` 常量的取值（`ReasonXxx = "..."`）。
//
// 解析不到返回空串（调用方 fail-closed）：常量被改名/改成拼接，这条判据就该有人回来对齐，
// 而不是安静地把一个空串当成"某类离队"。
func reasonLiteral(src, name string) string {
	re := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(src); len(m) == 2 {
		return m[1]
	}
	return ""
}
