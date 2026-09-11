package manager

// manager 测试夹具的唯一建造点。
//
// 此前包内有三个各自拼装的脚手架（newTestRig / testReapHarness / newSearchRig），
// 外加约二十处测试里手写的 `NewService(...) + NewOrchestrator(...) + t.Cleanup(orch.Close)`
// 组合。它们的差异其实只有四个开关：注册表装哪些 runtime、要不要事件总线、要不要 sqlite
// 库、预置哪些 agent。散在多处时，"关闭顺序"这类全局约定（编排器必须先于库关闭）就没人
// 负责，加一个资源也容易只在某一个 rig 里登记清理。
//
// 这里收成一个 newRig + 三个具名预设：**登记清理与接线只有一处**。三个预设保留原签名，
// 让既有调用点零改动——预设名本身也是意图文档（"这是跑 reaper 的架子"）。
//
// 为什么组合留在包内、而 internal/testutil 只放通用夹具：manager 的测试是内部测试包
// （`package manager`），内部测试包不能 import 一个反向 import manager 的包
// （`import cycle not allowed in test`）。详见 internal/testutil 的包注释。

import (
	"context"
	"testing"

	"github.com/morehao/ark-forge/internal/dto/dtoagent"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// rigConfig 夹具的差异面（零值即"最裸的架子"：空注册表、无总线、无库、无 agent）。
type rigConfig struct {
	echo   bool        // 注册表装 echo runtime（false = 空注册表）
	bus    bool        // 建事件总线（false = 传 nil，纯列表/纯状态用例不需要它）
	store  bool        // 开临时 sqlite 库并 EnableStore
	agents []*AgentRec // 直接注入的 agent 记录（绕过 CreateAgent 的门槛校验）
}

// rig 一次夹具建造的全部产物。用结构体而不是多返回值：预设各自只取需要的字段，
// 而新增一个产物（比如 transcript 目录）不会波及任何调用点。
type rig struct {
	Svc  *Service
	Orch *Orchestrator
	Bus  *eventbus.Bus
	St   *store.Store
}

// newRig 建造夹具：注册表 → 总线 → Service → （可选库）→ agent → Orchestrator，并登记清理。
//
// **清理顺序是这里唯一的全局约定，且靠 t.Cleanup 的后进先出表达**：
// Orchestrator 的清理最后登记、最先执行，库的清理最先登记、最后执行——先停编排器再关库，
// 避免在途写库落到已关闭的句柄上。原先三个脚手架各自手写这个顺序，漏一个就是偶发报错。
func newRig(t *testing.T, mutate ...func(*rigConfig)) *rig {
	t.Helper()
	cfg := rigConfig{}
	for _, m := range mutate {
		m(&cfg)
	}

	reg := runtime.NewRegistry()
	if cfg.echo {
		reg = testutil.EchoRegistry()
	}
	var bus *eventbus.Bus
	if cfg.bus {
		bus = eventbus.New()
	}

	svc := NewService(reg, bus)

	var st *store.Store
	if cfg.store {
		st = testutil.OpenStore(t, testutil.TempDBPath(t))
		svc.EnableStore(st)
		t.Cleanup(func() { _ = st.Close() })
	}
	for _, a := range cfg.agents {
		svc.agents[a.ID] = a
	}

	// NewOrchestrator 不读 svc.agents（只存 svc/reg、建管理器和挂回调），
	// 故 agent 在它之前注入与之后注入等价。
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)

	return &rig{Svc: svc, Orch: orch, Bus: bus, St: st}
}

// newTestRig 最裸的架子：空注册表 + 事件总线，无库、无 agent。
//
// 空注册表是有意的——用它的用例都不需要真的拉起 runtime（列表/状态/权限/transcript 等
// 面），注册表里多一个 spec 反而会让"解析不到 runtime 时怎么办"这类断言失去意义。
func newTestRig(t *testing.T) (*Service, *Orchestrator, *eventbus.Bus) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.bus = true })
	return r.Svc, r.Orch, r.Bus
}

// testReapHarness 跑孤儿回收/空闲回收的架子：echo 注册表 + 一个名为 alias-echo 的 agent。
// 无事件总线（回收路径不上报事件，只落 agent 诊断）。
//
// CreateAgent 的失败此前被 `_, _ =` 吞掉：那样一旦创建因任何原因失败，回收测试会退化成
// 在"没有 agent"的空架子上跑，还可能因为路径没变而照样绿。这里改为失败即终止。
func testReapHarness(t *testing.T) (*Orchestrator, *Service) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) { c.echo = true })
	if _, err := r.Svc.CreateAgent(context.Background(), dtoagent.AgentCreateReq{
		ID: "alias-echo", Runtimes: []string{"echo"},
	}); err != nil {
		t.Fatalf("reaper 架子创建 alias-echo 失败: %v", err)
	}
	return r.Orch, r.Svc
}

// newSearchRig 跑跨会话检索/导出的架子：echo 注册表 + 事件总线 + 临时 sqlite 库
// + 预置 agent a1（直接注入 svc.agents，跳过 CreateAgent）。
//
// 总线与库都要：消息读模型的物化是「域事件 → store」这条链的产物，缺任一端都验不到真东西。
func newSearchRig(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) {
		c.echo, c.bus, c.store = true, true, true
		c.agents = []*AgentRec{{ID: "a1", Name: "A1", Runtimes: []string{"echo"}}}
	})
	return r.Svc, r.St
}
