import { useEffect, useState } from 'react'
import { Input, Popover, Tooltip } from 'antd'
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  CheckOutlined,
  DeleteOutlined,
  EditOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  PlusOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { recentWorkspaces } from '@ark-mate/api'
import type { Workspace, WorkspaceRecent } from '@ark-mate/types'
import { useMate } from '../store'
import { DirLabel, Label, WorkspaceBrowser, splitDirPath } from './WorkspaceBrowser'

/** 当前选中：登记项 / 裸路径 / 未指定（落在默认工作空间）。 */
export interface WorkspaceChoice {
  /** 选中的登记 id（为空 = 没选登记项）。 */
  id: string | null
  /** 裸绝对路径（「输入路径…」那条路；与 id 互斥）。 */
  cwd: string | null
}

export const NO_WORKSPACE: WorkspaceChoice = { id: null, cwd: null }

interface Props {
  value: WorkspaceChoice
  onChange: (value: WorkspaceChoice) => void
  disabled?: boolean
}

/** 绝对路径判定（对齐 goose 的 isAbsolutePath：POSIX / UNC / Windows 盘符）。 */
function isAbsolutePath(p: string): boolean {
  return p.startsWith('/') || p.startsWith('\\\\') || /^[A-Za-z]:[\\/]/.test(p)
}

/** 一条登记的展示名：'' = 未命名 → 末级目录名；默认工作空间由服务端已渲染好。 */
export function workspaceLabel(ws: Workspace): string {
  if (ws.title) return ws.title
  return splitDirPath(ws.path).name || ws.path
}

/**
 * 工作空间选择器（新建会话时指定「在哪里工作」）。
 *
 * v14 重写（设计 §5.2）：事实源从"从既有会话派生的最近目录 + 白名单资料库"换成**登记表**
 * （`GET /workspaces`）。三处结构性变化，都不是外观调整：
 *   - 主列表是**登记项**（有 id、有名字、有顺序），选中它提交的是 `workspace_id` 而不是路径
 *     ——路径匹配会因符号链接/尾斜杠的写法差异失配；
 *   - 新增**创建工作空间**（原来无处可落：平台没有可写对象）；
 *   - 「最近使用」降级为「**未登记**」一栏（服务端已排除已登记与默认工作空间），
 *     点一下 = 登记 + 选中，一步完成，不留"点两次才到位"的中间态。
 *
 * 保留的既有裁剪（**没有做假的相似**，与 v13 之前逐字相同）：
 *  - **不做 Git 工作树小节**：平台没有这个集成，写一行「未找到工作树」会让人以为支持了；
 *  - **不做「在文件管理器中打开」**：服务端可能在远端，没有这个能力就不给入口。
 *
 * 语义要点：工作空间 = 会话的 ACP `cwd`，**创建时定稿、之后不可变**（它是底座会话身份的一部分，
 * 也是原生权限 `workspace-write` 的沙箱边界）——所以这个选择器只出现在新建流程，
 * 已建会话里只做只读展示（见 Composer 的 workspace 药丸）。
 */
export function WorkspacePicker({ value, onChange, disabled }: Props) {
  const showToast = useMate((s) => s.showToast)
  const workspaces = useMate((s) => s.workspaces)
  const loadWorkspaces = useMate((s) => s.loadWorkspaces)
  const createWorkspace = useMate((s) => s.createWorkspace)

  const [open, setOpen] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  // 创建失败**就地报错**并留在浏览视图（设计 §5.2 状态 E）：关掉弹层意味着用户得把目录树
  // 重新点到那一层；留在原地换一个目录即可。
  const [browseErr, setBrowseErr] = useState<string | null>(null)
  const [manual, setManual] = useState('')
  const [manualLoading, setManualLoading] = useState(false)
  const [recent, setRecent] = useState<WorkspaceRecent[]>([])
  const [busy, setBusy] = useState(false)

  // 展开时装载数据（对齐既有行为：每次打开都刷新，不在关闭态空跑请求）。
  useEffect(() => {
    if (!open) return
    setBrowsing(false)
    setBrowseErr(null)
    setManual('')
    void loadWorkspaces()
    recentWorkspaces(8)
      .then(setRecent)
      // 最近目录取不到不该挡住「登记表 + 添加工作空间 + 输入路径」三条主路径。
      .catch(() => setRecent([]))
  }, [open, loadWorkspaces])

  const items = workspaces?.items ?? []
  const writable = workspaces?.writable ?? false
  const defaultID = workspaces?.default_id ?? ''
  const defaultWS = items.find((w) => w.id === defaultID)

  // 当前选中项（登记项优先；裸路径走 cwd）。
  const current = value.id ? items.find((w) => w.id === value.id) : undefined
  const currentPath = current?.path ?? value.cwd ?? ''
  const currentName = current
    ? workspaceLabel(current)
    : value.cwd
      ? splitDirPath(value.cwd).name
      : '选择目录'

  const close = () => setOpen(false)

  const applyWorkspace = (ws: Workspace) => {
    onChange({ id: ws.id, cwd: null })
    close()
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
    setBrowseErr(null)
    try {
      // 输入路径 = 登记 + 选中（同一个动作，与「最近使用（未登记）」一致）：
      // 用户敲的是一个**项目目录**，让它在左栏有个分组是符合预期的；
      // 不想登记的话，选目录那条路也不登记（见下面 browse 的注释）。
      const ws = await createWorkspace(p)
      if (!ws) return
      setManual('')
      onChange({ id: ws.id, cwd: null })
      close()
    } finally {
      setManualLoading(false)
    }
  }

  /** 目录浏览里选定：**登记 + 选中**（与设计 ADR 的"添加工作空间只有一条路"一致）。 */
  const adoptPicked = async (path: string) => {
    setBusy(true)
    setBrowseErr(null)
    try {
      const ws = await createWorkspace(path)
      if (!ws) {
        setBrowseErr('创建失败：服务端未返回工作空间')
        return
      }
      onChange({ id: ws.id, cwd: null })
      close()
    } catch (err) {
      setBrowseErr(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  /** 「最近使用（未登记）」：一步完成登记 + 选中。 */
  const adoptRecent = async (r: WorkspaceRecent) => {
    setBusy(true)
    setBrowseErr(null)
    try {
      const ws = await createWorkspace(r.path)
      if (!ws) return
      onChange({ id: ws.id, cwd: null })
      close()
    } finally {
      setBusy(false)
    }
  }

  const menu = (
    <div className="fg-ws-pop" style={{ width: 380 }}>
      {/* 创建失败就地报错（§5.2 状态 E）：不关弹层，用户当前浏览位置还在 */}
      {browseErr && (
        <div className="fg-ws-error" role="alert">
          <WarningOutlined /> <span>{browseErr}</span>
        </div>
      )}

      <Label>当前</Label>
      <div className="fg-ws-item fg-ws-item-static">
        <FolderOpenOutlined className="fg-ws-item-icon" />
        {currentPath ? (
          <DirLabel dir={currentPath} />
        ) : (
          <span className="fg-ws-item-label">
            <span className="fg-ws-item-name">
              {defaultWS ? workspaceLabel(defaultWS) : '平台分配的目录'}
            </span>
            <span className="fg-ws-item-sub">
              {defaultWS ? defaultWS.path : '未指定工作空间时由平台分配，之后不可更改'}
            </span>
          </span>
        )}
        <CheckOutlined className="fg-ws-item-check" />
      </div>

      {/* 登记表（主列表）。空登记只可能出现在 memory 或用户删光的情形。 */}
      <div className="fg-ws-sep" />
      <Label>工作空间</Label>
      {workspaces === null && <div className="fg-ws-empty">读取工作空间…</div>}
      {workspaces !== null && items.length === 0 && (
        <div className="fg-ws-empty">
          还没有工作空间。
          <br />
          选一个目录作为你的第一个工作空间。
        </div>
      )}
      {items.map((ws) => {
        const broken = ws.dir_status !== 'ok'
        return (
          <button
            key={ws.id}
            type="button"
            className="fg-ws-item"
            title={ws.path}
            disabled={busy || broken}
            onClick={() => applyWorkspace(ws)}
          >
            {broken ? (
              <WarningOutlined className="fg-ws-item-icon" style={{ color: tokens.warning }} />
            ) : (
              <FolderOutlined className="fg-ws-item-icon" />
            )}
            <span className="fg-ws-item-label">
              <span className="fg-ws-item-name">
                {/* 未命名的项用**末级目录名**（服务端不给空标题编名字：那是语言相关的） */}
                {workspaceLabel(ws)}
              </span>
              <span className="fg-ws-item-sub">{ws.path}</span>
            </span>
            {broken && <span className="fg-ws-item-meta">目录不可用</span>}
            {ws.id === value.id && <CheckOutlined className="fg-ws-item-check" />}
          </button>
        )
      })}

      {/* 最近使用（未登记）：服务端已排除已登记与默认工作空间，所以点它一定有"登记"可做 */}
      {recent.length > 0 && (
        <>
          <div className="fg-ws-sep" />
          <Label>最近使用（未登记）</Label>
          {recent.map((r) => (
            <button
              key={r.path}
              type="button"
              className="fg-ws-item"
              title={r.path}
              disabled={busy}
              onClick={() => void adoptRecent(r)}
            >
              <FolderOutlined className="fg-ws-item-icon" />
              <DirLabel dir={r.path} />
              <span className="fg-ws-item-meta">{r.conversations} 个会话</span>
            </button>
          ))}
        </>
      )}

      {/* 添加工作空间 / 输入路径：writable=false（memory 持久层）时**整项不出现**（设计 ADR-11）
          ——不给一个必然失败的按钮，底下用一句原因说明为什么没有它。 */}
      {writable ? (
        <>
          <div className="fg-ws-sep" />
          <button
            type="button"
            className="fg-ws-item"
            disabled={busy}
            onClick={() => {
              setBrowseErr(null)
              setBrowsing(true)
            }}
          >
            <PlusOutlined className="fg-ws-item-icon" />
            <span className="fg-ws-item-label">
              <span className="fg-ws-item-name">添加工作空间…</span>
            </span>
          </button>
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
        </>
      ) : (
        workspaces !== null && (
          <div className="fg-ws-hint">
            当前持久层为 memory，工作空间不可创建（登记表只读）。
          </div>
        )
      )}

      {/* 未指定也是有工作目录的（默认工作空间），必须给回这条路，
          否则用户选了目录就回不到"让平台决定"。 */}
      {currentPath && defaultID && (
        <>
          <div className="fg-ws-sep" />
          <button
            type="button"
            className="fg-ws-item"
            onClick={() => {
              onChange(NO_WORKSPACE)
              close()
            }}
          >
            <FolderOpenOutlined className="fg-ws-item-icon" />
            <span className="fg-ws-item-label">
              <span className="fg-ws-item-name">
                使用{defaultWS ? `「${workspaceLabel(defaultWS)}」` : '平台分配的目录'}
              </span>
              <span className="fg-ws-item-sub">
                {defaultWS ? defaultWS.path : '未指定工作空间时由平台分配'}
              </span>
            </span>
          </button>
        </>
      )}
    </div>
  )

  // 浏览视图 = 共用实现（见 WorkspaceBrowser）：本组件只提供"选定后做什么"
  // （落库 + 设选中 + 收起弹层；失败留在原地报错）。
  const browseView = (
    <WorkspaceBrowser
      start={currentPath || undefined}
      onPick={(path) => void adoptPicked(path)}
      onBack={() => {
        setBrowsing(false)
        setBrowseErr(null)
      }}
      pickLabel={busy ? '登记中…' : '使用此目录'}
    />
  )

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setBrowseErr(null)
      }}
      trigger="click"
      placement="topLeft"
      content={browsing ? browseView : menu}
      arrow={false}
      styles={{ content: { padding: 6 } }}
    >
      <Tooltip
        title={
          currentPath
            ? currentPath
            : defaultWS
              ? `未指定：会话落在「${workspaceLabel(defaultWS)}」（${defaultWS.path}）`
              : '未指定：每个会话在创建时由平台分配一个独立工作目录'
        }
        placement="top"
      >
        <button type="button" className="fg-ws-pill" disabled={disabled}>
          <FolderOpenOutlined style={{ fontSize: 12, color: tokens.textPlaceholder }} />
          <span className="fg-ws-pill-name">{currentName}</span>
        </button>
      </Tooltip>
    </Popover>
  )
}

/**
 * 工作空间分组行上的管理入口（左栏用；**选择器里不做管理**，见设计 §5.1 的关键分线：
 * 左栏是浏览面、新建面板是选择面，两处共用同一份登记表但职责不重叠）。
 *
 * 顺序调整本期只做「上移/下移」两个方向（拖拽要处理"拖到后代上"这类兼容判定，
 * 成本与收益不匹配——设计 §5.5 与 ADR-12）。边界方向由调用方禁用，不给一个必然无效的按钮。
 */
export function WorkspaceManageMenu({
  ws,
  canMoveUp,
  canMoveDown,
  onMoveUp,
  onMoveDown,
  onRename,
  onDelete,
}: {
  ws: Workspace
  canMoveUp: boolean
  canMoveDown: boolean
  onMoveUp: () => void
  onMoveDown: () => void
  onRename: (title: string) => void
  onDelete: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(ws.title)

  if (editing) {
    return (
      <Input
        size="small"
        autoFocus
        value={draft}
        onClick={(e) => e.stopPropagation()}
        onChange={(e) => setDraft(e.target.value)}
        onPressEnter={() => {
          setEditing(false)
          onRename(draft.trim())
        }}
        onBlur={() => setEditing(false)}
        style={{ height: 22, fontSize: 12 }}
      />
    )
  }
  return (
    <span className="fg-ws-manage" onClick={(e) => e.stopPropagation()}>
      <button
        type="button"
        className="fg-ws-icon-btn"
        title="上移"
        disabled={!canMoveUp}
        onClick={onMoveUp}
      >
        <ArrowUpOutlined />
      </button>
      <button
        type="button"
        className="fg-ws-icon-btn"
        title="下移"
        disabled={!canMoveDown}
        onClick={onMoveDown}
      >
        <ArrowDownOutlined />
      </button>
      <button type="button" className="fg-ws-icon-btn" title="重命名" onClick={() => setEditing(true)}>
        <EditOutlined />
      </button>
      <button
        type="button"
        className="fg-ws-icon-btn"
        title={ws.is_default ? '默认工作空间不可移除' : '移除登记（不删除目录与会话）'}
        disabled={ws.is_default}
        onClick={onDelete}
      >
        <DeleteOutlined />
      </button>
    </span>
  )
}
