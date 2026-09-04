package runtime

import (
	"strings"
	"testing"
)

// TestOpenCodeSpecModelEnvPassthrough opencode 子进程透传 daemon 的 OPENCODE_MODEL（且只透传它）。
func TestOpenCodeSpecModelEnvPassthrough(t *testing.T) {
	t.Setenv("OPENCODE_MODEL", "yygu-v2/deepseek-v4-flash")
	t.Setenv("OPENCODE_OTHER", "should-not-pass")

	spec := opencodeSpec()
	var passed []string
	for _, e := range spec.Launch.Env {
		passed = append(passed, e)
	}
	if len(passed) != 1 || passed[0] != "OPENCODE_MODEL=yygu-v2/deepseek-v4-flash" {
		t.Fatalf("期望仅透传 OPENCODE_MODEL，got %v", passed)
	}

	// 未设置时 Env 为空（不注入空变量）。
	t.Setenv("OPENCODE_MODEL", "")
	if s2 := opencodeSpec(); len(s2.Launch.Env) != 0 {
		t.Fatalf("未设置 OPENCODE_MODEL 时 Env 应为空: %v", s2.Launch.Env)
	}
}

// TestPassthroughEnv 只透传存在且非空的变量。
func TestPassthroughEnv(t *testing.T) {
	t.Setenv("FORGE_TEST_A", "1")
	t.Setenv("FORGE_TEST_B", "")
	t.Setenv("FORGE_TEST_C", "3")
	out := passthroughEnv("FORGE_TEST_A", "FORGE_TEST_B", "FORGE_TEST_C", "FORGE_TEST_MISSING")
	if len(out) != 2 || !strings.Contains(strings.Join(out, ","), "FORGE_TEST_A=1") ||
		!strings.Contains(strings.Join(out, ","), "FORGE_TEST_C=3") {
		t.Fatalf("passthroughEnv 过滤不符: %v", out)
	}
}
