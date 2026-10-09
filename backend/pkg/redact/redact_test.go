package redact

import (
	"fmt"
	"strings"
	"testing"
)

// TestStringRedactsCredentialShapes 凭证形状正则族：命中必替换，且保留可辨识前缀。
func TestStringRedactsCredentialShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"openai sk", "key=sk-abcdefghijklmnop1234", "key=***"},
		{"openai 多段前缀", "sk-ant-api03-AbCdEfGhIjKlMnOpQrSt", "***"},
		{"bearer 保留前缀", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9abc", "Authorization: Bearer ***"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "***"},
		{"github pat", "token ghp_abcdefghijklmnopqrstuvwxyz01", "token ***"},
		{"github fine-grained", "github_pat_11ABCDEFG0123456789_abcdefghijklmnop", "***"},
		{"google", "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7", "***"},
		{"xai", "xai-abcdefghijklmnopqrstuvwx", "***"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1g", "***"},
		{"api_key 形态", `api_key = "1234567890abcdefghij"`, `api_key = "***"`},
		{"pem 私钥块", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "***"},
	}
	for _, tc := range cases {
		if got := String(tc.in); got != tc.want {
			t.Fatalf("%s: want %q got %q", tc.name, tc.want, got)
		}
	}
}

// TestStringKeepsBenignContent 正常内容不被洗掉（transcript 是事实源，误洗代价高）。
func TestStringKeepsBenignContent(t *testing.T) {
	benign := []string{
		"",
		"普通中文说明，没有密钥",
		"sha256=5d41402abc4b2a76b9719d911017c592c1e5b8f0a3d2c7e9f4b6a8d0c2e4f6a8", // 纯 hex 摘要
		"commit 550e8400e29b41d4a716446655440000",                                 // 短 hex（<40）
		"uuid 550e8400-e29b-41d4-a716-446655440000",                               // 带分隔符，非候选 token
		"函数名 runTestSuiteForConversationAttributionConsistency",                   // 长标识符（无数字）
		"短值 abc123", // 过短
		// 回归（TestWorkspaceRoutesWithRoots 曾因此变红）：绝对路径、URL、带扩展名文件名——
		// 密钥（base64url）不含 / \\ . : 这些分隔符，长路径绝不能被整条洗成 ***。
		"/Users/morehao/Documents/practice/go/ark-mate/backend/internal/manager",
		"/tmp/TestWorkspaceRoutesWithRoots1234/workspaceAbc",
		"https://api.example.com/v1/chat/completions?model=gpt-4o-mini",
		"docs/p0-hardening-design-20260911.md",
	}
	for _, in := range benign {
		if got := String(in); got != in {
			t.Fatalf("正常内容被改写: %q → %q", in, got)
		}
	}
}

// TestHighEntropyFallback 高熵兜底：像密钥的随机串被替换，纯 hex 与少字符类的长串不替换。
func TestHighEntropyFallback(t *testing.T) {
	// 48 字符、大小写+数字齐备、非纯 hex、熵足够 → 判定为密钥。
	secret := "aZ3kQ9mW2pL7xR4tY6uI8oP1sD5fG0hJ2kL4mN6bV8cX0zQ1"
	if got := String("token=" + secret); strings.Contains(got, secret) {
		t.Fatalf("高熵串应被替换: %q", got)
	}
	// 纯 hex（sha256）即便 64 字符也必须保留。
	hex := strings.Repeat("0123456789abcdef", 4)
	if got := String(hex); got != hex {
		t.Fatalf("纯 hex 摘要不应被替换: %q", got)
	}
	// 只有小写+数字（无大写）→ 不判定为密钥。
	lower := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghij"
	if got := String(lower); got != lower {
		t.Fatalf("小写长串不应被替换: %q", got)
	}
}

// TestRegisterDynamicSecret 真实密钥值登记：任何形状都能被替换，注销后恢复；过短值忽略。
func TestRegisterDynamicSecret(t *testing.T) {
	key := "hunter2-not-a-known-shape"
	unregister := RegisterDynamicSecret(key)
	if got := String("连接失败 token=" + key); strings.Contains(got, key) {
		t.Fatalf("登记后的真实密钥必须被替换: %q", got)
	}
	// 同一值重复登记 → 引用计数，注销一次仍生效。
	unregister2 := RegisterDynamicSecret(key)
	unregister()
	if got := String(key); strings.Contains(got, key) {
		t.Fatalf("仍有一次引用时应继续替换: %q", got)
	}
	unregister2()
	if got := String(key); got != key {
		t.Fatalf("注销后应恢复原样: %q", got)
	}

	// 过短（<8）不登记，避免把常见短串当密钥全局替换。
	short := "abc123"
	un := RegisterDynamicSecret(short)
	defer un()
	if got := String("值 abc123 与 abc1234 都在"); !strings.Contains(got, short) {
		t.Fatalf("过短值不应被登记: %q", got)
	}
}

// TestPayloadRedactsStringLeavesOnly 结构化载荷：只洗字符串叶子，非字符串值与容器类型不变，
// 未命中时原样返回（不改对象身份），命中时也不改写入参。
func TestPayloadRedactsStringLeavesOnly(t *testing.T) {
	orig := map[string]any{
		"text":    "请使用 sk-abcdefghijklmnop1234 调用",
		"count":   3,
		"ok":      true,
		"nested":  map[string]any{"stderr": []any{"Bearer abcdefghijklmnop", "plain"}},
		"strings": []string{"AKIAIOSFODNN7EXAMPLE", "plain"},
	}
	out, ok := Payload(orig).(map[string]any)
	if !ok {
		t.Fatalf("载荷类型必须保持 map[string]any，got %T", Payload(orig))
	}
	if s, _ := out["text"].(string); strings.Contains(s, "sk-abcdefghijklmnop1234") {
		t.Fatalf("顶层字符串未脱敏: %q", s)
	}
	if out["count"] != 3 || out["ok"] != true {
		t.Fatalf("非字符串值被改写: %+v", out)
	}
	inner, _ := out["nested"].(map[string]any)
	lines, _ := inner["stderr"].([]any)
	if s, _ := lines[0].(string); strings.Contains(s, "abcdefghijklmnop") {
		t.Fatalf("嵌套切片未脱敏: %q", s)
	}
	if lines[1] != "plain" {
		t.Fatalf("正常行被改写: %v", lines[1])
	}
	if ss, _ := out["strings"].([]string); len(ss) != 2 || strings.Contains(ss[0], "AKIA") || ss[1] != "plain" {
		t.Fatalf("[]string 未正确处理: %#v", out["strings"])
	}
	// 入参不被改写（发布链路的其他消费者仍看到原值，脱敏只作用于出口）。
	if s, _ := orig["text"].(string); !strings.Contains(s, "sk-abcdefghijklmnop1234") {
		t.Fatalf("入参不应被就地改写: %q", s)
	}

	// 未命中 → 原对象返回（不做无谓拷贝）。
	clean := map[string]any{"text": "普通内容"}
	if got := Payload(clean); !sameMap(got, clean) {
		t.Fatal("未命中时应原样返回同一对象（不做无谓拷贝）")
	}

	// nil / 标量 / 不支持的类型不 panic。
	if Payload(nil) != nil {
		t.Fatal("nil 应原样返回")
	}
	if Payload(42) != 42 {
		t.Fatal("标量应原样返回")
	}
}

// sameMap 判断两个 any 是否为同一个 map 实例。
func sameMap(a, b any) bool {
	ma, ok1 := a.(map[string]any)
	mb, ok2 := b.(map[string]any)
	if !ok1 || !ok2 || len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if mb[k] != v {
			return false
		}
	}
	// 内容相同不够：这里用 map 的身份（%p）判定是不是同一个实例——
	// 命中时脱敏会构造新 map，未命中必须原样返回入参。
	return fmt.Sprintf("%p", ma) == fmt.Sprintf("%p", mb)
}

// TestLines 逐行脱敏（stderr 尾巴路径）。
func TestLines(t *testing.T) {
	in := []string{"OPENAI_API_KEY=sk-abcdefghijklmnop1234", "listening on :8080"}
	out := Lines(in)
	if strings.Contains(out[0], "sk-abcdefghijklmnop1234") {
		t.Fatalf("stderr 行未脱敏: %q", out[0])
	}
	if out[1] != in[1] {
		t.Fatalf("正常行被改写: %q", out[1])
	}
	if in[0] != "OPENAI_API_KEY=sk-abcdefghijklmnop1234" {
		t.Fatalf("入参不应被就地改写: %q", in[0])
	}
	if Lines(nil) != nil {
		t.Fatal("nil 应原样返回")
	}
}
