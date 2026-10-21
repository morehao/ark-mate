package automation

import (
	domauto "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/contract/dtoautomation"
)

// ——— 自动化域视图 → 对外 DTO 的投影（v3.2 刀 7）———
//
// 逐字段搬运、不重命名。`Schedule` / `Target` / `RunRecord` 两侧是同一个值类型
// （`internal/platform/automation`），直接赋值即可——它们本来就没有 json tag。
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
		ConsecutiveFailures: v.ConsecutiveFailures,
		Error:               v.Error,
		RecentRuns:          v.RecentRuns,
	}
}

func automationResps(vs []domauto.AutomationView) []dtoautomation.AutomationResp {
	out := make([]dtoautomation.AutomationResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, automationResp(v))
	}
	return out
}
