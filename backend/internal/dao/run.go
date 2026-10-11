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

// CreateIfAbsent 插入一条 run；同 id 已存在则静默跳过（幂等，重放/重试安全）。
func (d *RunDao) CreateIfAbsent(ctx context.Context, r *model.RunEntity) error {
	return d.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(r).Error
}

// Finish 写入 run 终态（status/stop_reason/error_code/usage/ended_at）；行不存在返回 ErrRunNotFound。
func (d *RunDao) Finish(ctx context.Context, id, status, stopReason, errorCode, usageJSON string, endedAt string) error {
	res := d.DB(ctx).Model(&model.RunEntity{}).Where("id = ?", id).Updates(map[string]any{
		"status":      status,
		"stop_reason": stopReason,
		"error_code":  errorCode,
		"usage_json":  usageJSON,
		"ended_at":    endedAt,
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

// LastSeq 返回某会话最后一条 run 的 last_seq（0 = 尚无 run；供下一 run 的事件窗口起点）。
func (d *RunDao) LastSeq(ctx context.Context, sessionID string) (int64, error) {
	var seq int64
	if err := d.DB(ctx).Model(&model.RunEntity{}).
		Select("COALESCE(MAX(last_seq), 0)").
		Where("session_id = ?", sessionID).
		Scan(&seq).Error; err != nil {
		return 0, err
	}
	return seq, nil
}
