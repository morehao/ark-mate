package dao

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/pkg/object/objusage"
)

// UsageRow 是一条用量聚合输入行（runs ⨝ conversations，usage_json 已解析）。
type UsageRow struct {
	ConversationID string
	AgentID        string
	Status         string
	StartedAt      time.Time
	EndedAt        time.Time
	// Reported 该行的 usage_json 是否构成一次有效上报（空/损坏/未上报 → false）。
	Reported bool
	// Usage 上报内容；Reported 为 false 时为零值。
	Usage objusage.UsageInfo
}

// usageRawRow 是 Scan 目标：时间列保持字符串，避免依赖 gorm 对
// 「TEXT → time.Time」的隐式解析。
type usageRawRow struct {
	ConversationID string
	AgentID        string
	Status         string
	StartedAt      string
	EndedAt        string
	UsageJSON      string
}

// UsageRows 读取区间 [from, to) 内的 run 行（含 agent 归属与用量快照），按 started_at 升序。
//
// 边界必须以落库布局格式化后再比较：时间列是 RFC3339Nano **文本**，
// 直接绑 time.Time 会被驱动按自己的布局写成空格分隔，区间比较随即错位。
func (d *RunDao) UsageRows(ctx context.Context, from, to time.Time, agentID string) ([]UsageRow, error) {
	q := d.DB(ctx).Table(model.TableRun + " AS r").
		Select("r.conversation_id, c.agent_id, r.status, r.started_at, r.ended_at, r.usage_json").
		Joins("JOIN " + model.TableConversation + " AS c ON c.id = r.conversation_id")
	if !from.IsZero() {
		q = q.Where("r.started_at >= ?", model.FormatTime(from))
	}
	if !to.IsZero() {
		q = q.Where("r.started_at < ?", model.FormatTime(to))
	}
	if agentID != "" {
		q = q.Where("c.agent_id = ?", agentID)
	}

	var raw []usageRawRow
	if err := q.Order("r.started_at ASC").Scan(&raw).Error; err != nil {
		return nil, err
	}
	out := make([]UsageRow, 0, len(raw))
	for _, r := range raw {
		row := UsageRow{
			ConversationID: r.ConversationID,
			AgentID:        r.AgentID,
			Status:         r.Status,
			StartedAt:      model.ParseTime(r.StartedAt),
			EndedAt:        model.ParseTime(r.EndedAt),
		}
		if r.UsageJSON != "" {
			var u objusage.UsageInfo
			// 损坏的 usage_json 不致命：该行按「未上报」处理，不中断整段聚合。
			if err := json.Unmarshal([]byte(r.UsageJSON), &u); err == nil && u.Reported() {
				row.Reported = true
				row.Usage = u
			}
		}
		out = append(out, row)
	}
	return out, nil
}
