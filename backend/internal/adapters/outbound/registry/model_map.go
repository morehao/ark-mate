package registry

import (
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
)

// ——— 领域模型视图 → 对外 DTO 的投影（v3.2 刀 8）———
//
// 领域返回无 tag 的 `provider.ModelItem` / `AdvertisedModel`；对外形态在这一侧定。
func advertisedModels(vs []domprovider.AdvertisedModel) []dtomodel.AdvertisedModel {
	out := make([]dtomodel.AdvertisedModel, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtomodel.AdvertisedModel{
			Value: v.Value, Name: v.Name, Group: v.Group, GroupName: v.GroupName,
			Unavailable: v.Unavailable, UnavailableReason: v.UnavailableReason,
		})
	}
	return out
}
