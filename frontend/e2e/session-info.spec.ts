import { test, expect, type Page } from '@playwright/test'

/**
 * 会话信息面合并（2026-11-06）的布局 e2e（纯 mock，不打真后端）。
 *
 * 验的是三条用户诉求：
 *   1. 新建会话页（无 activeId）右侧不留栏、正文居中；
 *   2. 已有会话默认**收起** → 正文居中；点「会话信息」→ 右栏出现；再点 → 收起回居中；
 *   3. 宽屏（≥1280）进侧栏、窄屏（<1280）进抽屉；资料库产物抽屉不受影响（由组件测试覆盖）。
 *
 * 截图落在 .dsh/tmp/（gitignore，不入库），便于人工比对。
 */

const SESSION = {
  id: 's1',
  runtime_id: 'dsh',
  status: 'completed',
  title: '验证会话标题',
  pinned: false,
  archived: false,
  cwd: '/tmp/proj',
  workspace_id: 'ws1',
  workspace_title: '默认工作空间',
  workspace_temporary: false,
  model: null,
  model_name: '',
  capabilities: null,
}

const DETAIL = {
  ...SESSION,
  created_at: 1_700_000_000_000,
  updated_at: 1_700_000_100_000,
  pending: [],
  memories: [],
}

async function mockBackend(page: Page) {
  await page.route('**/v1/mate/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace(/^\/v1\/mate/, '')
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })

    if (path === '/meta') return ok({ runtime: 'dsh', webBuildId: null, payloads: [] })
    if (path === '/capabilities') return ok({ capabilities: [], entries: {} })
    if (path === '/sessions') return ok([SESSION])
    if (path === '/workspaces') return ok({ items: [], writable: false })
    if (path === '/runtime/diagnostics') return ok({})
    if (path === `/sessions/s1`) return ok(DETAIL)
    if (path === `/sessions/s1/history`) return ok({ events: [], has_more: false })
    if (path === `/sessions/s1/artifacts`) return ok({ artifacts: [], total: 0 })
    if (path === `/sessions/s1/runs`) return ok({ runs: [] })
    if (path.startsWith('/materials')) return ok({ nodes: [], total: 0 })
    if (path.startsWith('/models')) return ok({ models: [] })
    if (path.startsWith('/experts')) return ok([])
    if (path.startsWith('/skills')) return ok([])
    if (path.startsWith('/mcps')) return ok([])
    return ok(null)
  })
  await page.route('**/v1/mate/ws/**', async (route) => route.abort())
}

async function shot(page: Page, name: string) {
  await page.screenshot({ path: `../.dsh/tmp/session-info-${name}.png`, fullPage: false })
}

test.describe('会话信息面 · 宽屏（1920）', () => {
  test.use({ viewport: { width: 1920, height: 1080 } })
  test.beforeEach(async ({ page }) => { await mockBackend(page) })

  test('新建会话页：无右栏、正文居中', async ({ page }) => {
    await page.goto('/sessions')
    await expect(page.getByText('Hi，今天有什么安排？')).toBeVisible()
    // 右侧信息栏不渲染
    await expect(page.locator('aside[aria-label="会话信息"]')).toHaveCount(0)
    await shot(page, 'new-1920')
  })

  test('已有会话：默认收起居中 → 展开右栏 → 收起回居中', async ({ page }) => {
    await page.goto('/sessions/s1')
    // 默认收起：无会话信息右栏
    await expect(page.locator('aside[aria-label="会话信息"]')).toHaveCount(0)
    await shot(page, 'collapsed-1920')

    // 展开
    const btn = page.getByRole('button', { name: '会话信息' })
    await btn.click()
    await expect(page.locator('aside[aria-label="会话信息"]')).toBeVisible()
    await expect(page.locator('aside[aria-label="会话信息"]').getByText('验证会话标题')).toBeVisible()
    await shot(page, 'expanded-1920')

    // 收起
    await btn.click()
    await expect(page.locator('aside[aria-label="会话信息"]')).toHaveCount(0)
    await shot(page, 'collapsed-again-1920')
  })
})

test.describe('会话信息面 · 窄屏（1100）', () => {
  test.use({ viewport: { width: 1100, height: 900 } })
  test.beforeEach(async ({ page }) => { await mockBackend(page) })

  test('窄屏：点会话信息打开抽屉', async ({ page }) => {
    await page.goto('/sessions/s1')
    await expect(page.locator('aside[aria-label="会话信息"]')).toHaveCount(0)
    await page.getByRole('button', { name: '会话信息' }).click()
    // 抽屉出现（antd Drawer 有 role=dialog）
    await expect(page.getByRole('dialog')).toBeVisible()
    await shot(page, 'drawer-1100')
  })
})
