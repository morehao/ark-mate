package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **命令门禁的加载期自校验** 守卫（A2 / v28）。
//
// 这条判据钉的是一个**时序**性质：`match_samples` / `not_match_samples` 必须在**加载时**求值，
// 而不是等第一次真实调用才发现规则写错了。腐化方式是"改成惰性"——把校验从 `LoadCommandRules`
// 里挪到使用点（或干脆只留类型不校验）。那一刻所有测试照样绿，而表现是：
//
//	一份写错的前缀规则在运行时**该拦的没拦**或**该放的全拦**——两种都不报错，只静默改变行为。
//
// 判据三段，都 fail-closed。

// TestCommandPolicyMatchIsLoadTimeChecked 见文件头。
func TestCommandPolicyMatchIsLoadTimeChecked(t *testing.T) {
	root := backendRoot(t)
	path := filepath.Join(root, "internal", "domain", "interaction", "command_policy.go")
	src := stripLineComments(readFileOrFatal(t, path))

	// ① 加载入口必须存在，且**在函数体内**逐条调用 `Validate()`（不是返回一个待校验的东西）。
	load, ok := goFuncBody(src, "func LoadCommandRules(")
	if !ok {
		t.Fatalf("%s 里找不到 `LoadCommandRules(`（改名了？本守卫 fail-closed）", path)
	}
	if !strings.Contains(load, "r.Validate()") {
		t.Fatalf("`LoadCommandRules` 里没有 `r.Validate()`：**样例断言必须在加载期跑**——\n" +
			"它是这条规则表唯一能被「自己检查自己」的机会；挪到使用点就等于没有")
	}
	// ② 有坏规则时**整表拒绝**（返回值是 `nil, err`），不是"跳过坏的那条"。
	if !strings.Contains(load, "return nil, ") {
		t.Fatalf("`LoadCommandRules` 没有在失败时返回 `nil, err`：整表拒绝是刻意的——\n" +
			"跳过坏的一条意味着门禁覆盖面与用户以为的不一样，而「少一条规则」在运行时的表现是**放行**")
	}
	if strings.Contains(load, "continue") {
		t.Fatalf("`LoadCommandRules` 里出现了 `continue`：那意味着坏规则被跳过——" +
			"而「缺一条规则」在运行时的表现是**放行**，那是安全面上最坏的默认")
	}

	// ③ `Validate` 必须**真的**求值样例（不是只看判决取值合法）。
	validate, ok := goFuncBody(src, "func (r PrefixRule) Validate(")
	if !ok {
		t.Fatalf("%s 里找不到 `func (r PrefixRule) Validate(`", path)
	}
	for _, want := range []string{"r.MatchSamples", "r.NotMatchSamples", "r.Match(sample)"} {
		if !strings.Contains(validate, want) {
			t.Fatalf("`Validate` 里找不到 %q：只校验判决取值合法是不够的——\n"+
				"`match_samples` / `not_match_samples` 是这条规则表唯一能被「自己检查自己」的机会", want)
		}
	}

	// ④ 默认判决必须是**审批**（既不是 allow 也不是 forbidden）。
	decide, ok := goFuncBody(src, "func DecideCommand(")
	if !ok {
		t.Fatalf("%s 里找不到 `DecideCommand(`", path)
	}
	if !strings.Contains(decide, "return CommandPrompt, \"\", false") {
		t.Fatalf("`DecideCommand` 的兜底不是 `CommandPrompt`：\n" +
			"默认 allow 会让「我加了规则却忘了覆盖这条」变成静默放行；" +
			"默认 forbidden 会让门禁一上线就把正常用法全挡了")
	}

	// ⑤ 域里**不许**自带 token 切分（那是 adapter 的知识：引号/管道/重定向由底座解析）。
	if strings.Contains(src, "strings.Fields(") || strings.Contains(src, `func splitCommandTokens(`) {
		t.Fatalf("%s 里出现了命令切分实现：切 token 取决于**底座怎么解析命令行**（引号/转义/管道），\n"+
			"在本域写一份「按空白切分」只会让调用方以为这里已经有解析能力——直到某条带引号的命令把规则整条绕过", path)
	}
}
