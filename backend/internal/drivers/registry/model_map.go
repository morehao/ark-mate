package registry

import (
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
)

// ——— 领域模型视图 → 对外 DTO 的投影（v3.2 刀 8）———
//
// 领域返回无 tag 的 `provider.ModelItem` / `AdvertisedModel`；对外形态在这一侧定。
func modelResps(vs []domprovider.ModelItem) []dtomodel.ModelResp {
	out := make([]dtomodel.ModelResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtomodel.ModelResp{
			ID: v.ID, ProviderID: v.ProviderID, Model: v.Model, Name: v.Name, Note: v.Note,
			InputModalities: append([]string(nil), v.InputModalities...),
			MaxInputTokens:  v.MaxInputTokens, MaxOutputTokens: v.MaxOutputTokens,
			SupportsReasoning: v.SupportsReasoning,
			ReasoningEfforts:  append([]string{}, v.ReasoningEfforts...),
			DefaultEffort:     v.DefaultEffort,
			Revision:          v.Revision, UpdatedAt: v.UpdatedAt,
		})
	}
	return out
}

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

// CatalogResp 把领域目录快照投影成对外形态（供 `sessionruntime.ModelFace` 用）。
func CatalogResp(v *domprovider.CatalogView) *dtomodel.ModelCatalogResp {
	if v == nil {
		return nil
	}
	opts := make([]dtomodel.ConfigOptionResp, 0, len(v.Options))
	for _, o := range v.Options {
		vals := make([]dtomodel.ConfigOptionValueResp, 0, len(o.Values))
		for _, x := range o.Values {
			vals = append(vals, dtomodel.ConfigOptionValueResp{
				Value: x.Value, Name: x.Name, Description: x.Description,
				Group: x.Group, GroupName: x.GroupName,
				Unavailable: x.Unavailable, UnavailableReason: x.UnavailableReason,
				ModelID: x.ModelID,
			})
		}
		opts = append(opts, dtomodel.ConfigOptionResp{
			ID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category,
			Type: o.Type, CurrentValue: o.CurrentValue, Grouped: o.Grouped, Values: vals,
		})
	}
	guides := make([]dtomodel.AuthGuidanceResp, 0, len(v.AuthGuidance))
	for _, g := range v.AuthGuidance {
		guides = append(guides, dtomodel.AuthGuidanceResp{
			MethodID: g.MethodID, Name: g.Name, Description: g.Description,
			Terminal: g.Terminal, Command: append([]string(nil), g.Command...),
		})
	}
	return &dtomodel.ModelCatalogResp{
		Runtime: v.Runtime, Source: v.Source, ImageInput: v.ImageInput,
		FetchedAt: v.FetchedAt, Options: opts, AuthGuidance: guides, Note: v.Note,
	}
}

// ModelResp 单条模型（供 `ModelRow` 投影）。
func ModelResp(v *domprovider.ModelItem) *dtomodel.ModelResp {
	if v == nil {
		return nil
	}
	out := modelResps([]domprovider.ModelItem{*v})
	return &out[0]
}
