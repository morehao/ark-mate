// ─────────────────────────────────────────────────────────────────────────────
// SandboxAppFrame.tsx —— 二期 C 运行期应用页的**沙箱 iframe 容器**
//
// 与一期 A+Slot 的页面渲染（主窗直接渲染编译期组件）**并列，互不影响**：这里渲染的页面代码
// 来自运行期应用包，绝不能进主窗。硬约束（安全基线 v37 / 设计 §17.2 / magpie notes 机制 7）：
//
//   - iframe `sandbox="allow-scripts"`：**只给脚本**。**绝不给** `allow-same-origin`
//     （给了页面就与主窗同源，能读 cookie/DOM/storage，沙箱形同虚设），也**绝不给**
//     `allow-top-navigation`（页面不能导航主窗）。
//   - 页面文件从 `/_app/<token>/` 加载，服务端已下发 `connect-src 'none'` 的隔离 CSP——
//     页面自身**发不出**任何网络请求；所有读只能 postMessage 给主窗，主窗按清单 permissions
//     白名单代发（见 `console/runtimeAppPages.ts` 的 `createAppFrameBridge`）。
//   - 主窗只认**自己这个 iframe** 的 `ev.source`（其余来源的消息一律忽略：防同源其它 frame/
//     外站伪造）。桥的限流/白名单判据全在 bridge 里。
//
// 本组件只做「装配」：建 bridge、挂 message 监听、按来源过滤、把回复 postMessage 回 iframe。
import { useEffect, useMemo, useRef, useState } from 'react'
import { tokens } from '@ark-mate/ui'
import { listProviders, usageSummary } from '@ark-mate/api'
import {
  createAppFrameBridge,
  type BridgeReadKind,
  type RuntimeAppPage,
} from '../console/runtimeAppPages'

/** 沙箱页在没有 token / 加载失败时的占位（不把主窗搞成白屏，也不渲染任何包内代码）。 */
function FrameUnavailable({ appId, reason }: { appId: string; reason: string }) {
  return (
    <div
      style={{
        margin: 24,
        padding: 20,
        borderRadius: 12,
        border: `1px solid ${tokens.borderStrong}`,
        background: tokens.surface,
        color: tokens.textSecondary,
        fontSize: 13,
        lineHeight: 1.7,
      }}
    >
      <div style={{ color: tokens.text, fontWeight: 600, marginBottom: 6 }}>应用页面暂不可用</div>
      <div>
        应用 <code>{appId}</code> 的沙箱页无法加载：{reason}。
      </div>
      <div style={{ marginTop: 8, color: tokens.textPlaceholder }}>
        页面在隔离 iframe 中运行；无有效访问令牌或页面资源缺失时不会渲染任何包内代码。
      </div>
    </div>
  )
}

export interface SandboxAppFrameProps {
  /** 运行期应用页（含 frameToken；由 `deriveRuntimeAppPages` 产生）。 */
  page: RuntimeAppPage
  /** 该 app 清单声明的 permissions（决定桥放行哪些读）。缺省 = 无任何读放行。 */
  permissions?: readonly string[]
}

/**
 * 运行期应用沙箱页容器。
 *
 * `src` = `/_app/<frameToken>/index.html`（相对站内绝对路径，iframe 以主窗 origin 加载，
 * 但 sandbox 无 same-origin → 页面处于不透明源，无法触及主窗）。
 */
export function SandboxAppFrame({ page, permissions = [] }: SandboxAppFrameProps) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null)
  const [failed, setFailed] = useState<string | null>(null)

  // token 非空才渲染 iframe；token 由上游 derive 保证存在，这里再兜一次（防直接构造 props）。
  const token = page.frameToken.trim()
  const src = token ? `/_app/${encodeURIComponent(token)}/index.html` : ''

  // 桥：permissions/source 判定/限流/读实现都在这里收口。performRead 用主窗 API 代发。
  const bridge = useMemo(
    () =>
      createAppFrameBridge({
        permissions,
        // sourceMatches 在 handleMessage 内无法回填（bridge 是无状态的纯逻辑）；这里给一个
        // 惰性真值对象——真正的来源比对在下方 message 监听里先做（只有自己 iframe 的 source 才会
        // 走进 bridge.handleMessage）。此处传 true 是因为「能到这一步」已证明来源匹配。
        sourceMatches: true,
        performRead: (kind: BridgeReadKind) => {
          switch (kind) {
            case 'usage:summary':
              return usageSummary({})
            case 'providers:list':
              return listProviders()
            default:
              // allowedReads 已把未知 kind 挡在外面；走到这里是编程错误，抛出让桥回 read:error。
              throw new Error(`未支持的桥读种类：${kind}`)
          }
        },
      }),
    [permissions],
  )

  useEffect(() => {
    if (!src) return
    const onMessage = (ev: MessageEvent) => {
      const frameWin = iframeRef.current?.contentWindow
      // **来源判定**：只认自己这个 iframe 发来的消息。其余（含主窗自身、其它 frame、外站）一律忽略。
      if (!frameWin || ev.source !== frameWin) return
      void bridge.handleMessage(ev.data).then((reply) => {
        if (reply) {
          // 只发回**同一个** iframe（targetOrigin 用 '*' 是因为 sandbox 无 same-origin → 页面源是
          // 不透明源 'null'，无法用具体 origin 指定；安全性由 sandbox + 来源判定保证，发出去也到不了别人）。
          frameWin.postMessage(reply, '*')
        }
      })
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [bridge, src])

  if (!token) {
    return <FrameUnavailable appId={page.appId} reason="缺少页面访问令牌（未签发 token）" />
  }
  if (failed) {
    return <FrameUnavailable appId={page.appId} reason={failed} />
  }

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', background: tokens.bg }}>
      <iframe
        ref={iframeRef}
        title={`应用页面：${page.navLabel}`}
        // ⚠ 安全基线：只给 allow-scripts。**绝不**加 allow-same-origin / allow-top-navigation /
        //    allow-forms / allow-popups（都扩大页面能做的事）。改这一行等于改二阶 C 的信任边界。
        sandbox="allow-scripts"
        src={src}
        onError={() => setFailed('页面资源加载失败（404/网络错）')}
        style={{ flex: 1, minHeight: 0, width: '100%', border: 'none', background: tokens.bg }}
      />
    </div>
  )
}
