package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbiddenUserFacingWords 面向用户的**标题**里不许出现的代码词。
//
// 判据不是审美：这四类词（provider / mcp / skill / model_id 之类的资源层词）对用户没有信息量，
// 而这一节的用户词是「供应商」「模型」。历史上一版把这一节的标题写成「provider 档案」，
// 于是用户看到的标题在说平台内部的资源名——而那一节真正的内容是"填密钥 / 管模型"。
var forbiddenUserFacingWords = []string{"provider", "mcp", "skill", "token"}

// TestSettingsModelCopyAvoidsProvider A13：设置 · 模型这一节的**标题**只许说用户词。
//
// 覆盖三处（正是用户会读到的三行字）：
//  1. `console/settingsView.tsx` 里 `id: 'model'` 那一节的 `label`（弹窗左栏那一行）；
//  2. `ModelSettingsPanel.tsx` 里两个块的 `aria-label`（供应商 / 模型）；
//  3. 同文件里 `CardHeader` 的 `title`（两块的表头）。
//
// 三条纪律（缺一条这条守卫就会变成噪声或空转）：
//   - **大小写无关**地判（`Provider` 与 `provider` 是同一种错误）；
//   - **解析不到就失败**（fail-closed）：改名/改写法会让守卫静默失效，那比没有守卫更糟；
//   - 两块的标题**必须都存在**（≥2 个），否则"删掉一个块"也会让守卫绿。
func TestSettingsModelCopyAvoidsProvider(t *testing.T) {
	root := frontendRoot(t)

	// —— ① 弹窗左栏那一节 ——
	viewPath := filepath.Join(root, "apps", "web", "src", "console", "settingsView.tsx")
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
	assertNoCodeWords(t, "设置分节标题（settingsView.tsx）", m[1])

	// —— ②③ 两块（aria-label + CardHeader title）——
	panelPath := filepath.Join(root, "apps", "web", "src", "components", "settings", "ModelSettingsPanel.tsx")
	panel, err := os.ReadFile(panelPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", panelPath, err)
	}
	src := string(panel)

	ariaRE := regexp.MustCompile(`aria-label="([^"]*)"`)
	aria := ariaRE.FindAllStringSubmatch(src, -1)
	if len(aria) < 2 {
		t.Fatalf("%s 里解析到的块级 aria-label 少于 2 个（期望「供应商」与「模型」两块）："+
			"块的写法变了？请修本守卫而不是放宽它", panelPath)
	}
	for _, a := range aria {
		assertNoCodeWords(t, "块标签（aria-label）", a[1])
	}

	titleRE := regexp.MustCompile(`title="([^"]*)"`)
	titles := titleRE.FindAllStringSubmatch(src, -1)
	if len(titles) < 2 {
		t.Fatalf("%s 里解析到的 CardHeader title 少于 2 个（期望两块各一个）", panelPath)
	}
	for _, m := range titles {
		assertNoCodeWords(t, "块标题（CardHeader title）", m[1])
	}

	// 两块必须真的是「供应商」与「模型」（不是恰好没踩到禁用词的空标题）。
	wantBlocks := []string{"供应商", "模型"}
	for _, want := range wantBlocks {
		found := false
		for _, m := range aria {
			if strings.TrimSpace(m[1]) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("没找到标题恰好是 %q 的块：这一节的两个平级对象（供应商 / 模型）必须都在，"+
				"否则守卫会因为「少了东西」而误绿", want)
		}
	}

	// 面板里也不许把运行时的**不透明 value** 当展示名摆给用户看（只说 model_id 与显示名）。
	if strings.Contains(src, "JSON.stringify") {
		t.Error("面板里不该出现 JSON.stringify：运行时的 value 是不透明串，展示给用户的是 model_id 与显示名")
	}
}

// assertNoCodeWords 断言一段面向用户的文案里没有代码词。
func assertNoCodeWords(t *testing.T, where, text string) {
	t.Helper()
	low := strings.ToLower(text)
	for _, w := range forbiddenUserFacingWords {
		if strings.Contains(low, w) {
			t.Errorf("%s = %q 含代码词 %q：这一节的用户词是「供应商」「模型」，"+
				"代码词只许出现在指向具体资源/接口的地方（表格列、错误出路），不许当标题", where, text, w)
		}
	}
}
