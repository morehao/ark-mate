/**
 * 应用启动（M2-4）：**先过版本闸门，再挂载界面**。
 *
 * 顺序是有意的：闸门不过时不该把应用树拉起来（拉起来就会去拉数据、连事件流，
 * 然后在"版本不对"的根因上刷一堆衍生错误）。闸门过了才 createRoot。
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { AppShell } from '@ark-mate/ui'
import { listApps } from '@ark-mate/api'
import { loadCapabilityFacts } from './capabilityFacts'
import { applyAppPagesFromApps, clearAppPages, resolveAppPages } from './console/appRegistry'
import { deriveRuntimeAppPages, setRuntimeAppPages } from './console/runtimeAppPages'
import { watchDevAssets } from './transport/assetWatch'
import { renderVersionMismatch, runVersionGate } from './versionGate'

/**
 * 从 `/apps` 读应用定义，把带 `page` 的 app 补登进统一注册表（`console/appRegistry.ts`）。
 *
 * 纪律（设计 §2.2「封闭静态包」）：只登记页面**元数据**（route/nav/组件名），渲染按组件名
 * 查编译期组件映射表——绝不按接口字符串动态 import。拉不到 /apps 不抛（只少 app 页）。
 * 先清旧动态页再补，避免热重载/重拉时累积幽灵入口（内置页不受影响）。
 */
async function loadAppPages(): Promise<void> {
  try {
    const apps = await listApps()
    clearAppPages()
    applyAppPagesFromApps(apps)
    // 二期 C：把带 page + frame_token 的 app 派生为**运行期沙箱页**，与内置路由做冲突检测
    // （fail-closed 让位 + 告警）。必须先跑完 A+Slot 的 applyAppPagesFromApps——内置路由集
    // 取自它的 resolveAppPages()。运行期页写入独立注册表，App 挂载时被 navView/app.tsx 读取。
    const builtinRoutes = resolveAppPages().map((def) => def.route)
    const { pages, dropped } = deriveRuntimeAppPages(apps, builtinRoutes)
    for (const d of dropped) {
      // 「让位 + 告警」：冲突/缺 token 的运行期页被丢弃时留可观测痕迹（不静默吞掉）。
      console.warn(`[runtimeAppPages] 放弃运行期页 ${d.appId}（${d.route}）：${d.reason}`)
    }
    setRuntimeAppPages(pages)
  } catch (err) {
    // 失败不阻断：内置页照常，动态 app 页缺失即可（不把整个界面挂掉）。
    console.warn('[appRegistry] 读取 /apps 失败，动态应用页本次不补登：', err)
  }
}

export async function bootstrap(opts: { webBuildId: string }): Promise<void> {
  const root = document.getElementById('root')
  if (!root) throw new Error('缺少 #root 挂载点')

  const gate = await runVersionGate(opts.webBuildId)
  if (gate.unverifiable) {
    // 无法判定：放行 + 留痕（核心里没有 build-id.txt 是合法状态，不该把界面锁死）。
    console.warn(`[version-gate] 无法校验前端与核心版本是否一致：${gate.unverifiable}`)
  }
  if (!gate.ok && gate.mismatch) {
    // 混搭：整屏状态页（设计 §6.7），不是白屏、不是零散接口报错。
    console.error(`[version-gate] 版本不一致：前端 ${gate.mismatch.web} / 核心 ${gate.mismatch.core}`)
    renderVersionMismatch(root, gate.mismatch.web, gate.mismatch.core)
    return
  }

  // 能力声明（`/capabilities`）在挂载界面前读一次：目录选择器的形态是**服务端声明**的
  //   （宿主能不能抬系统面板）——挂载后再读会让"第一次点选目录"落在错的那条路上
  //   （本机浏览器本该用系统面板却开了应用内对话框）。
  // 失败不阻断：拉不到就按 UNKNOWN 走（原生面板不可用 → 应用内浏览）。
  await loadCapabilityFacts()

  // 应用页面入口：从 /apps 读带 page 的 app，把**页面元数据**补登进统一注册表。
  // 必须在 `import('./app')` **之前**完成——`appRegistry` 与 `navView` 的 `NAV_ENTRIES`
  // 在模块加载时即从注册表派生，晚于加载补登的页不会出现在左栏/路由里。
  // 失败不阻断：拉不到 /apps 就只少 app 页，内置页照常（界面不该因为一份列表而少一块）。
  await loadAppPages()

  // 注册表补登完成后再动态引入应用树（含 navView / app.tsx 的派生），保证同源派生拿到最终页集。
  const { App } = await import('./app')

  createRoot(root).render(
    <StrictMode>
      <AppShell>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AppShell>
    </StrictMode>,
  )

  // 开发期资产重建通知（P6-A）：`make dev` 下改完 UI 自动刷新，省掉手动那一次。
  // 放在挂载**之后**：通道不存在时它什么都不做（交付形态的每秒路径），存在时也只是等帧；
  // 先订阅的话，一次"刚好在挂载时到达"的 rebuild 会让刷新与首次渲染打架。
  watchDevAssets()
}
