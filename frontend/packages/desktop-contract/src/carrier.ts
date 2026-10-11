/**
 * 载体判定与形状守卫（纯函数，零依赖）。
 *
 * **判定**（`isDesktopCarrier`）回答"这个 document 是不是桌面载体主框架"——preload 用它决定
 * 要不要暴露 `__MATE_DESKTOP__`（不满足就一次都不调用，V18 的负例），主进程用它做 IPC origin 校验：
 * 每个 handler 首行拒绝非 app origin / 非主框架的调用方。
 *
 * **守卫**（`assertMateDesktop`）在 preload 暴露**之前**自检一次形状：形状错了就在 preload 阶段
 * 炸掉，而不是让页面拿到半个对象、在某个业务路径上以"某方法是 undefined"的形式晚炸。
 */
import { APP_ORIGIN, type MateDesktop } from './index.ts'

/** `location` 的最小投影：只取判定需要的两个字段（`URL` 与 `Location` 都满足）。 */
export type CarrierLocation = { protocol: string; hostname: string }

/**
 * 是否桌面载体的**主框架**。
 *
 * 判定口径（设计 §7.3）＝ 协议 + 主机 + 主框架**三者同时**成立：
 * `mate-app:` + `app` + `isMainFrame === true`。因此 `mate-app://shell/*`（壳静态页）、
 * `mate-app://other/*`、`https:` / `http:` 的任何页面、以及任意**子框架**都判否。
 */
export function isDesktopCarrier(loc: CarrierLocation | null | undefined, isMainFrame: boolean): boolean {
  if (loc == null || !isMainFrame) return false
  return `${loc.protocol}//${loc.hostname}` === APP_ORIGIN
}

function typeName(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  return typeof v
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function isFn(v: unknown): boolean {
  return typeof v === 'function'
}

/**
 * 运行期形状守卫：字段缺失或类型不符即抛错，**错误信息里列出出问题的字段名**。
 *
 * 校验的是**结构化形状**（不 import 任何 Electron 类型）：`boot` 是函数、`native` 是对象且
 * 里头的方法是函数。通过则原样返回（便于 `const api = assertMateDesktop(shape)`）。
 *
 * **2026-10-11 跟随执行面收窄**（P3/D5）：`platform` 整块与 `native` 的三个方法已删，校验清单
 * 相应只剩 `boot` + `native.pickDirectory`——**校验清单必须与契约逐条同步**，多校验一个已经
 * 不存在的方法会让 preload 每次启动都炸。
 */
export function assertMateDesktop(value: unknown): MateDesktop {
  if (!isRecord(value)) {
    throw new Error(`载体契约形状不符：期望对象，实际 ${typeName(value)}`)
  }

  // 缺失/类型错误一起收集后一次报出：preload 启动期一次就能看全，不来回试。
  const bad: string[] = []

  const requireFns = (scope: string, obj: Record<string, unknown>, names: string[]): void => {
    for (const name of names) {
      if (!isFn(obj[name])) bad.push(`${scope}.${name}（${obj[name] === undefined ? '缺失' : `类型 ${typeName(obj[name])}`}）`)
    }
  }

  if (!isFn(value.boot)) bad.push(`boot（${value.boot === undefined ? '缺失' : `类型 ${typeName(value.boot)}`}）`)

  if (!isRecord(value.native)) {
    bad.push(`native（${value.native === undefined ? '缺失' : `类型 ${typeName(value.native)}`}）`)
  } else {
    requireFns('native', value.native, ['pickDirectory'])
  }

  if (bad.length > 0) {
    throw new Error(`载体契约形状不符：${bad.join('，')}`)
  }
  return value as unknown as MateDesktop
}
