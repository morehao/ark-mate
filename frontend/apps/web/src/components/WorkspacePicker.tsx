import { useEffect, useState } from 'react'
import { Input, Popover, Tooltip } from 'antd'
import { BookOutlined, CheckOutlined, FolderOpenOutlined, FolderOutlined, PlusOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { browseWorkspaces, recentWorkspaces, workspaceLibraries } from '@ark/api'
import type { WorkspaceLibrary, WorkspaceRecent } from '@ark/types'
import { useForge } from '../store'
import { DirLabel, Label, WorkspaceBrowser, splitDirPath } from './WorkspaceBrowser'

interface Props {
  /** 已选工作空间绝对路径；null = 未指定（会话创建时由平台分配）。 */
  value: string | null
  onChange: (value: string | null) => void
  disabled?: boolean
}

/** 绝对路径判定（对齐 goose 的 isAbsolutePath：POSIX / UNC / Windows 盘符）。 */
function isAbsolutePath(p: string): boolean {
  return p.startsWith('/') || p.startsWith('\\\\') || /^[A-Za-z]:[\\/]/.test(p)
}

/**
 * 工作空间选择器（新建会话时指定「在哪里工作」）。
 *
 * 交互与展示对齐 goose 桌面端的 DirSwitcher（`ui/desktop/src/components/bottom_menu/DirSwitcher.tsx`）：
 * 关闭态是「图标 + 目录名」药丸（完整路径走 tooltip，hover 才显形），展开是从药丸**向上**弹出的
 * 宽菜单，小节顺序为「当前目录 → 输入路径 → 最近目录 → 选择目录…」；每一项都是两行
 * （目录名 + 上级路径）。文案也沿用它的措辞（Choose directory… / Enter path / Recent directories）。
 *
 * 两处按 ark-mate 的实际情况做了裁剪（**没有做假的相似**）：
 *  - **不做 Git 工作树小节**：goose 是真有 worktree 集成，平台目前没有；写一行「未找到工作树」
 *    会让人以为支持了。
 *  - **不做「在文件管理器中打开」**：goose 是本地桌面应用（`shell.openPath`），平台是控制面，
 *    服务端可能在远端，没有这个能力就不给这个入口。
 *  - 「选择目录…」在 Web 上无原生目录选择器（AionUi 用 webFsPicker 替代），因此改为在同一个弹层里
 *    下钻浏览（返回箭头回到菜单），这是同一交互意图的 Web 实现。
 *
 * 语义要点：工作空间 = 会话的 ACP `cwd`，**创建时定稿、之后不可变**（它是底座会话身份的一部分，
 * 也是原生权限 `workspace-write` 的沙箱边界）——所以这个选择器只出现在新建流程，
 * 已建会话里只做只读展示（见 Composer 的 workspace 药丸）。
 */
export function WorkspacePicker({ value, onChange, disabled }: Props) {
  const showToast = useForge((s) => s.showToast)
  const [open, setOpen] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const [manual, setManual] = useState('')
  const [manualLoading, setManualLoading] = useState(false)
  const [recent, setRecent] = useState<WorkspaceRecent[]>([])
  // 资料库（工作区根下的一级子目录）——只读面。目录浏览与文件清单整体交给 WorkspaceBrowser。
  const [libraries, setLibraries] = useState<WorkspaceLibrary[]>([])

  // 展开时装载数据（对齐 goose：每次打开都刷新，不在关闭态空跑请求）。
  useEffect(() => {
    if (!open) return
    setBrowsing(false)
    setManual('')
    recentWorkspaces(8)
      .then(setRecent)
      // 最近目录取不到不该挡住「输入路径 + 选择目录」两条主路径。
      .catch(() => setRecent([]))
    // 资料库来自服务端（根下的一级子目录）；未配白名单时为空数组 → 面板不显示这一节。
    workspaceLibraries()
      .then(setLibraries)
      .catch(() => setLibraries([]))
  }, [open, value])

  const apply = (path: string) => {
    onChange(path)
    setOpen(false)
  }

  const applyManual = async () => {
    const p = manual.trim()
    if (!p) return
    // 本地先挡一次明显错误（goose 同款提示），再交服务端校验 + 规范化：
    // 面板不能自己判断"这个目录能不能用"，也拿不到符号链接的真身。
    if (!isAbsolutePath(p)) {
      showToast('工作目录必须是绝对路径')
      return
    }
    setManualLoading(true)
    try {
      const resp = await browseWorkspaces(p)
      setManual('')
      apply(resp.path)
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err))
    } finally {
      setManualLoading(false)
    }
  }

  // 最近列表隐藏当前值（goose 同款过滤：已经在这了就不用再出现一次）。
  const recentDirs = recent.map((r) => r.path).filter((p) => p !== value)
  const countOf = (path: string) => recent.find((r) => r.path === path)?.conversations ?? 0

  const menu = (
    <div className="fg-ws-pop" style={{ width: 360 }}>
      <Label>当前目录</Label>
      <div className="fg-ws-item fg-ws-item-static">
        <FolderOpenOutlined className="fg-ws-item-icon" />
        {value ? (
          <DirLabel dir={value} />
        ) : (
          <span className="fg-ws-item-label">
            <span className="fg-ws-item-name">平台分配的目录</span>
            <span className="fg-ws-item-sub">会话创建时分配，之后不可更改</span>
          </span>
        )}
        <CheckOutlined className="fg-ws-item-check" />
      </div>

      {libraries.length > 0 && (
        <>
          <div className="fg-ws-sep" />
          {/* 资料库 = 运维画定的工作区根下的一级子目录（服务端零新状态派生）。
              对资料/文档场景这是主入口：选名字，而不是从家目录一路点到某个绝对路径。 */}
          <Label>资料库</Label>
          {libraries.map((lib) => (
            <button key={lib.path} type="button" className="fg-ws-item" title={lib.path} onClick={() => apply(lib.path)}>
              <BookOutlined className="fg-ws-item-icon" />
              <DirLabel dir={lib.path} />
              {lib.conversations > 0 && <span className="fg-ws-item-meta">{lib.conversations} 个会话</span>}
            </button>
          ))}
        </>
      )}

      <div className="fg-ws-sep" />
      <Label>输入路径</Label>
      <div style={{ padding: '4px 8px 6px' }}>
        <Input
          size="small"
          value={manual}
          placeholder="输入绝对路径（例如 /Users/me/workspace）"
          onChange={(e) => setManual(e.target.value)}
          onPressEnter={() => void applyManual()}
          disabled={manualLoading}
          style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}
        />
      </div>

      {recentDirs.length > 0 && (
        <>
          <div className="fg-ws-sep" />
          <Label>最近目录</Label>
          {recentDirs.map((dir) => (
            <button key={dir} type="button" className="fg-ws-item" title={dir} onClick={() => apply(dir)}>
              <FolderOutlined className="fg-ws-item-icon" />
              <DirLabel dir={dir} />
              <span className="fg-ws-item-meta">{countOf(dir)} 个会话</span>
            </button>
          ))}
        </>
      )}

      <div className="fg-ws-sep" />
      <button
        type="button"
        className="fg-ws-item"
        onClick={() => setBrowsing(true)}
      >
        <PlusOutlined className="fg-ws-item-icon" />
        <span className="fg-ws-item-label">
          <span className="fg-ws-item-name">选择目录…</span>
        </span>
      </button>
      {/* 未指定也是有工作目录的（会话创建时平台分配一个独立目录），必须给回这条路，
          否则用户选了目录就回不到"让平台决定"。 */}
      {value && (
        <button
          type="button"
          className="fg-ws-item"
          onClick={() => {
            onChange(null)
            setOpen(false)
          }}
        >
          <FolderOpenOutlined className="fg-ws-item-icon" />
          <span className="fg-ws-item-label">
            <span className="fg-ws-item-name">使用平台分配的目录</span>
            <span className="fg-ws-item-sub">每个会话独立，创建后不可更改</span>
          </span>
        </button>
      )}
    </div>
  )

  // 浏览视图 = 共用实现（见 WorkspaceBrowser）：本组件只提供"选定后做什么"（设 cwd 并收起弹层）。
  const browseView = (
    <WorkspaceBrowser
      start={value ?? undefined}
      onPick={apply}
      onBack={() => setBrowsing(false)}
      pickLabel="使用此目录"
    />
  )

  // 关闭态：图标 + 目录名（完整路径走 tooltip）——与 goose 的触发药丸一致；
  // 未指定时显示动作本身（「选择目录」），而不是编一个看着像路径的名字。
  const name = value ? splitDirPath(value).name : '选择目录'

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger="click"
      placement="topLeft"
      content={browsing ? browseView : menu}
      arrow={false}
      styles={{ content: { padding: 6 } }}
    >
      <Tooltip
        title={value ?? '未指定：每个会话在创建时由平台分配一个独立工作目录'}
        placement="top"
      >
        <button type="button" className="fg-ws-pill" disabled={disabled}>
          <FolderOpenOutlined style={{ fontSize: 12, color: tokens.textPlaceholder }} />
          <span className="fg-ws-pill-name">{name}</span>
        </button>
      </Tooltip>
    </Popover>
  )
}
