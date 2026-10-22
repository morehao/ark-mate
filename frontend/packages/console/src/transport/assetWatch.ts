/**
 * 开发期资产重建通知的**页面半边**（P6-A，2026-10-11）。
 *
 * 它解决什么：`make dev` 的循环是「构建一次 → `vite build --watch` 重写 dist → 核心服务**同一份
 * 产物**」——资产路径只有一条（P1 取消源码靶换来的），代价是**改完要手动刷新**。核心侧现
 * 在会在内容真的变了时经 SSE 推一帧（见 `backend/internal/router/web_watch.go`），这里收到
 * 就整页刷新，把那一次手动动作省掉。
 *
 * 三条纪律：
 *
 *  1. **没有编译期开关**。这份代码在交付产物里也打包着，而它"生不生效"由**核心有没有注册那条
 *     路由**决定（HEAD 探测）。这正是 P1 的单一资产路径纪律：一旦这里出现
 *     `import.meta.env.DEV` 这类分支，就又有了"只在某一种构建里生效的代码"
 *     （守卫 `TestDevLoopServesBuiltArtifact` 明确禁掉界面源码里的 `import.meta.env`）。
 *  2. **探测用 HEAD 且看 Content-Type**，不看状态码。交付形态下这个路径是**合法存在的**
 *     ——它会走 SPA 回落返回 index.html（200 + text/html）。只看 `res.ok` 会让交付形态也去开
 *     EventSource，然后拿到一坨 HTML 报错，再不停重连。
 *  3. **重试只发生在"确认过通道存在之后"**。EventSource 自带的重连在"通道本来就不存在"时
 *     是纯噪声（每 3 秒一次请求，直到页面关掉），所以交付形态那一侧必须一次都不重试；但开发
 *     形态里核心重启是常态（改了 Go 代码就得重启），不重试会让"通道半路没了"永远好不了。
 *     判据因此是**探测结论**而不是"有没有出过错"：探测拿到 `text/event-stream` 才算"通道在"，
 *     此后断了就退避重试；没确认过（或探测明确说不是 SSE）就彻底不重试。
 *
 * 与参照实现（DSH）的差别要写明：DSH 收到 `rebuilt` 后做的是 `entries.reload(id, rev)`
 * ——**模块级重度装载**，页面状态保住。那需要它的模块系统，本仓还没有（P6-B/C 才做），
 * 所以这里只能 `location.reload()`：**"不用手按 F5"回来了，"页面状态不丢"没有**。
 */

/** 端点路径（必须与核心 `router.AssetWatchPath` 逐字一致）。 */
export const ASSET_WATCH_PATH = '/dev/assets/events'

/** SSE 事件名（必须与核心 `writeSSE` 的事件名一致）。 */
const REBUILT_EVENT = 'rebuilt'

/** 是否已经开始盯着（StrictMode 下会挂载两次，重复订阅没有必要）。 */
let watching = false

/** 是否**确认过**通道存在（探测拿到 `text/event-stream`）。重试的判据见文件头纪律 3。 */
let hadChannel = false

/** 断线后的退避间隔。这条退避只服务于开发态（核心重启），交付形态永不进入。 */
const RETRY_DELAY_MS = 3000

/**
 * 订阅开发期资产重建通知。**通道不存在时静默什么都不做**（交付形态的正常路径）。
 *
 * 刻意不返回 cleanup：这条订阅的生命周期就是页面本身（收到 rebuilt 就整页刷新，
 * 不存在"卸载"这一态）。StrictMode 的双次挂载由 `watching` 兜住。
 */
export function watchDevAssets(): void {
  if (watching) return
  if (typeof window === 'undefined' || typeof EventSource === 'undefined') return
  watching = true

  void probe()
}

/**
 * 先探测再订阅。**探测在这里是判据的载体**（"这条通道到底在不在"），不是可有可无的优化：
 * 交付形态下该路径是合法存在的（走 SPA 回落给 index.html），只 `res.ok` 会让交付形态也去开
 * EventSource，然后拿到一坨 HTML 报错再不停重连。
 *
 * 结论三态，各自的动作不同（见文件头纪律 3）：
 *   - 是 SSE → 订阅；
 *   - 不是 SSE（200 + `text/html`：交付形态的 SPA 回落）→ **彻底停止**，一次都不再探测；
 *   - 网络层失败（核心正在重启）→ 只有**确认过通道存在**才退避重试，否则也停。
 *
 * 全部安静处理：交付形态每次页面加载会走**恰好一次** HEAD 探测（无响应体），不该在用户控制台留痕。
 */
async function probe(): Promise<void> {
  let isChannel = false
  let reachable = false
  try {
    const res = await fetch(ASSET_WATCH_PATH, { method: 'HEAD', cache: 'no-store' })
    reachable = true
    const type = res.headers.get('content-type') ?? ''
    isChannel = res.ok && type.includes('text/event-stream')
  } catch {
    // 网络层失败（核心还没起来/正在重启）。
  }
  if (!isChannel) {
    // 见文件头纪律 3：只有"确认过通道存在、现在够不着"才继续退避；其余一律停手。
    if (hadChannel && !reachable) setTimeout(() => void probe(), RETRY_DELAY_MS)
    return
  }

  hadChannel = true
  const source = new EventSource(ASSET_WATCH_PATH)
  source.addEventListener(REBUILT_EVENT, () => {
    // 整页刷新：见文件头第 3 段（本仓还没有模块系统，做不到 DSH 那种保状态重载）。
    // 先 close 再 reload：不然刷新过程中 EventSource 会立刻重连一次，白跑一个请求。
    source.close()
    window.location.reload()
  })
  source.addEventListener('error', () => {
    // 走到这里只可能是"确认过通道在、后来断了"（核心重启是最常见的那种）。关掉自带重连、
    // 改成自己的退避：自带重连是无上限的固定间隔，而我们要用探测结论决定还要不要继续。
    source.close()
    if (hadChannel) setTimeout(() => void probe(), RETRY_DELAY_MS)
  })
}
