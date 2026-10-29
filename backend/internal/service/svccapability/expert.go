package svccapability

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/core/failure"
	domexpert "github.com/morehao/ark-mate/internal/domain/expert"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// ——— 专家定义面（P0）：用例层的投影与委托 ———
//
// 领域（`domain/expert`）只产出无 tag 的 View；对外 JSON 形态由本层投影（与 skill/mcp 同一条纪律：
// 「哪些字段真的传出去了」在这里一眼可见）。

// ListExperts 列出全部专家（含禁用），列表**不返回 instructions 正文**（只带 has_instructions）。
func (s *capabilitySvc) ListExperts(ctx context.Context) ([]dtocapability.ExpertResp, error) {
	if s.experts == nil {
		return []dtocapability.ExpertResp{}, nil
	}
	views, err := s.experts.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]dtocapability.ExpertResp, 0, len(views))
	for _, v := range views {
		out = append(out, expertResp(v, false))
	}
	return out, nil
}

// GetExpert 读一条专家详情（返回 instructions 全文）。
func (s *capabilitySvc) GetExpert(ctx context.Context, id string) (*dtocapability.ExpertResp, error) {
	view, ok, err := s.experts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, expertNotFoundErr(id)
	}
	resp := expertResp(view, true)
	return &resp, nil
}

// WriteExpert 新建/覆盖一条专家定义。
func (s *capabilitySvc) WriteExpert(ctx context.Context, pathID string, req dtocapability.ExpertWriteReq, create bool) (*dtocapability.ExpertWriteResp, error) {
	res, err := s.experts.Write(ctx, pathID, domexpert.WriteRequest(req), create)
	if err != nil {
		return nil, err
	}
	return &dtocapability.ExpertWriteResp{ID: res.ID, Path: res.Path, Created: res.Created}, nil
}

// DeleteExpert 删除一条专家定义（幂等）。
func (s *capabilitySvc) DeleteExpert(ctx context.Context, id string) (*dtocapability.ExpertDeleteResp, error) {
	res, err := s.experts.Delete(ctx, id)
	if err != nil {
		return nil, err
	}
	return &dtocapability.ExpertDeleteResp{ID: res.ID, Deleted: res.Deleted, Path: res.Path}, nil
}

// SetExpertEnabled 启停一条专家定义。
func (s *capabilitySvc) SetExpertEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.ExpertToggleResp, error) {
	res, err := s.experts.SetEnabled(ctx, id, enabled)
	if err != nil {
		return nil, err
	}
	return &dtocapability.ExpertToggleResp{ID: res.ID, Enabled: res.Enabled}, nil
}

// expertResp 领域 View → 对外形态。withInstructions=false 时丢弃正文（列表不泄露/不膨胀）。
func expertResp(v domexpert.View, withInstructions bool) dtocapability.ExpertResp {
	resp := dtocapability.ExpertResp{
		ID: v.ID, Name: v.Name, Description: v.Description, Avatar: v.Avatar, Model: v.Model,
		Skills: append([]string(nil), v.Skills...), Mcps: append([]string(nil), v.Mcps...),
		Enabled: v.Enabled, Tags: append([]string(nil), v.Tags...),
		HasInstructions: v.HasInstructions(),
	}
	if withInstructions {
		resp.Instructions = v.Instructions
	}
	return resp
}

// expertNotFoundErr 目标专家不存在（40401，由 internal/core/code 的 notFoundCodes 提档）。
func expertNotFoundErr(id string) error {
	return failure.NewError(failure.ClassRequest, "expert_not_found", fmt.Sprintf("未知 expert %q", id), nil)
}
