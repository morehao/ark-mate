package svcmodel

import (
	domprovider "github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/dto/dtomodel"
	"github.com/morehao/ark-mate/internal/object/objdelivery"
)

// ——— 供应商域视图 → 对外 DTO 的投影（v3.2 刀 8）———
//
// 逐字段搬运、不重命名。没有 `omitempty` 的切片一律 `make(...,0,len)`
// （`problems` / `applicability` / `refs.models` / `refs.runtimes` 必须序列化成 `[]`）。

func providerResps(vs []domprovider.ProviderItem) []dtomodel.ProviderResp {
	out := make([]dtomodel.ProviderResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, providerResp(v))
	}
	return out
}

func providerResp(v domprovider.ProviderItem) dtomodel.ProviderResp {
	problems := make([]string, 0, len(v.Problems))
	problems = append(problems, v.Problems...)
	appl := make([]objdelivery.Applicability, 0, len(v.Applicability))
	appl = append(appl, v.Applicability...)
	return dtomodel.ProviderResp{
		ID:            v.ID,
		Name:          v.Name,
		Protocol:      v.Protocol,
		BaseURL:       v.BaseURL,
		HasKey:        v.HasKey,
		KeySource:     v.KeySource,
		KeyRef:        v.KeyRef,
		Note:          v.Note,
		Revision:      v.Revision,
		UpdatedAt:     v.UpdatedAt,
		ModelCount:    v.ModelCount,
		Problems:      problems,
		Applicability: appl,
		Refs: dtomodel.ProviderRefs{
			Models:   nonNilStrings(v.Refs.Models),
			Runtimes: nonNilStrings(v.Refs.Runtimes),
		},
		Effect: providerEffectDTO(v.Effect),
	}
}

// nonNilStrings 空集合必须是 `[]`（这两个字段没有 omitempty，前端不必判 null）。
func nonNilStrings(in []string) []string {
	out := make([]string, 0, len(in))
	return append(out, in...)
}

func providerEffectDTO(v *domprovider.ProviderEffect) *dtomodel.ProviderEffect {
	if v == nil {
		return nil
	}
	return &dtomodel.ProviderEffect{
		Channel:          v.Channel,
		Effect:           v.Effect,
		AffectedSessions: v.AffectedSessions,
		Deferred:         v.Deferred,
		Note:             v.Note,
	}
}

func providerDeleteResp(v *domprovider.ProviderDelete) *dtomodel.ProviderDeleteResp {
	if v == nil {
		return nil
	}
	return &dtomodel.ProviderDeleteResp{
		Deleted:            v.Deleted,
		ReferencedByModels: append([]string(nil), v.ReferencedByModels...),
		CredentialsLeft:    v.CredentialsLeft,
	}
}

func createRequest(req dtomodel.ProviderCreateReq) domprovider.CreateRequest {
	return domprovider.CreateRequest{
		ID: req.ID, Name: req.Name, Protocol: req.Protocol,
		BaseURL: req.BaseURL, APIKey: req.APIKey, Note: req.Note,
	}
}

func patchRequest(req dtomodel.ProviderPatchReq) domprovider.PatchRequest {
	return domprovider.PatchRequest{
		Name: req.Name, Protocol: req.Protocol, BaseURL: req.BaseURL,
		APIKey: req.APIKey, Note: req.Note,
	}
}
