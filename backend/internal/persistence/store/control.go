package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 控制面认证持久层（ark-iam RP 接入）的业务口径。
//
// 设计：`.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md`（DM-2 / C1 / C2）。
// 本文件只承载三类「平台侧」写读：
//   1. 租户的平台属性 ensure（懒建，默认 sandboxed）；
//   2. 用户的**幂等开通**（首次见到即 upsert，复用已有行，绝不新增第二行）；
//   3. 审计写入。
// **不认识登录资格/归属判定**：鉴权与所有权读实时令牌，不读这些行（见 model/control.go 的口径说明）。

// ControlTenantUpsert 是 ensureTenant 的入参（平台属性；tenant_id 已由令牌决定）。
type ControlTenantUpsert struct {
	TenantID string
	NowMilli int64
}

// EnsureControlTenant 懒创建租户平台属性行：不存在则插入（isolation 默认 sandboxed），
// 存在则只刷新 updated_at（不改 isolation/quota——那些归运维配置，不被登录覆盖）。
func (s *Store) EnsureControlTenant(ctx context.Context, in ControlTenantUpsert) error {
	if in.TenantID == "" {
		return fmt.Errorf("store: EnsureControlTenant tenant_id 为空")
	}
	row := model.ControlTenantEntity{
		TenantID:       in.TenantID,
		IsolationLevel: model.IsolationSandboxed,
		CreatedAt:      in.NowMilli,
		UpdatedAt:      in.NowMilli,
	}
	// 冲突时只更新 updated_at，其余平台配置列保持原值（登录不能改配额/隔离档）。
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"updated_at"}),
	}).Create(&row).Error
}

// ControlUserUpsert 是身份开通（幂等 upsert）的入参。
type ControlUserUpsert struct {
	TenantID  string
	UserID    string
	PersonID  string
	UserType  string
	ActedAt   int64 // Unix 毫秒
	CreatedAt int64 // Unix 毫秒（仅首次插入时落）
}

// UpsertControlUser 幂等开通：`(tenant_id, user_id)` 冲突时更新 person_id/user_type/acted_at，
// 不新增第二行（验收 #6）。tenant_id/user_id 为空时**不写**（绝不落空租户行，承 DM-2/C1）。
//
// 先在调用方（identitymap）保证 tenant_id 非空；这里再兜一道，空值直接报错拒绝。
func (s *Store) UpsertControlUser(ctx context.Context, in ControlUserUpsert) error {
	if in.TenantID == "" || in.UserID == "" {
		return fmt.Errorf("store: UpsertControlUser tenant_id/user_id 不得为空")
	}
	userType := in.UserType
	if userType == "" {
		userType = model.ControlUserTypeNormal
	}
	row := model.ControlUserEntity{
		TenantID:       in.TenantID,
		UserID:         in.UserID,
		PersonID:       in.PersonID,
		UserType:       userType,
		StatusSnapshot: "enable",
		ActedAt:        in.ActedAt,
		CreatedAt:      in.CreatedAt,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"person_id", "user_type", "acted_at"}),
	}).Create(&row).Error
}

// GetControlUser 按 `(tenant_id, user_id)` 读映射行（读不到返回 nil, false）。
// 仅供「按 user_id 关联会话/审计」与展示；**鉴权/所有权不得依赖它**。
func (s *Store) GetControlUser(ctx context.Context, tenantID, userID string) (*model.ControlUserEntity, bool, error) {
	var row model.ControlUserEntity
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &row, true, nil
}

// ControlIdentityAuditInsert 是审计写入入参。
type ControlIdentityAuditInsert struct {
	TenantID    string
	ActorUserID string
	Action      string // login | logout | deny
	Result      string // allow | deny
	SrcIP       string
	At          int64 // Unix 毫秒
}

// InsertControlIdentityAudit 追加一条认证/登出审计。
func (s *Store) InsertControlIdentityAudit(ctx context.Context, in ControlIdentityAuditInsert) error {
	row := model.ControlIdentityAuditEntity{
		TenantID:    in.TenantID,
		ActorUserID: in.ActorUserID,
		Action:      in.Action,
		Result:      in.Result,
		SrcIP:       in.SrcIP,
		At:          in.At,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

// InsertControlLogoutJTI 记一条已处理的登出 jti 摘要（主键冲突 = 已处理，幂等）。
// 返回 inserted=true 表示首次记录；false 表示重复投递（调用方据此回 200 幂等）。
func (s *Store) InsertControlLogoutJTI(ctx context.Context, jtiHash string, expiresAt, createdAt int64) (bool, error) {
	row := model.ControlLogoutJTIEntity{JTIHash: jtiHash, ExpiresAt: expiresAt, CreatedAt: createdAt}
	tx := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if tx.Error != nil {
		return false, tx.Error
	}
	return tx.RowsAffected == 1, nil
}

// HasControlLogoutJTI 报告某 jti 摘要是否存在且未过期（SLO 去重查询）。
func (s *Store) HasControlLogoutJTI(ctx context.Context, jtiHash string, nowMilli int64) (bool, error) {
	if jtiHash == "" {
		return false, nil
	}
	var count int64
	err := s.db.WithContext(ctx).Model(&model.ControlLogoutJTIEntity{}).
		Where("jti_hash = ? AND expires_at > ?", jtiHash, nowMilli).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
