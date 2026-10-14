package dao

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRunNotFound 目标 run 不存在（终态写入时行缺失）。
var ErrRunNotFound = errors.New("dao: run not found")

// RunCond 轮次查询条件。
type RunCond struct {
	*gormdao.BaseCond
	SessionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *RunCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(tableName+".session_id = ?", c.SessionID)
	}
}

// RunDao 访问 runs 表（每轮 prompt 的物化行）。
type RunDao struct {
	*gormdao.Dao[model.RunEntity, model.RunEntityList, string]
}

// NewRunDao 构造 RunDao（getDB 必填，见包注释）。
func NewRunDao(getDB gormdao.DBGetter) *RunDao {
	return &RunDao{Dao: newDao[model.RunEntity, model.RunEntityList, string]("RunDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *RunDao) WithTx(tx *gorm.DB) *RunDao {
	return &RunDao{Dao: d.Dao.WithTx(tx)}
}

// CreateIfAbsent 插入一条 run（**开轮**落行：status=running、first_seq 必填、last_seq=-1）。
//
// 幂等：`ON CONFLICT DO NOTHING` 覆盖**两个**唯一键——同 `id`（重放同一轮）与同
// `(session_id, first_seq)`（v22 的 `idx_runs_session_first_seq`）。后者才是真正的幂等键：
// 重放一次 `run.started` 时 id 可能不同（重铸过），但"这一轮的起点"永远相同。
func (d *RunDao) CreateIfAbsent(ctx context.Context, r *model.RunEntity) error {
	return d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(r).Error
}

// GetByID 按 id 取一条 run；不存在 → (零值, false, nil)（不把"没有"当错误往上抛，
// 终态物化的兜底路径要能区分"没有这一行"与"读库失败"）。
func (d *RunDao) GetByID(ctx context.Context, id string) (model.RunEntity, bool, error) {
	var r model.RunEntity
	err := d.DB(ctx).Where("id = ?", id).Limit(1).Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RunEntity{}, false, nil
	}
	if err != nil {
		return model.RunEntity{}, false, err
	}
	return r, true, nil
}

// RunningRows 取某会话**进行中**的行（first_seq 降序；最多两条）。
//
// 只给终态物化的兜底路径用：终态载荷缺 `run_id`（或那个 id 不在库）时，本轮的开轮行只能按
// "该会话那条 running"找。**刻意返回一页而不是一条**：同一会话里多于一条 running 行意味着
// 崩溃留下的残轮（那时"最近一条"只是猜测），调用方要能看见"不唯一"并选择不猜。
func (d *RunDao) RunningRows(ctx context.Context, sessionID string) (model.RunEntityList, error) {
	var rows model.RunEntityList
	err := d.DB(ctx).Model(&model.RunEntity{}).
		Where("session_id = ? AND status = ?", sessionID, "running").
		Order("first_seq DESC").Limit(2).Find(&rows).Error
	return rows, err
}

// Finish 写入 run 收口列（窗口右端 + 终态 + 首令牌耗时）；行不存在返回 ErrRunNotFound。
//
// 整组列**同一次 UPDATE**：它们是"这一轮到此为止"的各个面，分两次写会留下"已终态但窗口
// 还是 -1"的中间态。
func (d *RunDao) Finish(ctx context.Context, id string, p model.RunFinishPatch) error {
	res := d.DB(ctx).Model(&model.RunEntity{}).Where("id = ?", id).Updates(map[string]any{
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

// List 列出某会话全部 run（started_at 升序，同刻按 first_seq 升序；失败轮重试/统计按此序回溯）。
func (d *RunDao) List(ctx context.Context, sessionID string) (model.RunEntityList, error) {
	return d.GetListByCond(ctx, &RunCond{
		BaseCond:  &gormdao.BaseCond{OrderField: "started_at ASC, first_seq ASC"},
		SessionID: sessionID,
	})
}
