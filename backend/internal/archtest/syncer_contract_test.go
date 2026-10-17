package archtest

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/registry"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestSyncerContractEmptySet 端口契约③的**机器判据**：`Set` 为空时编排层**确实调用了**
// `SyncModels`，而不是"看到空就跳过"。
//
// 为什么这条值得单独钉住：v11 的真实缺陷正是"空清单 → 什么都不做"（`MaterializeRuntimeConfig`
// 在 `len(models)==0` 时直接返回，于是**上一份**配置原样留在生效状态：写过的文件还在、注入过的
// 内联内容还在、`--patch` 参数还在）。结果是"用户删光了模型、界面上空了，底座仍按旧清单工作"。
//
// 于是「空集必撤回」这条契约的正确实现**只能在 adapter 里**：只有它知道要撤销哪些字节。
// 编排层一旦"体贴地"在空集时短路，这条契约就再也执行不到，而**外观上一切正常**——
// 这正是本条守卫存在的理由：它断言的不是"某段代码写了什么"，而是"控制流真的走到了端口"。
func TestSyncerContractEmptySet(t *testing.T) {
	reg := runtime.NewRegistry(runtime.Spec{ID: "stub", Name: "Stub"})
	stub := &recordingSyncer{}
	reg.RegisterSyncer("stub", stub)

	// 同步面在 P5a 搬进 `registry`：它只依赖注册表 + 供应商域（没有会话/编排）。
	res := registry.NewPlanes(reg).Resources
	// 没有持久层 ⇒ 声明为空 ⇒ `buildSet` 产出**空 Set**（这正是要覆盖的那条路径：
	// 清单为空 / 记忆模式 / 全新装机都可能落在这里）。
	out, err := res.SyncRuntime(context.Background(), "stub")
	if err != nil {
		t.Fatalf("空集同步不该报错（撤回是正常路径）: %v", err)
	}
	if stub.calls == 0 {
		t.Fatal("Set 为空时编排层**必须**调用 SyncModels：跳过它等于退回 v11 的\"空清单什么都不做\"" +
			"（上一份配置会原样留在生效状态），而外观上一切正常——见 pkg/resource 的契约③")
	}
	if stub.calls != 1 {
		t.Fatalf("同一次同步只该调一次端口，实际 %d 次", stub.calls)
	}
	if len(stub.last.Models) != 0 || len(stub.last.Providers) != 0 {
		t.Fatalf("这次传入的应是空集: %+v", stub.last)
	}
	if stub.last.Runtime != "stub" {
		t.Fatalf("Set.Runtime 应是注册表键 %q，实际 %q", "stub", stub.last.Runtime)
	}
	if out.Status == "" {
		t.Fatalf("空集也要有如实结论（不许回空 status）: %+v", out)
	}

	// 反证：未注册 syncer 的 runtime **不**该被调用（那是 `unsupported`，不是"调了再说"）。
	// 这条同时说明上面的计数不是"什么都会调一次"的假绿。
	reg2 := runtime.NewRegistry(runtime.Spec{ID: "bare", Name: "Bare"})
	stub2 := &recordingSyncer{}
	res2 := registry.NewPlanes(reg2).Resources
	out2, err := res2.SyncRuntime(context.Background(), "bare")
	if err != nil {
		t.Fatalf("未声明交付面不该报错: %v", err)
	}
	if stub2.calls != 0 {
		t.Fatal("没有登记 syncer 的 runtime 不该被调用")
	}
	if out2.Status != resource.StatusUnsupported {
		t.Fatalf("未声明交付面应报 unsupported，实际 %q", out2.Status)
	}
}

// recordingSyncer 一个只记账、不做任何交付的 stub `resource.Syncer`。
//
// 它能存在本身就说明「端口类型定义在 leaf 包」这条设计（ADR-2）是成立的：测试**不必**伪造
// 一份完整 `runtime.Spec`、也不必碰任何底座格式，就能把编排层的控制流钉住。
type recordingSyncer struct {
	calls int
	last  resource.Set
}

func (s *recordingSyncer) SyncModels(_ context.Context, set resource.Set) (resource.Outcome, error) {
	s.calls++
	s.last = set
	return resource.Outcome{
		Status: resource.StatusApplied,
		Where:  "stub（测试替身，不落任何字节）",
		Effect: resource.EffectNextProcess,
	}, nil
}
