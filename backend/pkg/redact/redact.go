// Package redact 是密钥/凭证的**统一脱敏闸门**：所有可能外露的文本（事件载荷、transcript、
// stderr 诊断尾巴、对外错误文案）在离开进程前都必须过这里，避免「密钥随日志/报错/回放泄露」。
//
// 两档强度（对齐 compozy 的 display/structural 分档）：
//   - String / Lines：给人看的文本（stderr 行、错误文案）——正则族 + 已登记的真实密钥值；
//   - Payload：结构化载荷（事件 payload）——深度遍历 map/slice，只洗字符串叶子，
//     非字符串值原样保留（数字/布尔/时间等结构不被改写）。
//
// 三类命中来源，按可靠性排序：
//  1. **已登记的真实密钥值**（RegisterDynamicSecret）——零误报，最可靠；
//  2. **凭证形状正则**（sk-*/sk-ant-*/AKIA*/ghp_*/AIza*/xai-*/Bearer/JWT/PEM 私钥块）；
//  3. **高熵兜底**（保守阈值：≥40 字符、含大小写与数字、非纯 hex、Shannon 熵 ≥3.5）——
//     专治「形状未知但显然是随机串」的密钥；纯 hex（sha256/git 摘要）与 UUID 明确排除，
//     避免把正常内容洗掉。
//
// 注意：**脱敏是不可逆的**。历史已落库的事件不会被自动清洗（见
// docs/p0-hardening-design-20260911.md §3.3），只有新写入经此闸门。
package redact

import (
	"math"
	"reflect"
	"regexp"
	"strings"
	"sync"
)

// Placeholder 命中后的替换串（对外可观测；文档与测试共用同一字面量）。
const Placeholder = "***"

// maxPayloadDepth 结构化载荷的递归深度上限（防病态嵌套；超深原样返回、不阻断发布）。
const maxPayloadDepth = 12

// rule 是一条「正则 → 替换模板」规则（模板用 $1 保留可辨识前缀，如 `Bearer ***`）。
type rule struct {
	re   *regexp.Regexp
	repl string
}

// rules 是凭证形状规则族，**顺序敏感**：越具体的越靠前（如 sk-ant- 先于 sk-）。
var rules = []rule{
	// OpenAI 系（sk-、sk-proj-、sk-ant-…）；字符集含 - 以覆盖多段前缀。
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), Placeholder},
	// HTTP Bearer（保留前缀，便于判断「这里本该有令牌」）。
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), `${1}` + Placeholder},
	// AWS access key id。
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), Placeholder},
	// GitHub token 家族。
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`), Placeholder},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), Placeholder},
	// Google API key。
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), Placeholder},
	// xAI。
	{regexp.MustCompile(`\bxai-[A-Za-z0-9]{20,}`), Placeholder},
	// JWT（三段 base64url）。
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), Placeholder},
	// PEM 私钥块（跨行；(?s) 让 . 匹配换行）。
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), Placeholder},
	// 常见 `KEY=VALUE` / `"api_key": "VALUE"` 形态的长值（值本身无特征时的兜底）。
	{regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|client[_-]?secret|auth[_-]?token)(["']?\s*[=:]\s*["']?)([A-Za-z0-9_\-+/=]{16,})`), `${1}${2}` + Placeholder},
}

// entropyTokenRe 高熵兜底的候选 token（长且无空白/引号）。
var entropyTokenRe = regexp.MustCompile(`[A-Za-z0-9_+/=-]{40,}`)

// pureHexRe 纯 hex 串（sha256/git 摘要等正常内容）——熵高但不是密钥，排除。
var pureHexRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// dynamic 保存**真实密钥值**（按值登记，引用计数），使无固定形状的密钥也必被替换。
var dynamic = struct {
	sync.RWMutex
	values map[string]int
}{values: map[string]int{}}

// minDynamicLen 过短的登记值会误伤普通文本（如 4 位密码会命中大量无关内容），故设下限。
const minDynamicLen = 8

// RegisterDynamicSecret 登记一个真实密钥值（保存/注入 provider key 时调用），返回注销函数。
// 同一值重复登记按引用计数累加；**长度 < 8 的值被忽略**（避免把常见短串当密钥全局替换）。
func RegisterDynamicSecret(value string) (unregister func()) {
	if len(value) < minDynamicLen {
		return func() {}
	}
	dynamic.Lock()
	dynamic.values[value]++
	dynamic.Unlock()
	return func() {
		dynamic.Lock()
		defer dynamic.Unlock()
		if n := dynamic.values[value]; n <= 1 {
			delete(dynamic.values, value)
		} else {
			dynamic.values[value] = n - 1
		}
	}
}

// String 对一段文本脱敏（display 档）：先正则族，再已登记的真实密钥值，最后高熵兜底。
func String(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range rules {
		if r.re.MatchString(out) {
			out = r.re.ReplaceAllString(out, r.repl)
		}
	}
	out = replaceDynamic(out)
	return redactEntropyTokens(out)
}

// Lines 逐行脱敏（stderr 诊断尾巴等）。
func Lines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = String(l)
	}
	return out
}

// Payload 对结构化载荷脱敏（structural 档）：深度遍历 map/slice，只洗字符串叶子。
// 未发生替换时**原样返回入参**（不做无谓拷贝，避免改变调用方的对象身份）。
func Payload(v any) any {
	out, _ := redactValue(reflect.ValueOf(v), 0)
	return out
}

// replaceDynamic 替换已登记的真实密钥值（长值优先，避免短值先命中后留下长值尾巴）。
func replaceDynamic(s string) string {
	dynamic.RLock()
	if len(dynamic.values) == 0 {
		dynamic.RUnlock()
		return s
	}
	vals := make([]string, 0, len(dynamic.values))
	for v := range dynamic.values {
		vals = append(vals, v)
	}
	dynamic.RUnlock()
	for i := 0; i < len(vals); i++ {
		for j := i + 1; j < len(vals); j++ {
			if len(vals[j]) > len(vals[i]) {
				vals[i], vals[j] = vals[j], vals[i]
			}
		}
	}
	for _, v := range vals {
		s = strings.ReplaceAll(s, v, Placeholder)
	}
	return s
}

// redactEntropyTokens 只替换高熵 token，其余原样保留。
func redactEntropyTokens(s string) string {
	if !entropyTokenRe.MatchString(s) {
		return s
	}
	return entropyTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		if looksHighEntropy(tok) {
			return Placeholder
		}
		return tok
	})
}

// looksHighEntropy 判定一个 token 是否"像密钥"：足够长、三类字符齐全、非纯 hex、
// 不含路径/URL 标点、熵足够高。
//
// 阈值刻意保守——宁可漏掉个别形状怪异的密钥（还有形状正则与动态登记兜底），
// 也不要把**正常内容**洗掉（transcript 是事实源）：实测拿掉「路径标点」这一条后，
// 错误文案里的绝对路径会被整条替换成 `***`（`TestWorkspaceRoutesWithRoots` 直接红），
// 那是把诊断能力洗没了——比漏一个未知形状的密钥更糟。
func looksHighEntropy(tok string) bool {
	if len(tok) < 40 || pureHexRe.MatchString(tok) {
		return false
	}
	// 路径 / URL / 带扩展名的文件名：密钥（base64url）不会包含这些分隔符。
	if strings.ContainsAny(tok, `/\\.:`) {
		return false
	}
	var lower, upper, digit bool
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	if !lower || !upper || !digit {
		return false
	}
	return shannonEntropy(tok) >= 4.0
}

// shannonEntropy 计算字符串的 Shannon 熵（比特/字符）。
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var e float64
	n := float64(len(s))
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		e -= p * math.Log2(p)
	}
	return e
}

// redactValue 递归脱敏；返回 (结果, 是否发生替换)。只在发生替换时改写容器内容。
//
// 只处理：interface（取其动态值）、string、string 键的 map、slice。其余 Kind（指针/结构体/
// 数字/布尔/时间等）原样返回——结构化载荷脱敏只该洗字符串叶子，不该改写值的类型。
func redactValue(rv reflect.Value, depth int) (any, bool) {
	if !rv.IsValid() || depth > maxPayloadDepth {
		return nil, false
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return rv.Interface(), false
		}
		inner, changed := redactValue(rv.Elem(), depth)
		if !changed {
			return rv.Interface(), false
		}
		return inner, true
	case reflect.String:
		s := rv.String()
		rs := String(s)
		return rs, rs != s
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return rv.Interface(), false
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		changed := false
		iter := rv.MapRange()
		for iter.Next() {
			nv, ch := redactValue(iter.Value(), depth+1)
			if ch {
				out.SetMapIndex(iter.Key(), reflect.ValueOf(nv))
				changed = true
				continue
			}
			out.SetMapIndex(iter.Key(), iter.Value())
		}
		if !changed {
			return rv.Interface(), false
		}
		return out.Interface(), true
	case reflect.Slice:
		if rv.IsNil() {
			return rv.Interface(), false
		}
		out := reflect.MakeSlice(reflect.SliceOf(rv.Type().Elem()), rv.Len(), rv.Len())
		changed := false
		for i := 0; i < rv.Len(); i++ {
			nv, ch := redactValue(rv.Index(i), depth+1)
			if ch {
				out.Index(i).Set(reflect.ValueOf(nv))
				changed = true
				continue
			}
			out.Index(i).Set(rv.Index(i))
		}
		if !changed {
			return rv.Interface(), false
		}
		return out.Interface(), true
	default:
		return rv.Interface(), false
	}
}
