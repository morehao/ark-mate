package provider

import (
	"context"

	provrow "github.com/morehao/ark-mate/internal/domain/provider/row"
)

// ProviderStore 供应商档案与模型定义的载体（v3.2 刀 8）。
//
// 九条操作全是"供应商/模型这一件事"的读写：档案的 upsert/get/list/删除、模型定义的
// get/list、密钥列、默认模型。没有一条能碰别的聚合，所以它是**聚合仓储**而不是"把库交出来"。
//
// 签名用本域的行值（`provider/row`）：行形状是适配器的语言，不是域的。`*store.Store` 在
// 适配器里做行↔值映射，领域因此不认识 `adapters/outbound/sqlite/model`。
type ProviderStore interface {
	UpsertProviderDefinition(ctx context.Context, cols provrow.Columns, expectedRev int64, actor string) (bool, error)
	GetProviderDefinition(ctx context.Context, id string) (*provrow.Item, bool, error)
	ListProviderDefinitions(ctx context.Context) ([]provrow.Item, error)
	DeleteProviderDefinitionIfUnreferenced(ctx context.Context, id string, expectedRev int64, actor string) (bool, []string, error)
	SetProviderKey(ctx context.Context, providerID, key string) (bool, error)

	GetModelDefinition(ctx context.Context, id string) (*provrow.Model, bool, error)
	ListModelDefinitions(ctx context.Context) ([]provrow.Model, error)
	ListModelDefinitionsByProvider(ctx context.Context, providerID string) ([]provrow.Model, error)
	DefaultModelID(ctx context.Context) (string, bool, error)
}
