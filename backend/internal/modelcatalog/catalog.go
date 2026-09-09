// Package modelcatalog 提供「可选模型」目录能力（设计文档
// docs/model-selection-design-20260909.md §11）。
//
// 三段式降级：live（上游 /models）→ 磁盘缓存（TTL 24h）→ go:embed 编译期快照。
// Resolve 是唯一公开入口：live 成功即回写缓存并返回；失败回退未过期缓存；再回退内置快照。
// 本包是叶子包（只依赖标准库），不关心运行时注入（那属 Phase B）。
package modelcatalog

import (
	"context"
	"embed"
	"errors"
	"fmt"
)

// Source 标定目录数据来源（排障与降级提示用）。
type Source string

const (
	SourceLive     Source = "live"
	SourceCache    Source = "cache"
	SourceSnapshot Source = "snapshot"
)

// ProviderType 上游 provider 类型（与 manager/providers.go envKeyOf 保持一致）。
type ProviderType string

const (
	ProviderOpenAI     ProviderType = "openai"
	ProviderAnthropic  ProviderType = "anthropic"
	ProviderDeepSeek   ProviderType = "deepseek"
	ProviderOpenRouter ProviderType = "openrouter"
	ProviderGroq       ProviderType = "groq"
)

// Model 目录中的一个模型条目（归一化表示；ID 对齐各上游）。
type Model struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`                // 展示名（上游 name/display_name；缺省 = ID）
	Description string   `json:"description,omitempty"` // 归属 provider 展示（前端 ModelOption.description）
	Behavior    []string `json:"behavior,omitempty"`   // effort/reasoning 可选值（dsh 用）
	Recommended bool     `json:"recommended,omitempty"` // 推荐（星标；go:embed 快照标注）
	IsFree      bool     `json:"is_free,omitempty"`
}

// ErrUnknownProvider 未支持的 ProviderType。
var ErrUnknownProvider = errors.New("modelcatalog: unsupported provider type")

// validTypes 支持的 provider 类型集合（Valid 判定 + live 端点分发）。
var validTypes = map[ProviderType]bool{
	ProviderOpenAI:     true,
	ProviderAnthropic:  true,
	ProviderDeepSeek:   true,
	ProviderOpenRouter: true,
	ProviderGroq:       true,
}

// Valid 校验 ProviderType 是否被支持。
func Valid(typ ProviderType) bool { return validTypes[typ] }

// SupportedTypes 返回全部受支持的 provider 类型（稳定顺序，供聚合目录遍历）。
func SupportedTypes() []ProviderType {
	return []ProviderType{ProviderOpenAI, ProviderAnthropic, ProviderDeepSeek, ProviderOpenRouter, ProviderGroq}
}

//go:embed catalog/snapshots/*.json
var snapshotsFS embed.FS

// Resolve 三段式取模型目录。
//   - live 成功（apiKey 非空）→ 写磁盘缓存(TTL 24h) → (models, SourceLive, nil)；
//   - 未过期磁盘缓存 → (models, SourceCache, nil)；
//   - 否则 go:embed 快照 → (models, SourceSnapshot, nil)。
//
// apiKey 为空时跳过 live，直接走缓存/快照。cacheDir 为空则用 DefaultCacheDir()。
// 返回的 models 顺序保持：live 与快照按上游/内置次序，缓存按写回次序——调方按需排序。
func Resolve(ctx context.Context, typ ProviderType, apiKey, cacheDir string) ([]Model, Source, error) {
	if !Valid(typ) {
		return nil, "", fmt.Errorf("%w: %q", ErrUnknownProvider, typ)
	}
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}

	if apiKey != "" {
		models, err := liveFetch(ctx, typ, apiKey)
		if err == nil {
			if err := writeCache(typ, cacheDir, models); err != nil {
				// 缓存写失败不阻断：仍以 live 结果返回（下次可再降级）。
				_ = err
			}
			return models, SourceLive, nil
		}
		// live 失败 → 落到缓存/快照（不清除已存在缓存，交由 TTL 自然过期）。
	}

	if models, ok := readCache(typ, cacheDir); ok {
		return models, SourceCache, nil
	}

	models, err := snapshotModels(typ)
	if err != nil {
		return nil, "", fmt.Errorf("modelcatalog: 快照缺失 %q: %w", typ, err)
	}
	return models, SourceSnapshot, nil
}
