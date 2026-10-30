/**
 * 版本闸门（M2-4，设计 §6.7）：页面启动时比对"编译进 bundle 的构建 id"与"核心实际服务的那份资产"。
 *
 * 两种结局：
 *   - 一致 / 无法判定 → 正常进界面（无法判定时只记 console 警告，见契约里的理由）；
 *   - 两侧都是已知值且不一致 → **整屏状态页**（不是白屏、不是零散接口报错），带刷新按钮。
 *
 * 取数走 `/v1/mate/meta`：桌面态由壳反代到核心、浏览器态同源。
 *
 * "编译进页面的构建 id"由**入口**传进来（`bootstrap({ webBuildId })`）：它是应用构建期注入的常量，
 * 不是界面包的性质——包只做判定与渲染，不读 app 的全局（P5 迁包时的边界整理）。
 */
import { tokens } from '@ark-mate/ui'
import { versionGate, type VersionGateResult } from '@ark-mate/desktop-contract'

/** 从核心取它实际服务的那份资产的构建 id。失败返回 null（调用方按"无法判定"处理）。 */
async function fetchCoreBuildId(): Promise<string | null> {
  try {
    const res = await fetch('/v1/mate/meta')
    if (!res.ok) return null
    const body = (await res.json()) as { data?: { webBuildId?: string } }
    return body.data?.webBuildId ?? null
  } catch {
    // 核心没起来 / 网络错：闸门不拦（界面自己会报连不上），只在控制台留痕。
    return null
  }
}

/**
 * 跑一次闸门。
 *
 * **这里曾经有一段 `if (import.meta.env.DEV) return 放行`**（2026-10-11 之前）。当时 dev 的页面由
 * vite dev server 从源码**现编译**，而核心拿到的 `--web-dir` 指向上一次 `pnpm build` 的产物——
 * 比对它们等于拿 A 的身份证核对 B 的档案，所以只能放行。
 *
 * 那段分叉本身就是缺陷，现在**从结构上取消了**：开发态改为服务**同一份构建产物**
 * （`scripts/dev-loop.mjs`：构建一次 → `vite build --watch` 重建 → 核心服务它；
 * `apps/web/vite.config.ts` 在 `serve` 上直接报错兜住第二条路）。于是闸门在开发态与交付态
 * 是**同一个判据**，不再有"只在产物形态才成立"的例外——`import.meta.env.DEV` 这个维度
 * 在本仓已无消费者（由 archtest 守卫钉住）。
 */
export async function runVersionGate(webBuildId: string): Promise<VersionGateResult> {
  const coreBuildId = await fetchCoreBuildId()
  return versionGate(webBuildId, coreBuildId ?? '')
}

/**
 * 渲染整屏状态页（文案按设计 §6.7；用 tokens 取色，不硬编码）。
 *
 * 为什么直接写 DOM 而不是走 React：这一步发生在**挂载之前**，此刻不该把应用树拉起来——
 * 拉起来就意味着它会去拉数据、连事件流，然后在一片失败里刷错误。
 */
export function renderVersionMismatch(root: HTMLElement, web: string, core: string): void {
  root.innerHTML = ''
  const wrap = document.createElement('div')
  wrap.setAttribute('data-mate-version-gate', 'mismatch')
  wrap.style.cssText = [
    'position:fixed', 'inset:0', 'display:flex', 'flex-direction:column',
    'align-items:center', 'justify-content:center', 'gap:16px', 'padding:32px',
    'box-sizing:border-box', 'text-align:center',
    `background:${tokens.bg}`, `color:${tokens.text}`,
    'font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif',
  ].join(';')

  const title = document.createElement('div')
  title.style.cssText = 'font-size:18px;font-weight:600'
  title.textContent = '前端与核心版本不一致'

  const detail = document.createElement('div')
  detail.style.cssText = `font-size:13px;line-height:1.7;color:${tokens.textSecondary};max-width:560px`
  detail.textContent = `前端 ${web} / 核心 ${core}。请刷新或重新安装。`

  const hint = document.createElement('div')
  hint.style.cssText = `font-size:12px;color:${tokens.textPlaceholder}`
  hint.textContent = '这条判据比的是页面里编译进的构建 id 与核心实际读到的那份资产的 build-id.txt。'

  const btn = document.createElement('button')
  btn.id = 'mate-version-gate-reload'
  btn.textContent = '刷新'
  btn.style.cssText = [
    `background:${tokens.primary}`, 'color:#fff', 'border:none', 'border-radius:6px',
    'padding:8px 20px', 'font-size:13px', 'cursor:pointer',
  ].join(';')
  btn.onclick = () => location.reload()

  wrap.append(title, detail, hint, btn)
  root.append(wrap)
}
