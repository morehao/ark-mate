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
    // 应用市场：GET /mate/market（T3 已把前端路径从 /mate/apps/market 改为 /mate/market）。
    // 必须精确匹配 '/market'，不能落到兜底 null——否则 listMarket() 拿到 null，市场页崩。
    if (path === '/market') {
      return ok([
        {
          id: 'demo-app',
          name: '演示应用',
          icon: 'box',
          summary: { zh: '一个演示用应用包' },
          version: '1.0.0',
          tags: ['demo'],
          source: { type: 'dir', dir: 'demo-app' },
          installed: false,
          enabled: false,
          updatable: false,
          installable: true,
        },
      ])
    }
    // 我的应用：GET /mate/apps（列表页；必须精确匹配 '/apps'，避免吃掉 /apps/:id/ 等子路径走空）。
    if (path === '/apps') {
      return ok([
        {
          id: 'demo-app',
          name: '演示应用',
          description: '一个演示用应用',
          enabled: true,
          state: 'active',
          has_page: false,
          source: 'self',
          revision: 1,
          created_at: 0,
          updated_at: 0,
        },
        {
          id: 'market-app',
          name: '市场应用',
          description: '一个从市场装的应用',
          enabled: true,
          state: 'active',
          has_page: false,
          source: 'market',
          revision: 1,
          created_at: 0,
          updated_at: 0,
        },
      ])
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
    // 一级导航新增「应用」组（与扩展并列；组长行是 button 不是 <a>）。
    await expect(rail).toContainText('应用')
    await expect(rail.getByRole('button', { name: /「应用」/ })).toBeVisible()
    // 底部状态条常驻（无连接也渲染 wsState 文案）。WS 被 abort，可能停在初始「未连接」、
    // 过渡「连接中…」，或进入 closed「断开（重连中）」——三者都可能出现（时序），全部纳入断言。
    await expect(page.getByText(/未连接|连接中|已连接|断开/).first()).toBeVisible()
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

  // 2026-11-05：「扩展」总览页删掉后，组行**只切换展开**，不再跳页。
  test('点「扩展」只展开二级菜单，URL 不变', async ({ page }) => {
    await page.goto('/sessions')
    const rail = page.locator('aside[aria-label="主导航"]')
    const skillsLink = rail.getByRole('link', { name: '技能' })

    // /sessions 上默认收起（把高度让给会话列表）
    await expect(skillsLink).toHaveCount(0)

    await rail.getByRole('button', { name: '展开「扩展」' }).click()
    await expect(skillsLink).toBeVisible()
    await expect(page).toHaveURL(/\/sessions$/)

    await rail.getByRole('button', { name: '收起「扩展」' }).click()
    await expect(skillsLink).toHaveCount(0)
    await expect(page).toHaveURL(/\/sessions$/)
  })

  // 旧路径（含已删的总览页）落到默认二级页，且组自动展开、当前项高亮。
  test('旧 /extensions 路径落到技能页', async ({ page }) => {
    await page.goto('/extensions')
    await expect(page).toHaveURL(/\/extensions\/skills$/)

    const rail = page.locator('aside[aria-label="主导航"]')
    await expect(rail.getByRole('link', { name: '技能' })).toHaveAttribute('aria-current', 'page')
    await expect(rail.getByRole('button', { name: '收起「扩展」' })).toBeVisible()
  })

  // 「扩展」组现只含 专家/技能/连接器 三项（应用市场/我的应用已迁到独立的「应用」组）。
  test('展开「扩展」组只含 专家/技能/连接器 三项', async ({ page }) => {
    await page.goto('/sessions')
    const rail = page.locator('aside[aria-label="主导航"]')
    await rail.getByRole('button', { name: '展开「扩展」' }).click()

    for (const label of ['专家', '技能', '连接器']) {
      await expect(rail.getByRole('link', { name: label }).first()).toBeVisible()
    }
    // 应用市场 / 我的应用 不再属于扩展组。
    await expect(rail.getByRole('link', { name: '应用市场' })).toHaveCount(0)
    await expect(rail.getByRole('link', { name: '我的应用' })).toHaveCount(0)
  })

  // 「应用」组展开后看到 应用市场 / 我的应用（独立一级组）。
  test('展开「应用」组看到 应用市场/我的应用', async ({ page }) => {
    await page.goto('/sessions')
    const rail = page.locator('aside[aria-label="主导航"]')
    // /sessions 上默认收起。
    await expect(rail.getByRole('link', { name: '应用市场' })).toHaveCount(0)

    await rail.getByRole('button', { name: '展开「应用」' }).click()
    await expect(rail.getByRole('link', { name: '应用市场' })).toBeVisible()
    await expect(rail.getByRole('link', { name: '我的应用' })).toBeVisible()
  })

  // 应用市场页：拉 GET /mate/market（见 mockBackend），渲染出市场卡片。
  test('导航到应用市场并渲染卡片', async ({ page }) => {
    await page.goto('/apps/market')
    await expect(page.getByText('应用市场', { exact: true }).first()).toBeVisible()
    await expect(page.getByPlaceholder('搜索应用、标签')).toBeVisible()
    await expect(page.getByText('演示应用')).toBeVisible()
  })

  // 我的应用页：拉 GET /mate/apps，渲染卡片网格（搜索框 + 卡片容器在位）。
  test('导航到我的应用并渲染卡片网格', async ({ page }) => {
    await page.goto('/apps/my-apps')
    await expect(page.getByText('我的应用', { exact: true }).first()).toBeVisible()
    await expect(page.getByPlaceholder('搜索应用')).toBeVisible()
    await expect(page.getByText('演示应用')).toBeVisible()
    await expect(page.locator('.fg-cap-grid')).toBeVisible()
    // 来源标签：self → 「自建」、market → 「市场」（mock 各一条）。
    await expect(page.getByText('自建', { exact: true })).toBeVisible()
    await expect(page.getByText('市场', { exact: true })).toBeVisible()
  })

  // 旧路径 redirect：/extensions/market|my-apps → /apps/market|my-apps。
  test('旧 /extensions/market 与 /extensions/my-apps 重定向到新路径', async ({ page }) => {
    await page.goto('/extensions/market')
    await expect(page).toHaveURL(/\/apps\/market$/)
    await page.goto('/extensions/my-apps')
    await expect(page).toHaveURL(/\/apps\/my-apps$/)
  })
})
