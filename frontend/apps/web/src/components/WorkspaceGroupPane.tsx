import { useMemo, useState, type ReactNode } from 'react'
import { Input, Modal, Tooltip } from 'antd'
import {
  CaretRightOutlined,
  DownOutlined,
  PlusCircleOutlined,
  UpOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { ConversationSummary, Workspace } from '@ark-mate/types'
import { useMate } from '../store'
import { GROUP_IDLE_LIMIT, quotaSessions } from '../console/railView'
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
  // 重命名走**对话框**（v14.2，对齐 dsh）：初值 = 屏幕上显示的名字（未命名项 = 末级目录名），
  // 确认一段没改过的初值 = 把自动名钉成一个显式标题。行内输入框下线的原因不是好看：
  // 行内编辑要和"点行 = 折叠/展开"抢同一个点击区，还得为它一处特殊处理，而对话框天然
  // 不改变列表的布局与高度。
  const [renaming, setRenaming] = useState<Workspace | null>(null)
  const [draft, setDraft] = useState('')
  // 组内配额是否已展开（浏览器本地的视图偏好，不进 db，与折叠态同一口径）。
  const [quotaOpen, setQuotaOpen] = useState<Record<string, boolean>>({})
  // 哪一行的 `⋯` 菜单开着：菜单打开时鼠标往往已经移到浮层上、行不再 hover，
  // 需要这条状态让锚点按钮保持可见（对齐 dsh 的 `.projectRow.menuOpen`）。
  const [menuOpenID, setMenuOpenID] = useState<string | null>(null)

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
        // 含当前会话的组：leading 的 folder 用主色——"你在哪"必须在**未悬停**时也看得见，
        // 不能只存在于悬停态里（对齐 dsh 的 folderActive）。
        const containsCurrent = sessions.some((c) => c.id === activeId)
        // 组内配额：只对空闲会话计数，运行中与当前会话不占配额。
        // 判定只有一份实现（`railView.quotaSessions`），组件不自己数。
        const quota = quotaSessions(sessions, GROUP_IDLE_LIMIT, activeId)
        const quotaExpanded = quotaOpen[ws.id] ?? false
        const rows = quotaExpanded ? sessions : quota.rows

        return (
          <div key={ws.id}>
            <div
              className={`fg-wsgroup-row${isOpen ? ' fg-open' : ''}${menuOpenID === ws.id ? ' fg-menu-open' : ''}`}
              onClick={() => toggle(ws.id)}
              title={ws.path}
            >
              {/* leading 槽：**一颗实心三角，鼠标放不放上去都是它**（v14.2 修订）。
                  上一版照 dsh 抄的是"静止 folder、悬停换三角"，用户复核后否掉了：
                  一个图标不该因为鼠标在不在一块儿就变成另一个东西。展开态由它自己旋转
                  90° 表达（▶ → ▼），所以状态照样看得见，而且比开/合文件夹在 13px 下清楚得多。
                  含当前会话的组用主色——"你在哪"必须在未悬停时也看得见。 */}
              <span className={`fg-wsgroup-lead${containsCurrent ? ' fg-current' : ''}`}>
                <CaretRightOutlined />
                {broken && <WarningOutlined className="fg-wsgroup-warn" />}
              </span>
              <span className={`fg-wsgroup-name${unnamed ? ' fg-unnamed' : ''}`}>{workspaceLabel(ws)}</span>
              <span className="fg-wsgroup-count">{ws.conversations || ''}</span>
              {/* 行内动作（v14.1）：`⋯` = 管理（重命名 / 上移下移 / 删除），`⊕` = 在此工作空间
                  新建会话。两者悬停浮现——但它们的位置**一直占着**（CSS 用 `visibility` 而不是
                  `display`），所以鼠标移上去只有这两颗按钮出现，名字不会被挤、会话数不会消失。 */}
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
                  onOpenChange={(o) => setMenuOpenID(o ? ws.id : null)}
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
            </div>

            {isOpen && broken && (
              <div className="fg-wsgroup-note">
                目录已不存在，登记仍然保留；可重命名或删除登记。
              </div>
            )}
            {isOpen && !broken && sessions.length === 0 && (
              <div className="fg-wsgroup-note">{archived ? '暂无归档会话。' : '暂无会话。'}</div>
            )}
            {isOpen && !broken && sessions.length > 0 && (
              <>
                {renderBody(rows, archived)}
                {quota.hidden > 0 && (
                  <button
                    type="button"
                    className="fg-wsgroup-more"
                    onClick={() => setQuotaOpen((prev) => ({ ...prev, [ws.id]: !quotaExpanded }))}
                  >
                    {quotaExpanded ? <UpOutlined /> : <DownOutlined />}
                    {quotaExpanded ? '收起' : `展开其余 ${quota.hidden} 个会话`}
                  </button>
                )}
              </>
            )}
          </div>
        )
      })}

      {/* 重命名对话框（v14.2）：初值 = **屏幕上显示的名字**（未命名项 = 末级目录名）。
          确认一段没改过的初值 = 把自动名钉成一个显式标题：这是用户能预期到的结果，
          也是"改标题不改身份"（`workspace_id` 不变）在界面上的兑现。 */}
      <Modal
        open={renaming !== null}
        title="重命名工作空间"
        okText="保存"
        cancelText="取消"
        width={400}
        onOk={submitRename}
        onCancel={() => setRenaming(null)}
        destroyOnHidden
      >
        {renaming && (
          <div style={{ fontSize: 12.5 }}>
            <div style={{ marginBottom: 5, color: tokens.textSecondary }}>工作空间名称</div>
            <Input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onPressEnter={submitRename}
              placeholder="工作空间名称"
              autoFocus
              maxLength={80}
            />
            <div style={{ marginTop: 8, fontSize: 11.5, color: tokens.textPlaceholder, lineHeight: 1.7 }}>
              直接确认一段没改过的话，就把这个名字钉住，此后不再跟随目录改名或界面语言。
              改名不改身份：<code style={{ fontFamily: tokens.fontMono }}>workspace_id</code> 不变。
            </div>
          </div>
        )}
      </Modal>

      {/* 删除登记：措辞对齐 dsh 的「删除工作区」，但**保留边界必须写在确认框里**——
          菜单项只有一个词的位置，说不清"删的是登记不是数据"，没人敢点。 */}
      <Modal
        open={pendingDelete !== null}
        title="删除工作空间"
        okText="删除"
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
              将把「{workspaceLabel(pendingDelete)}」从工作空间列表中移除。<b>目录与会话记录都会保留</b>：
            </p>
            <p style={{ margin: '0 0 8px', fontFamily: tokens.fontMono, fontSize: 11.5, wordBreak: 'break-all' }}>
              {pendingDelete.path}
            </p>
            <p style={{ margin: 0 }}>
              它的 <b>{pendingDelete.conversations}</b> 个会话将回到「默认工作空间」，会话历史完整保留。
            </p>
          </div>
        )}
      </Modal>
    </>
  )
}
