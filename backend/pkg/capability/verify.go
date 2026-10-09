package capability

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// ——— Skill 正文装载期扫描（§13.2 S1） ———
//
// 为什么需要：`Skill.Body` 是平台 JSON 原文，被 `MaterializeSkill` **原样写进 runtime 原生技能
// 目录**——也就是直接进 agent 的上下文。写入前零检查时，任何人往 `<CapabilityDir>/skills/*.json`
// 放一段「忽略之前所有指令」，平台会主动把它物化进 agent 的技能目录。这条闸门管的是
// **注入别进**（与 §3 脱敏闸门的「密钥别泄」是两个方向）。
//
// 模式表逐条对照 compozy `internal/skills/verify.go:22-91`（正则逐字一致，pattern id 也保持
// 一致以便对照阅读；message 按本仓注释/文案用简体中文）。三级严重度：
//
//	critical 拒载（RegisterSkill 返回错误 → 文件装载路径告警跳过，绝不落进注册表）
//	warning  放行但记在 skill 上（`Skill.Warnings` → REST 读模型暴露，让人自己判断）
//	info     仅提示（当前只有「正文超长」一条）
//
// 与 compozy 的一处差异：那边「内置来源豁免校验」（`registry_load.go:254`），ark-mate 没有
// 内置 skill 来源（技能只来自 <CapabilityDir>/skills/*.json 与显式注入），故**不设豁免**——
// 少一个「谁能绕过闸门」的判断点。

// maxContentChars 正文长度上限（超出记 info，不拒载）。
const maxContentChars = 50_000

// WarningSeverity 扫描告警级别（数值越大越严重；排序与拒载判据都用它）。
type WarningSeverity int

const (
	// SeverityInfo 仅提示（正文超长）。
	SeverityInfo WarningSeverity = iota
	// SeverityWarning 可疑但放行（引用敏感路径 / 可疑的命令串联）。
	SeverityWarning
	// SeverityCritical 拒载（改写指令 / 角色劫持 / 凭据提取 / 破坏性命令…）。
	SeverityCritical
)

// String 人类可读的级别名（错误文案与 REST 用同一组字面量）。
func (s WarningSeverity) String() string {
	switch s {
	case SeverityCritical:
		return "critical"
	case SeverityWarning:
		return "warning"
	default:
		return "info"
	}
}

// Warning 一条扫描命中。
type Warning struct {
	Severity WarningSeverity `json:"severity"`
	// Pattern 命中规则的稳定 id（跨版本可检索；同一 id 最多报一次）。
	Pattern string `json:"pattern"`
	// Message 人类可读说明（简体中文）。
	Message string `json:"message"`
}

// verificationPattern 一条扫描规则。
type verificationPattern struct {
	pattern  string
	regex    *regexp.Regexp
	severity WarningSeverity
	message  string
}

// verificationPatterns 扫描规则表（顺序无意义：结果会按严重度+id 排序）。
var verificationPatterns = []verificationPattern{
	{
		pattern: "ignore-previous-instructions",
		regex: regexp.MustCompile(
			`(?i)\bignore\s+(?:\w+\s+)*(?:all|previous|prior|above)\s+(?:\w+\s+)*(?:instructions|rules|guidelines)\b`,
		),
		severity: SeverityCritical,
		message:  "正文试图覆盖既有指令（ignore previous instructions）",
	},
	{
		pattern: "disregard-existing-rules",
		regex: regexp.MustCompile(
			`(?i)\bdisregard\s+(?:\w+\s+)*(?:all|previous|prior|your)\s+(?:\w+\s+)*(?:instructions|rules|guidelines)\b`,
		),
		severity: SeverityCritical,
		message:  "正文试图跳过当前规则（disregard existing rules）",
	},
	{
		pattern: "forget-your-instructions",
		regex: regexp.MustCompile(
			`(?i)\bforget\s+(?:\w+\s+)*(?:your|all)\s+(?:\w+\s+)*(?:instructions|rules|guidelines)\b`,
		),
		severity: SeverityCritical,
		message:  "正文试图抹掉生效指令（forget your instructions）",
	},
	{
		pattern:  "role-hijack-you-are-now",
		regex:    regexp.MustCompile(`(?i)\byou\s+are\s+now\s+(?:a|an|the|assistant|agent|bot|system)\b`),
		severity: SeverityCritical,
		message:  "正文试图改写 agent 角色（you are now …）",
	},
	{
		pattern:  "new-instructions",
		regex:    regexp.MustCompile(`(?i)\bnew\s+instructions\s*:`),
		severity: SeverityCritical,
		message:  "正文插入新的覆盖性指令（new instructions:）",
	},
	{
		pattern:  "system-prompt-override",
		regex:    regexp.MustCompile(`(?i)\bsystem\s+prompt\s+override\b`),
		severity: SeverityCritical,
		message:  "正文试图覆盖 system prompt（system prompt override）",
	},
	{
		pattern:  "delete-all-files",
		regex:    regexp.MustCompile(`(?i)\bdelete\s+all\s+files\b`),
		severity: SeverityCritical,
		message:  "正文指示破坏性删除（delete all files）",
	},
	{
		pattern:  "rm-rf",
		regex:    regexp.MustCompile(`(?i)\brm\s+-rf\b`),
		severity: SeverityCritical,
		message:  "正文含破坏性 shell 命令（rm -rf）",
	},
	{
		pattern: "credential-extraction",
		regex: regexp.MustCompile(
			`(?i)\b(?:print|show|reveal|display|output)\s+(?:the\s+|your\s+)?(?:api\s+key|access\s+token|credentials?|secret(?:s)?|password(?:s)?)\b`,
		),
		severity: SeverityCritical,
		message:  "正文试图套取凭据（print your api key …）",
	},
	{
		pattern: "sensitive-path-reference",
		regex: regexp.MustCompile(
			`(?i)(?:^|[\s` + "`" + `"'(])(?:/etc/passwd|~/.ssh/|/root/.ssh/|\.ssh/id_(?:rsa|ed25519))\b`,
		),
		severity: SeverityWarning,
		message:  "正文引用了敏感文件系统路径（~/.ssh、/etc/passwd…）",
	},
	{
		pattern: "excessive-tool-chaining",
		regex: regexp.MustCompile(
			`(?i)\b(?:curl|wget|bash|sh|python3?|node)\b[^\n]{0,160}(?:\|\s*(?:sh|bash)\b|&&|\|\|)`,
		),
		severity: SeverityWarning,
		message:  "正文含可疑的命令串联（curl … | sh、&&、||）",
	},
}

// VerifyContent 扫描 skill 正文，返回命中告警（无命中 → nil）。
// 结果按「严重度降序、pattern id 升序」稳定排序；同一 pattern 最多报一次。
func VerifyContent(content string) []Warning {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	warnings := make([]Warning, 0, len(verificationPatterns)+1)
	seen := make(map[string]struct{}, len(verificationPatterns)+1)
	for _, p := range verificationPatterns {
		if !p.regex.MatchString(content) {
			continue
		}
		if _, ok := seen[p.pattern]; ok {
			continue
		}
		seen[p.pattern] = struct{}{}
		warnings = append(warnings, Warning{Severity: p.severity, Pattern: p.pattern, Message: p.message})
	}
	if utf8.RuneCountInString(content) > maxContentChars {
		warnings = append(warnings, Warning{
			Severity: SeverityInfo,
			Pattern:  "content-too-long",
			Message:  fmt.Sprintf("正文超过 %d 字符（超长正文会挤占 agent 上下文）", maxContentChars),
		})
	}
	if len(warnings) == 0 {
		return nil
	}
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Severity != warnings[j].Severity {
			return warnings[i].Severity > warnings[j].Severity
		}
		return warnings[i].Pattern < warnings[j].Pattern
	})
	return slices.Clip(warnings)
}

// HasCritical 判断告警里是否有 critical 级（拒载判据）。
func HasCritical(warnings []Warning) bool {
	for _, w := range warnings {
		if w.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

// criticalErr 把 critical 告警收成一条可执行的装载错误：
// 说清「哪些规则命中」+「怎么修」（改正文或删定义），而不是一句「非法」。
func criticalErr(skillID string, warnings []Warning) error {
	ids := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if w.Severity == SeverityCritical {
			ids = append(ids, w.Pattern)
		}
	}
	return fmt.Errorf("capability skill %q 正文命中 critical 规则 [%s]：该正文会被原样物化进 agent 技能目录，"+
		"疑似提示注入/破坏性指令，已拒载（改正文后重试，或删除该定义）",
		skillID, strings.Join(ids, ", "))
}
