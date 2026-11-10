import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import {
  allowedReads,
  createAppFrameBridge,
  deriveRuntimeAppPages,
  getRuntimeAppPages,
  normalizeRoute,
  routesCollide,
  setRuntimeAppPages,
  type AppsListItem,
} from '../src/console/runtimeAppPages.ts'

// 二期 C · WP-C7 纯逻辑判据：运行期页派生 + 冲突检测 + 受限 postMessage 桥白名单/来源/限流。

const builtin = ['/extensions/skills', '/extensions/experts', '/extensions/connectors', '/usage', '/materials', '/automations']

function app(id: string, over: Partial<AppsListItem> = {}): AppsListItem {
  return {
    id,
    name: `应用 ${id}`,
    enabled: true,
    page: { nav_label: `页 ${id}`, route: `/apps-ui/${id}` },
    frame_token: `tok-${id}`,
    ...over,
  }
}

describe('deriveRuntimeAppPages · 派生', () => {
  it('收齐齐备的 app；按 appId 稳定排序；order 从 60 起', () => {
    const { pages, dropped } = deriveRuntimeAppPages([app('b'), app('a')], builtin)
    assert.equal(dropped.length, 0)
    assert.deepEqual(pages.map((p) => p.appId), ['a', 'b'])
    assert.deepEqual(pages.map((p) => p.order), [60, 61])
    assert.equal(pages[0].navLabel, '页 a')
  })

  it('停用 / 无页面声明 / 缺 token → 不登记（缺 token 记 dropped）', () => {
    const { pages, dropped } = deriveRuntimeAppPages(
      [
        app('off', { enabled: false }),
        app('nopage', { page: null }),
        app('notoken', { frame_token: '' }),
      ],
      builtin,
    )
    assert.equal(pages.length, 0)
    // 停用/无页不报 dropped；只有「有页但缺 token」进 dropped。
    assert.deepEqual(dropped.map((d) => d.appId), ['notoken'])
    assert.match(dropped[0].reason, /frame_token/)
  })

  it('page.route 非站内绝对路径 → 丢弃', () => {
    const { pages, dropped } = deriveRuntimeAppPages([app('x', { page: { route: 'relative/path' } })], builtin)
    assert.equal(pages.length, 0)
    assert.equal(dropped.length, 1)
    assert.match(dropped[0].reason, /绝对路径/)
  })
})

describe('deriveRuntimeAppPages · 冲突检测（fail-closed 让位）', () => {
  it('与内置路由完全相同 → 让位丢弃', () => {
    const { pages, dropped } = deriveRuntimeAppPages(
      [app('bad', { page: { route: '/usage' } })],
      builtin,
    )
    assert.equal(pages.length, 0)
    assert.equal(dropped.length, 1)
    assert.match(dropped[0].reason, /冲突/)
  })

  it('与内置路由互为前缀 → 让位丢弃', () => {
    const { pages } = deriveRuntimeAppPages([app('deep', { page: { route: '/usage/extra' } })], builtin)
    assert.equal(pages.length, 0)
  })

  it('段边界不算冲突：/usages 不与 /usage 冲突', () => {
    const { pages } = deriveRuntimeAppPages([app('ok', { page: { route: '/usages' } })], builtin)
    assert.equal(pages.length, 1)
    assert.equal(pages[0].route, '/usages')
  })

  it('运行期页之间 route 冲突 → 保留先出现（appId 升序）者', () => {
    const { pages, dropped } = deriveRuntimeAppPages(
      [app('bb', { page: { route: '/dup' } }), app('aa', { page: { route: '/dup' } })],
      builtin,
    )
    assert.equal(pages.length, 1)
    assert.equal(pages[0].appId, 'aa')
    assert.equal(dropped.length, 1)
    assert.equal(dropped[0].appId, 'bb')
  })
})

describe('routesCollide / normalizeRoute', () => {
  it('相同 / 互为段前缀 冲突；段边界不同不冲突', () => {
    assert.equal(routesCollide('/a', '/a'), true)
    assert.equal(routesCollide('/a', '/a/b'), true)
    assert.equal(routesCollide('/a/b', '/a'), true)
    assert.equal(routesCollide('/a', '/ab'), false)
    assert.equal(routesCollide('/a', '/b'), false)
  })
  it('归一化去尾斜杠', () => {
    assert.equal(normalizeRoute('/a/b/'), '/a/b')
    assert.equal(normalizeRoute('/'), '/')
  })
})

describe('运行期页注册表单例', () => {
  it('set/get 往返', () => {
    setRuntimeAppPages([])
    assert.equal(getRuntimeAppPages().length, 0)
    setRuntimeAppPages([{ appId: 'x', navLabel: 'X', route: '/x', frameToken: 't', order: 60, permissions: [] }])
    assert.equal(getRuntimeAppPages().length, 1)
    setRuntimeAppPages([])
  })
})

describe('allowedReads · 权限→读白名单', () => {
  it('read:usage → usage:summary；read:providers → providers:list', () => {
    const a = allowedReads(['read:usage'])
    assert.deepEqual([...a], ['usage:summary'])
    const b = allowedReads(['read:usage', 'read:providers'])
    assert.deepEqual([...b].sort(), ['providers:list', 'usage:summary'])
  })
  it('未映射/未知权限 → 不放行任何读', () => {
    assert.equal(allowedReads(['net:domain', 'file:domain', 'bogus']).size, 0)
    assert.equal(allowedReads([]).size, 0)
  })
})

describe('createAppFrameBridge · 来源/白名单/限流', () => {
  const perform = (kind: string) => ({ echo: kind })

  it('来源不匹配 → 不回复（null）', async () => {
    const bridge = createAppFrameBridge({ permissions: ['read:usage'], sourceMatches: false, performRead: perform })
    const reply = await bridge.handleMessage({ type: 'read', kind: 'usage:summary' })
    assert.equal(reply, null)
  })

  it('白名单内且来源匹配 → 回 read:result', async () => {
    let clock = 0
    const bridge = createAppFrameBridge({
      permissions: ['read:usage'],
      sourceMatches: true,
      performRead: perform,
      now: () => (clock += 10_000), // 每次推进足够大，绕过限流
    })
    const reply = await bridge.handleMessage({ type: 'read', kind: 'usage:summary', id: 'r1' })
    assert.deepEqual(reply, { type: 'read:result', kind: 'usage:summary', id: 'r1', data: { echo: 'usage:summary' } })
  })

  it('白名单外的读 → 不回复（null）', async () => {
    const bridge = createAppFrameBridge({ permissions: ['read:usage'], sourceMatches: true, performRead: perform })
    assert.equal(await bridge.handleMessage({ type: 'read', kind: 'providers:list' }), null)
  })

  it('畸形消息 → null', async () => {
    const bridge = createAppFrameBridge({ permissions: ['read:usage'], sourceMatches: true, performRead: perform })
    assert.equal(await bridge.handleMessage(null), null)
    assert.equal(await bridge.handleMessage({ type: 'write', kind: 'usage:summary' }), null)
    assert.equal(await bridge.handleMessage({ type: 'read' }), null)
    assert.equal(await bridge.handleMessage('str'), null)
  })

  it('限流：窗口内只放行一次，其余丢弃', async () => {
    let t = 0
    const bridge = createAppFrameBridge({
      permissions: ['read:usage'],
      sourceMatches: true,
      performRead: perform,
      rateLimitMs: 1000,
      now: () => t, // 不推进 → 第二次必被限流
    })
    const first = await bridge.handleMessage({ type: 'read', kind: 'usage:summary' })
    assert.notEqual(first, null)
    const second = await bridge.handleMessage({ type: 'read', kind: 'usage:summary' })
    assert.equal(second, null)
    // 时间推进超过窗口后可再次放行。
    t = 1500
    const third = await bridge.handleMessage({ type: 'read', kind: 'usage:summary' })
    assert.notEqual(third, null)
  })

  it('performRead 抛错 → 回 read:error', async () => {
    const bridge = createAppFrameBridge({
      permissions: ['read:usage'],
      sourceMatches: true,
      performRead: () => {
        throw new Error('boom')
      },
      now: () => 99_999,
    })
    const reply = await bridge.handleMessage({ type: 'read', kind: 'usage:summary', id: 'e1' })
    assert.deepEqual(reply, { type: 'read:error', kind: 'usage:summary', id: 'e1', error: 'boom' })
  })
})
