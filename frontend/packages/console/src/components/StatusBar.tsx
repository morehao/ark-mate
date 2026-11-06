import { useLocation } from 'react-router-dom'
import { Dot, tokens } from '@ark-mate/ui'
import { railContextOf } from '../console/navView'
import { useMate } from '../store'

/**
 * 全局底部条：**只在真正出问题时**才占位——助手最近一次运行失败的说明。
 *
 * 它不再承载「会话数量」和「连接状态」：
 *   - 会话数跟着它统计的列表走，放进了左栏「会话 / 归档」tab；
 *   - 连接状态收进左栏 footer「设置」那一行的右端（紧挨设置，与左栏同一列）。
 * 这两样原先都压在横跨整个窗口的底条里，与上方左栏/主区都不同列，永远对不齐。
 *
 * 正常时本组件**不渲染任何高度**（返回 null），把 30px 让还给内容区；只有问答页且确有运行失败时
 * 才出现一条 30px 的提示。面向普通用户，不出现 eventbus / seq / attach 这类内部诊断词。
 */
export function StatusBar() {
  const pathname = useLocation().pathname
  const diag = useMate((s) => s.runtimeDiagnostics)

  // 只有确实出过问题才占位：这里回答的是"为什么跑不起来 / 为什么刚断了"。
  const err = diag?.last_error
  if (railContextOf(pathname) !== 'chat' || !err) return null

  return (
    <footer
      style={{
        flex: 'none',
        height: 30,
        display: 'flex',
        alignItems: 'center',
        gap: 6,
        padding: '0 16px',
        borderTop: `1px solid ${tokens.border}`,
        background: tokens.surface,
        fontSize: 10.5,
        color: tokens.error,
        overflow: 'hidden',
        whiteSpace: 'nowrap',
      }}
      title={[err.details, err.stderr_tail?.length ? err.stderr_tail[err.stderr_tail.length - 1] : '']
        .filter(Boolean)
        .join('\n')}
    >
      <Dot color={tokens.error} size={5} />
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
        助手最近一次运行失败（{err.code || err.class}）
      </span>
    </footer>
  )
}
