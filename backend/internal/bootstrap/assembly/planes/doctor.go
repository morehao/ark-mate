package planes

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/adapters/outbound/models"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/runtime"
)

// CollectRuntimeModelFacts 逐 runtime 采集模型清单事实（**只读**）。
//
// 三处只为读而做的事，逐条写下理由：
//
//	`ApplyIsolationEnv(home)`：落点声明里的 `${...}` 占位符只认该 runtime 自己的 Launch.Env
//	  （fail-closed）。不注入的话 `ResolveConfigProjection` 会报错、于是所有 runtime 都从报告里
//	  **静默消失**——那是最坏的假绿形态。
//	`EnableStore(st)`：把 `meta.runtime_config_digest` 读进内存（否则"已物化"会被报成"从未物化"）。
//	  顺带做一次装配期同步——与 `serve` 的启动路径同一个入口，体检看到的是**同一条**取值面。
//	**不**走 `LoadProviderDefinitionsFromStore`：那条路会经 `loadProviders` 读凭据面与库，
//	  而体检要的是"清单事实"，不是"装载档案"（装载会在缺镜像时按库重建镜像——那是落笔）。
//
// 落点为什么是平面容器而不是 `models` 包：它要**构造一整套平面**（并把库接进去），
// 而 `models` 是端口实现、不该回头认识容器（那会成环）。
func CollectRuntimeModelFacts(ctx context.Context, st *store.Store, home string, reg *runtime.Registry) []doctor.ProviderRuntimeFacts {
	out := []doctor.ProviderRuntimeFacts{}
	if st == nil || reg == nil {
		return out
	}
	reg.ApplyIsolationEnv(strings.TrimSpace(home))
	planes := New(reg)
	planes.EnableStore(st)

	return doctor.CollectRuntimeModelFacts(ctx, models.NewModelFacts(planes.Resources, planes.Providers), reg.List())
}
