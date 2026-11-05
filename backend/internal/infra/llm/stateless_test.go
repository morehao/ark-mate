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
