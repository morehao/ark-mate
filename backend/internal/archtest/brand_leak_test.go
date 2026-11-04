package archtest

// —— 底座标识不进用户可见面（会话输出 / 用量页） ——
//
// 这一类泄漏的共同形态：**把执行事实（底座 id，如 `dsh`）拼进用户可见的文案**。
// 它和「新会话 · dsh」「assistant · thinking」是同一族病（见 `titleFor` 的 `plain` 口径、
// `TestAboutPanelShowsProductWithoutEngineWords`、`TestThoughtFoldIsSingleSourced` 里删掉的
// 「思考」标签）。共同点：都**不会让编译或其它测试变红**，只是把内部标识摊到用户屏上。
//
// 本轮（2026-11-04「思考过程/会话输出里仍有 dsh」）修到两处具体出口：
//
//	① `store.ts` 的 `SessionResumed` 系统条目：原先把 `payload.runtime` 拼成
//	   「会话已恢复（原生会话续接） · dsh」推进对话流——那是渲染在问答面板里的**用户文案**；
//	② `usageView.ts` 的 `usageRows`：行标签原先是 `runtime_id || key`，`runtime_id` 就是
//	   `dsh`（用量页是用户可见面）。
//
// 判据都落在**剥注释后的源码**上（注释里写清"为什么不许拼 dsh"是正常的）。fail-closed：
// 锚点解析不到即红（防止某天代码被搬走、判据静默空转）。
//
// 反证（实跑）：把 `· ${runtime}` 拼回 store.ts 的 resume 文案 → ① 红；把 `runtime_id ||` 拼回
// usageRows → ② 红。

import (
	"strings"
	"testing"

	productidentity "github.com/morehao/ark-mate/internal/domain/identity"
)

// TestIdentityPromptForbidsRuntimeInternalsInNarration ③：产品身份文案必须带上
// 「不要在思考/回复里引用执行层内部标识」这条纪律。
//
// 为什么钉这条：思考过程是**模型自己写的自由文本**，平台无法改写措辞；能改的只有**给它看的
// 提示词**。这条纪律一旦被下一个人"精简文案"顺手删掉，思考里冒出 DSH_*/home 路径/品牌名这类
// 泄漏就会**静默回潮**（模型行为，没有任何其它测试会变红）。
//
// 判据落在**语义锚点**（"思考"+"不要引用"+"环境变量"这几个词同框）而不是整句逐字：文案可以
// 润色，但这条纪律的三要点（作用面=思考/回复、动作=不引用、对象=执行层环境标识）必须都在。
func TestIdentityPromptForbidsRuntimeInternalsInNarration(t *testing.T) {
	spec := productidentity.Render()
	for name, text := range map[string]string{"系统提示段": spec.SystemPrompt, "首轮块": spec.UserContext} {
		if !strings.Contains(text, "思考") {
			t.Errorf("%s 里没有「思考」：必须点名这条纪律作用于**思考过程**（不只是对外回复）", name)
		}
		if !strings.Contains(text, "环境变量") {
			t.Errorf("%s 里没有「环境变量」：执行层内部标识要覆盖环境变量名（DSH_* 那类）", name)
		}
	}
	// 「引用」这条禁令在系统提示与首轮块里都应出现（两段都是给模型的人格锚点）。文案可写成
	// 「不要引用」「都不引用」「不引用」——判据落在核心动词「引用」上，不锁死措辞。
	if !strings.Contains(spec.SystemPrompt, "引用") || !strings.Contains(spec.UserContext, "引用") {
		t.Errorf("系统提示段与首轮块都应有「（不要）引用执行层内部标识」这条纪律" +
			"（前者每轮、后者每执行上下文一次，互为兜底）")
	}
}

// TestSessionResumeNoticeHasNoRuntimeId ①：会话恢复的系统条目不得拼底座 id。
func TestSessionResumeNoticeHasNoRuntimeId(t *testing.T) {
	src := readConsoleFile(t, "store.ts")

	// 定位 `case Ev.SessionResumed: {` 这一段（到下一处 `case Ev.` 为止）。
	start := strings.Index(src, "case Ev.SessionResumed:")
	if start < 0 {
		t.Fatalf("store.ts 里找不到 `case Ev.SessionResumed:`——判据失去对象（fail-closed）")
	}
	block := src[start:]
	if next := strings.Index(block[len("case Ev.SessionResumed:"):], "case Ev."); next >= 0 {
		block = block[:len("case Ev.SessionResumed:")+next]
	}

	// 这条系统条目是**进对话流的用户文案**：里面不得读取 `payload.runtime`，也不得把某个
	// runtime 变量拼进 text。两种情况都判红——判据落在"这条文案会不会带上底座 id"上。
	for _, banned := range []string{"payload?.runtime", "payload.runtime", "· ${runtime}", "· ${r}"} {
		if strings.Contains(block, banned) {
			t.Fatalf("store.ts 的 SessionResumed 系统条目里仍有 %q：这是渲染在对话流里的**用户文案**，"+
				"底座 id（如 dsh）属于执行事实、不是用户概念——恢复原因本身已说清发生了什么。"+
				"（同一族病见 titleFor 的 plain 口径、AboutPanel 守卫）", banned)
		}
	}
	// 正向锚点：这条文案必须还在（防止整段被删、判据空转）。
	if !strings.Contains(block, "会话已恢复") {
		t.Fatalf("store.ts 的 SessionResumed 块里找不到「会话已恢复」——判据失去对象（fail-closed）")
	}
}

// TestUsageRowLabelHasNoRuntimeId ②：用量页行标签不得回退到底座 id。
func TestUsageRowLabelHasNoRuntimeId(t *testing.T) {
	src := readConsoleFile(t, "console", "usageView.ts")

	start := strings.Index(src, "export function usageRows(")
	if start < 0 {
		t.Fatalf("usageView.ts 里找不到 `export function usageRows(`——判据失去对象（fail-closed）")
	}
	block := src[start:]
	if end := strings.Index(block, "\n}"); end >= 0 {
		block = block[:end]
	}
	if strings.Contains(block, "runtime_id") {
		t.Fatalf("usageView.ts 的 usageRows 里仍用 `runtime_id` 做行标签：那是底座 id（如 dsh），" +
			"用量页是用户可见面——行标签用日期/键本身（v19 起分组恒为 day）。")
	}
	// 正向锚点：行模型仍在构造（label 键还在）。
	if !strings.Contains(block, "label:") {
		t.Fatalf("usageView.ts 的 usageRows 里找不到 `label:`——判据失去对象（fail-closed）")
	}
}
