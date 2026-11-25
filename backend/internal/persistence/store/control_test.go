package store

import (
	"testing"
)

// TestControlUserUpsertIdempotent 验证 P1 验收 #6：同 (tenant_id, user_id) 重复登录只一行。
func TestControlUserUpsertIdempotent(t *testing.T) {
	st := openTestDB(t)
	ctx := ctxT()

	if err := st.EnsureControlTenant(ctx, ControlTenantUpsert{TenantID: "t1", NowMilli: 1000}); err != nil {
		t.Fatalf("ensure tenant: %v", err)
	}
	first := ControlUserUpsert{TenantID: "t1", UserID: "u1", PersonID: "p1", UserType: "normal", ActedAt: 1000, CreatedAt: 1000}
	if err := st.UpsertControlUser(ctx, first); err != nil {
		t.Fatalf("首次 upsert: %v", err)
	}
	// 第二次（不同 acted_at / person_id）应更新而非新增。
	second := ControlUserUpsert{TenantID: "t1", UserID: "u1", PersonID: "p1b", UserType: "normal", ActedAt: 2000, CreatedAt: 9999}
	if err := st.UpsertControlUser(ctx, second); err != nil {
		t.Fatalf("二次 upsert: %v", err)
	}

	var count int64
	if err := st.db.WithContext(ctx).Table("control_users").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("幂等开通应只有 1 行，得到 %d", count)
	}
	row, ok, err := st.GetControlUser(ctx, "t1", "u1")
	if err != nil || !ok {
		t.Fatalf("读取失败 ok=%v err=%v", ok, err)
	}
	if row.PersonID != "p1b" || row.ActedAt != 2000 {
		t.Fatalf("冲突应更新 person_id/acted_at，得到 %+v", row)
	}
	if row.CreatedAt != 1000 {
		t.Fatalf("created_at 不应被二次 upsert 覆盖，得到 %d", row.CreatedAt)
	}
}

// TestControlUserUpsertRejectsEmpty 验证空 tenant_id/user_id 拒绝（不落空租户行，DM-2/C1）。
func TestControlUserUpsertRejectsEmpty(t *testing.T) {
	st := openTestDB(t)
	ctx := ctxT()
	if err := st.UpsertControlUser(ctx, ControlUserUpsert{TenantID: "", UserID: "u1"}); err == nil {
		t.Fatalf("空 tenant_id 应拒绝")
	}
	if err := st.UpsertControlUser(ctx, ControlUserUpsert{TenantID: "t1", UserID: ""}); err == nil {
		t.Fatalf("空 user_id 应拒绝")
	}
}

// TestEnsureControlTenantKeepsConfig 验证 ensure 只更新 updated_at、不覆盖 isolation/quota。
func TestEnsureControlTenantKeepsConfig(t *testing.T) {
	st := openTestDB(t)
	ctx := ctxT()
	if err := st.EnsureControlTenant(ctx, ControlTenantUpsert{TenantID: "t1", NowMilli: 1000}); err != nil {
		t.Fatalf("首次 ensure: %v", err)
	}
	// 运维把隔离档改成 os_user + 配额。
	if err := st.db.WithContext(ctx).Exec(
		`UPDATE control_tenants SET isolation_level='os_user', quota_cpu=8 WHERE tenant_id='t1'`).Error; err != nil {
		t.Fatalf("运维改配置: %v", err)
	}
	// 再次 ensure（登录触发）不应覆盖配置。
	if err := st.EnsureControlTenant(ctx, ControlTenantUpsert{TenantID: "t1", NowMilli: 2000}); err != nil {
		t.Fatalf("二次 ensure: %v", err)
	}
	var iso string
	var cpu int64
	if err := st.db.WithContext(ctx).Raw(`SELECT isolation_level, quota_cpu FROM control_tenants WHERE tenant_id='t1'`).
		Row().Scan(&iso, &cpu); err != nil {
		t.Fatalf("读回: %v", err)
	}
	if iso != "os_user" || cpu != 8 {
		t.Fatalf("ensure 不应覆盖运维配置，得到 iso=%s cpu=%d", iso, cpu)
	}
}

// TestInsertControlLogoutJTIDedup 验证 jti 去重幂等（DM-5）。
func TestInsertControlLogoutJTIDedup(t *testing.T) {
	st := openTestDB(t)
	ctx := ctxT()
	inserted, err := st.InsertControlLogoutJTI(ctx, "hash-abc", 5000, 1000)
	if err != nil || !inserted {
		t.Fatalf("首次应 inserted=true，got inserted=%v err=%v", inserted, err)
	}
	inserted, err = st.InsertControlLogoutJTI(ctx, "hash-abc", 5000, 1001)
	if err != nil {
		t.Fatalf("重复插入 err: %v", err)
	}
	if inserted {
		t.Fatalf("重复 jti 应 inserted=false（幂等）")
	}
}
