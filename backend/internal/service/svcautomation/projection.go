package svcautomation

import (
	pkgauto "github.com/morehao/ark-mate/internal/core/automation"
	"github.com/morehao/ark-mate/internal/core/object/objtime"
	domauto "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
)

// ——— 自动化域视图 → 对外 DTO 的投影（v3.2 刀 7）———
//
// 逐字段搬运、不重命名。`Schedule` / `Target` 两侧是同一个值类型（`internal/core/automation`），
// 直接赋值即可；`RunRecord` 例外：定义文件里的它是 `time.Time`（RFC3339），接口响应要毫秒，
// 所以经 `runRecordResps` 投影（文件载体与接口口径不能共用一个结构）。
func automationResp(v domauto.AutomationView) dtoautomation.AutomationResp {
	return dtoautomation.AutomationResp{
		ID:                  v.ID,
		Name:                v.Name,
		Enabled:             v.Enabled,
		State:               v.State,
		Schedule:            v.Schedule,
		Target:              v.Target,
		MaxRunDuration:      v.MaxRunDuration,
		NextDueAt:           v.NextDueAt,
		LastFiredAt:         v.LastFiredAt,
		LastSessionID:       v.LastSessionID,
		LastStatus:          v.LastStatus,
		LastReason:          v.LastReason,
		LastReasonKind:      v.LastReasonKind,
		ConsecutiveFailures: v.ConsecutiveFailures,
		Error:               v.Error,
		RecentRuns:          runRecordResps(v.RecentRuns),
	}
}

// runRecordResps 把定义文件里的运行痕迹投影成对外的毫秒形态。
func runRecordResps(in []pkgauto.RunRecord) []dtoautomation.RunRecordResp {
	out := make([]dtoautomation.RunRecordResp, 0, len(in))
	for _, r := range in {
		out = append(out, dtoautomation.RunRecordResp{
			ScheduledFor: objtime.Milli(r.ScheduledFor),
			FiredAt:      objtime.Milli(r.FiredAt),
			Status:       r.Status,
			Reason:       r.Reason,
			ReasonKind:   r.ReasonKind,
			SessionID:    r.SessionID,
			Trigger:      r.Trigger,
		})
	}
	return out
}

func automationResps(vs []domauto.AutomationView) []dtoautomation.AutomationResp {
	out := make([]dtoautomation.AutomationResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, automationResp(v))
	}
	return out
}
