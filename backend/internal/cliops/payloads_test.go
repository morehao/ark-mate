package cliops

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtosystem"
)

// TestParsePayloadsValid 合法清单被原样解析成结构化值：字段一一对应、顺序保持、entry 被忽略。
func TestParsePayloadsValid(t *testing.T) {
	raw := `[{"name":"dsh","version":"0.1.7-rc.2","digest":"sha256:aa","entry":"/abs/lib/bin.js","source":"bundled"},` +
		`{"name":"rtx","version":"1.2.3","digest":"sha256:bb","source":"runtime-env"},` +
		`{"name":"dsh-extra","version":"","digest":"","source":"path"}]`
	got, err := parsePayloads(raw)
	if err != nil {
		t.Fatalf("合法清单不应报错: %v", err)
	}
	want := []dtosystem.PayloadInfo{
		{Name: "dsh", Version: "0.1.7-rc.2", Digest: "sha256:aa", Source: "bundled"},
		{Name: "rtx", Version: "1.2.3", Digest: "sha256:bb", Source: "runtime-env"},
		{Name: "dsh-extra", Source: "path"}, // path 命中可能报不出版本与摘要（如实留空，不编）
	}
	if len(got) != len(want) {
		t.Fatalf("条数不符: got %d want %d（%+v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条不符: got %+v want %+v", i+1, got[i], want[i])
		}
	}
}

// TestParsePayloadsEmptyArrayStaysArray 空数组解析成**非 nil** 的空切片。
//
// 为什么单钉这一条：`/meta` 的 payloads 契约是"恒为数组"（壳/前端只写一种分支），
// 而 Go 里 nil 切片会 marshal 成 `null`——两者在 JS 里行为不同。这里守住前半段，
// 响应形状由 ctrsystem 的 Snapshot 与 internal/app 的透出用例共同守住。
func TestParsePayloadsEmptyArrayStaysArray(t *testing.T) {
	got, err := parsePayloads("[]")
	if err != nil {
		t.Fatalf("空数组应合法（= 壳解析出零份载荷）: %v", err)
	}
	if got == nil {
		t.Fatal("空数组必须解析成非 nil 的空切片（nil 会被 marshal 成 null）")
	}
	if len(got) != 0 {
		t.Fatalf("空数组不该产出条目: %+v", got)
	}
}

// TestParsePayloadsRejectsInvalid 非法清单一律在启动期报错（宁可起不来，也不要展示假来源）。
func TestParsePayloadsRejectsInvalid(t *testing.T) {
	// 16 条上限 + 1（合法条目的最小形状）。
	over := make([]string, 0, maxPayloads+1)
	for i := 0; i <= maxPayloads; i++ {
		over = append(over, `{"name":"p`+string(rune('a'+i))+`","source":"bundled"}`)
	}
	cases := []struct {
		name string
		raw  string
		want string // 期望错误文案里出现的关键词
	}{
		{"JSON 语法错误", `[{"name":"dsh",}]`, "不是合法 JSON"},
		{"不是数组", `{"name":"dsh"}`, "期望一个 JSON 数组"},
		{"显式 null", `null`, "期望一个 JSON 数组"},
		{"缺 name", `[{"version":"1.0.0","source":"bundled"}]`, "缺 name"},
		{"name 只有空白", `[{"name":"   ","source":"bundled"}]`, "缺 name"},
		{"缺 source", `[{"name":"dsh"}]`, "缺 source"},
		{"source 非法", `[{"name":"dsh","source":"npm"}]`, "只接受 bundled|runtime-env|path"},
		{"source 大小写不符", `[{"name":"dsh","source":"Bundled"}]`, "只接受"},
		{"name 重复", `[{"name":"dsh","source":"bundled"},{"name":"dsh","source":"path"}]`, "重复"},
		{"超上限", `[` + strings.Join(over, ",") + `]`, "超过上限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePayloads(tc.raw)
			if err == nil {
				t.Fatalf("应报错，实际通过: %+v", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案应含 %q，实际: %v", tc.want, err)
			}
			if !strings.HasPrefix(err.Error(), "--payloads:") {
				t.Fatalf("错误文案应带 --payloads 前缀（用户要知道是哪个 flag）: %v", err)
			}
		})
	}
}
