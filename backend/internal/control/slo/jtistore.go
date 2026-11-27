package slo

import (
	"context"
	"time"

	"github.com/morehao/ark-iam/sdk/rp/logout"
)

// StoreJTI 是 jti 落表所需的持久层窄端口（生产传 *store.Store）。
type StoreJTI interface {
	// InsertControlLogoutJTI 记一条已处理 jti 摘要；inserted=false 表示重复（已处理）。
	InsertControlLogoutJTI(ctx context.Context, jtiHash string, expiresAt, createdAt int64) (bool, error)
	// HasControlLogoutJTI 报告该摘要是否存在且未过期（去重查询）。
	HasControlLogoutJTI(ctx context.Context, jtiHash string, nowMilli int64) (bool, error)
}

// DBJTIStore 把 ark-iam receiver 的 JTIStore 接口落到 `control_logout_jtis` 表（多副本共享去重，DM-5）。
type DBJTIStore struct {
	st  StoreJTI
	now func() time.Time
}

// NewDBJTIStore 构造落表 JTIStore。now 为 nil 用 time.Now。
func NewDBJTIStore(st StoreJTI, now func() time.Time) *DBJTIStore {
	if now == nil {
		now = time.Now
	}
	return &DBJTIStore{st: st, now: now}
}

// Seen 实现 logout.JTIStore：查摘要是否已记录且未过期。
func (s *DBJTIStore) Seen(ctx context.Context, jti string) bool {
	if s == nil || s.st == nil || jti == "" {
		return false
	}
	ok, err := s.st.HasControlLogoutJTI(ctx, logout.HashJTI(jti), s.now().UnixMilli())
	if err != nil {
		// 查询失败按「未见过」处理（留给 Record 的 upsert 去重；宁可多处理一次也不误判已登出）。
		return false
	}
	return ok
}

// Record 实现 logout.JTIStore：落一条摘要（主键冲突 = 已处理，幂等）。
func (s *DBJTIStore) Record(ctx context.Context, jti string, expiresAt time.Time) error {
	if s == nil || s.st == nil || jti == "" {
		return nil
	}
	_, err := s.st.InsertControlLogoutJTI(ctx, logout.HashJTI(jti), expiresAt.UnixMilli(), s.now().UnixMilli())
	return err
}
