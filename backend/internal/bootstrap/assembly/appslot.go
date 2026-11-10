package assembly

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/golib/glog"
)

// ——— WP6：App Slot 运行时汇总 ———
//
// 装配根在启动（以及将来刷新）时把「当前启用的 app 定义」交给 `appslot.Reconcile`：
// 按 slots[].impl_id 到编译期白名单查实现 → 依赖排序 → apply（失败隔离）→ 得到一棵撤销栈；
// app 停用 / 进程关闭时按**逆序**撤销（设计 §5.4 / G6「关得干净」）。
//
// **只做静态字段追加，形状不变式不改**（设计 §6 边界）：本步只新增一个启动动作与一个撤销栈
// 字段，不改任何既有的对象图形状。
//
// ⚠ 临时接法（WP6 与 WP3 并行期）：svcapp 尚未落地，enabledApps 目前取**空列表**——Reconcile
// 空跑（零 slot，零副作用，符合「无 app = 零行为」的默认态）。Lead 合并时把 `enabledApps()`
// 换成 `as.services.App` 的 enabled app 列表即可，其余（排序/隔离/撤销）不变。

// enabledAppsForReconcile 返回本次要 reconcile 的已启用 app 列表。
//
// 现状：svcapp 未就绪，返回 nil（Reconcile 空跑 = 零 slot）。svcapp 落地后在此读
// `as.services.App.ListApps` 过滤 `IsEnabled()`——**接口不变，只换数据来源**。
func (as *assembly) enabledAppsForReconcile(ctx context.Context) []capability.App {
	// 占位：svcapp 未就绪，无启用 app。Lead 合并时接真实来源。
	_ = ctx
	return nil
}

// startAppSlots 启动时 Reconcile 已启用 app 的 slots（失败隔离在 Reconcile 内部；本步非 critical）。
//
// 非 critical 的理由：单个 app 的 slot apply 失败已被 Reconcile 隔离成该 slot FAILED，
// 不该因某个 app 的钩子问题拒绝整个进程启动（与「装了一个坏 app 就全站起不来」相反）。
func (as *assembly) startAppSlots(ctx context.Context) error {
	apps := as.enabledAppsForReconcile(ctx)
	results, dispose := appslot.Reconcile(apps)
	as.appSlotDispose = dispose
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

// stopAppSlots 逆序撤销所有已 apply 的 slot（进程关闭时；幂等）。
func (as *assembly) stopAppSlots(context.Context) error {
	if as.appSlotDispose != nil {
		as.appSlotDispose()
		as.appSlotDispose = nil
	}
	return nil
}
