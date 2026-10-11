package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbiddenSettingsWords A13 的禁词表：设置 · 模型 那一节**任何一个用户可见的字**里都不许出现。
//
// 前五个是 2026-10-10 拍板新增（ADR-22，用户硬要求）：底座名与底座概念词一个都不能回流到用户面；
// 后四个是既有的代码词（这一节的用户词是「供应商」「模型」）。
//
// **只扫字符串字面量**，不扫标识符：`runtimeId` / `provider_id` 这类内部名（以及对象键、字段访问）
// 天天要出现，扫它们会让守卫变成噪声，而噪声守卫迟早被人绕过。判据是"用户会不会读到这个串"。
var forbiddenSettingsWords = []string{
	"dsh", "opencode", "dsh", "runtime", "底座",
	"provider", "mcp", "skill", "token",
}

// TestSettingsModelCopyAvoidsProvider A13 + A14：设置 · 模型那一节的用户面文案与结构。
//
// A13（用户面零底座词汇）：扫描面是那一节**全部用户可见字符串**（标题 + 正文 + toast + tooltip +
// 空态 + 错误出路 + 状态标签），判据 = 禁词表命中数 0。
// A14（默认模型在用户面只有一个）：`GET /models` 的用户面字段里没有逐 runtime 的默认映射
// （只有单一 `default_model`），且前端只有一个默认模型选择器。
//
// 四条纪律（缺一条守卫就会变成噪声或空转）：
//   - **大小写无关**地判（`Provider` 与 `provider` 是同一种错误）；
//   - **只扫字符串字面量**（单引号/双引号/反引号），先剥注释——注释与标识符是给维护者看的，
//     不是用户会读到的字；
//   - **解析不到就失败**（fail-closed）：改名/改写法会让守卫静默失效，那比没有守卫更糟；
//   - 两块（「供应商」「模型」）必须都在，否则"删掉一个块"也会让守卫绿。
func TestSettingsModelCopyAvoidsProvider(t *testing.T) {
	root := frontendRoot(t)

	// —— ① 弹窗左栏那一节（保留 v11 起的解析方式；解析不到即失败） ——
	viewPath := filepath.Join(root, "packages", "console", "src", "console", "settingsView.tsx")
	view, err := os.ReadFile(viewPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", viewPath, err)
	}
	sectionRE := regexp.MustCompile(`id:\s*'model'\s*,\s*label:\s*'([^']*)'`)
	m := sectionRE.FindStringSubmatch(string(view))
	if m == nil {
		t.Fatalf("在 %s 里找不到 `id: 'model', label: '…'`：设置第 4 节被改名或改写法了？"+
			"（本守卫是 fail-closed：解析不到即失败，避免它变成空转）", viewPath)
	}
	assertNoBannedWord(t, "设置分节标题（settingsView.tsx）", m[1])

	// —— ② 整节字符串字面量（标题 + 正文 + toast + tooltip + 空态 + 错误出路 + 状态标签） ——
	panelPath := filepath.Join(root, "packages", "console", "src", "components", "settings", "ModelSettingsPanel.tsx")
	panel, err := os.ReadFile(panelPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", panelPath, err)
	}
	src := string(panel)

	literals := extractStringLiterals(src)
	if len(literals) == 0 {
		t.Fatalf("%s 里一条字符串字面量都没解析到：抽取器失效（守卫会静默变绿），请修本守卫", panelPath)
	}
	// 抽取器必须真的读到了这一节的内容（否则"零命中"可能只是抽了个空文件）。
	joined := strings.Join(literals, "\x00")
	for _, want := range []string{"供应商", "模型"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("%s 的字符串字面量里找不到 %q：抽取器没读到这一节的内容（fail-closed）", panelPath, want)
		}
	}
	for _, lit := range literals {
		assertNoBannedWord(t, "面板字符串字面量", lit)
	}

	// —— ③ 两块必须真的是「供应商」与「模型」（不是恰好没踩到禁词的空标题） ——
	ariaRE := regexp.MustCompile(`aria-label="([^"]*)"`)
	aria := ariaRE.FindAllStringSubmatch(src, -1)
	if len(aria) < 2 {
		t.Fatalf("%s 里解析到的块级 aria-label 少于 2 个（期望「供应商」与「模型」两块）："+
			"块的写法变了？请修本守卫而不是放宽它", panelPath)
	}
	for _, a := range aria {
		assertNoBannedWord(t, "块标签（aria-label）", a[1])
	}
	wantBlocks := []string{"供应商", "模型"}
	for _, want := range wantBlocks {
		found := false
		for _, a := range aria {
			if strings.TrimSpace(a[1]) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("没找到标题恰好是 %q 的块：这一节的两个平级对象（供应商 / 模型）必须都在，"+
				"否则守卫会因为「少了东西」而误绿", want)
		}
	}

	// —— ④ 两个块的表头（CardHeader title） ——
	titleRE := regexp.MustCompile(`title="([^"]*)"`)
	titles := titleRE.FindAllStringSubmatch(src, -1)
	if len(titles) < 2 {
		t.Fatalf("%s 里解析到的 CardHeader title 少于 2 个（期望两块各一个）", panelPath)
	}
	for _, ttl := range titles {
		assertNoBannedWord(t, "块标题（CardHeader title）", ttl[1])
	}

	assertModelsResponseHasNoPerRuntimeDefault(t)
	assertPanelHasSingleDefaultSelector(t, src)
}

// assertModelsResponseHasNoPerRuntimeDefault A14 的后端半场：读 `dtomodel/list.go` 源码，
// 断言 `GET /models` 的**用户面字段**里没有逐 runtime 的默认映射（只有单一 `default_model`）。
//
// 关键区分（不许"顺手"把写面也 ban 掉）：`ModelCreateReq` / `ModelPatchReq` 里的
// `IsDefault *bool` 是**写面的触发字段**（"把这条设为唯一默认" / "取消默认"），它一次只描述
// 一条模型、不带 runtime 维度——所以它**不在**这条断言的范围里；本函数反过来还要断言它存在
// （写面少了两条入口，"只有一个默认"就落不了地）。
func assertModelsResponseHasNoPerRuntimeDefault(t *testing.T) {
	t.Helper()
	path := filepath.Join(backendRoot(t), "internal", "dto", "dtomodel", "list.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（这块契约的落点改了？请同步 A14 的断言）", path, err)
	}
	src := string(raw)

	respBody := structBodyOrFail(t, path, src, "ModelsResp")
	// 单一默认值：字段必须在。
	if !fieldRE("DefaultModel").MatchString(respBody) {
		t.Errorf("ModelsResp 里没有 DefaultModel 字段：用户面的默认模型只有一个（单一值），" +
			"A14 靠它成立——删掉它等于把默认模型踢出用户面")
	}
	// 逐 runtime 的默认映射：字段不许在（名字形态与 map 形态都拦）。
	if strings.Contains(respBody, "DefaultModels") || regexp.MustCompile(`(?m)^\s*\w*[Dd]efault\w*\s+map\[`).MatchString(respBody) {
		t.Errorf("ModelsResp 里出现了逐 runtime 的默认映射（DefaultModels / map 形态的默认字段）："+
			"ADR-21 要求默认模型是单一值——逐 runtime 存必然把「是哪个底座的默认」逼回界面\n%s", respBody)
	}
	// 用户面的生效结论两字段也在（A14 的另一半：结论只读聚合字段）。
	for _, f := range []string{"Effective", "EffectiveAt"} {
		if !fieldRE(f).MatchString(respBody) {
			t.Errorf("ModelsResp 里没有 %s 字段：用户面的生效结论只读聚合字段（ADR-22）", f)
		}
	}

	modelBody := structBodyOrFail(t, path, src, "ModelResp")
	for _, f := range []string{"IsDefault", "Runtime"} {
		if fieldRE(f).MatchString(modelBody) {
			t.Errorf("ModelResp 里出现了 %s 字段：模型行**不带** runtime 维度、也没有逐条默认标记"+
				"（默认是 meta 里的单一值）——v11 形状回流了", f)
		}
	}

	// 写面的触发字段：必须在，且是 `*bool`（指针语义表达"不改"）。
	for _, req := range []string{"ModelCreateReq", "ModelPatchReq"} {
		body := structBodyOrFail(t, path, src, req)
		if !regexp.MustCompile(`(?m)^\s*IsDefault\s+\*bool\s`).MatchString(body) {
			t.Errorf("%s 里的 IsDefault 不是 `*bool`（或缺失）：它是**写面的触发字段**"+
				"（nil = 不改），不是逐 runtime 的默认映射——两者必须在类型上分得开", req)
		}
	}
}

// assertPanelHasSingleDefaultSelector A14 的前端半场：面板里**只有一个**默认模型选择器。
//
// 判据是结构标记（不是文案）：选择器上带唯一标记 `aria-label="默认模型"`，出现次数必须恰好 1。
// 行卡上的「默认」标签是**只读**状态标签、不带这个标记，所以不会被算进来；解析不到（0 个）即失败
// ——否则"删掉默认选择器"也会让守卫绿。
func assertPanelHasSingleDefaultSelector(t *testing.T, src string) {
	t.Helper()
	const marker = `aria-label="默认模型"`
	if n := strings.Count(src, marker); n != 1 {
		t.Errorf("面板里 %q 出现 %d 次（期望恰好 1 次）：默认模型在用户面只有一个，"+
			"且必须是唯一的那个选择器（行卡上的「默认」标签是只读的、不带这个标记）", marker, n)
	}
	// 逐 runtime 的默认映射不许在前端回流（哪怕只是类型名）。
	if strings.Contains(src, "default_models") {
		t.Error("面板里出现了逐 runtime 的默认映射（default_models）：ADR-21 要求前端也只有一个默认值")
	}
}

// fieldRE 匹配结构体正文里以 name 开头的一个字段声明。
func fieldRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*` + name + `\s`)
}

// structBodyOrFail 从 Go 源码里取出 `type <name> struct { … }` 的正文；解析不到即 t.Fatalf。
//
// 用正则而不是 go/parser：本守卫读的是**源码文本**（与 A13 同一手法），且只需要字段名级别的判据。
// 代价是它依赖"结构体正文以行首 `}` 收尾"这一格式——所以解析不到时**失败**而不是跳过。
func structBodyOrFail(t *testing.T, path, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)type\s+` + name + `\s+struct\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("在 %s 里解析不到 `type %s struct { … }`：结构改名/改写法了？"+
			"（本守卫 fail-closed：解析不到即失败，避免 A14 静默失效）", path, name)
	}
	return m[1]
}

// extractStringLiterals 抽取 TS/TSX 源码里的字符串字面量（单引号 / 双引号 / 反引号），
// **先剥掉注释**（`//` 与 `/* */`）——注释是给维护者看的，不是用户会读到的字。
//
// 为什么用状态机而不是纯正则：正则分不开"字符串里的 `//`"与"真注释"，也容易被转义引号骗过；
// 一个逐字符的小状态机是**只扫字符串字面量**这条要求最直接的实现。
//
// 已知边界：正则字面量（`/…/`）里的引号会被当成字符串起始——本仓的设置面板里没有这种写法；
// 模板串里的 `${…}` 表达式连同其中的子串会被整段当成一个字面量（仍是用户可见文本，判据不受影响）。
func extractStringLiterals(src string) []string {
	var out []string
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '/' && i+1 < len(r) && r[i+1] == '/':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			i += 2
			for i+1 < len(r) && !(r[i] == '*' && r[i+1] == '/') {
				i++
			}
			i += 2
		case c == '\'' || c == '"' || c == '`':
			quote := c
			i++
			var b strings.Builder
			for i < len(r) {
				if r[i] == '\\' && i+1 < len(r) {
					b.WriteRune(r[i])
					b.WriteRune(r[i+1])
					i += 2
					continue
				}
				if r[i] == quote {
					i++
					break
				}
				b.WriteRune(r[i])
				i++
			}
			out = append(out, b.String())
		default:
			i++
		}
	}
	return out
}

// assertNoBannedWord 断言一段面向用户的字里没有禁词。
func assertNoBannedWord(t *testing.T, where, text string) {
	t.Helper()
	low := strings.ToLower(text)
	for _, w := range forbiddenSettingsWords {
		if strings.Contains(low, w) {
			t.Errorf("%s = %q 含禁词 %q：设置 · 模型 那一节的用户词是「供应商」「模型」「已生效 / 待生效」；"+
				"底座名与代码词只许出现在指向接口的映射代码里（对象键、字段访问），不许出现在用户可见的字里", where, text, w)
		}
	}
}
