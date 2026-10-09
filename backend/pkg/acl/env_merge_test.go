package acl

import "testing"

// TestMergeEnvExplicitWins 显式传入的环境（含 M3-3 的 runtime 配置隔离）压过 os.Environ 的同名项。
//
// 为什么这条必须有：`cmd.Env` 里同名两条时，POSIX `getenv` 取**第一条**（用户环境赢）、
// Node `process.env` 取**最后一条**（我们的值赢）——同一份进程环境在 Go 子进程与 JS 子进程里
// 语义相反，隔离就守不住了。合并后必须"每键一次 + 显式值胜出"。
func TestMergeEnvExplicitWins(t *testing.T) {
	base := []string{"PATH=/usr/bin", "XDG_CONFIG_HOME=/Users/me/.config", "LANG=zh_CN.UTF-8"}
	override := []string{"XDG_CONFIG_HOME=/home/mate/runtime/opencode/config", "DSH_HOME=/home/mate/runtime/dsh"}

	got := mergeEnv(base, override)

	seen := map[string]int{}
	for _, kv := range got {
		key, _, _ := cutKV(kv)
		seen[key]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Fatalf("%s 出现 %d 次：%v", key, n, got)
		}
	}
	if !hasKV(got, "XDG_CONFIG_HOME=/home/mate/runtime/opencode/config") {
		t.Fatalf("显式值没赢：%v", got)
	}
	if !hasKV(got, "PATH=/usr/bin") || !hasKV(got, "LANG=zh_CN.UTF-8") {
		t.Fatalf("未冲突的既有项丢了：%v", got)
	}
	if !hasKV(got, "DSH_HOME=/home/mate/runtime/dsh") {
		t.Fatalf("新增项丢了：%v", got)
	}
}

// TestMergeEnvKeepsPosition 覆盖发生在原位（顺序稳定 → 两次启动可逐字比对）。
func TestMergeEnvKeepsPosition(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=old", "C=3"}, []string{"B=new"})
	want := []string{"A=1", "B=new", "C=3"}
	if len(got) != len(want) {
		t.Fatalf("长度变了：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项：期望 %q 得到 %q（%v）", i, want[i], got[i], got)
		}
	}
}

func cutKV(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

func hasKV(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
