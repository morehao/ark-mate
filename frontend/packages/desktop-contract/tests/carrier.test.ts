/**
 * 载体判定与形状守卫的单测（`node --test`，Node 24 内置，零新依赖）。
 *
 * 判定的取值组合**全枚举写死**（3 协议 × 4 主机 × 2 主框架 = 24 条）：这张表就是 V18 的
 * "判定层"覆盖——`mate-app://shell/*` 与子框架这两条负例正是 preload origin 守卫的回归点。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

import { APP_ORIGIN, DESKTOP_NAMESPACE, isDesktopCarrier, assertMateDesktop } from '../src/index.ts'
import type { MateDesktop } from '../src/index.ts'

const PROTOCOLS = ['mate-app:', 'https:', 'http:'] as const
const HOSTNAMES = ['app', 'shell', 'other', ''] as const

test('契约常量与设计文档 §0.3 一致', () => {
  assert.equal(DESKTOP_NAMESPACE, '__MATE_DESKTOP__')
  assert.equal(APP_ORIGIN, 'mate-app://app')
})

test('isDesktopCarrier：全组合逐条写死', () => {
  let trues = 0
  for (const protocol of PROTOCOLS) {
    for (const hostname of HOSTNAMES) {
      for (const isMainFrame of [true, false]) {
        const want = protocol === 'mate-app:' && hostname === 'app' && isMainFrame
        const got = isDesktopCarrier({ protocol, hostname }, isMainFrame)
        assert.equal(
          got,
          want,
          `${protocol}//${hostname} isMainFrame=${isMainFrame} 期望 ${want}，实际 ${got}`,
        )
        if (got) trues++
      }
    }
  }
  // 24 条里只有一条为真——判定必须"窄"，多一条真就意味着一类页面能拿到桌面能力。
  assert.equal(trues, 1)
})

test('isDesktopCarrier：null / undefined 判否（不抛错）', () => {
  assert.equal(isDesktopCarrier(null, true), false)
  assert.equal(isDesktopCarrier(undefined, true), false)
})

/** 一个形状完整的载体对象（正例基线；各负例在它上面改一处）。 */
function goodCarrier(): unknown {
  return {
    boot: async () => ({ streamBaseUrl: 'ws://127.0.0.1:1', streamToken: 't' }),
    native: {
      pickDirectory: async () => null,
      pathFor: () => '',
      openPath: async () => {},
      openExternal: async () => {},
    },
    platform: { setBounds: async () => {}, close: async () => {} },
  }
}

test('assertMateDesktop：正例原样返回', () => {
  const good = goodCarrier()
  const got: MateDesktop = assertMateDesktop(good)
  assert.equal(got, good)
})

test('assertMateDesktop：六种坏形状全部抛错', () => {
  const missingField = goodCarrier() as { native: Record<string, unknown> }
  delete missingField.native.openPath

  const wrongType = goodCarrier() as Record<string, unknown>
  wrongType.boot = 'not-a-function'

  const cases: Array<[string, unknown]> = [
    ['缺字段（native.openPath 被删）', missingField],
    ['字段类型错（boot 是字符串）', wrongType],
    ['空对象', {}],
    ['null', null],
    ['数组', []],
    ['字符串', 'mate-app://app'],
  ]

  for (const [name, value] of cases) {
    assert.throws(() => assertMateDesktop(value), /载体契约形状不符/, `${name} 应抛错`)
  }
})

test('assertMateDesktop：错误信息含出问题的字段名', () => {
  const nestedMissing = goodCarrier() as { platform: Record<string, unknown> }
  delete nestedMissing.platform.close
  assert.throws(() => assertMateDesktop(nestedMissing), /platform\.close/)

  const bootMissing = goodCarrier() as Record<string, unknown>
  delete bootMissing.boot
  assert.throws(() => assertMateDesktop(bootMissing), /boot（缺失）/)

  const nativeWrong = goodCarrier() as Record<string, unknown>
  nativeWrong.native = null
  assert.throws(() => assertMateDesktop(nativeWrong), /native（类型 null）/)
})
