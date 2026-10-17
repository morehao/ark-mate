package interaction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 命令门禁的运行时那一半：取命令行 / 切分 / 裁决。判据的重点是**不可切分就绝不猜**。

func TestCommandLineFromInput(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{"dsh bash 形状", map[string]any{"command": "git status", "description": "看状态"}, "git status", true},
		{"裸字符串", "ls -la", "ls -la", true},
		{"空字符串", "   ", "", false},
		{"没有 command 键", map[string]any{"path": "/tmp/x"}, "", false},
		{"command 不是字符串", map[string]any{"command": 42}, "", false},
		{"nil", nil, "", false},
	}
	for _, c := range cases {
		got, ok := CommandLineFromInput(c.input)
		if got != c.want || ok != c.ok {
			t.Fatalf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestSplitSimpleCommand(t *testing.T) {
	simple := []string{
		"git status",
		"  rm   -rf  /tmp/x  ",
		"./scripts/build.sh --release",
		"/bin/ls -la",
	}
	for _, cmd := range simple {
		if _, ok := SplitSimpleCommand(cmd); !ok {
			t.Fatalf("%q 应判为简单命令", cmd)
		}
	}
	complex := []string{
		`echo "hi"`,
		`echo $HOME`,
		`ls | grep go`,
		`a && b`,
		`a; rm -rf /`,
		`echo $(date)`,
		`ls > /tmp/x`,
		`rm -rf /tmp/*`,
		"echo a\nrm -rf /",
		"echo `id`",
	}
	for _, cmd := range complex {
		if _, ok := SplitSimpleCommand(cmd); ok {
			t.Fatalf("%q 含 shell 语法，必须判 complex（拿空白切分匹配它等于漏判）", cmd)
		}
	}
}

func TestGateCommand(t *testing.T) {
	rules, err := LoadCommandRules([]PrefixRule{
		{Name: "deny rm", Pattern: [][]string{{"rm"}}, Decision: CommandForbidden},
		{Name: "allow git status", Pattern: [][]string{{"git"}, {"status"}}, Decision: CommandAllow},
	})
	if err != nil {
		t.Fatalf("LoadCommandRules: %v", err)
	}

	cases := []struct {
		name     string
		input    any
		want     CommandDecision
		wantRule string
		wantGate bool
	}{
		{"命中 forbidden", map[string]any{"command": "rm -rf /tmp/x"}, CommandForbidden, "deny rm", true},
		{"命中 allow", map[string]any{"command": "git status"}, CommandAllow, "allow git status", true},
		{"没命中 → prompt", map[string]any{"command": "ls -la"}, CommandPrompt, "", true},
		{"复杂命令 → prompt", map[string]any{"command": "git status; rm -rf /"}, CommandPrompt, "", true},
		{"不是 shell 命令", map[string]any{"path": "/x"}, CommandPrompt, "", false},
	}
	for _, c := range cases {
		d, rule, gated := GateCommand(rules, c.input)
		if d != c.want || rule != c.wantRule || gated != c.wantGate {
			t.Fatalf("%s: got (%q,%q,%v), want (%q,%q,%v)", c.name, d, rule, gated, c.want, c.wantRule, c.wantGate)
		}
	}

	// 规则表为空 = 门禁未启用：即便入参是一条 shell 命令也不参与（行为与改前逐字相同）。
	if d, _, gated := GateCommand(nil, map[string]any{"command": "rm -rf /"}); gated || d != CommandPrompt {
		t.Fatalf("空规则表必须不参与，got (%q,%v)", d, gated)
	}
}

func TestLoadCommandPolicyFile(t *testing.T) {
	dir := t.TempDir()
	if rules, err := LoadCommandPolicyFile(filepath.Join(dir, "missing.json")); err != nil || rules != nil {
		t.Fatalf("文件不存在应 (nil,nil)，got (%v,%v)", rules, err)
	}
	if rules, err := LoadCommandPolicyFile(""); err != nil || rules != nil {
		t.Fatalf("空路径应 (nil,nil)，got (%v,%v)", rules, err)
	}

	write := func(name string, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
		return p
	}

	valid := CommandPolicyFile{Rules: []PrefixRule{{
		Name: "deny rm", Pattern: [][]string{{"rm"}}, Decision: CommandForbidden,
		MatchSamples: [][]string{{"rm", "-rf", "/"}}, NotMatchSamples: [][]string{{"git", "status"}},
	}}}
	b, _ := json.Marshal(valid)
	rules, err := LoadCommandPolicyFile(write("ok.json", string(b)))
	if err != nil || len(rules) != 1 || rules[0].Name != "deny rm" {
		t.Fatalf("合法文件应加载 1 条，got (%+v,%v)", rules, err)
	}

	if _, err := LoadCommandPolicyFile(write("empty-rules.json", `{"rules":[]}`)); err != nil {
		t.Fatalf("空规则表不是错误: %v", err)
	}
	if _, err := LoadCommandPolicyFile(write("bad.json", `{"rules":`)); err == nil {
		t.Fatal("坏 JSON 必须报错（坏文件不能被当成没有规则）")
	}
	if _, err := LoadCommandPolicyFile(write("unknown.json", `{"rules":[],"oops":1}`)); err == nil {
		t.Fatal("未知键必须报错（拼错键等于规则没生效）")
	}
	// 规则自校验（样例不通过）在加载期就红。
	if _, err := LoadCommandPolicyFile(write("bad-rule.json",
		`{"rules":[{"name":"x","pattern":[["ls"]],"decision":"allow","match_samples":[["rm"]]}]}`)); err == nil {
		t.Fatal("样例断言不通过的规则必须整表拒绝")
	}
}
