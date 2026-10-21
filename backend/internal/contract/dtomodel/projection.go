package dtomodel

import (
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
)

// ——— 领域模型视图 → 对外 DTO 的投影（v3.2 刀 8；阶段 0.2 从 registry 移入）———
//
// 投影是"域 → DTO"的纯函数，既不属于执行面也不属于装配面。放在 dto 包里，registry 与
// `app/run.ModelFace` 才能共用同一份（否则就是两处各写一遍的静默分叉）。
func ModelResps(vs []domprovider.ModelItem) []ModelResp {
	out := make([]ModelResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, ModelResp{
			ID: v.ID, ProviderID: v.ProviderID, Model: v.Model, Name: v.Name, Note: v.Note,
			InputModalities: append([]string(nil), v.InputModalities...),
			MaxInputTokens:  v.MaxInputTokens, MaxOutputTokens: v.MaxOutputTokens,
			Revision: v.Revision, UpdatedAt: v.UpdatedAt,
		})
	}
	return out
}

// CatalogResp 把领域目录快照投影成对外形态（供 `app/run.ModelFace` 用）。
func CatalogRespOf(v *domprovider.CatalogView) *ModelCatalogResp {
	if v == nil {
		return nil
	}
	opts := make([]ConfigOptionResp, 0, len(v.Options))
	for _, o := range v.Options {
		vals := make([]ConfigOptionValueResp, 0, len(o.Values))
		for _, x := range o.Values {
			vals = append(vals, ConfigOptionValueResp{
				Value: x.Value, Name: x.Name, Description: x.Description,
				Group: x.Group, GroupName: x.GroupName,
				Unavailable: x.Unavailable, UnavailableReason: x.UnavailableReason,
				ModelID: x.ModelID,
			})
		}
		opts = append(opts, ConfigOptionResp{
			ID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category,
			Type: o.Type, CurrentValue: o.CurrentValue, Grouped: o.Grouped, Values: vals,
		})
	}
	guides := make([]AuthGuidanceResp, 0, len(v.AuthGuidance))
	for _, g := range v.AuthGuidance {
		guides = append(guides, AuthGuidanceResp{
			MethodID: g.MethodID, Name: g.Name, Description: g.Description,
			Terminal: g.Terminal, Command: append([]string(nil), g.Command...),
		})
	}
	return &ModelCatalogResp{
		Runtime: v.Runtime, Source: v.Source, ImageInput: v.ImageInput,
		FetchedAt: v.FetchedAt, Options: opts, AuthGuidance: guides, Note: v.Note,
	}
}

// ModelResp 单条模型（供 `ModelRow` 投影）。
func ModelRespOf(v *domprovider.ModelItem) *ModelResp {
	if v == nil {
		return nil
	}
	out := ModelResps([]domprovider.ModelItem{*v})
	return &out[0]
}
