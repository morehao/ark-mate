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
// `opencode` 是**已下线底座**的名字，刻意留着：本表拦的是"底座名回流到用户面"这一类，
// 而不是"某个底座还在不在仓库里"——留着它，下一个底座接进来时照抄同一张表的成本更低，
// 而删掉它只会少一层保护。
var forbiddenSettingsWords = []string{
	"dsh", "opencode", "runtime", "底座",
	"provider", "mcp", "skill", "token",
}

// TestSettingsModelCopyAvoidsProvider A13 + A14：设置 · 模型那一节的用户面文案与结构。
//
// A13（用户面零底座词汇）：扫描面是那一节**全部用户可见字符串**（标题 + 正文 + toast + tooltip +
// 空态 + 错误出路 + 状态标签），判据 = 禁词表命中数 0。
// A14（默认模型在用户面只有一个）：`GET /models` 的用户面字段里没有逐 runtime 的默认映射
// （只有单一 `default_model`），且前端只有**一个**默认面（v28：页头一行只读状态 + 每张行卡上
// 那颗设/取消按钮，改的都是同一个单一值）。
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
	assertPanelHasSingleDefaultSurface(t, src)
}

// assertModelsResponseHasNoPerRuntimeDefault A14 的后端半场：读 `dtomodel/model_list.go` 源码，
// 断言 `GET /models` 的**用户面字段**里没有逐 runtime 的默认映射（只有单一 `default_model`）。
//
// 关键区分（不许"顺手"把写面也 ban 掉）：`ModelCreateReq` / `ModelPatchReq` 里的
// `IsDefault *bool` 是**写面的触发字段**（"把这条设为唯一默认" / "取消默认"），它一次只描述
// 一条模型、不带 runtime 维度——所以它**不在**这条断言的范围里；本函数反过来还要断言它存在
// （写面少了两条入口，"只有一个默认"就落不了地）。
func assertModelsResponseHasNoPerRuntimeDefault(t *testing.T) {
	t.Helper()
	path := filepath.Join(backendRoot(t), "internal", "dto", "dtomodel", "model_list.go")
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

// assertPanelHasSingleDefaultSurface A14 的前端半场：默认模型在用户面只有一个面。
//
// v28 起这个面**拆成两半**（用户要求：设默认是列表里的一个单独按钮），判据随之分两条：
//   - **读**：当前默认由页头那一行只读状态给出，唯一结构标记 `aria-label="默认模型"`，出现次数
//     必须恰好 1（0 个 = 把"现在是哪条"从用户面删掉了——那正是上一版选择器消失后最容易发生的事，
//     所以解析不到即失败，而不是放宽成"有一个就行"）；
//   - **写**：设与取消都必须在，且落在行卡那颗按钮上（`设为默认模型` / `取消默认模型` 两个
//     aria-label 都在）。少任何一个方向 = "能设不能取消"或整个写入口消失，而二者都不会让别的
//     守卫变红——用户看到的只是"点了没反应"。
//
// 行卡上的「默认」标签是**只读**状态标记、不带那个 aria-label，所以不会被算进来。
func assertPanelHasSingleDefaultSurface(t *testing.T, src string) {
	t.Helper()
	// 判据一律落在**剥掉注释的代码**上（见 codeWithoutComments）：注释里写一遍这个标记（本文件与
	// 面板里都写了）会让原样计数把"属性被删掉"这事故意盖住——注释越多越绿，正是本仓最忌讳的
	// "守卫静默空转"。
	code := codeWithoutComments(src)
	const marker = `aria-label="默认模型"`
	if n := strings.Count(code, marker); n != 1 {
		t.Errorf("面板里 %q 出现 %d 次（期望恰好 1 次）：默认模型在用户面只有一个，"+
			"页头那行只读状态就是它唯一的读面（行卡上的「默认」标签是只读的、不带这个标记）", marker, n)
	}
	// 写面（v28）：行卡那颗按钮，**两个方向**都必须能走。判据是那颗按钮 aria-label 的完整形态
	// （空白归一后逐字比对），而不是"两个字面量在不在"——后者**会假绿**：本文件的 toast 文案里
	// 就写着「已把「…」设为默认模型」/「已取消默认模型：…」，只查字面量的话把按钮整个删掉
	// 照样通过（反证实跑过）。形态比对对改名/重排的代价是**红**：那是 fail-closed 的正确方向
	// ——改到这里请连着改本判据，而不是把它放宽。
	const writeFace = `aria-label={isDefault ? '取消默认模型' : '设为默认模型'}`
	if !strings.Contains(strings.Join(strings.Fields(code), " "), writeFace) {
		t.Errorf("面板里找不到行卡上的默认按钮（期望 %s）：设/取消默认自 v28 起是**每条模型行卡上的一个"+
			"按钮**，两个方向都要在——只剩一个方向等于把「取消默认」变成做不到的事，"+
			"整个写入口消失则会以「点了没反应」的样子出现", writeFace)
	}
	// 逐 runtime 的默认映射不许在前端回流（哪怕只是类型名）。
	if strings.Contains(code, "default_models") {
		t.Error("面板里出现了逐 runtime 的默认映射（default_models）：ADR-21 要求前端也只有一个默认值")
	}
}

// codeWithoutComments 剥掉 TS/TSX 源码里的注释（`//` 与 `/* */`，含 JSX 的 `{/* … */}`），
// **字符串字面量原样保留**（A14 那个结构标记本身就在双引号里）。
//
// 与 extractStringLiterals 同一个逐字符状态机手法，只是把"抽字面量"换成"抄非注释的代码"：
// 正则分不开"字符串里的 `//`"与"真注释"，也会被转义引号骗过。
func codeWithoutComments(src string) string {
	var b strings.Builder
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			quote := c
			b.WriteRune(c)
			i++
			for i < len(r) {
				if r[i] == '\\' && i+1 < len(r) {
					b.WriteRune(r[i])
					b.WriteRune(r[i+1])
					i += 2
					continue
				}
				b.WriteRune(r[i])
				closed := r[i] == quote
				i++
				if closed {
					break
				}
			}
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
		default:
			b.WriteRune(c)
			i++
		}
	}
	return b.String()
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
//
// 判据分两档（2026-10-15 v25 加的，起因是那一节第一次出现**协议键**字面量）：
//
//	含中文的字面量   → 禁词表**子串**匹配（这一屏的用户文案在本仓一律是中文，子串能抓住
//	                   「…不会被下发给底座」这类夹在句子里的泄漏）；
//	纯 ASCII 的字面量 → 只做**整串相等**匹配。理由：ASCII 字面量在这一节是协议键与依赖路径
//	                   （`"max_input_tokens"`、`"provider_id"` 这类对象键/`Form.Item name`），
//	                   用户**读不到**它们——拿子串扫它们会把 `"max_input_tokens"` 里的 `token`
//	                   判成"代码词回流用户面"（假红），而假红是这套守卫最常见的死法。
//	                   `"provider"` / `"runtime"` 这种**整串就是禁词**的形态照样红（那才是泄漏）。
//
// 反证（实跑）：往面板里写一句 `'Runtime 设置'` → 红（含中文那一档的子串匹配）；
// 写一个整串为 `'runtime'` 的字面量 → 红（ASCII 那一档的整串匹配）。
func assertNoBannedWord(t *testing.T, where, text string) {
	t.Helper()
	low := strings.ToLower(text)
	cjk := strings.ContainsFunc(text, func(r rune) bool { return r > 0x2E80 })
	for _, w := range forbiddenSettingsWords {
		hit := strings.Contains(low, w)
		if !cjk {
			hit = low == w
		}
		if hit {
			t.Errorf("%s = %q 含禁词 %q：设置 · 模型 那一节的用户词是「供应商」「模型」「已生效 / 待生效」；"+
				"底座名与代码词只许出现在指向接口的映射代码里（对象键、字段访问），不许出现在用户可见的字里", where, text, w)
		}
	}
}

// TestProviderProbeProseAvoidsRuntimeVocabulary A13 的**后端那一半**：设置 · 模型 那一屏渲染的
// 不只是界面自己的字，还有核心给的自检文案（`GET /providers/:id/check` 的 `detail`/`remedy`，
// 见 `ModelSettingsPanel.tsx` 里那两个 `<span className="fg-probe-…">`）。A13 判的是**用户读到的
// 整屏文本**，所以这些串同样在判据里——只扫前端会漏掉这条：前端一个字不改、后端塞一句
// 「…不会被下发给底座」，面板上照样出现底座词汇（2026-10-14 的动态验收 `面板正文零底座词汇`
// 命中的就是这一条，当时它只在"某条档案没被挂靠"这一种状态下才渲染出来）。
//
// 判据只看**含中文的字面量**：这一屏的用户文案在本仓一律是中文，而 ASCII 字面量是协议键与
// 依赖路径（`"github.com/…/internal/runtime"`、`"max_tokens"`、`"data:image/png;base64,…"`）——
// 用户读不到它们，判进来只会逼着守卫加白名单，而白名单是这套守卫最常见的死法。
// fail-closed：文件读不到、或中文串少到说明提取器/文件已经失效，都当场失败（守卫不许空转）。
//
// P0：原先钉着一个精确文件路径（`provider_probe.go`）。该文件随供应商域搬进 `domain/provider`
// （设计 §3.4），钉路径在搬家后只会"读不到文件"（假红）。判据改锚在**符号**上：`probeLocal`
// （自检检查表的构造入口，这些 `detail`/`remedy` 文案就产在它和它的同族函数里）。刻意**不锚
// 导出的 `ProbeProvider`**——它在 `service/svcmodel` 很可能只剩一行转发，锚到它会让判据
// 盯错文件。锚点由 `uniqueSourceFile` 保证全仓恰好一处（0 处 = 判据失去对象，≥2 处 = 自检实现
// 有了第二份），下面的 `scanned >= 40` 是第二道 fail-closed 兜底。
func TestProviderProbeProseAvoidsRuntimeVocabulary(t *testing.T) {
	path := uniqueSourceFile(t, filepath.Join(backendRoot(t), "internal"), nil,
		declREFor("probeLocal").String(), "供应商自检检查表构造入口 `probeLocal`")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v（文件被搬走/改名了？本守卫 fail-closed）", path, err)
	}
	cjk := regexp.MustCompile(`\p{Han}`)
	scanned := 0
	for _, lit := range extractStringLiterals(string(raw)) {
		if !cjk.MatchString(lit) {
			continue
		}
		scanned++
		assertNoBannedWord(t, "provider 自检文案（"+path+"）", lit)
	}
	// 实测（2026-10-14）58 条；阈值取 40 是留给"正常改文案"的余量——真要掉到 40 以下，
	// 那是文件被换掉或提取器失效，不是文案变短。
	if scanned < 40 {
		t.Fatalf("只解析到 %d 条中文自检文案（期望 ≥ 40）：提取器失效或该文件已被换掉，"+
			"零命中不能算通过（fail-closed）", scanned)
	}
}
