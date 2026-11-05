import { defineConfig, devices } from '@playwright/test'

/**
 * 前端 e2e（刀 2）：Playwright 打**已构建产物** `apps/web/dist`，REST 全部 route mock。
 *
 * 定位：取代手写 CDP 的"界面真的长出来了"验收里**不依赖真底座/真模型**的那部分。
 * 真会话/真模型仍留 `smoke:e2e:live`（本地手工），不进这里。
 *
 * 为什么 headless 可进 CI：页面由静态服务提供，`/v1/mate/**` 由 spec 内 `page.route` 拦截喂固定
 * JSON —— 不需要后端、不需要模型、不需要 Linux runner 上跑 Electron。
 *
 * 浏览器：默认复用**系统 Chrome**（`channel: 'chrome'`），不下载 Playwright 自带浏览器，
 * 避免往 `~/Library/Caches/ms-playwright` 写盘（本机隔离）。CI 上若没装 Chrome，可临时
 * `pnpm exec playwright install chromium` 后把 channel 换成 'chromium'。
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'list' : [['list'], ['html', { open: 'never', outputFolder: 'e2e-report' }]],
  outputDir: 'e2e/.artifacts',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    // 本地复用系统 Chrome（channel:'chrome'，不下载 Playwright 浏览器，避免写 ~/Library/Caches）。
    // CI（Linux runner 无系统 Chrome）用 Playwright 自带的 chromium（配合
    // `pnpm exec playwright install --with-deps chromium`）。
    process.env.CI
      ? { name: 'chromium', use: { ...devices['Desktop Chrome'] } }
      : { name: 'chrome', use: { ...devices['Desktop Chrome'], channel: 'chrome' } },
  ],
  // 静态服务：直接 serve 构建产物。**不**另起核心/底座——那会把纯前端 e2e 变成集成测试。
  webServer: {
    command: 'node scripts/static-server.mjs 4173 apps/web/dist',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
