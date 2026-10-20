package provider

import (
	"context"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ProviderStore 供应商档案与模型定义的载体（v3.2 刀 8）。
//
// 九条操作全是"供应商/模型这一件事"的读写：档案的 upsert/get/list/删除、模型定义的
// get/list、密钥列、默认模型。没有一条能碰别的聚合，所以它是**聚合仓储**而不是"把库交出来"。
//
// 签名用 `model.*`（`store.ProviderColumns` 本来就是 `model.ProviderColumns` 的别名），
// 所以 `*store.Store` 结构上直接满足，装配期不需要适配层。
type ProviderStore interface {
	UpsertProviderDefinition(ctx context.Context, cols model.ProviderColumns, expectedRev int64, actor string) (bool, error)
	GetProviderDefinition(ctx context.Context, id string) (*model.ProviderEntity, bool, error)
	ListProviderDefinitions(ctx context.Context) (model.ProviderEntityList, error)
	DeleteProviderDefinitionIfUnreferenced(ctx context.Context, id string, expectedRev int64, actor string) (bool, []string, error)
	SetProviderKey(ctx context.Context, providerID, key string) (bool, error)

	GetModelDefinition(ctx context.Context, id string) (*model.ModelEntity, bool, error)
	ListModelDefinitions(ctx context.Context) (model.ModelEntityList, error)
	ListModelDefinitionsByProvider(ctx context.Context, providerID string) (model.ModelEntityList, error)
	DefaultModelID(ctx context.Context) (string, bool, error)
}
