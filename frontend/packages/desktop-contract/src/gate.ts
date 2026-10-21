/**
 * UI/API 版本闸门（M2-4，设计 §6.7）：**纯判定函数**放契约包，页面只负责取数与渲染。
 *
 * 背景：一份前端产物可能被三种方式送到浏览器（独立二进制的内嵌资产、桌面壳的包内目录、
 * 部署者自己的 dist），而核心**只认自己实际服务的那份目录**。当 index.html 与 assets/ 的
 * 构建代不同源、或页面缓存了旧 bundle 时，表现是"界面看起来正常、个别接口行为怪"——
 * 混搭最难排查。判据必须是一个**编译进 bundle 的常量**对比**核心实测到的文件内容**。
 *
 * 为什么在这个包：本仓前端没有测试框架（C12），可执行判定逻辑一律放这里用 `node --test` 跑。
 */

/** 核心读不到 `build-id.txt` 时 `/meta` 回报的口径（与后端 internal/delivery/router 一致）。 */
export const UNKNOWN_BUILD_ID = 'unknown'

/** 版本闸门的判定结果。 */
export type VersionGateResult = {
  /** 是否放行。false 时调用方必须渲染整屏状态页，而不是继续进界面。 */
  ok: boolean
  /** 两侧都有值且不一致时的取值；否则为 null。 */
  mismatch: { web: string; core: string } | null
  /**
   * 无法判定的原因（`ok` 仍为 true）：某一侧缺失或为 `unknown`。
   *
   * 这种状态**放行**而不是拦下：核心里没有 `build-id.txt` 是合法的（手工放的 dist、旧构建、
   * 第三方部署），因为"核心里没有清单"就把界面锁死，属于把判据用成了自伤。
   * 真正的混搭（两侧都是已知值且不同）必须拦下。
   */
  unverifiable: string | null
}

const norm = (s: string | null | undefined): string => (s ?? '').trim()

/**
 * 比较"编译进页面的构建 id"与"核心实际服务的那份资产的构建 id"。
 *
 * @param webBuildId 页面里的编译期常量（`__MATE_WEB_BUILD_ID__`）
 * @param coreBuildId `/meta` 回报的 `webBuildId`
 */
export function versionGate(webBuildId: string, coreBuildId: string): VersionGateResult {
  const web = norm(webBuildId)
  const core = norm(coreBuildId)
  if (!web || web === UNKNOWN_BUILD_ID) {
    return { ok: true, mismatch: null, unverifiable: `前端没有构建 id（${web || '空'}）` }
  }
  if (!core || core === UNKNOWN_BUILD_ID) {
    return { ok: true, mismatch: null, unverifiable: `核心读不到构建 id（${core || '空'}）` }
  }
  if (web !== core) {
    return { ok: false, mismatch: { web, core }, unverifiable: null }
  }
  return { ok: true, mismatch: null, unverifiable: null }
}
