package manager

import (
	"strings"
	"testing"
)

// TestDeriveTitleCJKShort 回归：中文 prompt「字节数超过 48 但 rune 数不足 48」
// 不得触发 runes[:48] 越界（旧实现按 len()=字节判断会 panic）。
func TestDeriveTitleCJKShort(t *testing.T) {
	// 20 个汉字 = 60 字节 > 48，但 runes=20 < 48：旧实现 panic，新实现原样返回。
	text := strings.Repeat("字", 20)
	got := deriveTitle(text)
	if got != text {
		t.Fatalf("短 CJK 不应截断: got %q", got)
	}
}

func TestDeriveTitleTruncatesByRunes(t *testing.T) {
	// 长 ASCII：60 个字符 → 截到 48 runes + "…"（49 runes）。
	long := strings.Repeat("a", 60)
	got := deriveTitle(long)
	if len([]rune(got)) != 49 || !strings.HasSuffix(got, "…") {
		t.Fatalf("长 ASCII 截断不符: len(runes)=%d %q", len([]rune(got)), got)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 48)) {
		t.Fatalf("截断丢失前缀: %q", got)
	}

	// 60 个汉字 → 同样按 48 runes 截断（rune 计数，不是字符数按字节错切）。
	cjk := strings.Repeat("汉", 60)
	gotCJK := deriveTitle(cjk)
	if len([]rune(gotCJK)) != 49 || !strings.HasPrefix(gotCJK, strings.Repeat("汉", 48)) {
		t.Fatalf("长 CJK 截断不符: %q", gotCJK)
	}
}

func TestDeriveTitleCollapseSpace(t *testing.T) {
	if got := deriveTitle("  a   b \t c  "); got != "a b c" {
		t.Fatalf("空白折叠不符: %q", got)
	}
	if got := deriveTitle(""); got != "" {
		t.Fatalf("空串应原样: %q", got)
	}
}
