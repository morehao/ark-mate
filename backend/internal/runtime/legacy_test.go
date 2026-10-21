package runtime

import (
	"testing"
)

// TestNormalizeID 历史别名归一（2026-10-11：注册表键 `deepseekharness` → `dsh`）。
//
// 这三条边界合起来才是"只归一该归一的东西"：老数据要认得出来，而"未指定""用户自己声明的
// 远程 id"一个字都不许被改写。
func TestNormalizeID(t *testing.T) {
	if got := NormalizeID(LegacyDeepseekharnessID); got != DefaultRuntimeID {
		t.Fatalf("历史别名应归一到 %q，得到 %q", DefaultRuntimeID, got)
	}
	if got := NormalizeID(" " + LegacyDeepseekharnessID + " "); got != DefaultRuntimeID {
		t.Fatalf("带空白的历史别名也应归一（配置/定义里手工编辑常见），得到 %q", got)
	}
	if got := NormalizeID(""); got != "" {
		t.Fatalf("空串是「未指定」，不是别名：得到 %q", got)
	}
	for _, id := range []string{DefaultRuntimeID, "opencode", "my-remote-runtime", "deepseek-harness"} {
		if got := NormalizeID(id); got != id {
			t.Fatalf("%q 不该被改写，得到 %q", id, got)
		}
	}
	// 展示名 `deepseek-harness` **刻意不映射**：把它当 id 认，会让"用户自己声明了一个叫这名字的
	// 远程 runtime"被静默改写（见 NormalizeID 的注释）。
	if NormalizeIDs(nil) != nil {
		t.Fatal("nil 应原样返回 nil（保持「未声明」与「空数组」在 wire 上的可区分性）")
	}
	if got := NormalizeIDs([]string{LegacyDeepseekharnessID, "opencode"}); len(got) != 2 || got[0] != DefaultRuntimeID || got[1] != "opencode" {
		t.Fatalf("逐个归一不符: %v", got)
	}
}

// TestLegacyAliasesOf 别名只挂在 dsh 名下（别让别的 runtime 认领到它的旧名字）。
func TestLegacyAliasesOf(t *testing.T) {
	got := LegacyAliasesOf(DefaultRuntimeID)
	if len(got) != 1 || got[0] != LegacyDeepseekharnessID {
		t.Fatalf("dsh 的历史别名应为 [%s]，得到 %v", LegacyDeepseekharnessID, got)
	}
	for _, id := range []string{"opencode", "my-remote-runtime", ""} {
		if a := LegacyAliasesOf(id); len(a) != 0 {
			t.Fatalf("%q 不该有历史别名，得到 %v", id, a)
		}
	}
}
