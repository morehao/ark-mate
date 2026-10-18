package dsh

import "github.com/morehao/ark-mate/internal/resource"

// ——— 路由键与展示名：平台身份 → 底座命名空间的**唯一**派生点 ———
//
// 平台身份是 `providers.id`（用户起的 slug），它有三个角色：DB 主键 / REST 段 / 审计键。**第四个**
// 角色曾经是"底座配置里的路由键"（`llm-pi-ai.providers.<id>`）——那是本文件要拆掉的那一个。
//
// 为什么必须拆（不是洁癖，是一个静默的错配）：`providers` 的 dict 键**就是交付面**——它命中
// pi-ai 已装的 provider 时，那一家的端点 / 协议 / 展示名 / 模型目录会变成这条路由的**逐字段默认值**
// （`resolveEntry` 的 catalog 兜底）。于是"平台配了 A，实际按 A 的目录解析出 B"这件事**静默发生**：
// 配置里写的是平台字段，跑起来用的却是底座内置目录里那一家的端点。带保留前缀 ⇒ `catalogProvider()`
// 的精确查表恒落空 ⇒ 路由恒为平台手写 ⇒ 交付字节成为平台字段的纯函数。
//
// 前缀取 `mate-`（定案 Q1）：可读、可断言；它还会进 ACP 的不透明值
// （`["mate-deepseek-main","gpt-4o"]`），那一串在日志与诊断里人眼也要能读懂。`ark-` 与阿里云 SDK
// 生态的前缀同名、容易引起联想；哈希串则无从反推是哪条档案。
//
// 这一处派生的**唯一性**由守卫③（`TestDshRouteKeyDerivationIsSinglePoint`）钉住：`routePrefix`
// 只在定义处出现一次、`routeKey(` 的调用点恰好 3 处（`config.go` 两处 + `model_value.go` 一处）。
// 为什么非要这么紧：配置里声明的键与 ACP 模型值的第一段必须**逐字相同**，而它们分别在两个文件里
// ——只改一处就是"所有选了模型的会话立刻起不来"（`set_config_option` 找不到 provider）。
const routePrefix = "mate-"

// routeKey 把平台供应商 id 派生成底座命名空间里的**保留**路由键。
//
// 保留前缀不在 pi-ai 内置 provider 的命名空间里，所以这个键永远命不中内置目录。
func routeKey(providerID string) string { return routePrefix + providerID }

// displayNameOf 路由条目的展示名：**供应商展示名**，空则回落**路由键**。
//
// 这就是 DSH 的口径逐字（`displayName`：`Name shown by configuration surfaces; defaults to the
// route key`）。v24 之前这里写的是路由键**本身**，于是用户给供应商起的名字在交付面上**不可见**
// ——平台库里叫「内网网关 Alpha」，底座界面上却一律显示 `alpha-gw`。
//
// 回落取路由键而不是平台 id：展示名是这条路由**在底座里的名字**，回落成路由键时"配置里那个键"
// 与"界面显示的名字"是同一个串（用户对着 dsh 的界面就能找到平台里的哪一条）。计划
// §4.M2.T4.3 的判据（新增一家 `Name: ""` 的供应商，断言 `displayName` 回落 `mate-<id>`）钉的就是
// 这条。`route` 由调用方传入（而不是在这里再派生一次）——派生点只能有一个，见上面守卫③的说明。
func displayNameOf(p resource.Provider, route string) string {
	if p.Name != "" {
		return p.Name
	}
	return route
}
