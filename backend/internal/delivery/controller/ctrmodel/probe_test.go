package ctrmodel

import (
	"context"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/infra/dsh"

	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/drivers/mechanism/credential"
	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	"github.com/morehao/ark-mate/internal/service/svcmodel"
)

// ——— 自检用例的**接线判据**（实例由装配根造，控制器只收接口） ———
//
// 用例本身的判据在 `internal/service/svcmodel/probe_test.go`（零出网 / 上游四结论 / 目录
// 未知档 / redact 闸门），那里拿的是两个窄端口。本文件验的是**控制器有没有把它们接上**：
//
//	① `NewProviderCtr` 必须把**两个**用例都接上（`actions == nil` 的表现是 `/providers` 四条
//	   路由 nil panic，`probe == nil` 的表现是 `POST /providers/:id/probe` 直接 500）；
//	② 接进来的用例必须**用的是交进来的那份域平面**（空平面探一次，第一条结论必然说"不在册"）。
//
// 为什么这条判据值得存在：v3.1 起用例实例只在装配根造、控制器只收接口——"谁造"换地方时最
// 容易出的错不是编译失败（少一个入参就会），而是**接错事实**（把别的平面接进来、或两个都传
// nil：前者读的是另一份状态，后者静默变成"自检永远说不知道"）。

// fakeProviderCatalog 模型目录读面替身（`svcmodel.Catalog`）。
type fakeProviderCatalog struct{ called bool }

func (f *fakeProviderCatalog) ModelCatalog(context.Context) (*provider.CatalogView, error) {
	f.called = true
	return &provider.CatalogView{}, nil
}

// TestNewProviderCtrKeepsBothUseCases 见文件头。
//
// 构造函数收两个**已构造**的用例；这里交一个**真**域平面（空档案）造出它们：
//
//	① 两个字段都必须被接上（nil 的表现分别是 nil panic 与端点 500）；
//	② 自检必须**读的是交进来的那份平面**——空平面的第一条结论必然是 `exists` 的 fail，
//	   文案里带 id。把平面接错（或忘了接）在这里就会现形，而"编译通过 + 档案面照常工作"看不见它。
func TestNewProviderCtrKeepsBothUseCases(t *testing.T) {
	reg := dsh.Registry()
	plane := provider.NewPlane(reg, registry.NewProviderSecrets(credential.New(reg)))
	plane.SetProviderDir(t.TempDir())
	ctr := NewProviderCtr(svcmodel.NewProvider(plane), svcmodel.NewProbe(plane, &fakeProviderCatalog{}))

	pc, ok := ctr.(*providerCtr)
	if !ok {
		t.Fatalf("NewProviderCtr 交出的类型不符: %T", ctr)
	}
	if pc.actions == nil {
		t.Fatal("档案用例没被造出来（`actions` 为 nil）：/providers 四条路由会 nil panic")
	}
	if pc.probe == nil {
		t.Fatal("自检用例没被造出来（`probe` 为 nil）：POST /providers/:id/probe 会直接 500")
	}

	resp, err := pc.probe.ProbeProvider(context.Background(), "p1", dtomodel.ProviderProbeReq{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(resp.Checks) == 0 {
		t.Fatal("自检一条结论都没有：接进来的平面没被用上")
	}
	if first := resp.Checks[0]; first.Name != "exists" || first.Status != "fail" || !strings.Contains(first.Detail, "p1") {
		t.Fatalf("空平面的第一条结论应是 exists 的 fail（带 id），got %+v", first)
	}
}
