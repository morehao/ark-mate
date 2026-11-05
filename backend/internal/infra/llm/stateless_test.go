package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
)

// stubProviders 假 ProviderSource：按 id 返回固定拨号信息。
type stubProviders struct {
	info map[string]corellm.ProviderInfo
	err  error
}

func (s stubProviders) Resolve(_ context.Context, id string) (corellm.ProviderInfo, error) {
	if s.err != nil {
		return corellm.ProviderInfo{}, s.err
	}
	return s.info[id], nil
}

// TestStatelessCallParsesUsage 端到端：发 OpenAI 兼容请求 → 解析 Content 与 token 明细。
func TestStatelessCallParsesUsage(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	var gotMsgs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, gotMsgs = body.Model, body.Messages
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"hello from model"}}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,
			  "prompt_tokens_details":{"cached_tokens":3},
			  "completion_tokens_details":{"reasoning_tokens":2}}
		}`))
	}))
	defer srv.Close()

	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{
			"p1": {Protocol: "openai", BaseURL: srv.URL, Key: "sk-test"},
		}},
		HTTP: srv.Client(),
	})
	res, err := exec.Call(context.Background(), corellm.Request{
		Purpose:  corellm.PurposeSummary,
		Provider: "p1",
		Model:    "deepseek-chat",
		System:   "you are helpful",
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Text != "hello from model" {
		t.Fatalf("正文不符: %q", res.Text)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 7 || res.Usage.TotalTokens != 18 ||
		res.Usage.CachedReadTokens != 3 || res.Usage.ThoughtTokens != 2 {
		t.Fatalf("usage 解析不符: %+v", res.Usage)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization 头不符: %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("请求路径不符: %q", gotPath)
	}
	// system 消息应作为首条进 messages。
	if len(gotMsgs) != 2 || gotMsgs[0]["role"] != "system" || gotMsgs[0]["content"] != "you are helpful" {
		t.Fatalf("messages 组装不符: %+v", gotMsgs)
	}
	if gotModel != "deepseek-chat" {
		t.Fatalf("model 不符: %q", gotModel)
	}
}

// TestStatelessFailClosed 缺配置时 fail-closed（不静默发空 key / 不猜模型 / 不识别协议即拒）。
func TestStatelessFailClosed(t *testing.T) {
	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{
			"nokey":     {Protocol: "openai", BaseURL: "http://x", Key: ""},
			"nourl":     {Protocol: "openai", BaseURL: "", Key: "k"},
			"anthropic": {Protocol: "anthropic", BaseURL: "http://x", Key: "k"},
		}},
	})
	cases := []struct {
		name string
		req  corellm.Request
	}{
		{"无 provider", corellm.Request{Purpose: corellm.PurposeJudge}},
		{"缺密钥", corellm.Request{Purpose: corellm.PurposeJudge, Provider: "nokey", Model: "m"}},
		{"缺端点", corellm.Request{Purpose: corellm.PurposeJudge, Provider: "nourl", Model: "m"}},
		{"协议不支持", corellm.Request{Purpose: corellm.PurposeJudge, Provider: "anthropic", Model: "m"}},
	}
	for _, c := range cases {
		if _, err := exec.Call(context.Background(), c.req); err == nil {
			t.Fatalf("%s: 应 fail-closed 返回错误", c.name)
		}
	}

	// 无模型：Provider 解析正常但 Model/DefaultModel 都空 → 报错。
	exec2 := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{
			"ok": {Protocol: "openai", BaseURL: "http://x", Key: "k"},
		}},
	})
	if _, err := exec2.Call(context.Background(), corellm.Request{Provider: "ok"}); err == nil {
		t.Fatal("无模型应报错")
	}
}

// TestStatelessUpstreamError 上游非 2xx → 返回错误（含状态码）。
func TestStatelessUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()
	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{
			"p": {Protocol: "openai", BaseURL: srv.URL, Key: "k"},
		}},
		HTTP: srv.Client(),
	})
	if _, err := exec.Call(context.Background(), corellm.Request{Provider: "p", Model: "m"}); err == nil {
		t.Fatal("401 应返回错误")
	}
}

// TestStatelessStructuredOK 结构化请求：合法 JSON → Structured 填充、Outcome=OK，且下发 response_format。
func TestStatelessStructuredOK(t *testing.T) {
	var gotRF map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat map[string]any `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotRF = body.ResponseFormat
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"name\":\"小明\",\"age\":18}"}}],"usage":{"total_tokens":5}}`))
	}))
	defer srv.Close()

	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{"p1": {Protocol: "openai", BaseURL: srv.URL, Key: "k"}}},
		HTTP:      srv.Client(),
	})
	res, err := exec.Call(context.Background(), corellm.Request{
		Purpose:  corellm.PurposeExtract,
		Provider: "p1",
		Model:    "m",
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "抽取"}},
		Schema:   map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Outcome != corellm.OutcomeOK {
		t.Fatalf("Outcome 应为 ok，得到 %q", res.Outcome)
	}
	m, ok := res.Structured.(map[string]any)
	if !ok || m["name"] != "小明" {
		t.Fatalf("Structured 解析不符: %+v", res.Structured)
	}
	if gotRF == nil || gotRF["type"] != "json_schema" {
		t.Fatalf("应下发 response_format json_schema，得到 %+v", gotRF)
	}
}

// TestStatelessStructuredRepair 首次非法 JSON → 修复重试成功。
func TestStatelessStructuredRepair(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json at all"}}],"usage":{"total_tokens":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"total_tokens":4}}`))
	}))
	defer srv.Close()

	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{"p1": {Protocol: "openai", BaseURL: srv.URL, Key: "k"}}},
		HTTP:      srv.Client(),
	})
	res, err := exec.Call(context.Background(), corellm.Request{
		Purpose:  corellm.PurposeExtract,
		Provider: "p1",
		Model:    "m",
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "x"}},
		Schema:   "{\"type\":\"json_object\"}",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Outcome != corellm.OutcomeOK {
		t.Fatalf("修复后应 ok，得到 %q", res.Outcome)
	}
	if m, ok := res.Structured.(map[string]any); !ok || m["ok"] != true {
		t.Fatalf("修复后 Structured 不符: %+v", res.Structured)
	}
	if res.Usage.TotalTokens != 7 { // 3 + 4 累加
		t.Fatalf("两次调用 token 应累加为 7，得到 %d", res.Usage.TotalTokens)
	}
	if call != 2 {
		t.Fatalf("应调用两次（含修复），得到 %d", call)
	}
}

// TestStatelessStructuredStrictFailed 两次都非法 + Strict → 返回 error；非 Strict → Outcome=Failed 无 error。
func TestStatelessStructuredStrictFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"still not json"}}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()
	exec := NewStatelessExecutor(StatelessConfig{
		Providers: stubProviders{info: map[string]corellm.ProviderInfo{"p1": {Protocol: "openai", BaseURL: srv.URL, Key: "k"}}},
		HTTP:      srv.Client(),
	})

	// 非 Strict：Outcome=Failed，无 error。
	res, err := exec.Call(context.Background(), corellm.Request{
		Purpose: corellm.PurposeExtract, Provider: "p1", Model: "m",
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "x"}},
		Schema:   "{}",
	})
	if err != nil {
		t.Fatalf("非 Strict 不应返回 error: %v", err)
	}
	if res.Outcome != corellm.OutcomeFailed {
		t.Fatalf("应 failed，得到 %q", res.Outcome)
	}

	// Strict：返回 error。
	_, err = exec.Call(context.Background(), corellm.Request{
		Purpose: corellm.PurposeExtract, Provider: "p1", Model: "m",
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "x"}},
		Schema:   "{}", Strict: true,
	})
	if err == nil {
		t.Fatal("Strict 模式解析失败应返回 error")
	}
}
