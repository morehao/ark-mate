package interaction

import (
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"path/filepath"
)

// 命令门禁：**前缀规则**（A2 / v28）。
//
// 形状借自参照产品（`prefix_rule`）：一条规则 = 有序 token 模式 × 一个判决
//
//	pattern     有序 token；每个位置可以是**一组备选**（`[["git","hg"],["push"]]`）
//	decision    allow | prompt | forbidden
//	match_samples      **必须命中**的样例（加载期当单测跑）
//	not_match_samples  **必须不命中**的样例（同上）
//	host_executable  只对这些绝对路径开放"按 basename 回退比较"
//
// 三条设计判据：
//
//	① **不引 Rust / OPA / CEL**：Rust 是那个参照产品的实现语言（不是我们的问题）；OPA 是第二个
//	   常驻进程（本仓单机单进程）；CEL 表达不了"规则表在**加载期**自校验"这件事——而那是本文件
//	   唯一真正想要的性质（见 ②）。
//	② **样例断言在加载期求值**：它们是**样例断言**，不是运行时判据。一条写错的
//	   前缀规则在运行时的表现是"该拦的没拦"或"该放的全拦"——两种都不会报错，只会静默改变行为。
//	   所以加载时把样例跑一遍，不通过就**拒绝加载**（fail-closed：宁可不启用门禁，也不用一份
//	   自己都不认识自己的规则表）。
//	③ `host_executable` 的 basename 回退**只对列出的绝对路径**开放：`/bin/bash` 与 `bash`
//	   在 `PATH` 上未必是同一个东西，无条件按 basename 比较等于让"我明确写了绝对路径"失效。
type CommandDecision string

const (
	// CommandAllow 直接放行（不经审批）。
	CommandAllow CommandDecision = "allow"
	// CommandPrompt 走既有审批出口（与 ACP 权限请求同一条路）。
	CommandPrompt CommandDecision = "prompt"
	// CommandForbidden 直接拒绝（连审批都不给：那不是"要不要"的问题）。
	CommandForbidden CommandDecision = "forbidden"
)

// ValidCommandDecisions 判决取值集（唯一事实源；加载期与报错文案共用）。
var ValidCommandDecisions = []CommandDecision{CommandAllow, CommandPrompt, CommandForbidden}

// PrefixRule 一条前缀规则。
type PrefixRule struct {
	// Name 规则名（日志与拒绝文案用；空 = 按序号渲染）。
	Name string `json:"name,omitempty"`
	// Pattern 有序 token 模式；每个位置是一组备选（单元素 = 精确匹配那一个 token）。
	Pattern [][]string `json:"pattern"`
	// Decision 命中后的判决。
	Decision CommandDecision `json:"decision"`
	// MatchSamples 加载期**必须命中**的样例（空 = 不校验这一侧）。
	MatchSamples [][]string `json:"match_samples,omitempty"`
	// NotMatchSamples 加载期**必须不命中**的样例。
	NotMatchSamples [][]string `json:"not_match_samples,omitempty"`
	// HostExecutable 只对列出的绝对路径开放 basename 回退比较（空 = 不回退）。
	HostExecutable []string `json:"host_executable,omitempty"`
}

// Match 该规则是否命中一串 token。
//
// 匹配语义：
//
//	① 逐位置比较；每个位置的**备选**任一相等即该位置命中；
//	② 第一位置额外允许"按 basename 回退"——**但只对 `HostExecutable` 里列出的绝对路径**：
//	   规则写了备选里某个绝对路径，而实参是它的 basename 时才算命中（`/bin/bash` ↔ `bash`）；
//	③ 模式比实参**长** → 不命中（"必须有这些 token"而不是"以前缀开头就行"之外还要够长）；
//	   模式比实参短 → 命中（前缀语义）。
func (r PrefixRule) Match(tokens []string) bool {
	if len(r.Pattern) == 0 || len(tokens) < len(r.Pattern) {
		return false
	}
	for i, alts := range r.Pattern {
		if !r.tokenMatches(i, alts, tokens[i]) {
			return false
		}
	}
	return true
}

// tokenMatches 单位置比较（含受控的 basename 回退）。
func (r PrefixRule) tokenMatches(pos int, alts []string, got string) bool {
	for _, want := range alts {
		if want == got {
			return true
		}
		// 回退**只**在第一位置、且 want 是 `HostExecutable` 列出的绝对路径时开放。
		if pos != 0 || !r.hostExecutableAllows(want) {
			continue
		}
		if filepath.Base(want) == got {
			return true
		}
	}
	return false
}

// hostExecutableAllows want 是否在放行名单里（逐字相等；名单里也允许写绝对路径）。
func (r PrefixRule) hostExecutableAllows(want string) bool {
	for _, p := range r.HostExecutable {
		if p == want {
			return true
		}
	}
	return false
}

// Validate 加载期自校验：判决取值合法 + 样例断言全部成立。失败即**拒绝加载**。
//
// 这是本文件的核心：`match` / `not_match` 是**样例**，不是运行时判据。一条写错的前缀规则在运行时
// 的表现是"该拦的没拦"或"该放的全拦"——两种都不报错。
func (r PrefixRule) Validate() error {
	name := r.Name
	if name == "" {
		name = "(未命名)"
	}
	valid := false
	for _, d := range ValidCommandDecisions {
		if r.Decision == d {
			valid = true
			break
		}
	}
	if !valid {
		return commandPolicyErr(fmt.Sprintf("规则 %q 的 decision 需为 allow|prompt|forbidden 之一，得到 %q", name, r.Decision))
	}
	if len(r.Pattern) == 0 {
		return commandPolicyErr(fmt.Sprintf("规则 %q 的 pattern 为空：空模式会命中一切（等于一条无条件的判决）", name))
	}
	for _, alts := range r.Pattern {
		if len(alts) == 0 {
			return commandPolicyErr(fmt.Sprintf("规则 %q 的 pattern 里有一个空备选组：那个位置永远不可能命中", name))
		}
	}
	for _, sample := range r.MatchSamples {
		if !r.Match(sample) {
			return commandPolicyErr(fmt.Sprintf(
				"规则 %q 的 match 样例 %v **没有命中**：这条规则写的不是它想要的意思（加载期拒绝）", name, sample))
		}
	}
	for _, sample := range r.NotMatchSamples {
		if r.Match(sample) {
			return commandPolicyErr(fmt.Sprintf(
				"规则 %q 的 not_match 样例 %v **命中了**：这条规则比它声明的宽（加载期拒绝）", name, sample))
		}
	}
	return nil
}

// LoadCommandRules 加载一张规则表：**逐条**做加载期自校验，任一失败即整表拒绝。
//
// 为什么整表拒绝而不是"跳过坏的那条"：一张规则表是一份**策略**。跳过坏的一条意味着门禁的
// 覆盖面与用户以为的不一样，而"少了一条规则"在运行时的表现是**放行**——那是安全面上最坏的默认。
func LoadCommandRules(rules []PrefixRule) ([]PrefixRule, error) {
	out := make([]PrefixRule, 0, len(rules))
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("第 %d 条规则: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// DecideCommand 按**有序**规则表裁决一串 token：第一条命中的规则说了算（与 `prefix_rule` 同义）。
//
// 没有任何规则命中 → `(CommandPrompt, false)`：**默认走审批**。默认 allow 会让"我加了规则却
// 忘了覆盖这条"变成静默放行；默认 forbidden 会让门禁一上线就把正常用法全挡了。
func DecideCommand(rules []PrefixRule, tokens []string) (CommandDecision, string, bool) {
	for _, r := range rules {
		if r.Match(tokens) {
			return r.Decision, r.Name, true
		}
	}
	return CommandPrompt, "", false
}

// commandPolicyErr 命令策略的加载期错误（唯一出口：40001 + `command_policy_invalid`）。
func commandPolicyErr(msg string) error {
	return failure.NewError(failure.ClassRequest, "command_policy_invalid", msg, nil)
}

// **刻意没有** `splitCommandTokens`：切 token 不是本域的事。
//
// 命令行怎么切（引号、转义、管道、重定向）取决于**底座怎么解析它**，而那是 adapter 的知识；
// 在域里写一份"按空白切分"的实现，只会让调用方以为这里已经有解析能力——直到某条带引号的
// 命令把规则整条绕过。所以本文件只接受**已经切好的 token**，切分与「从工具入参里取出命令行」
// 都留在接线的那个调用点（它还知道这条工具调用到底是 bash 还是别的）。
