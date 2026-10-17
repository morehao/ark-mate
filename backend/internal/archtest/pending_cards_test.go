package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **未结算确认卡不渲染在条目流里** 的守卫（S9 / Q2 选①）。
//
// 三种腐化都不会让任何既有测试变红：
//
//	① **卡片加回条目流**：`<PermissionCard e={e} />` 那一行恢复成无条件的。界面照常工作，
//	   只是用户往上翻历史时又被一张浮在中段的卡片拦路——而"现在需要我做什么"是个**当下的问题**；
//	② **确认区被复制一份**：第二处渲染未结算卡。两张卡各自持有自己的一份表单状态，用户在 A 处
//	   填了一半、在 B 处看到的是空的——而这种分叉只在"两个都画出来"的那一刻才看得出来；
//	③ **确认区退回单卡**（原 O19，已移入 C35）：只画 `pending[0]`、其余挂一个「还有 N 条」计数。它看起来
//	   更克制，代价是用户必须逐条等下一张出现——而卡片之间没有先后依赖；"我还要处理几件事"
//	   变成一道需要来回翻看的记忆题。判据是「按列表 map、每张都带 key」，不是"有没有计数"。
//
// 判据四段，都 fail-closed。

// TestPendingCardsLiveOutsideTranscript 见文件头。
func TestPendingCardsLiveOutsideTranscript(t *testing.T) {
	root := backendRoot(t)
	consoleDir := filepath.Join(root, "..", "frontend", "packages", "console", "src", "components")

	// ① `EntryView` 的两个确认类分支必须是**条件**渲染（`resolved ? ... : null`）。
	tr := stripTSComments(readFileOrFatal(t, filepath.Join(consoleDir, "Transcript.tsx")))
	for _, kind := range []string{"permission", "elicitation"} {
		label := "case '" + kind + "':"
		i := strings.Index(tr, label)
		if i < 0 {
			t.Fatalf("Transcript.tsx 里找不到 `%s` 分支（改名/重构了？本守卫 fail-closed）", label)
		}
		rest := tr[i+len(label):]
		if j := strings.Index(rest, "\n    case "); j >= 0 {
			rest = rest[:j]
		}
		if !strings.Contains(rest, "e.resolved ?") || !strings.Contains(rest, ": null") {
			t.Fatalf("`%s` 分支不是条件渲染：未结算的确认卡必须**不**画在条目流里（S9）——\n"+
				"条目流是**历史**，用户往上翻时会被一张浮在中段的卡片反复拦路；而「现在需要我做什么」"+
				"是一个当下的问题，它该在离输入框最近的地方（`ConfirmArea`）", label)
		}
	}

	// ② 确认区必须存在，且**只**在 ChatPage 里挂一次。
	confirmPath := filepath.Join(consoleDir, "ConfirmArea.tsx")
	confirm := stripTSComments(readFileOrFatal(t, confirmPath))
	if !strings.Contains(confirm, "export function ConfirmArea(") {
		t.Fatalf("%s 里找不到 `export function ConfirmArea(`", confirmPath)
	}
	// 卡片本体必须是**复用**（同一份实现），不是在这里再画一遍。
	if !strings.Contains(confirm, "PermissionCard") || !strings.Contains(confirm, "ElicitationCard") {
		t.Fatalf("确认区没有复用 `PermissionCard` / `ElicitationCard`：" +
			"各画一份就是「同一件事在历史里和确认区里长得不一样」")
	}
	// 优先级：权限 > 提问（两者都急，但权限的超时更短、且那一轮卡在这里动不了）。
	if !strings.Contains(confirm, "en.kind === 'permission'") {
		t.Fatalf("确认区没有实现「权限 > 提问」的优先级：同时存在时先显示权限")
	}
	if !strings.Contains(confirm, "if (pending.length === 0) return null") {
		t.Fatalf("确认区在空的时候没有 `return null`：留一个空框会让人以为「这里本来要有什么」")
	}
	// O19：**并列显示**，不是"画一张 + 计数"。判据落在渲染方式上：按列表 `map`、每张带 key。
	// 只看"有没有 `还有 N 条`"不够——换个说法的单卡实现照样能过。
	if !strings.Contains(confirm, "ordered.map(") || !strings.Contains(confirm, "key={en.id}") {
		t.Fatalf("确认区没有并列渲染全部未结算卡（O19）：必须是 `ordered.map(...)` 且每张带 `key={en.id}`——\n" +
			"只画第一张、其余挂「还有 N 条」会让用户逐条等下一张出现，而卡片之间没有先后依赖")
	}
	if strings.Contains(confirm, "pending[0]") || strings.Contains(confirm, "pending.length - 1") {
		t.Fatalf("确认区退回「只画第一张」（O19）：`pending[0]` / `pending.length - 1` 是单卡形态的痕迹")
	}

	usage := 0
	for _, p := range uiSrcRoots(t) {
		for _, f := range listFrontendSources(t, p) {
			// 数**出现次数**而不是"含它的文件数"：同一屏挂两次是第一版放过的那种情形
			// （两个 `<ConfirmArea>` 在同一个文件里，按文件数永远是 1）。
			usage += strings.Count(stripTSComments(readFileOrFatal(t, f)), "<ConfirmArea")
		}
	}
	if usage != 1 {
		t.Fatalf("`<ConfirmArea` 的挂载点有 %d 处（want 1）：多挂一处就有两张卡各自持有一份表单状态，"+
			"用户在 A 处填了一半、在 B 处看到的是空的", usage)
	}
}
