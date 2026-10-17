package interaction

import "testing"

// TestPrefixRuleMatchTokens 前缀规则的匹配语义（A2）。
//
// 四条判据各自对应用户会说的话：「这条算不算我要拦的」。
func TestPrefixRuleMatchTokens(t *testing.T) {
	r := PrefixRule{
		Name:     "git push",
		Pattern:  [][]string{{"git"}, {"push"}},
		Decision: CommandPrompt,
	}
	// ① 精确前缀命中。
	if !r.Match([]string{"git", "push", "--force"}) {
		t.Fatal("`git push --force` 应命中 `git push`（前缀语义）")
	}
	// ② 模式比实参短 = 命中；比实参长 = 不命中。
	if r.Match([]string{"git"}) {
		t.Fatal("`git` 不该命中 `git push`：模式要求有两个 token")
	}
	// ③ 任一位置不等 = 不命中。
	if r.Match([]string{"git", "status"}) {
		t.Fatal("`git status` 不该命中 `git push`")
	}

	// ④ basename 回退**只对 HostExecutable 里列出的绝对路径**开放。
	abs := PrefixRule{
		Name:           "bash -c",
		Pattern:        [][]string{{"/bin/bash"}, {"-c"}},
		Decision:       CommandForbidden,
		HostExecutable: []string{"/bin/bash"},
	}
	if !abs.Match([]string{"bash", "-c", "rm -rf /"}) {
		t.Fatal("列入了 HostExecutable 的绝对路径应允许按 basename 回退")
	}
	if abs.Match([]string{"/usr/bin/bash", "-c", "x"}) {
		t.Fatal("另一个绝对路径不该命中：把 `/bin/bash` 写的规则套到 `/usr/bin/bash` 上是另一件事")
	}
	notListed := abs
	notListed.HostExecutable = nil
	if notListed.Match([]string{"bash", "-c", "x"}) {
		t.Fatal("没列入 HostExecutable 时**不许**按 basename 回退")
	}
	// ⑤ 备选组：任一相等即该位置命中。
	alt := PrefixRule{Pattern: [][]string{{"git", "hg"}, {"push"}}, Decision: CommandAllow}
	if !alt.Match([]string{"hg", "push"}) {
		t.Fatal("备选组里的第二个取值应命中")
	}
}

// TestCommandRulesValidateAtLoad 加载期自校验（A2 的核心判据）。
//
// `match_samples` / `not_match_samples` 是**样例断言**，不是运行时判据。一条写错的前缀规则在运行时
// 的表现是"该拦的没拦"或"该放的全拦"——两种都不报错、只静默改变行为。所以加载时把样例跑一遍，
// 不通过就**拒绝加载**。
func TestCommandRulesValidateAtLoad(t *testing.T) {
	ok := PrefixRule{
		Name:            "git push 需审批",
		Pattern:         [][]string{{"git"}, {"push"}},
		Decision:        CommandPrompt,
		MatchSamples:    [][]string{{"git", "push"}, {"git", "push", "--force"}},
		NotMatchSamples: [][]string{{"git", "status"}, {"ls", "push"}},
	}
	if _, err := LoadCommandRules([]PrefixRule{ok}); err != nil {
		t.Fatalf("自洽的规则应能加载: %v", err)
	}

	// ① match 样例**没命中** → 拒绝加载（规则写的不是它想要的意思）。
	badMatch := ok
	badMatch.MatchSamples = [][]string{{"git", "commit"}}
	if _, err := LoadCommandRules([]PrefixRule{badMatch}); err == nil {
		t.Fatal("match 样例没命中应**拒绝加载**")
	}

	// ② not_match 样例**命中了** → 拒绝加载（规则比它声明的宽）。
	badNot := ok
	badNot.NotMatchSamples = [][]string{{"git", "push", "--force"}}
	if _, err := LoadCommandRules([]PrefixRule{badNot}); err == nil {
		t.Fatal("not_match 样例命中了应**拒绝加载**")
	}

	// ③ 判决取值非法 / 空模式 → 拒绝加载（空模式会命中一切）。
	if _, err := LoadCommandRules([]PrefixRule{{Pattern: [][]string{{"x"}}, Decision: "nuke"}}); err == nil {
		t.Fatal("非法 decision 应拒绝加载")
	}
	if _, err := LoadCommandRules([]PrefixRule{{Pattern: nil, Decision: CommandAllow}}); err == nil {
		t.Fatal("空 pattern 应拒绝加载：它会命中一切（等于一条无条件的判决）")
	}
	// ④ **整表**拒绝：坏的第二条不该被静默跳过（少一条规则在运行时 = 放行）。
	good := PrefixRule{Pattern: [][]string{{"ls"}}, Decision: CommandAllow}
	if _, err := LoadCommandRules([]PrefixRule{good, badMatch}); err == nil {
		t.Fatal("表里有一条坏规则时整表拒绝：跳过坏的那条 = 门禁覆盖面与用户以为的不一样，而缺一条等于放行")
	}
}

// TestDecideCommandDefaultsToPrompt 没有规则命中时**默认走审批**（不是放行、也不是拒绝）。
func TestDecideCommandDefaultsToPrompt(t *testing.T) {
	rules := []PrefixRule{{Name: "ls", Pattern: [][]string{{"ls"}}, Decision: CommandAllow}}
	if d, _, hit := DecideCommand(rules, []string{"ls", "-la"}); !hit || d != CommandAllow {
		t.Fatalf("命中的规则应生效，got decision=%q hit=%v", d, hit)
	}
	if d, name, hit := DecideCommand(rules, []string{"rm", "-rf", "/"}); hit || d != CommandPrompt || name != "" {
		t.Fatalf("没有规则命中时应默认 prompt 且 hit=false，got decision=%q name=%q hit=%v", d, name, hit)
	}
	// 有序：第一条命中的说了算。
	both := []PrefixRule{
		{Name: "先", Pattern: [][]string{{"git"}}, Decision: CommandForbidden},
		{Name: "后", Pattern: [][]string{{"git"}, {"push"}}, Decision: CommandAllow},
	}
	if d, name, _ := DecideCommand(both, []string{"git", "push"}); d != CommandForbidden || name != "先" {
		t.Fatalf("有序表里第一条命中的规则说了算，got decision=%q name=%q", d, name)
	}
}
