import { test, expect, type Page } from '@playwright/test'

/**
 * 刀 3 · 交互旅程（纯 mock 后端）：设置 · 通用 → 手输起点 → 保存。
 *
 * 与 `smoke.spec.ts`（首屏/导航渲染）互补：这里走的是**跨组件的一次完整写面交互**——
 * 左栏 footer「设置」→ 弹窗切「通用」分节 → 输入框加路径 → 保存 → 断言 `PUT /settings/workspace`
 * 真的发出且界面回显。
 *
 * 为什么放着 Vitest 的 `generalSettingsPanel.test.tsx` 还要这个：Vitest 是**单组件、无路由**；
 * 这里验的是"从真实导航入口到弹窗到分节"的**整条装配路径**（store.settingsOpen / 路由 / 弹窗壳）。
 * 二者视角不同、不重复。真栈（真底座）那层仍归 `ui:accept`。
 */

const MOCKED_SETTINGS = {
  configured: [],
  prefer_roots: false,
  provision_included: false,
  source: 'default',
  writable: true,
}

async function mockBackend(page: Page, captured: { putBody: unknown[] }) {
  await page.route('**/v1/mate/**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname.replace(/^\/v1\/mate/, '')
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })

    if (path === '/meta') return ok({ runtime: 'dsh', webBuildId: null, payloads: [] })
    if (path === '/capabilities') return ok({ capabilities: [], entries: {} })
    if (path === '/sessions') return ok([])
    if (path === '/workspaces') return ok({ items: [], writable: false })

    if (path === '/settings/workspace') {
      if (req.method() === 'PUT') {
        const body = req.postDataJSON() as { roots?: string[] }
        captured.putBody.push(body)
        const roots = body.roots ?? []
        return ok({ ...MOCKED_SETTINGS, configured: roots, prefer_roots: roots.length > 0 })
      }
      return ok(MOCKED_SETTINGS)
    }
    return ok(null)
  })
  await page.route('**/v1/mate/ws/**', async (route) => route.abort())
}

test('设置·通用：手输起点并保存（PUT 真的发出）', async ({ page }) => {
  const captured = { putBody: [] as unknown[] }
  await mockBackend(page, captured)
  await page.goto('/')

  // 左栏 footer「设置」（nav 里唯一的 footer 入口）
  const rail = page.locator('aside[aria-label="主导航"]')
  await rail.getByText('设置', { exact: true }).click()

  // 设置弹窗 + 切到「通用」分节
  const dlg = page.locator('.fg-setdlg')
  await expect(dlg).toBeVisible()
  await dlg.getByText('通用', { exact: true }).first().click()

  // 通用面板：手输绝对路径 → 加入 → 保存
  const input = page.getByPlaceholder(/输入目录绝对路径/)
  await expect(input).toBeVisible()
  await input.fill('/srv/e2e-root')
  await page.getByRole('button', { name: /加入/ }).click()
  await expect(page.getByText('/srv/e2e-root')).toBeVisible()

  await page.getByRole('button', { name: '保存起点' }).click()

  // 断言：PUT 真的发出，且 body 携带全量名单
  await expect.poll(() => captured.putBody.length).toBeGreaterThan(0)
  expect(captured.putBody[0]).toEqual({ roots: ['/srv/e2e-root'] })
})
