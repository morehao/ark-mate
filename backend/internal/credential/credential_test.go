package credential_test

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/credential"
)

// TestSetEmptyIsNotPermissive 空表**不放行**：不鉴权的判定属于调用方（Required()），
// Authorize 在空表上必须一律拒绝——把"忘记配置"变成拒绝，而不是默默放行。
func TestSetEmptyIsNotPermissive(t *testing.T) {
	set, err := credential.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if set.Required() {
		t.Fatal("空表不应报告 Required")
	}
	if set.Authorize("anything", credential.ScopeREST) {
		t.Fatal("空表不得放行任何 token")
	}
	var nilSet *credential.Set
	if nilSet.Required() {
		t.Fatal("nil 表不应报告 Required")
	}
	if nilSet.Authorize("x", credential.ScopeStream) {
		t.Fatal("nil 表不得放行")
	}
	if nilSet.Count() != 0 {
		t.Fatal("nil 表 Count 应为 0")
	}
}

// TestScopesAreMutuallyExclusive 桌面态两个面互斥（V15 的核心）：
// restToken 打不到事件流、streamToken 打不到 REST。
func TestScopesAreMutuallyExclusive(t *testing.T) {
	const rest = "rest-token-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const stream = "stream-token-bbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	set, err := credential.New(
		credential.Pair{Token: rest, Scopes: credential.ScopeREST},
		credential.Pair{Token: stream, Scopes: credential.ScopeStream},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !set.Required() || set.Count() != 2 {
		t.Fatalf("凭据表状态不符: required=%v count=%d", set.Required(), set.Count())
	}

	cases := []struct {
		name  string
		token string
		want  credential.Scope
		ok    bool
	}{
		{"restToken → REST", rest, credential.ScopeREST, true},
		{"restToken → 事件流（必须拒绝）", rest, credential.ScopeStream, false},
		{"streamToken → 事件流", stream, credential.ScopeStream, true},
		{"streamToken → REST（必须拒绝）", stream, credential.ScopeREST, false},
		{"未知令牌 → REST", "nope", credential.ScopeREST, false},
		{"未知令牌 → 事件流", "nope", credential.ScopeStream, false},
		{"空令牌 → REST", "", credential.ScopeREST, false},
		{"空令牌 → 事件流", "", credential.ScopeStream, false},
		{"prefix 不是匹配（restToken 少一位）", rest[:len(rest)-1], credential.ScopeREST, false},
		{"want=0 一律拒绝", rest, 0, false},
	}
	for _, c := range cases {
		if got := set.Authorize(c.token, c.want); got != c.ok {
			t.Errorf("%s：Authorize = %v，期望 %v", c.name, got, c.ok)
		}
	}
}

// TestStandaloneTokenHasBothScopes 独立态（FORGE_TOKEN）保持既有语义：
// 同一个令牌同时具备两个面，REST 与事件流都能用。
func TestStandaloneTokenHasBothScopes(t *testing.T) {
	const tok = "legacy-single-operator-token"
	set := credential.MustNew(credential.Pair{Token: tok, Scopes: credential.ScopeREST | credential.ScopeStream})
	if !set.Authorize(tok, credential.ScopeREST) || !set.Authorize(tok, credential.ScopeStream) {
		t.Fatal("独立态令牌应同时具备 rest 与 stream 两个面")
	}
}

// TestNewRejectsMisconfiguration 两条装配期硬错误：空 scope、同一令牌跨面。
func TestNewRejectsMisconfiguration(t *testing.T) {
	if _, err := credential.New(credential.Pair{Token: "t", Scopes: 0}); err == nil {
		t.Fatal("scope=0 应报错")
	} else if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("错误信息应说明 scope 问题：%v", err)
	}

	_, err := credential.New(
		credential.Pair{Token: "same", Scopes: credential.ScopeREST},
		credential.Pair{Token: "same", Scopes: credential.ScopeStream},
	)
	if err == nil {
		t.Fatal("同一令牌跨两个面应报错（那正是 V15 要防的越权）")
	}
	if !strings.Contains(err.Error(), "互斥") {
		t.Fatalf("错误信息应说明两个面必须互斥：%v", err)
	}

	// 同 token 同 scope 重复出现是无害的（不报错）。
	if _, err := credential.New(
		credential.Pair{Token: "same", Scopes: credential.ScopeREST},
		credential.Pair{Token: "same", Scopes: credential.ScopeREST},
	); err != nil {
		t.Fatalf("同 token 同 scope 不应报错：%v", err)
	}

	// 空 token 视为未配置该面，跳过（桌面态两个都必填的校验在 stdin 解析处）。
	set, err := credential.New(credential.Pair{Token: "", Scopes: credential.ScopeREST})
	if err != nil {
		t.Fatalf("空 token 应被跳过而不是报错：%v", err)
	}
	if set.Required() {
		t.Fatal("只有空 token 时不应报告 Required")
	}
}

func TestScopeString(t *testing.T) {
	cases := map[credential.Scope]string{
		0:                      "none",
		credential.ScopeREST:   "rest",
		credential.ScopeStream: "stream",
		credential.ScopeREST | credential.ScopeStream: "rest|stream",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Scope(%d).String() = %q，期望 %q", s, got, want)
		}
	}
}
