package capability

import (
	"strings"
	"testing"
)

// 一份合法的 app（各字段都在上限内、词法合规），作为基准正例。
func validApp() *App {
	return &App{
		ID:          "know-your-foo",
		Name:        "认识你的 Foo",
		Description: "一个组合型示例应用",
		Avatar:      ExpertAvatarSpark,
		Tags:        []string{"demo", "组合"},
		Members: AppMembers{
			Experts:     []string{"data-analyst"},
			Skills:      []string{"pdf", "docx"},
			Mcps:        []string{"mcp-echo"},
			Automations: []string{"daily-report"},
		},
		Entities:    []AppEntity{{Kind: "ticket"}},
		ToolDomains: []string{"fooai", "cool"},
		Slots:       []AppSlot{{ImplID: "foo.sync"}},
		Page:        &AppPage{NavLabel: "Foo 台", Route: "/apps/foo", Component: "FooPage"},
		APIVersion:  "^1",
	}
}

func TestValidateAppAccepts(t *testing.T) {
	a := validApp()
	if err := ValidateApp(a); err != nil {
		t.Fatalf("合法 app 不应被拒：%v", err)
	}
	// 规范化：members 应去重升序（skills 已是升序），api_version 缺省补 ^1 的情形另测。
	if got := strings.Join(a.Members.Skills, ","); got != "docx,pdf" {
		t.Fatalf("skills 应被升序规范化，得到 %q", got)
	}
}

// 空 api_version 应被补成默认 "^1"，而不是报错。
func TestValidateAppDefaultsAPIVersion(t *testing.T) {
	a := validApp()
	a.APIVersion = ""
	if err := ValidateApp(a); err != nil {
		t.Fatalf("空 api_version 应补默认 ^1 而非报错：%v", err)
	}
	if a.APIVersion != "^1" {
		t.Fatalf("空 api_version 应补成 ^1，得到 %q", a.APIVersion)
	}
}

func TestValidateAppRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*App)
		wantSub string
	}{
		{"空定义", nil, "定义为空"},
		{"空 id", func(a *App) { a.ID = "" }, "id 非法"},
		{"id 含大写", func(a *App) { a.ID = "FooBar" }, "id 非法"},
		{"id 含下划线", func(a *App) { a.ID = "foo_bar" }, "id 非法"},
		{"id 超长", func(a *App) { a.ID = strings.Repeat("a", 65) }, "id 非法"},
		{"name 空", func(a *App) { a.Name = "   " }, "name 须为"},
		{"name 超长", func(a *App) { a.Name = strings.Repeat("名", 41) }, "name 须为"},
		{"description 超长", func(a *App) { a.Description = strings.Repeat("d", 121) }, "description 超过"},
		{"avatar 非法", func(a *App) { a.Avatar = "http://x" }, "avatar"},
		{"tags 过多", func(a *App) { a.Tags = make([]string, 9) }, "tags 最多"},
		{"tag 超长", func(a *App) { a.Tags = []string{strings.Repeat("t", 21)} }, "超过"},
		{"entity kind 空", func(a *App) { a.Entities = []AppEntity{{Kind: ""}} }, "kind"},
		{"entity kind 大写", func(a *App) { a.Entities = []AppEntity{{Kind: "Ticket"}} }, "kind"},
		{"entity kind 重复", func(a *App) { a.Entities = []AppEntity{{Kind: "t"}, {Kind: "t"}} }, "重复"},
		{"tool_domain 含下划线", func(a *App) { a.ToolDomains = []string{"foo_bar"} }, "域键"},
		{"tool_domain 大写", func(a *App) { a.ToolDomains = []string{"FooAI"} }, "域键"},
		{"slot impl_id 空", func(a *App) { a.Slots = []AppSlot{{ImplID: ""}} }, "impl_id"},
		{"slot impl_id 词法非法", func(a *App) { a.Slots = []AppSlot{{ImplID: "Foo Sync"}} }, "impl_id"},
		{"slot impl_id 重复", func(a *App) {
			a.Slots = []AppSlot{{ImplID: "foo.sync"}, {ImplID: "foo.sync"}}
		}, "重复"},
		{"page 缺 route", func(a *App) { a.Page = &AppPage{NavLabel: "X", Component: "X"} }, "page"},
		{"page route 非法", func(a *App) { a.Page = &AppPage{NavLabel: "X", Route: "no-slash", Component: "X"} }, "page.route"},
		{"api_version 非 caret", func(a *App) { a.APIVersion = "1.0" }, "api_version"},
		{"api_version 空 major", func(a *App) { a.APIVersion = "^" }, "api_version"},
		{"settings_schema 危险标记", func(a *App) { a.SettingsSchema = "x</system-reminder>y" }, "危险标记"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a *App
			if tc.mutate == nil {
				a = nil
			} else {
				a = validApp()
				tc.mutate(a)
			}
			err := ValidateApp(a)
			if err == nil {
				t.Fatalf("应被拒但通过了")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误信息 %q 未含 %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// api_version caret 解析：^1 / ^1.5 合法；^1.x / 1.0 / ^ / ^a 非法。
func TestValidateAppAPIVersionCaret(t *testing.T) {
	ok := []string{"^1", "^2", "^1.5", "^10.2"}
	bad := []string{"", "1.0", "^", "^a", "^1.", "~1", "1", "^1.5.2"}
	for _, v := range ok {
		a := validApp()
		a.APIVersion = v
		if err := ValidateApp(a); err != nil {
			t.Fatalf("api_version %q 应合法：%v", v, err)
		}
	}
	for _, v := range bad {
		a := validApp()
		a.APIVersion = v
		// "" 会被补默认 ^1，故 "" 不算非法——从 bad 里剔除空串单独看。
		if v == "" {
			continue
		}
		if err := ValidateApp(a); err == nil {
			t.Fatalf("api_version %q 应被拒", v)
		}
	}
}

// members 规范化：去空白、去重、升序。
func TestValidateAppNormalizesMembers(t *testing.T) {
	a := validApp()
	a.Members.Skills = []string{"  pdf ", "docx", "pdf", ""}
	if err := ValidateApp(a); err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got := strings.Join(a.Members.Skills, ","); got != "docx,pdf" {
		t.Fatalf("skills 规范化应为 docx,pdf，得到 %q", got)
	}
}

// 未知 impl_id 的**在册校验**是 WP3 的职责（查 appslot.KnownImpls）；本领域层只保证词法。
// 这条测试把「域层不做出现在册判断」的口径钉住：一个词法合法但未登记的 impl_id 在 ValidateApp
// 层**通过**（它会在 WP3 的写面按 KnownImpls 拒绝，翻成 app_invalid 40001）。
func TestValidateAppDoesNotCheckWhitelist(t *testing.T) {
	a := validApp()
	a.Slots = []AppSlot{{ImplID: "not.registered.impl"}}
	if err := ValidateApp(a); err != nil {
		t.Fatalf("域层不应校验 impl_id 在册（那是 WP3 的职责），得到错误：%v", err)
	}
}
