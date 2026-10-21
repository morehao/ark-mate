package capability

import (
	domskill "github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/contract/dtocapability"
)

// ——— 技能域视图 → 对外 DTO 的投影（v3.2 刀 4）———
//
// 领域返回的是**无 tag 的视图**（`skill.SkillView` 等），对外 JSON 形态在这里定。
// 逐字段搬运、不重命名：投影里出现的每一行都对应 DTO 的一个键，"哪些字段真的传出去了"
// 看这一份就够，不会在领域与用例之间产生第二套命名。
//
// 切片一律按 `make(...,0,len)` 建：`States` 没有 `omitempty`，空集合必须序列化成 `[]`
// 而不是 `null`——那是对外契约（契约基线钉着）。

func skillResps(vs []domskill.SkillView) []dtocapability.SkillResp {
	out := make([]dtocapability.SkillResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, skillResp(v))
	}
	return out
}

func skillResp(v domskill.SkillView) dtocapability.SkillResp {
	return dtocapability.SkillResp{
		ID:          v.ID,
		Name:        v.Name,
		Description: v.Description,
		Body:        v.Body,
		Runtimes:    append([]string(nil), v.Runtimes...),
		Scope:       v.Scope,
		Enabled:     v.Enabled,
		Warnings:    skillWarnings(v.Warnings),
	}
}

func skillWarnings(ws []domskill.WarningView) []dtocapability.SkillWarningResp {
	if len(ws) == 0 {
		return nil
	}
	out := make([]dtocapability.SkillWarningResp, 0, len(ws))
	for _, w := range ws {
		out = append(out, dtocapability.SkillWarningResp{
			Severity: w.Severity,
			Pattern:  w.Pattern,
			Message:  w.Message,
		})
	}
	return out
}

func skillWriteResp(v *domskill.WriteResult) *dtocapability.SkillWriteResp {
	if v == nil {
		return nil
	}
	return &dtocapability.SkillWriteResp{
		ID:                v.ID,
		Path:              v.Path,
		Created:           v.Created,
		Written:           v.Written,
		Removed:           v.Removed,
		Warnings:          skillWarnings(v.Warnings),
		MaterializeErrors: append([]string(nil), v.MaterializeErrors...),
		NextAction:        v.NextAction,
	}
}

func skillDeleteResp(v *domskill.DeleteResult) *dtocapability.SkillDeleteResp {
	if v == nil {
		return nil
	}
	return &dtocapability.SkillDeleteResp{
		ID:                v.ID,
		Deleted:           v.Deleted,
		Path:              v.Path,
		Removed:           v.Removed,
		MaterializeErrors: append([]string(nil), v.MaterializeErrors...),
		NextAction:        v.NextAction,
	}
}

func skillToggleResp(v *domskill.ToggleResult) *dtocapability.SkillToggleResp {
	if v == nil {
		return nil
	}
	return &dtocapability.SkillToggleResp{
		ID:                v.ID,
		Enabled:           v.Enabled,
		Path:              v.Path,
		Written:           v.Written,
		Removed:           v.Removed,
		MaterializeErrors: append([]string(nil), v.MaterializeErrors...),
		NextAction:        v.NextAction,
	}
}

func skillMaterializationResp(v *domskill.MaterializationView) *dtocapability.SkillMaterializationResp {
	if v == nil {
		return nil
	}
	states := make([]dtocapability.SkillMaterializationState, 0, len(v.States))
	for _, s := range v.States {
		states = append(states, dtocapability.SkillMaterializationState{
			Root:       s.Root,
			Runtime:    s.Runtime,
			Dir:        s.Dir,
			Status:     s.Status,
			Hash:       s.Hash,
			ActualHash: s.ActualHash,
			WrittenAt:  s.WrittenAt,
			Owners:     append([]string(nil), s.Owners...),
			Detail:     s.Detail,
		})
	}
	return &dtocapability.SkillMaterializationResp{
		ID:      v.ID,
		Enabled: v.Enabled,
		States:  states,
		Notes:   append([]string(nil), v.Notes...),
	}
}
