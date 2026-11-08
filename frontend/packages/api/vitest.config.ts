import { defineConfig } from 'vitest/config'

/**
 * `@ark-mate/api` 的纯逻辑单测配置（node 环境，无 DOM / 无 React）。
 *
 * 与 console 那套 vitest（jsdom + Testing Library）无关：这里只测 file.ts 这类
 * 不碰组件树的数据层逻辑，所以不引 @vitejs/plugin-react、不进 jsdom。
 * 就近放置：测试与被测代码同目录（`src/resources/__tests__/*.test.ts`）。
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/__tests__/**/*.test.ts'],
  },
})
