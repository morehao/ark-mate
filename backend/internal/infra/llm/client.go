// Package llm 是内部模型调用的**执行器实现**（L5 基础设施）：
//   - `stateless.go`：HTTP 直连 provider 的无状态执行器（OpenAI 兼容 `/chat/completions`）。
//
// 契约与值对象在 `internal/core/llm`（L1）。本包只做"与外面说话"的实现，不含业务编排——
// 选执行器、记账都在 `core/llm` 的网关里。这样分层是为了让 domain/service 依赖抽象而不依赖
// 本包（否则会形成 L2→L5 越层边，见 archtest layers_v32）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
)

// openAIChatPath OpenAI 兼容的对话补全路径（DeepSeek / OpenAI / 多数自建网关共用）。
const openAIChatPath = "/chat/completions"

// HTTPClient 是本包对 http.Client 的最小依赖（测试可注入 httptest 的 client）。
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// openAIClient 一个薄的 OpenAI 兼容 chat client。
type openAIClient struct {
	http HTTPClient
	// timeout 单次请求超时（由 Stateless 执行器注入；<=0 用默认 60s）。
	timeout time.Duration
}

// chatRequest OpenAI 兼容请求体（只含本平台用到的字段）。
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// ResponseFormat 结构化输出约束（可空）。OpenAI 兼容：{"type":"json_object"} 或
	// {"type":"json_schema","json_schema":{...}}。由执行器按 Request.Schema 装配。
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse OpenAI 兼容响应体（只取正文与 usage）。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// chatWithFormat 发一次非流式 chat 请求，额外携带 response_format（结构化输出约束）。responseFormat
// 为 nil 时不带 response_format。返回正文、token 明细。
func (c *openAIClient) chatWithFormat(ctx context.Context, baseURL, apiKey, model string, msgs []corellm.Message, system string, responseFormat json.RawMessage) (string, corellm.TokenUsage, error) {
	reqMsgs := make([]chatMessage, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		reqMsgs = append(reqMsgs, chatMessage{Role: corellm.RoleSystem, Content: system})
	}
	for _, m := range msgs {
		role := m.Role
		if role == "" {
			role = corellm.RoleUser
		}
		reqMsgs = append(reqMsgs, chatMessage{Role: role, Content: m.Text})
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: reqMsgs, Stream: false, ResponseFormat: responseFormat})
	if err != nil {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 编请求体失败: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + openAIChatPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// 单次请求超时：叠在调用方 ctx 之上（谁先到谁生效）。
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	req = req.WithContext(reqCtx)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 上游 %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 解响应失败: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", corellm.TokenUsage{}, fmt.Errorf("llm: 响应无 choices")
	}
	usage := corellm.TokenUsage{}
	if u := cr.Usage; u != nil {
		usage.InputTokens = int64(u.PromptTokens)
		usage.OutputTokens = int64(u.CompletionTokens)
		usage.TotalTokens = int64(u.TotalTokens)
		if u.PromptTokensDetails != nil {
			usage.CachedReadTokens = int64(u.PromptTokensDetails.CachedTokens)
		}
		if u.CompletionTokensDetails != nil {
			usage.ThoughtTokens = int64(u.CompletionTokensDetails.ReasoningTokens)
		}
	}
	return cr.Choices[0].Message.Content, usage, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
