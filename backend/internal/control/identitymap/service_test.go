package identitymap

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// fakeStore 记录收到的调用，验证编排行为（不碰真库；真库路径由 store 包测试覆盖）。
type fakeStore struct {
	ensureTenantCalls []store.ControlTenantUpsert
	upsertUserCalls   []store.ControlUserUpsert
}

func (f *fakeStore) EnsureControlTenant(_ context.Context, in store.ControlTenantUpsert) error {
	f.ensureTenantCalls = append(f.ensureTenantCalls, in)
	return nil
}
func (f *fakeStore) UpsertControlUser(_ context.Context, in store.ControlUserUpsert) error {
	f.upsertUserCalls = append(f.upsertUserCalls, in)
	return nil
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.UnixMilli(1700000000000) }
}

// TestOnboard_PersonToken 人令牌：ensure 租户 + upsert 用户（normal，带 person_id）。
func TestOnboard_PersonToken(t *testing.T) {
	fs := &fakeStore{}
	svc := New(fs, fixedClock())
	id := &contract.Identity{TenantID: "t1", UserID: "u1", PersonID: "p1"}
	res, err := svc.Onboard(context.Background(), id)
	if err != nil {
		t.Fatalf("Onboard err: %v", err)
	}
	if !res.Onboarded || res.TenantID != "t1" || res.UserID != "u1" {
		t.Fatalf("结果不符：%+v", res)
	}
	if len(fs.ensureTenantCalls) != 1 || fs.ensureTenantCalls[0].TenantID != "t1" {
		t.Fatalf("应 ensure 一次租户：%+v", fs.ensureTenantCalls)
	}
	if len(fs.upsertUserCalls) != 1 {
		t.Fatalf("应 upsert 一次用户：%+v", fs.upsertUserCalls)
	}
	got := fs.upsertUserCalls[0]
	if got.UserType != "normal" || got.PersonID != "p1" || got.ActedAt != 1700000000000 {
		t.Fatalf("用户 upsert 入参不符：%+v", got)
	}
}

// TestOnboard_MachineToken 机器凭证：user_type=service，person_id 空。
func TestOnboard_MachineToken(t *testing.T) {
	fs := &fakeStore{}
	svc := New(fs, fixedClock())
	id := &contract.Identity{TenantID: "t1", UserID: "svc-1", IsMachine: true}
	if _, err := svc.Onboard(context.Background(), id); err != nil {
		t.Fatalf("Onboard err: %v", err)
	}
	if len(fs.upsertUserCalls) != 1 || fs.upsertUserCalls[0].UserType != "service" {
		t.Fatalf("机器凭证应为 service：%+v", fs.upsertUserCalls)
	}
}

// TestOnboard_EmptyTenantSkips 无 tenant_id：不写库（不落空租户行，DM-2/C1）。
func TestOnboard_EmptyTenantSkips(t *testing.T) {
	fs := &fakeStore{}
	svc := New(fs, fixedClock())
	res, err := svc.Onboard(context.Background(), &contract.Identity{TenantID: "", UserID: "u1"})
	if err != nil {
		t.Fatalf("Onboard err: %v", err)
	}
	if res.Onboarded {
		t.Fatalf("无 tenant_id 不应 onboard")
	}
	if len(fs.ensureTenantCalls) != 0 || len(fs.upsertUserCalls) != 0 {
		t.Fatalf("无 tenant_id 不应写库：tenant=%d user=%d", len(fs.ensureTenantCalls), len(fs.upsertUserCalls))
	}
}

// TestOnboard_NilIdentity nil 身份安全返回。
func TestOnboard_NilIdentity(t *testing.T) {
	svc := New(&fakeStore{}, fixedClock())
	res, err := svc.Onboard(context.Background(), nil)
	if err != nil || res.Onboarded {
		t.Fatalf("nil 身份应安全返回：res=%+v err=%v", res, err)
	}
}
