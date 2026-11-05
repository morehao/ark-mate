package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
)

// StatelessConfig 构造无状态执行器的依赖。
type StatelessConfig struct {
	// Providers 供应商解析端口（必填）：provider 档案 id → 协议/端点/密钥。
	Providers corellm.ProviderSource
	// HTTP 注入的 http client（nil = http.DefaultClient；测试注入 httptest 的 client）。
	HTTP HTTPClient
	// Timeout 单次请求超时（<=0 = 60s）。
	Timeout time.Duration
	// DefaultModel 请求未指定 Model 时的兜底模型名（空 = 不兜底，交由上游报错）。
	DefaultModel string
}

// statelessExecutor 无状态执行器：一次调用一次 HTTP，不持有会话/上下文。
type statelessExecutor struct {
	providers    corellm.ProviderSource
	client       *openAIClient
	defaultModel string
}

// NewStatelessExecutor 构造无状态执行器（实现 corellm.Executor）。
func NewStatelessExecutor(cfg StatelessConfig) corellm.Executor {
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient // *http.Client 满足 HTTPClient（Do 方法）
	}
	return &statelessExecutor{
		providers:    cfg.Providers,
		client:       &openAIClient{http: httpClient, timeout: cfg.Timeout},
		defaultModel: cfg.DefaultModel,
	}
}

// Call 执行一次无状态调用。
//
// 流程：解析 provider（协议/端点/密钥）→ 校验协议 → 选模型 → 发 chat 请求 → 汇总结果。
// 任一环节失败都返回明确错误（不静默降级：缺密钥就报缺密钥，而不是发一个 401 出去）。
func (e *statelessExecutor) Call(ctx context.Context, req corellm.Request) (corellm.Result, error) {
	if e.providers == nil {
		return corellm.Result{}, fmt.Errorf("llm: 未注入 ProviderSource")
	}
	if req.Provider == "" {
		return corellm.Result{}, fmt.Errorf("llm: 无状态执行器需要指定 Provider")
	}
	info, err := e.providers.Resolve(ctx, req.Provider)
	if err != nil {
		return corellm.Result{}, err
	}
	if info.Protocol != "" && info.Protocol != "openai" {
		return corellm.Result{}, fmt.Errorf("llm: 不支持的供应商协议 %q（无状态执行器当前仅支持 openai 兼容）", info.Protocol)
	}
	if info.BaseURL == "" {
		return corellm.Result{}, fmt.Errorf("llm: 供应商 %q 缺端点（base_url）", req.Provider)
	}
	if info.Key == "" {
		// 空 key 打出去只会得到 401（在很远的地方报错）；就地报可执行的原因。
		return corellm.Result{}, fmt.Errorf("llm: 供应商 %q 未配置密钥", req.Provider)
	}
	model := req.Model
	if model == "" {
		model = e.defaultModel
	}
	if model == "" {
		return corellm.Result{}, fmt.Errorf("llm: 未指定模型（Request.Model 与 DefaultModel 都为空）")
	}

	text, usage, err := e.client.chatWithFormat(ctx, info.BaseURL, info.Key, model, req.Messages, req.System, responseFormatOf(req.Schema))
	if err != nil {
		return corellm.Result{}, err
	}

	// 未要求结构化：普通文本返回。
	if req.Schema == nil {
		return corellm.Result{Text: text, Usage: usage, Outcome: corellm.OutcomeOK}, nil
	}

	// 要求结构化：解析正文 → 失败则修复重试（限 1 次）→ 仍失败按 Strict 决定返回方式。
	structured, ok := parseStructured(text)
	res := corellm.Result{Text: text, Usage: usage}
	if ok {
		res.Structured = structured
		res.Outcome = corellm.OutcomeOK
		return res, nil
	}

	// 修复重试：把"上次输出 + 解析错误"补一条 user 消息，要求重出。
	repairMsgs := append(append([]corellm.Message{}, req.Messages...),
		corellm.Message{Role: corellm.RoleAssistant, Text: text},
		corellm.Message{Role: corellm.RoleUser, Text: "上一次输出不是合法 JSON。请只输出符合要求的 JSON，不要任何解释或代码块围栏。"},
	)
	text2, usage2, err2 := e.client.chatWithFormat(ctx, info.BaseURL, info.Key, model, repairMsgs, req.System, responseFormatOf(req.Schema))
	res.Usage = addUsage(usage, usage2)
	if err2 == nil {
		if s2, ok2 := parseStructured(text2); ok2 {
			res.Text = text2
			res.Structured = s2
			res.Outcome = corellm.OutcomeOK
			return res, nil
		}
		res.Text = text2
	}

	// 修复后仍失败：Strict → 返回 error；否则以 Outcome=Failed 表达（不阻塞主流程）。
	res.Outcome = corellm.OutcomeFailed
	if req.Strict {
		return res, fmt.Errorf("llm: 结构化输出解析失败（已修复重试 1 次）")
	}
	return res, nil
}

// responseFormatOf 把 Request.Schema 装配成 OpenAI 兼容的 response_format。
//
// 支持两种 Schema 形状：
//   - `json.RawMessage` / `[]byte` / `string`：视为**完整的 response_format JSON**，直接用
//     （调用方需自带 {"type":...} 结构，掌控最强）；
//   - 其它任意值（map/struct）：视为**JSON Schema body**，包成
//     {"type":"json_schema","json_schema":{"name":"output","schema":<该值>}}。
//
// nil → nil（不约束）。装配失败 → nil（退化为普通文本，由上层 Outcome 兜底）。
func responseFormatOf(schema any) json.RawMessage {
	if schema == nil {
		return nil
	}
	switch v := schema.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return nil
		}
		return v
	case []byte:
		if len(v) == 0 {
			return nil
		}
		return json.RawMessage(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return json.RawMessage(v)
	}
	// 其它值：当作 JSON Schema body 包一层。
	raw, err := json.Marshal(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "output",
			"schema": schema,
		},
	})
	if err != nil {
		return nil
	}
	return raw
}

// parseStructured 把模型正文解析成结构化对象。容忍 ```json 围栏（部分模型会加）。
func parseStructured(text string) (any, bool) {
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}

// addUsage 累加两次调用的 token（修复重试的第一次也计入成本）。
func addUsage(a, b corellm.TokenUsage) corellm.TokenUsage {
	return corellm.TokenUsage{
		InputTokens:       a.InputTokens + b.InputTokens,
		OutputTokens:      a.OutputTokens + b.OutputTokens,
		CachedReadTokens:  a.CachedReadTokens + b.CachedReadTokens,
		CachedWriteTokens: a.CachedWriteTokens + b.CachedWriteTokens,
		ThoughtTokens:     a.ThoughtTokens + b.ThoughtTokens,
		TotalTokens:       a.TotalTokens + b.TotalTokens,
	}
}
