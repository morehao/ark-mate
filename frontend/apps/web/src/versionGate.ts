/**
 * 版本闸门（M2-4，设计 §6.7）：页面启动时比对"编译进 bundle 的构建 id"与"核心实际服务的那份资产"。
 *
 * 两种结局：
 *   - 一致 / 无法判定 → 正常进界面（无法判定时只记 console 警告，见契约里的理由）；
 *   - 两侧都是已知值且不一致 → **整屏状态页**（不是白屏、不是零散接口报错），带刷新按钮。
 *
 * 取数走 `/v1/mate/meta`：桌面态由壳反代注入 restToken、浏览器态同源（独立部署时带用户令牌）。
 */
import { getToken } from '@ark-mate/api'
import { tokens } from '@ark-mate/ui'
import { UNKNOWN_BUILD_ID, versionGate, type VersionGateResult } from '@ark-mate/desktop-contract'

/** 编译期注入的构建 id（声明见 src/env.d.ts）。取不到就按 unknown——与核心同一口径。 */
export const INJECTED_BUILD_ID: string =
  typeof __MATE_WEB_BUILD_ID__ === 'string' ? __MATE_WEB_BUILD_ID__ : UNKNOWN_BUILD_ID

/** 从核心取它实际服务的那份资产的构建 id。失败返回 null（调用方按"无法判定"处理）。 */
async function fetchCoreBuildId(): Promise<string | null> {
  const token = getToken()
  try {
    const res = await fetch('/v1/mate/meta', {
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
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
 * **vite dev 下直接放行**：dev 的页面由 vite 从源码现编译（`__MATE_WEB_BUILD_ID__` 是启动那刻
 * 的 HEAD），而 `make dev` 给核心的 `--web-dir` 指向的是上一次 `pnpm build` 的产物——两者本来
 * 就可能不是同一次构建，比对它们等于拿 A 的身份证核对 B 的档案。闸门的契约是"**被服务的产物**"
 * 前后一致，因此只对产物形态生效（内嵌 release、桌面壳的 dist、部署者的 dist）。
 */
export async function runVersionGate(): Promise<VersionGateResult> {
  if (import.meta.env.DEV) {
    return { ok: true, mismatch: null, unverifiable: null }
  }
  const coreBuildId = await fetchCoreBuildId()
  return versionGate(INJECTED_BUILD_ID, coreBuildId ?? '')
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
