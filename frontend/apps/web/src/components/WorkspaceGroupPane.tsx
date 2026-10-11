import { useMemo, useState, type ReactNode } from 'react'
import { Input, Modal, Tooltip } from 'antd'
import { DownOutlined, PlusCircleOutlined, RightOutlined, WarningOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { ConversationSummary, Workspace } from '@ark-mate/types'
import { useMate } from '../store'
import { WorkspaceManageMenu, workspaceLabel } from './WorkspacePicker'

/** 折叠态的前端本地持久键（**不进 db**：它是"这台机器的视图偏好"，不是跨设备的事实）。 */
const COLLAPSED_KEY = 'mate.web.wsCollapsed'

function readCollapsed(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(COLLAPSED_KEY)
    if (!raw) return {}
    const v: unknown = JSON.parse(raw)
    return v && typeof v === 'object' ? (v as Record<string, boolean>) : {}
  } catch {
    return {}
  }
}

function writeCollapsed(v: Record<string, boolean>): void {
  try {
    localStorage.setItem(COLLAPSED_KEY, JSON.stringify(v))
  } catch {
    // 隐私模式/配额满：折叠态记不住不影响任何功能（下次回到默认展开），不打扰用户。
  }
}

/**
 * 分组的内容渲染器：由 `ConversationPane` 注入（这样"会话行长什么样"只有一份实现）。
 *
 * @param sessions 该分组下的会话（已按 pane 的口径过滤/排序/分页）
 * @param archived 是否处于归档视图（分组内的空态文案不同）
 */
export type GroupBodyRenderer = (sessions: ConversationSummary[], archived: boolean) => ReactNode

interface Props {
  /** 当前视图（active/archived）：归档视图同样按工作空间分组（归档不该换一套信息架构）。 */
  archived: boolean
  /** 视图态由 pane 负责；本组件只负责"分组 + 每组的会话体"。 */
  renderBody: GroupBodyRenderer
  /** 点分组行的「＋」＝在此工作空间新建会话（pane 负责跳转并预选）。 */
  onCreate: (workspaceId: string) => void
}

/**
 * 左栏的**工作空间分组**（v14；设计 §5.4）。
 *
 * 它是"浏览面"：看有哪些工作空间、各自有哪些会话、在这里管理（改名/删除）。
 * **选择面**是新建面板里的 `WorkspacePicker`——两处共用同一份登记表（`state.workspaces`），
 * 但职责不重叠：这里改的是"我的项目列表"，那里改的是"这条会话在哪跑"。
 *
 * 分组顺序 = 服务端的 `list_order`（登记表的顺序是唯一 durable 的用户意图）；
 * 组内顺序沿用 pane 的既有口径（置顶在前、其余按时间）。
 *
 * 折叠态存**本地**：它是这台机器的视图偏好（设计 §5.4 第 2 条），进 db 会让"我在另一台
 * 机器上折叠过"这种事变成一个需要同步的状态。
 */
export function WorkspaceGroupPane({ archived, renderBody, onCreate }: Props) {
  const workspaces = useMate((s) => s.workspaces)
  const conversations = useMate((s) => s.conversations)
  const activeId = useMate((s) => s.activeId)
  const renameWorkspace = useMate((s) => s.renameWorkspace)
  const deleteWorkspace = useMate((s) => s.deleteWorkspace)
  const moveWorkspace = useMate((s) => s.moveWorkspace)

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(readCollapsed)
  const [pendingDelete, setPendingDelete] = useState<Workspace | null>(null)
  // 就地重命名（设计 §5.5）：预填**屏幕上显示的名字**（未命名项 = 末级目录名）——
  // 确认一段没改过的文本 = 把自动名钉成一个显式标题，这是用户能预期到的结果。
  const [renaming, setRenaming] = useState<Workspace | null>(null)
  const [draft, setDraft] = useState('')

  const items = workspaces?.items ?? []
  const defaultID = workspaces?.default_id ?? ''

  // id → 会话列表（一次分桶，避免每个分组各扫一遍全表）。
  const byWorkspace = useMemo(() => {
    const m = new Map<string, ConversationSummary[]>()
    for (const c of conversations) {
      // 归属由服务端下发（历史会话也带默认工作空间的 id）——这里**不**按 cwd 归组：
      // 路径写法差异（符号链接、尾斜杠）会让匹配静默失败，出现"会话明明在却不在任何分组里"。
      const key = c.workspace_id ?? ''
      const arr = m.get(key)
      if (arr) arr.push(c)
      else m.set(key, [c])
    }
    return m
  }, [conversations])

  if (workspaces === null) {
    // 登记表未装载（服务不可达 / 老版本服务端）：不渲染分组，也不报错——
    // 会话区本身仍可用（下面的调用方会退回按时间的平铺列表）。
    return null
  }

  if (items.length === 0) {
    return (
      <div className="fg-wsgroup-note" style={{ padding: '2px 10px 8px' }}>
        还没有工作空间。在新建面板里「添加工作空间…」选一个目录即可。
      </div>
    )
  }

  const toggle = (id: string) => {
    setCollapsed((prev) => {
      const next = { ...prev, [id]: !prev[id] }
      writeCollapsed(next)
      return next
    })
  }

  const startRename = (ws: Workspace) => {
    setDraft(workspaceLabel(ws))
    setRenaming(ws)
  }

  const submitRename = () => {
    const ws = renaming
    setRenaming(null)
    if (ws) void renameWorkspace(ws.id, draft.trim())
  }

  return (
    <>
      {items.map((ws, idx) => {
        // 默认工作空间**默认折叠**（除它下面就是当前会话）：它通常是杂项会话的落点，
        // 展开会把它上面真正在用的项目挤出视口。
        const isOpen = collapsed[ws.id] === undefined
          ? ws.id !== defaultID || conversations.some((c) => c.id === activeId && c.workspace_id === ws.id)
          : !collapsed[ws.id]
        const sessions = byWorkspace.get(ws.id) ?? []
        const broken = ws.dir_status !== 'ok'
        const unnamed = !ws.title
        const editing = renaming?.id === ws.id

        return (
          <div key={ws.id}>
            <div
              className={`fg-wsgroup-row${isOpen ? ' fg-open' : ''}`}
              onClick={() => {
                // 改名期间点行内任何地方都不该顺手折叠/展开（输入框那一层已各自 stopPropagation）。
                if (!editing) toggle(ws.id)
              }}
              title={ws.path}
            >
              <span className="fg-wsgroup-caret">{isOpen ? <DownOutlined /> : <RightOutlined />}</span>
              {broken ? (
                <WarningOutlined className="fg-wsgroup-warn" />
              ) : (
                <span style={{ flex: 'none', fontSize: 12, color: tokens.textPlaceholder }}>📁</span>
              )}
              {editing ? (
                <Input
                  size="small"
                  autoFocus
                  value={draft}
                  onClick={(e) => e.stopPropagation()}
                  onChange={(e) => setDraft(e.target.value)}
                  // 只有 `↵` 落库；`Esc` 与失焦都**放弃**（与收成菜单之前的语义逐字一致：
                  // 点开一行、随手点走，不该把名字改成手滑打进去的那半截）。
                  onPressEnter={submitRename}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') setRenaming(null)
                  }}
                  onBlur={() => setRenaming(null)}
                  style={{ flex: 1, minWidth: 0, height: 22, fontSize: 12 }}
                />
              ) : (
                <span className={`fg-wsgroup-name${unnamed ? ' fg-unnamed' : ''}`}>{workspaceLabel(ws)}</span>
              )}
              {!editing && (
                <>
                  <span className="fg-wsgroup-count">{ws.conversations || ''}</span>
                  {/* 行内动作（v14.1）：`⋯` = 管理（重命名 / 移除 / 上移下移），`⊕` = 在此工作空间
                      新建会话。两者**一起悬停浮现**（对齐 dsh 左栏的工作区行），未悬停时整行只剩
                      "名字 + 会话数"——工作空间名不再被一排图标挤成省略号。 */}
                  <span className="fg-ws-manage" onClick={(e) => e.stopPropagation()}>
                    <WorkspaceManageMenu
                      ws={ws}
                      canMoveUp={idx > 0}
                      canMoveDown={idx < items.length - 1}
                      onMoveUp={() => void moveWorkspace(ws.id, items[idx - 1]?.id)}
                      onMoveDown={() => {
                        // 「下移一位」= 插到"再下一个"之前；已在末尾时用 append（beforeId 缺省）。
                        const after = items[idx + 2]?.id
                        void moveWorkspace(ws.id, after)
                      }}
                      onRename={() => startRename(ws)}
                      onDelete={() => setPendingDelete(ws)}
                    />
                    {/* 「在此工作空间新建会话」：目录不可用时禁用并给出原因（点了必然被服务端拒，不如不给）。 */}
                    <Tooltip title={broken ? '目录不可用，无法在此新建会话' : '在此工作空间新建会话'}>
                      <button
                        type="button"
                        className="fg-ws-icon-btn"
                        disabled={broken}
                        onClick={() => onCreate(ws.id)}
                      >
                        <PlusCircleOutlined />
                      </button>
                    </Tooltip>
                  </span>
                </>
              )}
            </div>

            {isOpen && broken && (
              <div className="fg-wsgroup-note">
                目录已不存在，登记仍然保留；可重命名或移除登记。
              </div>
            )}
            {isOpen && !broken && sessions.length === 0 && (
              <div className="fg-wsgroup-note">{archived ? '此工作空间没有归档会话。' : '还没有会话。'}</div>
            )}
            {isOpen && !broken && sessions.length > 0 && renderBody(sessions, archived)}
          </div>
        )
      })}

      <Modal
        open={pendingDelete !== null}
        title="移除工作空间登记"
        okText="移除登记"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        onCancel={() => setPendingDelete(null)}
        onOk={() => {
          const ws = pendingDelete
          setPendingDelete(null)
          if (ws) void deleteWorkspace(ws.id)
        }}
      >
        {pendingDelete && (
          <div style={{ fontSize: 12.5, lineHeight: 1.9 }}>
            <p style={{ margin: '0 0 8px' }}>
              将移除「{workspaceLabel(pendingDelete)}」的登记，<b>不会</b>删除目录及其中的文件：
            </p>
            <p style={{ margin: '0 0 8px', fontFamily: tokens.fontMono, fontSize: 11.5, wordBreak: 'break-all' }}>
              {pendingDelete.path}
            </p>
            <p style={{ margin: 0 }}>
              该工作空间下的 <b>{pendingDelete.conversations}</b> 个会话将回到「默认工作空间」，
              会话历史完整保留。
            </p>
          </div>
        )}
      </Modal>
    </>
  )
}
