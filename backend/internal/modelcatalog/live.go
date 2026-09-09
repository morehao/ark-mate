package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// endpoints 各 provider type 的 live /models 端点（OpenAI 兼容协议）。
// 测试可整体覆盖以便指向 httptest server。
var endpoints = map[ProviderType]string{
	ProviderOpenAI:     "https://api.openai.com/v1/models",
	ProviderAnthropic:  "https://api.anthropic.com/v1/models",
	ProviderDeepSeek:   "https://api.deepseek.com/models",
	ProviderOpenRouter: "https://openrouter.ai/api/v1/models",
	ProviderGroq:       "https://api.groq.com/openai/v1/models",
}

// liveClient 有界超时（防上游长连接拖住请求）。
var liveClient = &http.Client{Timeout: 20 * time.Second}

// modelsResponse 上游 /models 的通用归一化视图（OpenAI 兼容 data[]；
// Anthropic/OpenRouter 亦为 data[] {id, name|display_name}）。
type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`         // openai / openrouter
		DisplayName string `json:"display_name"` // anthropic
	} `json:"data"`
}

// liveFetch 拉上游 /models 并归一化。仅当 typ 命中 endpoints 时调用（否则 ErrUnknownProvider）。
func liveFetch(ctx context.Context, typ ProviderType, apiKey string) ([]Model, error) {
	url, ok := endpoints[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, typ)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if typ == ProviderAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := liveClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("modelcatalog: 上游 %s 返回 HTTP %d", typ, resp.StatusCode)
	}
	var r modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("modelcatalog: 解析 %s 模型列表: %w", typ, err)
	}
	models := make([]Model, 0, len(r.Data))
	for _, d := range r.Data {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			continue
		}
		models = append(models, Model{ID: id, Title: firstNonEmpty(d.Name, d.DisplayName, id)})
	}
	return models, nil
}

// firstNonEmpty 依次取首个非空值（并去空白）；全空返回 ""。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
