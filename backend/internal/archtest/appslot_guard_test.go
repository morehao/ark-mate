package archtest

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/capability"
)

// ——— WP6：App Slot 运行时 3 条守卫（设计 §5.4 / §6 R1 / G6）———
//
// 三条都盯「Slot 退化成数据驱动的服务定位图 / 关不干净 / 绕过分层」这三类**静默失效**——
// 它们不会让任何既有测试变红，只有在某个 app 装进来之后才以「调到不该调的实现」「停用后还在跑」
// 「impl 偷偷拿 service 层」的形态暴露出来。每条都配反例说明（见 guards-history 口径）。

// TestAppSlotImplWhitelisted：app 定义的 impl_id / entity.kind 必须是编译期登记在册的白名单。
//
// 「数据只选 impl，不提供 impl」是 App Slot 存在的理由：app 定义里的 slots[].impl_id 只能引用
// 一张**编译期已知**的实现表（RegisterImpl/KnownImpls），绝不能据定义里的字符串去「查/装载」
// 一个实现——否则 Slot 就退化成「数据驱动的服务定位图」，数据即代码。
//
// 判据用**行为**钉（比静态扫描更有牙齿）：构造一个引用**未登记** impl_id 的 app，喂给
// appslot.Reconcile，断言该 slot 记 FAILED 且**没有**被 apply。若 Reconcile 哪天改成「未知 impl
// 也想办法找一个来跑」，这条当场红——那正是退化的形态。同时正向断言已知 impl 能 applied，
// 保证判据不是「全 failed 也绿」。
func TestAppSlotImplWhitelisted(t *testing.T) {
	const knownID = "archtest.slot.whitelisted.known"
	var knownRan bool
	appslot.RegisterImpl(knownID, func(appslot.Ctx) error { knownRan = true; return nil })

	apps := []capability.App{
		{ID: "archtest-app-known", Slots: []capability.AppSlot{{ImplID: knownID}}},
		// 未登记的实现 id：只存在于数据里，不在 KnownImpls 白名单。
		{ID: "archtest-app-unknown", Slots: []capability.AppSlot{{ImplID: "archtest.slot.whitelisted.NOT_REGISTERED"}}},
	}
	results, dispose := appslot.Reconcile(apps)
	defer dispose()

	byID := map[string]appslot.SlotResult{}
	for _, r := range results {
		byID[r.Slot.ImplID] = r
	}

	// 正向：在册 impl 真的被 apply（防止「什么都不 apply」也蒙混过关）。
	if byID[knownID].Status != appslot.StatusApplied || !knownRan {
		t.Fatalf("在册 impl 应 applied 且真跑过，got status=%v ran=%v", byID[knownID].Status, knownRan)
	}
	// 反向（牙齿）：未在册 impl 必须 FAILED——数据不能凭空指定一个实现。
	unk, ok := byID["archtest.slot.whitelisted.NOT_REGISTERED"]
	if !ok {
		t.Fatalf("未知 impl 的 slot 未出现在结果里——Reconcile 是否把它静默丢弃/自造了实现？")
	}
	if unk.Status != appslot.StatusFailed {
		t.Fatalf("未在册 impl_id 必须 FAILED（白名单底线，防退化成服务定位图），got %v", unk.Status)
	}
	// 白名单快照是只读的：KnownImpls 返回值不能反过来影响注册表（防开放表）。
	if got := appslot.KnownImpls()[knownID]; got == nil {
		t.Fatalf("KnownImpls 快照应含已登记 %q", knownID)
	}
}

// TestAppSlotDisposeOnOff：app 停用 → OnDispose **逆序**撤销（后 apply 的先撤），G6「关得干净」。
//
// 撤销顺序错了的表现是「被依赖的先被撤掉，依赖它的那个 dispose 时撞空」——只在那一条 app 的
// 停机路径上偶发，别处全绿。判据用行为钉：两个 impl 依赖链，Reconcile 后调 disposer，断言
// 撤销顺序 == apply 逆序。把 disposeAll 改成正序执行 → 红。
func TestAppSlotDisposeOnOff(t *testing.T) {
	var disposed []string
	// base 被 dep 依赖：apply 顺序 base → dep；撤销应 dep → base。
	appslot.RegisterImpl("archtest.slot.off.base", func(c appslot.Ctx) error {
		c.OnDispose(func() { disposed = append(disposed, "base") })
		return nil
	})
	appslot.RegisterImpl("archtest.slot.off.dep", func(c appslot.Ctx) error {
		c.OnDispose(func() { disposed = append(disposed, "dep") })
		return nil
	})

	// Requires 不在 app 定义里，走 Slot 拓扑；这里直接验证 Reconcile 全链路：app 两个 slot，
	// 定义顺序故意 dep 在前，靠 topoOrder 需 Requires——app 定义不带 Requires，故 apply 顺序按 id
	// 稳定排序（base < dep），撤销即 dep → base。这与「后 apply 的先撤」等价。
	apps := []capability.App{
		{ID: "archtest-app-off", Slots: []capability.AppSlot{
			{ImplID: "archtest.slot.off.base"},
			{ImplID: "archtest.slot.off.dep"},
		}},
	}
	results, dispose := appslot.Reconcile(apps)
	for _, r := range results {
		if r.Status != appslot.StatusApplied {
			t.Fatalf("应全部 applied，got %s(%v)", r.Slot.ImplID, r.Err)
		}
	}
	if len(disposed) != 0 {
		t.Fatalf("未停用前不应撤销，got %v", disposed)
	}
	dispose() // app 停用
	if !reflect.DeepEqual(disposed, []string{"dep", "base"}) {
		t.Fatalf("停用后应逆序撤销（dep 先于 base），got %v", disposed)
	}
	// 幂等：再撤一次不重复（disposeAll 清空栈）。
	dispose()
	if !reflect.DeepEqual(disposed, []string{"dep", "base"}) {
		t.Fatalf("dispose 应幂等，二次调用后 got %v", disposed)
	}
}

// TestAppSlotStaysAtLeaf：impl 只能依赖 Core / 已 Provide 的 service，不得直接 import
// `service/svc*`（也不得 import `domain` 具体实现）——R1「Slot 不得绕过 service 层拿实现」。
//
// 静态判据：扫 `internal/core/appslot` 生产源码的 import。appslot 是最内层扩展点契约，
// 它认识 `core/*`（值层/契约）是应当的；但若某天有人让 impl 直接 import 一个 `service/svc*`
// 或 `domain` 具体域去「顺手拿点业务逻辑」，分层就被从 Slot 这个后门绕过了，而所有既有守卫
// （层表只看方向、不看角色）不会因此变红。fail-closed：一个 appslot 源文件都扫不到即失败，
// 防目录改名后静默空转。
func TestAppSlotStaysAtLeaf(t *testing.T) {
	dir := filepath.Join(backendRoot(t), "internal", "core", "appslot")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("扫 %s: %v", dir, err)
	}
	forbidden := []string{
		"internal/service/",     // 用例层：impl 不得直接持有 svc*
		"internal/controller/",  // 入站适配层
		"internal/router/",      // 入站适配层
		"internal/persistence/", // 持久化：impl 只认端口传入的服务
		"internal/domain/",      // 域具体实现（Core 值对象已在 core/*，域实现属越界）
	}
	scanned := 0
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, fb := range forbidden {
				if strings.Contains(p, fb) {
					t.Errorf("%s import 了 %s —— App Slot（impl）只能依赖 Core / 已 Provide 的服务，"+
						"不得直接 import %s（R1：Slot 不得绕过 service 层从前门拿实现，而这里是从后门）", path, p, fb)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("一个 appslot 源文件都没扫到——internal/core/appslot 目录改名了？（守卫空转）")
	}
}
