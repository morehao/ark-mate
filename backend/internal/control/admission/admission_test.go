package admission

import (
	"testing"

	"github.com/morehao/ark-iam/contract"
)

func TestRequireTenant(t *testing.T) {
	if err := RequireTenant(&contract.Identity{TenantID: "t1"}); err != nil {
		t.Fatalf("有 tenant_id 应通过：%v", err)
	}
	if err := RequireTenant(&contract.Identity{TenantID: ""}); err != ErrTenantScopeMissing {
		t.Fatalf("空 tenant_id 应 ErrTenantScopeMissing，得到 %v", err)
	}
	if err := RequireTenant(nil); err != ErrTenantScopeMissing {
		t.Fatalf("nil 身份应 ErrTenantScopeMissing，得到 %v", err)
	}
}

func TestOwnerMatches(t *testing.T) {
	id := &contract.Identity{TenantID: "t1", UserID: "u1"}
	if !OwnerMatches(id, "t1", "u1") {
		t.Fatalf("同租户同用户应匹配")
	}
	if OwnerMatches(id, "t2", "u1") {
		t.Fatalf("跨租户不应匹配")
	}
	if OwnerMatches(id, "t1", "u2") {
		t.Fatalf("同租户他人不应匹配")
	}
	if OwnerMatches(&contract.Identity{TenantID: "", UserID: "u1"}, "", "u1") {
		t.Fatalf("空 tenant 不应匹配")
	}
	if OwnerMatches(nil, "t1", "u1") {
		t.Fatalf("nil 身份不应匹配")
	}
}
