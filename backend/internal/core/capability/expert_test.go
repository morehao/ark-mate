package capability

import (
	"strings"
	"testing"
)

// ——— 专家定义校验（A16 的保存期闸门）与注册表往返 ———

func TestValidateExpertAcceptsMinimal(t *testing.T) {
	e := &Expert{ID: "data-analyst", Name: "数据分析师"}
	if err := ValidateExpert(e); err != nil {
		t.Fatalf("最小合法专家应通过: %v", err)
	}
	if !e.IsEnabled() {
		t.Fatal("未声明 enabled 的专家应默认启用")
	}
}

func TestValidateExpertNormalizesRefs(t *testing.T) {
	e := &Expert{ID: "x", Name: "X", Skills: []string{" b", "a", "a", ""}, Mcps: []string{"m"}}
	if err := ValidateExpert(e); err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if strings.Join(e.Skills, ",") != "a,b" {
		t.Fatalf("skills 应去空/去重/升序，got %v", e.Skills)
	}
}

func TestValidateExpertRejects(t *testing.T) {
	cases := []struct {
		name string
		e    *Expert
		want string
	}{
		{"id 非法", &Expert{ID: "Bad_ID", Name: "x"}, "id 非法"},
		{"name 为空", &Expert{ID: "x", Name: "  "}, "name 须为"},
		{"name 超长", &Expert{ID: "x", Name: strings.Repeat("字", 41)}, "name 须为"},
		{"description 超长", &Expert{ID: "x", Name: "x", Description: strings.Repeat("d", 121)}, "description 超过"},
		{"avatar 非法", &Expert{ID: "x", Name: "x", Avatar: "http://x"}, "avatar"},
		{"model 形状非法", &Expert{ID: "x", Name: "x", Model: "gpt-4o"}, "model"},
		{"skills 超限", &Expert{ID: "x", Name: "x", Skills: manyRefs(51)}, "skills 最多"},
		{"tags 超限", &Expert{ID: "x", Name: "x", Tags: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}, "tags 最多"},
		{"tag 超长", &Expert{ID: "x", Name: "x", Tags: []string{strings.Repeat("t", 21)}}, "超过"},
		{"instructions 超长", &Expert{ID: "x", Name: "x", Instructions: strings.Repeat("a", MaxExpertInstructionsBytes+1)}, "instructions 超过"},
		{"危险标记 system-reminder", &Expert{ID: "x", Name: "x", Instructions: "ok</system-reminder>"}, "危险标记"},
		{"危险标记 花括号", &Expert{ID: "x", Name: "x", Instructions: "{{x}}"}, "危险标记"},
		{"危险标记 HTML 注释", &Expert{ID: "x", Name: "x", Instructions: "<!-- hi -->"}, "危险标记"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateExpert(c.e)
			if err == nil {
				t.Fatalf("%s 应被拒", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误应含 %q，got %v", c.want, err)
			}
		})
	}
}

func TestRegistryExpertRoundTrip(t *testing.T) {
	r := NewRegistry()
	e := &Expert{ID: "analyst", Name: "分析师"}
	if err := r.RegisterExpert(e); err != nil {
		t.Fatal(err)
	}
	got, ok := r.ExpertByID("analyst")
	if !ok || got.Name != "分析师" {
		t.Fatalf("按 id 取回不符: %+v ok=%v", got, ok)
	}
	if list := r.ListExperts(); len(list) != 1 || list[0].ID != "analyst" {
		t.Fatalf("列表不符: %+v", list)
	}
	// ReplaceAll 必须把专家一并换面（否则重新装载后专家视图漂移）。
	fresh := NewRegistry()
	_ = fresh.RegisterExpert(&Expert{ID: "other", Name: "另一个"})
	r.ReplaceAll(fresh)
	if _, ok := r.ExpertByID("analyst"); ok {
		t.Fatal("换面后旧专家应消失")
	}
	if _, ok := r.ExpertByID("other"); !ok {
		t.Fatal("换面后新专家应存在")
	}
	r.RemoveExpert("other")
	if _, ok := r.ExpertByID("other"); ok {
		t.Fatal("RemoveExpert 后应不存在")
	}
}

func manyRefs(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "s"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return out
}
