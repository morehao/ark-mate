package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// formInterfaceRE 抓「表单取值接口」的声明与成员体（设置 · 模型 的两张就地展开的表单卡）。
// 只认这两个名字：改名即失败（fail-closed），避免守卫静默空转。
var formInterfaceRE = regexp.MustCompile(`interface\s+(ProviderFormValues|ModelFormValues)\s*\{([^}]*)\}`)

// formFieldRE 取成员体里的**键名**（`image_input?: boolean` → `image_input`）。
var formFieldRE = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\??\s*:`)

// TestSettingsModelFormRendersEveryField 设置 · 模型的**表单字段**必须真的画出来。
//
// 它挡的是一类**静默**缺口：`interface XxxFormValues` 里声明了键、`submitXxx` 也读了它，
// 但卡片里没有对应的 `<Form.Item name="…">`。antd 的 `Form.validateFields()` **只返回已注册
// 字段**，所以那个键恒为 `undefined`——"用户以为填了""用户以为没改"与"其实发的是默认值"
// 看起来一模一样，编译、typecheck、别的守卫全绿。
//
// 2026-10-11 的真实事故（本次修的）：`ModelFormValues.image_input` 三处被读写、表单里却没有
// 对应的 Form.Item，于是**只改一次备注**就会把该模型已声明的图片能力静默改回仅文本；
// 同一次故障里"字符串字面量"那半由 `settings_copy_test.go` 管，这里管"结构"那半。
//
// 三条纪律：
//   - 判据是"每个键都有 `name="<键>"`"，不是"有 Form.Item"（后者对不上号）；
//   - 先剥注释再解析（注释里出现键名是正常的，不该算数）；
//   - **解析不到即失败**：接口被改名/搬家、或某个接口的成员体读成空，都当场判失败——
//     否则守卫会在一次重构之后安静地什么都不检查。
func TestSettingsModelFormRendersEveryField(t *testing.T) {
	panelPath := filepath.Join(frontendRoot(t), "packages", "console", "src", "components", "settings", "ModelSettingsPanel.tsx")
	raw, err := os.ReadFile(panelPath)
	if err != nil {
		t.Fatalf("读不到 %s: %v（路径改了请同步本守卫）", panelPath, err)
	}
	src := stripTSComments(string(raw))

	matches := formInterfaceRE.FindAllStringSubmatch(src, -1)
	if len(matches) != 2 {
		t.Fatalf("在 %s 里找到 %d 个表单接口（应为 2 个：ProviderFormValues / ModelFormValues）；"+
			"接口被改名或改了写法？本守卫是 fail-closed：解析不到即失败，避免它变成空转",
			panelPath, len(matches))
	}

	seen := map[string]bool{}
	for _, m := range matches {
		name, body := m[1], m[2]
		if seen[name] {
			t.Fatalf("%s 里重复声明了 %s", panelPath, name)
		}
		seen[name] = true

		fields := formFieldRE.FindAllStringSubmatch(body, -1)
		if len(fields) == 0 {
			t.Fatalf("%s 的 %s 读不出任何字段（成员体解析失败）——守卫 fail-closed", panelPath, name)
		}
		for _, f := range fields {
			key := f[1]
			// 表单字段在 JSX 里以字符串字面量形式出现（`name="modalities"`），
			// 所以剥注释那一步刻意保留引号内的内容。
			if !strings.Contains(src, `name="`+key+`"`) {
				t.Errorf("%s 的 %s 声明了 %q，但同文件里没有 `<Form.Item name=%q>`："+
					"未注册的字段在 `validateFields()` 里恒为 undefined——"+
					"用户填不进、也改不动，而提交逻辑会按默认值静默改写既有声明",
					panelPath, name, key, key)
			}
		}
	}
}
