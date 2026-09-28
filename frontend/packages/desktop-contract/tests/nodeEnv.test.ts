/**
 * 桌面态子进程环境（设计 §6.6）的判定口径。
 *
 * 为什么值得单测：这两条规则（继承 + PATH 只前置）是桌面态"工具链还能用"与"我们的 Node 一定优先"
 * 同时成立的前提；写成纯函数才可能在没有 Electron 的环境里被反复验证。
 */
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { NODE_ENV_VARS, desktopNodeEnvironment, pathPrecedesCaller } from '../src/index.ts'

test('继承调用方环境：一个键都不丢（不做白名单）', () => {
  const env = desktopNodeEnvironment({
    executable: '/App/Electron',
    binDir: '/App/bin',
    base: { HOME: '/Users/me', HTTPS_PROXY: 'http://p:1', SSH_AUTH_SOCK: '/tmp/ssh', PATH: '/usr/bin:/bin' },
  })
  assert.equal(env.HOME, '/Users/me')
  assert.equal(env.HTTPS_PROXY, 'http://p:1')
  assert.equal(env.SSH_AUTH_SOCK, '/tmp/ssh')
})

test('PATH 只做前置：私有 bin 在前、调用方 PATH 原样在后', () => {
  const env = desktopNodeEnvironment({
    executable: '/App/Electron',
    binDir: '/App/bin',
    base: { PATH: '/usr/local/bin:/usr/bin:/bin' },
  })
  assert.equal(env.PATH, '/App/bin:/usr/local/bin:/usr/bin:/bin')
  assert.equal(pathPrecedesCaller(env.PATH, '/App/bin', '/usr/local/bin:/usr/bin:/bin'), true)
})

test('PATH 分隔符跟着平台（Windows 是分号）', () => {
  const env = desktopNodeEnvironment({
    executable: 'C:\\App\\Electron.exe',
    binDir: 'C:\\App\\bin',
    base: { PATH: 'C:\\Windows' },
    delimiter: ';',
  })
  assert.equal(env.PATH, 'C:\\App\\bin;C:\\Windows')
  assert.equal(pathPrecedesCaller(env.PATH, 'C:\\App\\bin', 'C:\\Windows', ';'), true)
})

test('binDir 缺省或空串：只切 Node 模式，不动 PATH', () => {
  for (const binDir of [undefined, '']) {
    const env = desktopNodeEnvironment({ executable: '/App/Electron', binDir, base: { PATH: '/usr/bin' } })
    assert.equal(env.PATH, '/usr/bin')
    assert.equal(env[NODE_ENV_VARS.runAsNode], '1')
  }
})

test('调用方没有 PATH 时，PATH 就等于私有 bin（不产出以分隔符开头的怪值）', () => {
  const env = desktopNodeEnvironment({ executable: '/App/Electron', binDir: '/App/bin', base: {} })
  assert.equal(env.PATH, '/App/bin')
})

test('base 里的 undefined 值不会被写成字符串 "undefined"', () => {
  const env = desktopNodeEnvironment({ executable: '/App/Electron', binDir: '/App/bin', base: { FOO: undefined } })
  assert.equal('FOO' in env, false)
})

test('pathPrecedesCaller：调用方 PATH 被改写/丢失都要判否', () => {
  assert.equal(pathPrecedesCaller('/App/bin:/usr/bin', '/App/bin', '/other'), false)
  assert.equal(pathPrecedesCaller('/usr/bin:/App/bin', '/App/bin', '/usr/bin'), false)
  assert.equal(pathPrecedesCaller(undefined, '/App/bin', '/usr/bin'), false)
  // 调用方本来没有 PATH：只要求私有 bin 在首位。
  assert.equal(pathPrecedesCaller('/App/bin', '/App/bin', undefined), true)
})
