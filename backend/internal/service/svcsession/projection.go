package svcsession

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// ——— 会话域视图 → 对外 DTO（v3.2 刀 9）———
//
// 逐字段搬运、不重命名；没有 `omitempty` 的切片按 `make(...,0,len)` 建。

// ComposerSkills 把内置技能目录（v4 P1）投影成对外 DTO。
//
// 它是**包级只读函数**而不是 `SessionSvc` 的方法：内置技能是无状态常量，没有会话态、
// 没有校验、没有错误码出口——挂成用例方法只是徒增一个端口（`SessionSvc` 有反向守卫，
// 见其接口注释）。控制器直接调本函数即可。
func ComposerSkills() dtosession.ComposerSkillsResp {
	src := session.BuiltinSkills()
	items := make([]dtosession.ComposerSkillResp, 0, len(src))
	for _, s := range src {
		items = append(items, dtosession.ComposerSkillResp{ID: s.ID, Name: s.Name, Desc: s.Desc})
	}
	return dtosession.ComposerSkillsResp{Items: items}
}

func capabilitiesDTO(v *session.SessionCapabilities) *dtosession.SessionCapabilitiesResp {
	if v == nil {
		return nil
	}
	return &dtosession.SessionCapabilitiesResp{
		Skills: v.Skills, Mcps: v.Mcps, Expert: v.Expert, ExpertMissing: v.ExpertMissing, Fingerprint: v.Fingerprint,
	}
}

func runPhaseDTO(v *session.RunPhase) *dtosession.RunPhaseResp {
	if v == nil {
		return nil
	}
	return &dtosession.RunPhaseResp{
		RunID: v.RunID, Status: v.Status, Phase: v.Phase, Detail: v.Detail,
		ToolCallID: v.ToolCallID, ToolName: v.ToolName, WaitingFor: v.WaitingFor,
		Reason: v.Reason, At: v.At, Seq: v.Seq,
	}
}

func sessionRespDTO(v session.SessionItem) dtosession.SessionResp {
	return dtosession.SessionResp{
		ID: v.ID, RuntimeSessionID: v.RuntimeSessionID, RuntimeID: v.RuntimeID,
		Status: v.Status, Run: runPhaseDTO(v.Run), Title: v.Title, Pinned: v.Pinned,
		Archived: v.Archived, Cwd: v.Cwd, WorkspaceID: v.WorkspaceID,
		WorkspaceTitle: v.WorkspaceTitle, WorkspaceTemporary: v.WorkspaceTemporary,
		Model: v.Model, ModelName: v.ModelName, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Origin: v.Origin,
		Capabilities: capabilitiesDTO(v.Capabilities),
		NativeMode:   v.NativeMode,
	}
}

func sessionRespsDTO(vs []session.SessionItem) []dtosession.SessionResp {
	out := make([]dtosession.SessionResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, sessionRespDTO(v))
	}
	return out
}

func sessionSearchHitsDTO(vs []session.SessionSearchHitItem) []dtosession.SessionSearchHit {
	out := make([]dtosession.SessionSearchHit, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtosession.SessionSearchHit{
			SessionResp: sessionRespDTO(v.SessionItem), Snippet: v.Snippet, Matches: v.Matches,
		})
	}
	return out
}

func runUsageDTO(v *session.RunUsage) *dtosession.RunUsageResp {
	if v == nil {
		return nil
	}
	return &dtosession.RunUsageResp{
		Used: v.Used, Size: v.Size, CostAmount: v.CostAmount, CostCurrency: v.CostCurrency,
	}
}

func runRespsDTO(vs []session.RunItem) []dtosession.RunResp {
	out := make([]dtosession.RunResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtosession.RunResp{
			ID: v.ID, UserText: v.UserText, Status: v.Status, ErrorCode: v.ErrorCode,
			StopReason: v.StopReason, FirstSeq: v.FirstSeq, LastSeq: v.LastSeq,
			StartedAt: v.StartedAt, EndedAt: v.EndedAt, FirstTokenMS: v.FirstTokenMS,
			Usage: runUsageDTO(v.Usage),
		})
	}
	return out
}
