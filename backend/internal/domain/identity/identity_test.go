package identity

import (
	"strings"
	"testing"
	"unicode/utf8"

	pkgidentity "github.com/morehao/ark-mate/internal/platform/identity"
)

// ——— 产品身份文案的边界（P4 的确定性判据） ———
//
// A1（"问你是谁 3/3 答 ark-mate"）是**模型行为**验收，天然有随机性。这里钉的是它能成立的
// 三个前提，全都是确定性的：
//
//	① 两段文案都点名 ark-mate（否则模型没有理由自称它）；
//	② 文案里不出现任何执行引擎名（否则模型会把运行时名当自己的名字——那正是本方案要修的病）；
//	③ 文案里不出现模板标记（否则某底座的模板渲染会在**首轮组装**时抛错，每次会话都失败）。

// runtimeWords 身份文案里不得出现的执行引擎名（大小写不敏感）。
//
// 刻意保留已下线底座的名字：这条判据的语义是"身份文本不得引用任何底座"，不是"仓库里还有谁"。
var runtimeWords = []string{"dsh", "deepseek harness", "opencode", "codebuddy", "claude"}

func TestRenderNamesTheProduct(t *testing.T) {
	spec := Render()
	for name, text := range map[string]string{"systemPrompt": spec.SystemPrompt, "userContext": spec.UserContext} {
		if !strings.Contains(text, Name) {
			t.Fatalf("%s 里没有产品名 %q：模型没有理由自称它", name, Name)
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("%s 为空", name)
		}
	}
	if spec.Version != Version || spec.Name != Name || spec.DisplayName != DisplayName || spec.Tagline != Tagline {
		t.Fatalf("Render 的产品面字段与常量不一致: %+v", spec)
	}
}

func TestPromptsCarryNoRuntimeNames(t *testing.T) {
	spec := Render()
	lowered := strings.ToLower(spec.SystemPrompt + "\n" + spec.UserContext)
	for _, w := range runtimeWords {
		if strings.Contains(lowered, w) {
			t.Fatalf("身份文案里出现了执行引擎名 %q：身份是产品人格，不是运行时的别名", w)
		}
	}
}

func TestPromptsCarryNoTemplateMarkers(t *testing.T) {
	spec := Render()
	for name, text := range map[string]string{"systemPrompt": spec.SystemPrompt, "userContext": spec.UserContext} {
		if pkgidentity.HasTemplateBraces(text) {
			t.Fatalf("%s 含模板花括号：底座的 persona 是模板，未知变量在首轮组装时才抛错", name)
		}
		if strings.Contains(text, "{%") || strings.Contains(text, "%}") {
			t.Fatalf("%s 含模板标签（{%% / %%}）", name)
		}
	}
}

// TestPromptBudgetIsBounded 预算上限：这两段字每轮（系统提示）或每个执行上下文（首轮块）
// 都要进 token 账单，写长了是真金白银。下限挡的是"文案被清空"。
func TestPromptBudgetIsBounded(t *testing.T) {
	spec := Render()
	sys := utf8.RuneCountInString(spec.SystemPrompt)
	if sys < 120 || sys > 900 {
		t.Fatalf("系统提示段 %d 个字符，超出预算（120–900）", sys)
	}
	uc := utf8.RuneCountInString(spec.UserContext)
	if uc < 60 || uc > 600 {
		t.Fatalf("首轮块 %d 个字符，超出预算（60–600）", uc)
	}
}

// TestRenderIsDeterministic 同一次构建必须得到同一份字节：身份正文含时间/随机数会让系统提示
// 的前缀缓存（KV cache）每轮失效，而这件事只在账单上现形。
func TestRenderIsDeterministic(t *testing.T) {
	a, b := Render(), Render()
	if a != b {
		t.Fatal("Render 不是确定性的：两次渲染得到不同的 Spec")
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("确定性渲染的指纹必须相同")
	}
}

// TestModeApply 四个模式各自关掉哪些面（A7 的静态那一半）。
func TestModeApply(t *testing.T) {
	full := Render()
	cases := []struct {
		mode                        pkgidentity.Mode
		wantSystem, wantUserContext bool
	}{
		{pkgidentity.ModeAuto, true, true},
		{pkgidentity.ModeSystem, true, false},
		{pkgidentity.ModeUser, false, true},
		{pkgidentity.ModeOff, false, false},
	}
	for _, c := range cases {
		got := c.mode.Apply(full)
		if got.UsesSystemPrompt() != c.wantSystem || got.UsesUserContext() != c.wantUserContext {
			t.Fatalf("模式 %s：system=%v user=%v（期望 system=%v user=%v）",
				c.mode, got.UsesSystemPrompt(), got.UsesUserContext(), c.wantSystem, c.wantUserContext)
		}
	}
}

// TestModeParse 取值域：空 = auto；非法值必须报错（静默回落会让"我以为关掉了"与"还在交付"
// 长得一模一样）。
func TestModeParse(t *testing.T) {
	if m, err := pkgidentity.ParseMode(""); err != nil || m != pkgidentity.ModeAuto {
		t.Fatalf("空值应为 auto，得到 %q / %v", m, err)
	}
	if _, err := pkgidentity.ParseMode("nope"); err == nil {
		t.Fatal("非法取值应报错")
	}
}
