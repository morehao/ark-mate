package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住"能力声明"这条边的**两端一致性**（2026-10-11，P3/D5）。
//
// 背景：`GET /capabilities` 描述的是"桌面壳比浏览器多给了什么"。它此前声明了六条，其中四条
// （`open-path` / `diagnostics-export` / `deep-link` / `update`）在两个入口里**都没有任何消费者**：
// 前端契约里声明了方法、preload 实现了、主进程也接了 IPC 并带越权校验，而唯一的界面
// （`apps/web/src`，2026-10-11 P5 起整体搬进 `packages/console/src`）一次都没调用。这种缺口不会让任何别的测试变红——它只会让下一个读
// `/capabilities` 的人以为产品支持"用系统程序打开路径"，并据此设计功能。
//
// 判据三段，全部 **fail-closed**（解析不到即失败，而不是"没有可检查对象"就变绿）：
//  ① Go 侧的 `capabilityDecls` **能解析出来**且非空（改名/搬文件会让本条静默空转）；
//  ② 它与前端契约 `CapabilityId` 联合类型**逐条相等**（这条边此前只有注释在维护）；
//  ③ 每条声明必须在界面源码（`uiSrcRoots()`：apps/web + packages/console + packages/ui）里**被真实引用**（剥注释后找 id 字面量）；
//     确需保留"界面不读"的必须登记进 `declaredWithoutConsumer` 并写明理由——登记的每一条同样
//     会被校验（理由不能是空串，"先登记个空的"过不去）。
//
// 反证（改完请手工过一遍）：① 把 `capabilityDecls` 里加一条 `{"open-path", "ui:show-path-copy", false}`
// → ③ 立刻红（界面源码里没有 `open-path`）；② 只改前端联合类型、不改 Go → ② 红。

// declaredWithoutConsumer 允许"声明了但界面不读"的能力，**必须写清为什么可以**。
//
// 只放一条：托盘是纯增强（没有浏览器等价物、也不阻塞任何流程），壳真的装了它
// （`apps/desktop/src/tray.ts`），只是控制台界面不需要读这条声明。其余任何条目都该有消费者——
// 没有消费者就删掉声明，而不是登记进这里。
var declaredWithoutConsumer = map[string]string{
	"tray": "纯增强能力：壳装了托盘（apps/desktop/src/tray.ts），但界面上没有任何去处需要知道它是否存在；" +
		"它也是唯一允许 browserFallback=none 的能力（V3：不阻塞任何流程才允许没有浏览器等价路径）",
}

// capabilityDeclPattern 抓 Go 里 `capabilityDecls` 的条目：`{"id", "fallback", bool}`。
var capabilityDeclPattern = regexp.MustCompile(`\{\s*"([a-z0-9-]+)"\s*,\s*"([a-z0-9:-]+)"\s*,\s*(?:true|false)\s*\}`)

// capabilityIDPattern 抓前端契约里 `CapabilityId` 联合类型的成员。
var capabilityIDPattern = regexp.MustCompile(`'([a-z0-9-]+)'`)

// TestDeclaredCapabilitiesHaveConsumers 见文件头。
func TestDeclaredCapabilitiesHaveConsumers(t *testing.T) {
	root := frontendRoot(t)
	repoRoot := filepath.Dir(root)

	declSrc, err := os.ReadFile(filepath.Join(repoRoot, "backend", "internal", "controller", "ctrsystem", "system.go"))
	if err != nil {
		t.Fatalf("读 capabilityDecls 失败: %v——守卫没有可检查的对象，不允许静默通过", err)
	}
	declBlock := extractGoVarBlock(stripGoComments(string(declSrc)), "capabilityDecls")
	if declBlock == "" {
		t.Fatal("解析不到 capabilityDecls 的变量块（改名/换写法请同步本守卫，别让它空转）")
	}
	var goIDs []string
	for _, m := range capabilityDeclPattern.FindAllStringSubmatch(declBlock, -1) {
		goIDs = append(goIDs, m[1])
	}
	if len(goIDs) == 0 {
		t.Fatal("capabilityDecls 解析出 0 条能力——解析式失效了，不是真的没有能力")
	}

	// ② 与前端契约的联合类型逐条相等（顺序也要一致：前端可以按下标渲染）。
	contractSrc, err := os.ReadFile(filepath.Join(root, "packages", "desktop-contract", "src", "index.ts"))
	if err != nil {
		t.Fatalf("读前端契约失败: %v", err)
	}
	union := extractTSTypeUnion(stripTSComments(string(contractSrc)), "CapabilityId")
	if union == "" {
		t.Fatal("解析不到前端契约的 CapabilityId 联合类型（改名/换写法请同步本守卫）")
	}
	tsIDs := capabilityIDPattern.FindAllStringSubmatch(union, -1)

	t.Run("② Go 声明与前端联合类型逐条一致", func(t *testing.T) {
		if len(tsIDs) != len(goIDs) {
			t.Fatalf("条数不同：Go %d 条 %v，前端 %d 条", len(goIDs), goIDs, len(tsIDs))
		}
		for i := range goIDs {
			if tsIDs[i][1] != goIDs[i] {
				t.Errorf("第 %d 条不同：Go %q，前端 %q（顺序也是契约：前端可按下标渲染）", i, goIDs[i], tsIDs[i][1])
			}
		}
	})

	// ③ 每条声明都要有界面消费者（或在登记表里写明为什么可以没有）。
	t.Run("③ 每条声明都有界面消费者，或已登记理由", func(t *testing.T) {
		files := listUISources(t) // 界面可能同时在 apps/web 与 packages/console（见 uiSrcRoots）
		// 红的时候要能一眼看出"在哪些根里找过了"——否则读者会去改一个本来就没漏的目录。
		var rootsTxt []string
		for _, r := range uiSrcRoots(t) {
			if rel, err := filepath.Rel(root, r); err == nil {
				rootsTxt = append(rootsTxt, rel)
			}
		}
		// 先把整个 UI 源码表拼起来：id 出现在哪一行不重要，"有没有人读"才重要。
		var sb strings.Builder
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("读 %s 失败: %v", f, err)
			}
			sb.WriteString(stripTSComments(string(raw)))
			sb.WriteString("\n")
		}
		ui := sb.String()

		for _, id := range goIDs {
			// 命中形态：`'native-dialog'` / `"native-dialog"`（读声明、比对、当键用都算）。
			consumed := strings.Contains(ui, `"`+id+`"`) || strings.Contains(ui, `'`+id+`'`)
			if consumed {
				if _, registered := declaredWithoutConsumer[id]; registered {
					t.Errorf("能力 %q 已经被界面源码引用了，请把它从 declaredWithoutConsumer 登记表里删掉"+
						"（登记表只放「真的没人读」的，否则它会掩盖下一个没人读的能力）", id)
				}
				continue
			}
			reason, registered := declaredWithoutConsumer[id]
			if !registered {
				t.Errorf("能力 %q 在界面源码（%s）里没有任何消费者：**要么删掉这条声明**（连同契约里对应的方法），"+
					"要么登记进 declaredWithoutConsumer 并写明为什么它可以没人读"+
					"（一份「声明了但没人执行」的能力面会让下一个人以为产品支持它）", id, strings.Join(rootsTxt, ", "))
				continue
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("能力 %q 登记了但没写理由——理由是这条登记的**全部价值**", id)
			}
		}
	})

	t.Run("机制自检：两个解析式都能抓住已知形状", func(t *testing.T) {
		if got := capabilityDeclPattern.FindAllStringSubmatch(`{"tray", "none", false}`, -1); len(got) != 1 {
			t.Error("capabilityDeclPattern 抓不住 `{\"tray\", \"none\", false}`——解析式坏了")
		}
		if got := capabilityIDPattern.FindAllStringSubmatch(`'tray' | 'native-dialog'`, -1); len(got) != 2 {
			t.Error("capabilityIDPattern 抓不住联合类型成员——解析式坏了")
		}
	})
}

// extractGoVarBlock 取 `var <name> = ... { ... }` 的**花括号块**（含外层括号），取不到返回空串。
//
// 为什么不用 go/parser：这个文件里其它守卫都是"读源码文本 + 正则"的同一套路（守卫读的是
// **形状**而不是语义）；引 go/parser 会把"声明换了一种写法"从"守卫红"变成"守卫静默空转"。
func extractGoVarBlock(src, name string) string {
	idx := strings.Index(src, "var "+name+" =")
	if idx < 0 {
		return ""
	}
	start := strings.Index(src[idx:], "{")
	if start < 0 {
		return ""
	}
	start += idx
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return ""
}

// extractTSTypeUnion 取 `export type <name> = A | B` 到行尾（或到下一个顶层声明）的文本。
func extractTSTypeUnion(src, name string) string {
	idx := strings.Index(src, "export type "+name+" =")
	if idx < 0 {
		return ""
	}
	rest := src[idx:]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
