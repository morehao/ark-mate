package registry

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/mechanism/doctor"
	goruntime "github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
)

// ——— `mate doctor` 的模型事实来源（P5a 取样面的最后一块）———
//
// 判断在机制层（`doctor.CollectRuntimeModelFacts`：怎么读状态、差集怎么算、"无法校验"与
// "一条都没生效"怎么区分），数据在这里：同步结果、目录取样、平台清单与不透明值的派生规则。
//
// 落点为什么是 registry：体检要的是**装配事实**（平面 + 同步面），不需要会话/编排。此前它
// 靠 "造一个只读 Service" 逃生，而 Service 一解散那条路就断了；现在直接拿 `Planes`。

// ModelFacts 把装配平面的读数面收窄成 `doctor.ModelFactsSource`。
type ModelFacts struct {
	providers *provider.Plane
	resources *Resources
}

// NewModelFacts 造模型事实来源（装配点与体检入口共用）。
func NewModelFacts(p *Planes) ModelFacts {
	return ModelFacts{providers: p.Providers, resources: p.Resources}
}

// RuntimeStates 各 runtime 最后一次同步的结论。
func (f ModelFacts) RuntimeStates(ctx context.Context) map[string]dtomodel.RuntimeSyncResp {
	return f.resources.RuntimeConfigStates(ctx)
}

// DeclaredModels 平台声明的模型清单。
func (f ModelFacts) DeclaredModels(ctx context.Context) ([]dtomodel.ModelResp, error) {
	views, err := f.providers.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	return modelResps(views), nil
}

// Advertised 运行时目录取样（带来源判定：`Known=false` = 无法校验）。
func (f ModelFacts) Advertised(ctx context.Context) (doctor.AdvertisedSample, error) {
	sample, err := f.providers.AdvertisedSample(ctx)
	if err != nil {
		return doctor.AdvertisedSample{}, err
	}
	// `Known()` 的来源词汇（session|cache|resume|none）是目录域的；端口只带判定结果过去。
	return doctor.AdvertisedSample{
		Runtime: sample.Runtime, Models: advertisedModels(sample.Models),
		Known: sample.Known(), Note: sample.Note,
	}, nil
}

// ModelValue 平台行 → 该 runtime 认得的不透明值。
func (f ModelFacts) ModelValue(runtimeID, providerID, model string) (string, error) {
	return f.providers.ModelValueFor(runtimeID, providerID, model)
}

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
func CollectRuntimeModelFacts(ctx context.Context, st *store.Store, home string, reg *goruntime.Registry) []doctor.ProviderRuntimeFacts {
	out := []doctor.ProviderRuntimeFacts{}
	if st == nil || reg == nil {
		return out
	}
	reg.ApplyIsolationEnv(strings.TrimSpace(home))
	planes := NewPlanes(reg)
	planes.EnableStore(st)

	return doctor.CollectRuntimeModelFacts(ctx, NewModelFacts(planes), reg.List())
}
