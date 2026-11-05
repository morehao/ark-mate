package llm

import (
	"context"
	"fmt"
	"net/http"
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

	text, usage, err := e.client.chat(ctx, info.BaseURL, info.Key, model, req.Messages, req.System)
	if err != nil {
		return corellm.Result{}, err
	}
	return corellm.Result{Text: text, Usage: usage}, nil
}
