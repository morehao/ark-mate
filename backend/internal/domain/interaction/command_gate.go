package interaction

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// 命令门禁的**运行时那一半**：从工具入参取命令行、把简单命令切成 token、按规则表裁决。
//
// 为什么这三件事放在这里（而不是 manager）：
//   - 规则表与判决已经在域里（`command_policy.go`）；
//   - "命令行从哪个入参取"是**工具形状知识**，但它是所有 shell 工具共通的那一个键
//     （`{"command": "..."}`），不是某个底座私有的格式；
//   - 切分口径必须与规则匹配口径放在一起——分开写，两边迟早对"什么算一个 token"各有一套。
//
// **切分刻意只覆盖简单命令**（见 `SplitSimpleCommand`）：引号/转义/变量/管道/重定向/子 shell
// 一律判 complex，调用方只能退回人工审批。拿空白切分去匹配 `echo a; rm -rf /` 会得到
// `["echo","a;","rm","-rf","/"]`——第一条规则永远匹配不上，而用户以为门禁"看过了"。

// CommandPolicyFile 是 `<MATE_HOME>/command_policy.json` 的形状。
//
// 只支持这一种外层形状（对象 + `rules`）：顶格数组是"另一种配置格式"，而两种格式并存时
// 下一个人写错一种只会得到"文件读到了、但一条规则都没生效"——那正是本文件最该避免的静默。
type CommandPolicyFile struct {
	Rules []PrefixRule `json:"rules"`
}

// LoadCommandPolicyFile 读取并**加载期校验**规则文件。
//
//	文件不存在        → (nil, nil)：门禁未启用（默认形态，不改变既有行为）；
//	文件存在但 JSON 坏 → error（调用方必须拒绝启动：坏文件不能被当成"没有规则"）；
//	规则非法          → error（`LoadCommandRules` 的样例断言在这一步就跑）。
//
// 路径为空 = 显式关闭（不再尝试读任何文件）。
func LoadCommandPolicyFile(path string) ([]PrefixRule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读命令门禁规则 %s: %w", path, err)
	}
	var f CommandPolicyFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // 键写错（例如 `match_sample`）= 启动失败，而不是"这条没生效"
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("解析命令门禁规则 %s: %w", path, err)
	}
	if len(f.Rules) == 0 {
		// 空规则表**不是错误**：它等价于"文件在、但今天没有规则"，与"文件不在"行为相同。
		// 但空文件（0 字节）会在这里被 JSON 解析拦下——那是配置写坏，不是"没有规则"。
		return nil, nil
	}
	rules, err := LoadCommandRules(f.Rules)
	if err != nil {
		return nil, fmt.Errorf("命令门禁规则 %s 非法: %w", path, err)
	}
	return rules, nil
}

// CommandLineFromInput 从工具入参里取命令行（只认两种形状）。
//
//	dsh bash（2026-10-17 从底座源码确认）：`{"command": "...", "description": "...", ...}`
//	裸字符串：某些底座把命令行直接当 rawInput
//
// 取不到 → (\"\", false)：这不是一条可识别的 shell 命令，门禁**不碰**它。
func CommandLineFromInput(input any) (string, bool) {
	switch v := input.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return s, true
		}
	case map[string]any:
		if s, ok := v["command"].(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				return s, true
			}
		}
	}
	return "", false
}

// shellSyntaxChars 是"这条命令不是简单命令"的判据字符集。
//
// 刻意**宁可多判 complex**：漏判一个引号，规则就会按错误 token 匹配（该拦的没拦）；
// 多判一个，只是让用户多点一次审批（可恢复）。两个方向的代价不对称。
const shellSyntaxChars = "'\"\\$`;&|<>(){}*?[]~!#\n\r\t"

// SplitSimpleCommand 把命令行切成 token；**只对简单命令**返回 simple=true。
//
// 简单 = 只由空白分隔的普通 token，没有任何 shell 语法。含 shellSyntaxChars 任一字符即
// complex；调用方必须退回人工审批，**不得**按空白切分后拿结果当判据。
func SplitSimpleCommand(cmd string) ([]string, bool) {
	t := strings.TrimSpace(cmd)
	if t == "" {
		return nil, false
	}
	if strings.ContainsAny(t, shellSyntaxChars) {
		return nil, false
	}
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

// GateCommand 用规则表裁决一次工具入参。
//
// 返回值：
//
//	decision  裁决（allow / prompt / forbidden）
//	rule      命中的规则名（空 = 没有规则命中）
//	isShell   **这次调用是不是一条可识别的 shell 命令**——false 表示门禁不参与
//	          （调用方走既有审批/记忆流程），**不是**"允许"。
//
// 三种情形都返回 isShell=true：规则命中 allow / 命中 forbidden / 没命中（prompt）。
// complex 命令返回 (prompt, "", true)：它是 shell 命令，但**不可切分**，只能问人。
// 规则表为空 → isShell=false（门禁未启用，行为与改前逐字相同）。
func GateCommand(rules []PrefixRule, input any) (CommandDecision, string, bool) {
	if len(rules) == 0 {
		return CommandPrompt, "", false
	}
	cmd, ok := CommandLineFromInput(input)
	if !ok {
		return CommandPrompt, "", false
	}
	tokens, simple := SplitSimpleCommand(cmd)
	if !simple {
		return CommandPrompt, "", true
	}
	d, name, _ := DecideCommand(rules, tokens)
	return d, name, true
}
