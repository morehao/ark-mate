// vitest 组件测试的全局 setup（刀 0）。
// 只做 jest-dom 断言扩展 + 每个用例后自动 cleanup，不加任何业务 mock。
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

afterEach(() => {
  cleanup()
})
