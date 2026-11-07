import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

/**
 * 组件渲染单测配置（刀 0 探针落地，2026-11-05）。
 *
 * 与既有 `node --test`（`pnpm test:console`，纯函数测试）**并存**，互不影响：
 *   - 纯逻辑（`src/console/*.ts`）：沿用 node:test，不迁；
 *   - 组件渲染/交互（`src/components/**`、`src/pages/**`）：走 vitest + Testing Library。
 *
 * 为什么放包内而不是 apps/web：真正 UI 全在 `packages/console`（`apps/web/src` 只有 main.tsx）。
 * 为什么能零额外构建配置：vitest 与 Vite 同源，直接复用 @vitejs/plugin-react 的 JSX 转换。
 */
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    // 组件测试与纯逻辑测试分目录，避免 vitest 误收 node:test 的用例（反之亦然）。
    // preview/__tests__：FE-P3 的 V6 单测就近放在组件目录下（与实现同目录），一并纳入。
    include: [
      'tests/components/**/*.test.tsx',
      'tests/components/**/*.test.ts',
      'src/components/preview/__tests__/**/*.test.tsx',
      'src/components/preview/__tests__/**/*.test.ts',
    ],
    setupFiles: ['./tests/components/setup.ts'],
    css: false,
  },
})
