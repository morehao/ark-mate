package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm/clause"
)

// ErrRunNotFound 目标 run 不存在（终态写入时行缺失）。
var ErrRunNotFound = errors.New("store: run not found")

// 轮次的读序：列表按开轮时刻（同刻按位次），进行中的兜底取最近两条。
const (
	runListOrder = "started_at ASC, first_seq ASC"
)

// ——— runs 表（一轮 prompt 的**实体**：v22 起开轮落行，终态 UPDATE） ———

// CreateRun 插入一条 run 行（**开轮**语义见 manager.openRun：status=in_progress、first_seq=本轮
// `run.started` 的位次、last_seq=-1）。幂等：同 id 或同 `(session_id, first_seq)` 已存在即跳过。
//
// 幂等的两个唯一键：同 `id`（重放同一轮）与同 `(session_id, first_seq)`（v22 的
// `idx_runs_session_first_seq`）。后者才是真正的幂等键：重放一次 `run.started` 时 id 可能不同
// （重铸过），但"这一轮的起点"永远相同。
//
// 刻意只做"行级插入"、不在这里塞业务语义：本函数也被测试用来直接种一行终态（那时它就是
// 一条普通插入）。"开轮该写什么"属于 manager。
func (s *Store) CreateRun(ctx context.Context, r model.RunEntity) error {
	r.StartedAt = nonZeroTime(r.StartedAt)
	return s.runs.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error
}

// RunByID 按 id 取一条 run；不存在 → (零值, false, nil)。
func (s *Store) RunByID(ctx context.Context, id string) (model.RunEntity, bool, error) {
	r, err := s.runs.GetByID(ctx, id)
	if err != nil {
		return model.RunEntity{}, false, err
	}
	if r == nil {
		return model.RunEntity{}, false, nil
	}
	return *r, true, nil
}

// RunningRuns 取某会话进行中的 run 行（first_seq 降序，最多两条；终态物化的兜底路径用）。
//
// 返回一页而不是一条：多于一条意味着崩溃残轮，"最近一条"只是猜测——调用方要能选择不猜。
//
// 保留手写查询：`BaseCond` 没有 limit 这个轴；若改用分页方法会为一条"最多两条"的兜底查询
// 多做一次 COUNT。
func (s *Store) RunningRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	var rows model.RunEntityList
	err := s.runs.DB(ctx).Model(&model.RunEntity{}).
		Where("session_id = ? AND status = ?", sessionID, "in_progress").
		Order("first_seq DESC").Limit(2).Find(&rows).Error
	return rows, err
}

// FinishRun 更新 run 收口列（窗口右端 + 终态 + 首令牌耗时）；行不存在返回 ErrRunNotFound。
//
// 整组列**同一次 UPDATE**：它们是"这一轮到此为止"的各个面，分两次写会留下"已终态但窗口
// 还是 -1"的中间态。
//
// 保留手写 UPDATE：`ErrRunNotFound` 要用 RowsAffected 判定，而 `UpdateMap` 不返回它。
func (s *Store) FinishRun(ctx context.Context, id string, p model.RunFinishPatch) error {
	res := s.runs.DB(ctx).Model(&model.RunEntity{}).Where("id = ?", id).Updates(map[string]any{
		"last_seq":       p.LastSeq,
		"status":         p.Status,
		"stop_reason":    p.StopReason,
		"error_code":     p.ErrorCode,
		"usage_json":     p.UsageJSON,
		"first_token_ms": p.FirstTokenMS,
		// 落库文本在这里格式化（map 形式的 Updates 不过 serializer，见 model.LayoutTS）。
		"ended_at": model.FormatTimeOrNow(p.EndedAt),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRunNotFound
	}
	return nil
}

// ListRuns 列出某会话全部 run（started_at 升序；失败轮重试/统计按此序回溯）。
//
// 排序刻意不动：进行中的行（v22 起在库里）`started_at` 就是开轮时刻，天然排在正确位置。
func (s *Store) ListRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error) {
	return s.runs.GetListByCond(ctx, &dao.RunCond{
		BaseCond:  &gormdao.BaseCond{OrderField: runListOrder},
		SessionID: sessionID,
	})
}

// ——— 用量聚合的输入读面（runs.usage_json） ———

// usageRawRow 是 Scan 目标：时间列保持字符串，避免依赖 gorm 对
// 「TEXT → time.Time」的隐式解析。
type usageRawRow struct {
	SessionID string
	Status    string
	StartedAt string
	EndedAt   string
	UsageJSON string
}

// usageRowsForRange 读取区间 [from, to) 内的 run 行（含用量快照），按 started_at 升序，
// 直接产出 `usageRow`（聚合用的行结构，定义在 usage.go）。
//
// 边界必须以落库布局格式化后再比较：时间列是 RFC3339Nano **文本**，
// 直接绑 time.Time 会被驱动按自己的布局写成空格分隔，区间比较随即错位。
func (s *Store) usageRowsForRange(ctx context.Context, from, to time.Time) ([]usageRow, error) {
	q := s.runs.DB(ctx).Table(model.TableNameRun + " AS r").
		Select("r.session_id, r.status, r.started_at, r.ended_at, r.usage_json")
	if !from.IsZero() {
		q = q.Where("r.started_at >= ?", model.FormatTime(from))
	}
	if !to.IsZero() {
		q = q.Where("r.started_at < ?", model.FormatTime(to))
	}

	var raw []usageRawRow
	if err := q.Order("r.started_at ASC").Scan(&raw).Error; err != nil {
		return nil, err
	}
	out := make([]usageRow, 0, len(raw))
	for _, r := range raw {
		row := usageRow{
			sessionID: r.SessionID,
			status:    r.Status,
			startedAt: model.ParseTime(r.StartedAt),
			endedAt:   model.ParseTime(r.EndedAt),
		}
		if r.UsageJSON != "" {
			var u objusage.UsageInfo
			// 损坏的 usage_json 不致命：该行按「未上报」处理，不中断整段聚合。
			if err := json.Unmarshal([]byte(r.UsageJSON), &u); err == nil && u.Reported() {
				row.usage = u
				// 字段级可用性：一次上报可能只带上下文或只带费用，聚合时按字段各取「区间内最后一条」。
				row.hasContext, row.hasCost = u.HasContext(), u.HasCost()
			}
		}
		out = append(out, row)
	}
	return out, nil
}
