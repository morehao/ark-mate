package apppkg

import (
	"sort"

	"github.com/morehao/ark-mate/internal/core/appslot"
)

// ——— 运行期 impl 来源对接（WP-C2，把 manifest.provides.impls 适配成 appslot.ImplSource）———
//
// apppkg 描述「一个包**提供**什么」（manifest），appslot 描述「impl 白名单来源」的抽象。
// 本文件是两者之间的**单向桥**：把已装载包的 `Manifest.Provides.Impls[]{impl_id,kind}` 映射成
// `appslot.RuntimeImplSpec`，构造运行期 ImplSource。**appslot 不 import apppkg**（appslot 在最内层），
// 依赖方向只有 apppkg → appslot，无环。
//
// 分工：本文件只负责「清单 → 来源声明」的映射与合并装载时冲突裁决的准备；运行期 impl 的
// **执行**接 C3 的 host 子进程、**Reconcile 接线**接 C5，二者都不在本 WP。

// RuntimeImplSpecs 把一组已装载包各自的 impl 声明摊平成 appslot 侧来源声明列表。
//
// 每个 impl 带上它的 PackageID（= 包 manifest 的 ID），供 appslot 的来源路由（RouteByApps）判断
// 「这个 impl 属于哪个包目录 app」。返回按 impl_id 升序稳定排列。
func RuntimeImplSpecs(pkgs []*Package) []appslot.RuntimeImplSpec {
	out := make([]appslot.RuntimeImplSpec, 0)
	for _, p := range pkgs {
		if p == nil || p.Manifest == nil {
			continue
		}
		pkgID := p.Manifest.ID
		for _, im := range p.Manifest.Provides.Impls {
			out = append(out, appslot.RuntimeImplSpec{
				ImplID:    im.ImplID,
				Kind:      im.Kind,
				PackageID: pkgID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImplID < out[j].ImplID })
	return out
}

// RuntimeSource 从一组已装载包构造**运行期 impl 来源**（appslot.ImplSource）。
//
// 白名单来自每个包的 manifest `provides.impls`——「已装包清单」这条来源的落点（§17.4）。
// 同一 impl_id 被多个包重复声明会经 appslot.NewRuntimeSource panic（配置事故，装载期即炸）。
// 空集合 → 合法的空来源（没有装任何提供 impl 的包，白名单判定只回落到编译期）。
func RuntimeSource(pkgs []*Package) appslot.ImplSource {
	return appslot.NewRuntimeSource(RuntimeImplSpecs(pkgs))
}

// CombinedImplSource 是装配期便捷构造：把编译期来源（appslot.CompiledSource，A+Slot 内置 app）
// 与「这组已装载包」的运行期来源并列成一个来源（compiled ∪ runtime）。
//
// 白名单判定（svcapp / Reconcile / WP-C5）用它的 `Has(id)`：任一来源在册即合法——白名单从
// 「仅二进制」扩为「二进制 ∪ 已装包清单」，但仍是封闭表（不退化服务定位图）。
// 编译期优先（内置 app 实现已在二进制里可直接执行）。
func CombinedImplSource(pkgs []*Package) appslot.ImplSource {
	return appslot.CombinedSource(appslot.CompiledSource(), RuntimeSource(pkgs))
}
