import { test, expect, type Page } from '@playwright/test'

/**
 * 刀 2 · 首个 e2e 旅程：**纯前端渲染**（REST 全部 route mock，不依赖真后端/真模型/真底座）。
 *
 * 验的是"界面真的长出来了"——左栏导航、状态条、路由切换、能力清单渲染。这与手写 CDP 的
 * `ui:accept` 目标是同一类，但换成可维护的 spec + 自动等待 + trace。
 *
 * 后端契约不在这里验（那是 `smoke:e2e` 的职责，它打真服务）。所以这里所有 `/v1/mate/**`
 * 都返回固定信封 `{ code: 0, data: ... }`，让 store 能拉完首屏数据、界面进入稳定态。
 */

/** 统一的 mock：按路径给最小可用的空/样例数据，够页面渲染即可。 */
async function mockBackend(page: Page) {
  await page.route('**/v1/mate/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace(/^\/v1\/mate/, '')
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })

    // 版本闸门：webBuildId 与页面编译进的值不同也不拦（null = 无法判定 → 放行）。
    if (path === '/meta') {
      return ok({
        runtime: 'dsh',
        webBuildId: null,
        payloads: [],
      })
    }
    if (path === '/capabilities') {
      return ok({ capabilities: [], entries: {} })
    }
    if (path === '/sessions') {
      return ok([])
    }
    if (path === '/workspaces') {
      // GET /workspaces 是对象不是数组（{ items, writable }）；返回数组会让 SessionPane 读 .items.length 崩。
      return ok({ items: [], writable: false })
    }
    if (path === '/runtime/diagnostics') {
      return ok({})
    }
    if (path.startsWith('/experts')) {
      return ok([{ id: 'exp_demo', name: '演示专家', enabled: true, has_instructions: false }])
    }
    if (path.startsWith('/skills')) {
      return ok([])
    }
    if (path.startsWith('/mcps')) {
      return ok([])
    }
    if (path.startsWith('/models')) {
      return ok({ models: [] })
    }
    // 兜底：空信封，避免未 mock 的路径让 store 抛错。
    return ok(null)
  })

  // WS 事件流：不真连（连不上只是 wsState=closed，界面照常渲染）。
  // 显式挡掉，避免真实握手在静态服务上变成 200 响应让浏览器刷 "handshake failed" 噪音。
  await page.route('**/v1/mate/ws/**', async (route) => route.abort())
}

test.describe('前端首屏与导航', () => {
  test.beforeEach(async ({ page }) => {
    await mockBackend(page)
  })

  test('首屏渲染出左栏导航与状态条', async ({ page }) => {
    await page.goto('/')
    // 左栏导航入口：在 <aside aria-label="主导航"> 作用域内断言文字（"扩展"是分组按钮不是 <a>，
    // 且窄屏另有隐藏副本，按容器整体文本断言最稳）。
    const rail = page.locator('aside[aria-label="主导航"]')
    await expect(rail).toBeVisible()
    for (const label of ['资料库', '用量', '定时任务']) {
      await expect(rail.getByText(label, { exact: true }).first()).toBeVisible()
    }
    await expect(rail).toContainText('扩展')
    // 底部状态条常驻（无连接也渲染 wsState 文案；WS 被挡 → "连接断开"）
    await expect(page.getByText(/未连接|连接中|连接断开/).first()).toBeVisible()
  })

  test('导航到扩展 · 专家页并渲染清单', async ({ page }) => {
    await page.goto('/extensions/experts')
    // mock 的专家卡片渲染出来（证明路由 + 数据拉取 + 列表渲染链路通）
    await expect(page.getByText('演示专家')).toBeVisible()
    // 搜索框在位
    await expect(page.getByPlaceholder('搜索专家')).toBeVisible()
  })

  test('导航到用量页不白屏', async ({ page }) => {
    await page.goto('/usage')
    // 能渲染出左栏即说明路由切换没把应用打崩（用量页具体内容依赖数据面，不作强断言）。
    await expect(page.getByText('用量', { exact: true }).first()).toBeVisible()
    await expect(page.locator('#root')).not.toBeEmpty()
  })
})
