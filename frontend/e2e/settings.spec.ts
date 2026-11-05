import { test, expect, type Page } from '@playwright/test'

/**
 * 刀 3 · 交互旅程（纯 mock 后端）：设置 · 通用（v14.6 改版后）。
 *
 * 与 `smoke.spec.ts`（首屏/导航渲染）互补：这里走的是"从真实导航入口到弹窗到分节"的
 * **整条装配路径**（store.settingsOpen / 路由 / 弹窗壳）——左栏 footer「设置」→ 弹窗切
 * 「通用」分节 → 断言面板展示**只读的默认工作区**与**键盘操作键位**，且**不再**出现
 * 「工作区起始位置」的编辑入口（该设置面已前后端一并删除）与「发送键」切换控件。
 *
 * 真栈（真底座）那层仍归 `ui:accept`。
 */

const MOCKED_WORKSPACES = {
  items: [{ id: 'wsp-1', path: '/Users/me/ark-mate', title: 'ark-mate', is_default: true, list_order: 0 }],
  default_id: 'wsp-1',
  writable: false,
}

async function mockBackend(page: Page) {
  await page.route('**/v1/mate/**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname.replace(/^\/v1\/mate/, '')
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })

    if (path === '/meta') return ok({ runtime: 'dsh', webBuildId: null, payloads: [] })
    if (path === '/capabilities') return ok({ capabilities: [], entries: {} })
    if (path === '/sessions') return ok([])
    if (path === '/workspaces') return ok(MOCKED_WORKSPACES)
    return ok(null)
  })
  await page.route('**/v1/mate/ws/**', async (route) => route.abort())
}

test('设置·通用：展示默认工作区与键盘操作，不含起始位置编辑', async ({ page }) => {
  await mockBackend(page)
  await page.goto('/')

  // 左栏 footer「设置」（nav 里唯一的 footer 入口）
  const rail = page.locator('aside[aria-label="主导航"]')
  await rail.getByText('设置', { exact: true }).click()

  // 设置弹窗 + 切到「通用」分节
  const dlg = page.locator('.fg-setdlg')
  await expect(dlg).toBeVisible()
  await dlg.getByText('通用', { exact: true }).first().click()

  // 面板：只读默认工作区路径 + 键盘操作键位。
  await expect(dlg.getByText('默认工作区', { exact: true }).first()).toBeVisible()
  await expect(dlg.getByText('/Users/me/ark-mate')).toBeVisible()
  await expect(dlg.getByText('Enter', { exact: true })).toBeVisible()
  await expect(dlg.getByText('Shift + Enter')).toBeVisible()

  // 起始位置编辑入口与发送键切换都已删除。
  await expect(dlg.getByText('工作区起始位置')).toHaveCount(0)
  await expect(dlg.getByPlaceholder(/输入目录绝对路径/)).toHaveCount(0)
  await expect(dlg.getByText('发送键')).toHaveCount(0)
})
