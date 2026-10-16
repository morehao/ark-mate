package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **v27「模型下拉只出现『设置』里配置过的模型」** 的守卫（2026-10-16）。
//
// 触发是用户原话：「模型下拉列表应该只出现【设置】中配置的模型」——截图里那个下拉除了
// 用户配过的那条，还摆着运行时自己广告的两条（标「平台未声明」、置灰）。v26 刻意把它们摆出来
// （"底座广告了、平台没声明"最该被看见），v27 反过来：**这条事实归诊断面**（`GET /models` 的
// `sync[].advertised` / `missing`），选择器只渲染**运行时广告 ∩ 平台清单**。
//
// 这条收窄有三种静默失效方式，各有一段判据：
//
//	① 收窄点分叉（某个消费面自己去拿广告面）⇒ 用户又看见没配过的模型；
//	② 收窄**悄悄发生**（下拉少了几条，界面上一个字都不解释）——那正是 v26 当初不肯过滤的理由，
//	   所以"广告了 N 个、都不在清单里"必须换成一句人话；
//	③ 「平台没声明」的候选重新变成**可点**的（置灰兜底被删）⇒ 用户点一次换回一个 40401。
//
// 判据都落在**能被解析的源码**上（Go 读 TS，本仓前端零测试框架的既有模式），解析不到即
// fatal——改名/搬迁会让守卫静默空转，那比红更糟。
func TestModelDropdownShowsDeclaredCandidatesOnly(t *testing.T) {
	typesPath := filepath.Join(frontendRoot(t), "packages", "types", "src", "model.ts")
	types := readTSStripped(t, typesPath)

	// ① 收窄只有一处：选择器的唯一入口 `modelValues` = 广告面 ∩ 平台清单。
	if c := strings.Count(types, "export const modelValues = "); c != 1 {
		t.Fatalf("`modelValues` 应恰好定义 1 处，实际 %d 处——多一处就是第二个真相源"+
			"（一个收窄、一个不收窄，用户会在两个界面上看到两种候选）", c)
	}
	if c := strings.Count(types, "export const advertisedModelValues = "); c != 1 {
		t.Fatalf("`advertisedModelValues`（广告面原始取数）应恰好定义 1 处，实际 %d 处", c)
	}
	body := tsConstExpr(t, types, "export const modelValues = ")
	if !strings.Contains(body, "advertisedModelValues(") {
		t.Fatal("`modelValues` 不再从 `advertisedModelValues(` 派生：广告面必须只有一份取数，" +
			"两条链各扫一遍 `options[category=model]` 迟早分叉")
	}
	if !strings.Contains(body, ".filter(") || !strings.Contains(body, "model_id") {
		t.Fatal("`modelValues` 没有按 `model_id` 收窄：没有平台注解的候选（运行时广告了、平台没声明过）" +
			"会重新出现在下拉里——那正是 2026-10-16 用户报的「设置里没配过的模型也在列表里」")
	}

	// ② 两个选择器调用点：候选经收窄入口，广告面不许直接进 JSX。
	for _, name := range []string{"StartPanel.tsx", "SessionDetail.tsx"} {
		path := filepath.Join(frontendRoot(t), "packages", "console", "src", "components", name)
		src := readTSStripped(t, path)
		if !strings.Contains(src, "<ModelSelect") {
			t.Fatalf("%s 里找不到 `<ModelSelect`——守卫的对象不见了（改名/搬家？），fail-closed", name)
		}
		if !strings.Contains(src, "modelValues(") {
			t.Fatalf("%s 不再经 `modelValues(` 取候选：它自己那份取数没有收窄，下拉里会重新出现"+
				"运行时广告、平台没声明过的模型", name)
		}
		// 选择器的 props 区（从 `<ModelSelect` 到本元素结束）里不得出现广告面。
		for _, seg := range modelSelectProps(t, src) {
			if strings.Contains(seg, "advertised") {
				t.Errorf("%s 把**广告面**直接喂给了选择器：\n%s", name, seg)
			}
		}
	}

	// ③ 收窄不许是静默的：广告了、但平台没声明的那部分要在空态里换成一句人话
	//    （v26 当初不肯过滤的理由就是"悄悄过滤会让这条状态消失"——过滤可以，但要说出来）。
	panel := readTSStripped(t, filepath.Join(frontendRoot(t), "packages", "console", "src", "components", "StartPanel.tsx"))
	if !strings.Contains(panel, "undeclaredModelValues(") {
		t.Fatal("StartPanel 不再计算「广告了、但平台没声明」的那部分：收窄会变成**静默过滤**——" +
			"用户看到下拉里少了模型，界面上一个字都不解释")
	}
	noteExpr := tsConstExpr(t, panel, "const narrowedNote")
	if !strings.Contains(noteExpr, "undeclared.length") {
		t.Fatal("`narrowedNote` 没有用到「广告了、但平台没声明」的条数：那句话要么讲不出数量，" +
			"要么是在为另一件事道歉")
	}
	// 算出来还得**接到出口**：没人渲染的说明等于没解释。
	emptyAt := strings.Index(panel, "emptyNote=")
	if emptyAt < 0 {
		t.Fatal("StartPanel 里找不到 `emptyNote=`（下拉空态的出口）——解析不到即失败")
	}
	if !strings.Contains(panel[emptyAt:min(len(panel), emptyAt+220)], "narrowedNote") {
		t.Fatal("`narrowedNote` 没有接到 `emptyNote=` 上：算出来了却没人渲染，等于没解释")
	}

	// ④ 兜底：没有平台注解的候选**永远不可选**（哪怕它溜进了候选列表——例如调用方补的
	//    「当前模型」那一行）。删掉置灰 = 用户点一次换回一个 40401。
	sel := readTSStripped(t, filepath.Join(frontendRoot(t), "packages", "console", "src", "components", "ModelSelect.tsx"))
	if !strings.Contains(sel, "disabled: !v.model_id") {
		t.Fatal("ModelSelect 不再对没有平台注解的候选置灰：任何一条溜进候选列表的" +
			"「平台没声明」项都会变成可点，而平台只接受自己声明过的模型（40401）")
	}
	if strings.Contains(sel, "平台未声明") {
		t.Fatal("ModelSelect 里又出现「平台未声明」这个标签：v27 起这类候选根本不进选择器，" +
			"唯一会命中它的只剩调用方补的「当前模型」那一行——而那一行恰恰是**平台声明过的行**，" +
			"这个标签在它上面是句假话")
	}
}

// readTSStripped 读一个前端文件并剥掉注释（行注释 + 块注释；字符串字面量保留）。
//
// 与 `readStripped`（只剥 `//`）分开：本文件的判据要看**字面文案**（例如"「平台未声明」标签
// 不许回来"），块注释里提到那个词是正当的，不该判红。
func readTSStripped(t *testing.T, path string) string {
	t.Helper()
	return stripTSComments(string(mustReadFile(t, path)))
}

// tsConstExpr 抽出 `export const <名字> = <表达式>` 的表达式部分（到空行为止）。
//
// 这些声明都是"一行签名 + 一行表达式"的形状（本项目里 `modelValues` 系列如此）；抽不到
// （改名/改成函数体）即 fatal：判据必须看得见它，否则会静默变绿。
func tsConstExpr(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("找不到 %q——改名/搬迁会让这条守卫静默空转（fail-closed）", decl)
	}
	rest := src[i+len(decl):]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	if len(rest) < 16 {
		t.Fatalf("%q 的表达式只解析出 %d 字节——解析看起来失效了", decl, len(rest))
	}
	return rest
}

// modelSelectProps 返回文件里每个 `<ModelSelect` 元素的 props 区（到本元素结束 `/>` 为止）。
//
// 越界就取到文件尾：宁可多扫一点，也不要因为解析不到结束标记而把断言变成空转。
func modelSelectProps(t *testing.T, src string) []string {
	t.Helper()
	out := []string{}
	rest := src
	for {
		i := strings.Index(rest, "<ModelSelect")
		if i < 0 {
			break
		}
		seg := rest[i:]
		if j := strings.Index(seg, "/>"); j >= 0 {
			seg = seg[:j+2]
		}
		out = append(out, seg)
		rest = rest[i+len("<ModelSelect"):]
	}
	return out
}
