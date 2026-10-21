package models

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
)

// ——— `mate doctor` 的模型事实来源（P5a 取样面的最后一块）———
//
// 判断在机制层（`doctor.CollectRuntimeModelFacts`：怎么读状态、差集怎么算、"无法校验"与
// "一条都没生效"怎么区分），数据在这里：同步结果、目录取样、平台清单与不透明值的派生规则。
//
// 落点为什么是 models（资源同步 / 模型读面）：体检要的是**装配事实**（供应商平面 + 同步面），
// 不需要会话/编排。此前它靠 "造一个只读 Service" 逃生，而 Service 一解散那条路就断了。

// ModelFacts 把装配平面的读数面收窄成 `doctor.ModelFactsSource`。
type ModelFacts struct {
	providers *provider.Plane
	resources *Resources
}

// NewModelFacts 造模型事实来源（装配点与体检入口共用）。
//
// 参数是**两份具体面**而不是平面容器：容器住在装配根（`bootstrap/assembly/planes`），
// 本包不该反过来认识它——那会让 "adapters 认识装配根" 这条边凭空出现。
func NewModelFacts(res *Resources, providers *provider.Plane) ModelFacts {
	return ModelFacts{providers: providers, resources: res}
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
	return dtomodel.ModelResps(views), nil
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
