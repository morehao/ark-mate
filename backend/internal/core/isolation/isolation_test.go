package isolation

import (
	"path/filepath"
	"testing"
)

// TestSatisfiesOrdering 强度序：actual>=declared 才满足（same 或更高档满足）。
func TestSatisfiesOrdering(t *testing.T) {
	cases := []struct {
		declared, actual Level
		want             bool
	}{
		{LevelNone, LevelNone, true},
		{LevelNone, LevelOSUser, true}, // 声明更低，实际更高：满足
		{LevelNone, LevelSandboxed, true},
		{LevelOSUser, LevelOSUser, true},
		{LevelOSUser, LevelSandboxed, true},
		{LevelSandboxed, LevelSandboxed, true},
		// 反目标 2：实际低于声明 → 一律不满足（绝不降级）
		{LevelOSUser, LevelNone, false},
		{LevelSandboxed, LevelOSUser, false},
		{LevelSandboxed, LevelNone, false},
	}
	for _, c := range cases {
		if got := Satisfies(c.declared, c.actual); got != c.want {
			t.Errorf("Satisfies(declared=%s, actual=%s)=%v want %v", c.declared, c.actual, got, c.want)
		}
	}
}

// TestSatisfiesFailClosedOnInvalid：任一档非法 → fail-closed（不满足），绝不放过。
func TestSatisfiesFailClosedOnInvalid(t *testing.T) {
	if Satisfies("bogus", LevelSandboxed) {
		t.Fatal("非法声明档应判不满足（fail-closed）")
	}
	if Satisfies(LevelSandboxed, "bogus") {
		t.Fatal("非法实际档应判不满足（fail-closed）")
	}
	if Satisfies("", "") {
		t.Fatal("空档应判不满足")
	}
}

// TestParseLevel：合法解析、非法报错（不静默回落默认档）。
func TestParseLevel(t *testing.T) {
	for _, s := range []string{"none", "os_user", "sandboxed"} {
		l, err := ParseLevel(s)
		if err != nil || string(l) != s {
			t.Fatalf("ParseLevel(%q)=%q,%v", s, l, err)
		}
	}
	for _, s := range []string{"", "shared", "OS_USER", "root", "docker"} {
		if _, err := ParseLevel(s); err == nil {
			t.Fatalf("ParseLevel(%q) 应报错（非法档不得静默接受）", s)
		}
	}
}

// TestWorkspaceRootLayout：按 <base>/workspaces/<tenant>/<user>/<slug> 组织。
func TestWorkspaceRootLayout(t *testing.T) {
	got, err := WorkspaceRoot("/data/mate", "t1", "u1", "ses_abc")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/data/mate", "workspaces", "t1", "u1", "ses_abc")
	if got != want {
		t.Fatalf("布局不符：got %q want %q", got, want)
	}
}

// TestWorkspaceRootRejectsInjection：任一段含分隔符/./.. → 拒绝（防跨租户越权写）。
func TestWorkspaceRootRejectsInjection(t *testing.T) {
	bad := []struct{ tenant, user, slug string }{
		{"../other", "u1", "s1"},
		{"t1", "u1", "../../etc"},
		{"t1", "u1", "a/b"},
		{"t1", "u1", ".."},
		{"t1", "", "s1"},
		{"", "u1", "s1"},
	}
	for _, b := range bad {
		if got, err := WorkspaceRoot("/data", b.tenant, b.user, b.slug); err == nil {
			t.Errorf("应拒绝非法段 tenant=%q user=%q slug=%q，却得到 %q", b.tenant, b.user, b.slug, got)
		}
	}
}

// TestSingleSegment：边界（空/./../含分隔符 → false；普通串 → true）。
func TestSingleSegment(t *testing.T) {
	for _, s := range []string{"", ".", "..", "a/b", `a\b`} {
		if SingleSegment(s) {
			t.Errorf("SingleSegment(%q) 应为 false", s)
		}
	}
	for _, s := range []string{"ses_01ABC", "u-1", "tenant.1", "a"} {
		if !SingleSegment(s) {
			t.Errorf("SingleSegment(%q) 应为 true", s)
		}
	}
}
