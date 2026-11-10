package assembly

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/golib/glog"
)

// ——— App Slot 运行时汇总（一期 WP6 + 二期 C WP-C5）———
//
// 装配根在启动（以及将来刷新）时把「当前启用的 app 定义」交给 `appslot.Reconcile`：
// 按 slots[].impl_id 到白名单查实现 → 依赖排序 → apply（失败隔离）→ 得到一棵撤销栈；
// app 停用 / 进程关闭时按**逆序**撤销（设计 §5.4 / G6「关得干净」）。
//
// **二期 C 起白名单扩为「编译期 ∪ 运行期」两个来源（§17.4）**：装配根把
// `CombinedSource(编译期 KnownImpls, 运行期已装包清单)` 喂给来源感知的 Reconcile；运行期包 app 的
// slot，其 impl 的执行经 C3 的 `ImplRunner`→`Host.ApplySlot` RPC（在内核调包内代码这条路类型上不存在），
// 撤销经 `DisposeSlot` RPC 精确撤销。依赖排序 / 失败隔离 / OnDispose 逆序撤销语义**完全不变**（§17.1）。
//
// **只做静态字段追加，形状不变式不改**（设计 §6 边界）：本步只新增一个启动动作与一个撤销栈
// 字段，不改任何既有的对象图形状。

// enabledAppsForReconcile 返回本次要 reconcile 的已启用 app 列表（读 svcapp 的真实来源，过滤启用）。
//
// svcapp 未就绪（as.services.App 为 nil）→ 返回 nil（Reconcile 空跑 = 零 slot，符合「无 app = 零
// 行为」的默认态）。数据从 `ListApps` 读出后按 `IsEnabled()`（Enabled bool）过滤，再转成 capability.App。
func (as *assembly) enabledAppsForReconcile(ctx context.Context) []capability.App {
	svc := as.services.App
	if svc == nil {
		return nil
	}
	resps, err := svc.ListApps(ctx)
	if err != nil {
		glog.Warnw(ctx, "serve: 读取 app 定义失败，本次 App Slot 空跑", "err", err)
		return nil
	}
	apps := make([]capability.App, 0, len(resps))
	for i := range resps {
		r := &resps[i]
		if !r.Enabled {
			continue
		}
		apps = append(apps, capability.App{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Avatar:      r.Avatar,
			Tags:        r.Tags,
			ToolDomains: r.ToolDomains,
			Slots:       appSlotsFromResp(r.Slots),
			APIVersion:  r.APIVersion,
			Enabled:     boolPtr(true),
		})
	}
	return apps
}

// appSlotsFromResp 把 dto 的 slots 转成 capability.AppSlot（一期 AppSlot 只带 ImplID）。
func appSlotsFromResp(in []dtoapp.AppSlotResp) []capability.AppSlot {
	if len(in) == 0 {
		return nil
	}
	out := make([]capability.AppSlot, 0, len(in))
	for _, s := range in {
		out = append(out, capability.AppSlot{ImplID: s.ImplID})
	}
	return out
}

// boolPtr 返回指向 b 的指针（Enabled 用指针表达「未声明 = 启用」；这里显式给 true，已过滤过禁用）。
func boolPtr(b bool) *bool { return &b }

// buildImplResolver 构造本次 Reconcile 的来源感知 resolver：把「编译期 KnownImpls ∪ 运行期已装包清单」
// 合并成来源，并为每个在册 impl 提供执行函数与精确撤销：
//   - 运行期包 app（来自 apppkg 目录、带 manifest）的 impl：exec = ImplRunner.Impl(implID)（Host.ApplySlot
//     RPC 封装），dispose = ImplRunner.DisposeImpl(implID)（DisposeSlot RPC 精确撤销）；
//   - 编译期内置 app 的 impl：exec = KnownImpls 里的真函数（已在二进制里可直接执行），dispose 空操作
//     （其撤销走 Ctx.OnDispose，由 runtimeCtx 逆序执行，语义与一期一致）。
//
// **按 app 来源路由**（T-C2.2）：先用 appslot.RouteByApps 结合「哪些 app 来自包目录」判定 impl 属运行期
// 还是编译期，再给对应来源填执行函数。未装任何包 / 无 host → 运行期来源为空，execs 只有编译期 impl，
// 与一期行为一致（A+Slot 不回归）。
func (as *assembly) buildImplResolver(ctx context.Context, pkgs []*apppkg.Package, hosts map[string]*apphost.Host) appslot.ImplResolver {
	compiled := appslot.CompiledSource()
	runtime := apppkg.RuntimeSource(pkgs)
	combined := appslot.CombinedSource(compiled, runtime)

	// 哪些 app 来自包目录（带 manifest）——用于按 app 归属路由 impl 到运行期来源。
	loadedAppIDs := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		if p != nil && p.Manifest != nil {
			loadedAppIDs[p.Manifest.ID] = true
		}
	}

	execs := make(map[string]appslot.Impl)
	disposers := make(map[string]func() error)

	// 编译期 impl：真函数已在 KnownImpls 里，可直接执行（撤销走 Ctx.OnDispose，无需 disposer）。
	for id, fn := range appslot.KnownImpls() {
		execs[id] = fn
	}

	// 运行期 impl：执行/撤销都经 app-host 子进程 RPC（ImplRunner 包成 appslot.Impl）。
	// 逐包构造 runner（每包一个 Host，崩溃隔离彻底）；仅当该包 Host 就绪才接执行函数。
	for _, p := range pkgs {
		if p == nil || p.Manifest == nil {
			continue
		}
		host := hosts[p.Manifest.ID]
		if host == nil {
			glog.Warnw(ctx, "serve: 运行期包无可用 host，其 impl 不可执行（该 slot 将记 FAILED）", "pkg", p.Manifest.ID)
			continue
		}
		runner := apphost.NewImplRunner(host)
		for _, im := range p.Manifest.Provides.Impls {
			implID := im.ImplID
			// 按 app 归属路由：只有该 impl 归属已装包 app、且来源为运行期时才走 host；编译期同名 impl（若有）
			// 已在上面填了真函数，编译期优先（与 CombinedSource.Impl 的优先级一致）。
			if kind, ok := appslot.RouteByApps(compiled, runtime, loadedAppIDs, implID); ok && kind == appslot.SourceRuntime {
				if fn := runner.Impl(implID); fn != nil {
					execs[implID] = fn
				}
				disposers[implID] = func() error { return runner.DisposeImpl(implID) }
			}
		}
	}

	return appslot.NewSourceImplResolver(combined, execs, disposers)
}

// startAppSlots 启动时 Reconcile 已启用 app 的 slots（失败隔离在 Reconcile 内部；本步非 critical）。
//
// 非 critical 的理由：单个 app 的 slot apply 失败已被 Reconcile 隔离成该 slot FAILED，
// 不该因某个 app 的钩子问题拒绝整个进程启动（与「装了一个坏 app 就全站起不来」相反）。
func (as *assembly) startAppSlots(ctx context.Context) error {
	apps := as.enabledAppsForReconcile(ctx)
	if len(apps) == 0 {
		// 无启用 app：与一期空跑一致（零 slot、零副作用），不构造 resolver/host（默认态零成本）。
		as.appSlotDispose = nil
		return nil
	}

	// 扫描本地应用包目录（C1 的 apppkg）：拿运行期包清单来源与每包 host。
	scan, err := apppkg.LoadFromDir()
	if err != nil {
		glog.Warnw(ctx, "serve: 扫描应用包目录失败，本次仅用编译期来源", "err", err)
		scan = &apppkg.ScanResult{}
	}
	hosts := as.startAppHosts(ctx, scan.Loaded)

	resolver := as.buildImplResolver(ctx, scan.Loaded, hosts)
	results, dispose := appslot.ReconcileWithResolver(collectAssemblySlots(apps), resolver)
	as.appSlotDispose = disposesThen(dispose, hosts)

	applied, failed := 0, 0
	for _, r := range results {
		if r.Status == appslot.StatusApplied {
			applied++
		} else {
			failed++
			glog.Warnw(ctx, "serve: app slot apply 失败（其它照跑）",
				"impl_id", r.Slot.ImplID, "app", r.Slot.AppID, "err", r.Err)
		}
	}
	if len(results) > 0 {
		glog.Infow(ctx, "serve: app slot 已 reconcile", "applied", applied, "failed", failed)
	}
	return nil
}

// collectAssemblySlots 把已启用 app 的 slots 展开成扁平 Slot 列表（与 appslot.collectSlots 同构；
// ReconcileWithResolver 收 Slot 列表，这里在装配根展开以保留 AppID 诊断）。
func collectAssemblySlots(apps []capability.App) []appslot.Slot {
	out := make([]appslot.Slot, 0)
	for _, a := range apps {
		for _, s := range a.Slots {
			out = append(out, appslot.Slot{ImplID: s.ImplID, AppID: a.ID})
		}
	}
	return out
}

// startAppHosts 为每个已装包拉起一个隔离子进程（每包一进程，崩溃隔离彻底），返回包 id → Host。
//
// 拉不起来（包坏了 / 二进制不可用）→ 该包无 host，其 impl 在执行时记 FAILED（失败隔离），不影响内核
// 与其它包。这一步在 C5 里只做「尽量起」——host 骨架的执行真正接线在 C4（注入真实 Handlers）；此处
// 提供的是 Reconcile 走 RuntimeSource 所需的执行通道，未注入的手艺会在 applySlot 回明确错误。
func (as *assembly) startAppHosts(ctx context.Context, pkgs []*apppkg.Package) map[string]*apphost.Host {
	hosts := make(map[string]*apphost.Host, len(pkgs))
	spawn := apphost.ExecSpawn()
	for _, p := range pkgs {
		if p == nil || p.Manifest == nil {
			continue
		}
		h := apphost.NewHost(apphost.HostConfig{
			PackageID:  p.Manifest.ID,
			PkgDir:     p.Dir,
			APIVersion: p.Manifest.APIVersion,
			Spawn:      spawn,
		})
		if err := h.Start(ctx); err != nil {
			glog.Warnw(ctx, "serve: 运行期包 host 启动失败，其 impl 不可用（失败隔离）",
				"pkg", p.Manifest.ID, "err", err)
			_ = h.Close()
			continue
		}
		hosts[p.Manifest.ID] = h
		as.appHosts = append(as.appHosts, h)
	}
	return hosts
}

// stopAppSlots 逆序撤销所有已 apply 的 slot，并关闭运行期包 host（进程关闭时；幂等）。
//
// 顺序：先跑撤销栈（运行期 impl 的 disposeSlot RPC 需要 host 仍活着 → 先撤后关），再关 host。
// appSlotDispose 已在 startAppSlots 里经 disposesThen 包好「先撤 slot 再关 host」；这里只调它一次
// （Host.Close 本身幂等，appHosts 仅用于兜底/诊断，置空即可）。
func (as *assembly) stopAppSlots(context.Context) error {
	if as.appSlotDispose != nil {
		as.appSlotDispose()
		as.appSlotDispose = nil
	}
	for _, h := range as.appHosts {
		if h != nil {
			_ = h.Close() // 幂等：disposesThen 已关过的这里空操作
		}
	}
	as.appHosts = nil
	return nil
}

// disposesThen 把「撤销栈」与「关闭 host 列表」粘成一个 disposer：先逆序撤 slot，再关 host。
// 撤销用 recover 隔离（单个 dispose panic 不阻断后续，与 runtimeCtx.disposeAll 同一口径）。
func disposesThen(dispose func(), hosts map[string]*apphost.Host) func() {
	return func() {
		defer func() { _ = recover() }()
		if dispose != nil {
			dispose()
		}
		for _, h := range hosts {
			if h != nil {
				_ = h.Close()
			}
		}
	}
}
