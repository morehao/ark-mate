import { useEffect, useState } from 'react'
import { Dropdown, Popover, Tooltip } from 'antd'
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  CheckOutlined,
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  MoreOutlined,
  PlusOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import type { MenuProps } from 'antd'
import { tokens } from '@ark-mate/ui'
import { toErrorMessage } from '@ark-mate/api'
import type { Workspace } from '@ark-mate/types'
import { useMate } from '../store'
import { directoryPickerKind, pickDirectoryNative } from '../directoryPicker'
import { DirectoryBrowser } from './DirectoryBrowser'
import { DirLabel, Label, splitDirPath } from './dirPath'

/**
 * 选择器的值。
 *
 * v14.3 起**面板只产生 `{id}`**（选择面收敛为"登记项 + 添加工作空间"，手输路径那条路已删）。
 * 仍带一个 `path` 是因为**别的调用方**值本身就是一个路径字面量：新建会话的「工作目录」是登记 id，
 * 而自动化的目标存的是**路径字符串**（见 `AutomationsPage` 的 `WorkspacePickField`）——它的配置
 * 不该因为"那个目录今天没被登记"而被动清空，所以选择器要能把未登记的路径原样显示为"当前"。
 */
export interface WorkspaceChoice {
  /** 选中的登记 id（null = 当前值不是登记项）。 */
  id: string | null
  /** 未登记的绝对路径（与 id 互斥；只有自动化的路径字面量会走到这里）。 */
  path?: string | null
}

interface Props {
  value: WorkspaceChoice
  onChange: (value: WorkspaceChoice) => void
  disabled?: boolean
  /**
   * 触发形态（v21）：
   *  - `chip`（默认在对话框页面）＝ 贴在输入卡上沿左上角的**事实** chip（目录图标 + 名字 + ⌄），
   *    静息无边框、hover 才出软底（对齐 DSH 的 `WorkspaceChip`）；
   *  - `pill` ＝ 底栏表单控件形态的药丸（自动化目标字段这类"填一个值"的地方仍用它）。
   */
  variant?: 'pill' | 'chip'
  /**
   * 是否允许**新建**工作空间（弹层里那条「＋ 添加工作空间…」，它会抬宿主目录面板 / 开应用内浏览框）。
   *
   * 对话框页面（新建会话面板）传 `false`：那里只允许在**既有**登记项之间切换——建目录要抬系统面板，
   * 属于左栏「＋」与设置的事，不该从输入区伸手到宿主文件系统。默认 `true`（其它调用方面不改）。
   */
  allowCreate?: boolean
}

/**
 * 库里的默认工作空间标题是**语言中立的哨兵**（Go 侧 `store.DefaultWorkspaceTitle`）；
 * 展示时映射成中文「默认」。用户改过名的默认工作空间照旧显示用户的名字。
 */
const DEFAULT_WORKSPACE_TITLE = 'default-workspace'

/**
 * 一条登记的展示名：默认工作空间读作「默认」；'' = 未命名 → 末级目录名。
 *
 * 为什么不在后端本地化：那是语言无关的 wire（跨语言切换/跨端必须一致），
 * 同一条哨兵由 Go 的 `defaultWorkspaceDisplayTitle()`（散文语域用完整名词）与这里
 * （行名/选择器/chip 用「默认」）各自映射。
 */
export function workspaceLabel(ws: Workspace): string {
  if (ws.is_default && (!ws.title || ws.title === DEFAULT_WORKSPACE_TITLE)) return '默认'
  if (ws.title) return ws.title
  return splitDirPath(ws.path).name || ws.path
}

/**
 * 工作空间选择器（新建会话时指定「在哪里工作」）。
 *
 * v14 重写：事实源从"从既有会话派生的最近目录 + 白名单资料库"换成**登记表**（`GET /workspaces`），
 * 并新增**创建工作空间**（原来无处可落：平台没有可写对象）。
 *
 * v14.3 按 dsh 的选择面收敛（设计 §2.4），菜单只剩三样东西：当前 / 工作空间登记 / 添加工作空间…。
 * 删掉的两条各有理由，都不是"少写一点"：
 *   - **「输入路径」**：手输绝对路径要求用户先知道白名单边界与规范化规则（本地只能预检
 *     "是不是绝对路径"，真校验在服务端），失败率高；而它想解决的问题（"新项目还没建出来"）
 *     已由目录对话框的**新建文件夹**覆盖——用户要的是"那里有个目录"，不是"证明我知道绝对路径"。
 *   - **「最近使用（未登记）」**：v14 起登记表才是主列表，而这一栏剩下的恰恰是**没被登记**的目录；
 *     点它 = 登记 + 选中，与「添加工作空间…」是同一个动作的两个入口，留着只多一层判断
 *     （对齐 dsh：选择面只有登记项 + 一条添加）。
 *
 * 保留的既有裁剪（**没有做假的相似**）：**不做 Git 工作树小节**（平台没有这个集成，写一行
 * 「未找到工作树」会让人以为支持了）；**不做「在文件管理器中打开」**（服务端可能在远端）。
 *
 * 语义要点：工作空间 = 会话的 ACP `cwd`，**创建时定稿、之后不可变**（它是底座会话身份的一部分，
 * 也是原生权限 `workspace-write` 的沙箱边界）——所以这个选择器只出现在新建流程，
 * 已建会话里只做只读展示（见 Composer 的 workspace 药丸）。
 */
export function WorkspacePicker({ value, onChange, disabled, variant = 'pill', allowCreate = true }: Props) {
  const showToast = useMate((s) => s.showToast)
  const workspaces = useMate((s) => s.workspaces)
  const loadWorkspaces = useMate((s) => s.loadWorkspaces)
  const createWorkspace = useMate((s) => s.createWorkspace)

  const chip = variant === 'chip'
  const [open, setOpen] = useState(false)
  const [browsing, setBrowsing] = useState(false)
  const [busy, setBusy] = useState(false)

  // 展开时装载数据（对齐既有行为：每次打开都刷新，不在关闭态空跑请求）。
  useEffect(() => {
    if (!open) return
    setBrowsing(false)
    void loadWorkspaces()
  }, [open, loadWorkspaces])

  const items = workspaces?.items ?? []
  const writable = workspaces?.writable ?? false
  const defaultID = workspaces?.default_id ?? ''
  const defaultWS = items.find((w) => w.id === defaultID)

  // 当前选中项（登记 id 优先；未登记的路径字面量只有自动化的目标会走到）。
  const current = value?.id ? items.find((w) => w.id === value.id) : undefined
  const currentPath = current?.path ?? value?.path ?? ''
  const currentName = (() => {
    if (current) return workspaceLabel(current)
    if (value?.path) return splitDirPath(value.path).name
    // 未显式选中 = 会话落在**默认工作空间**（不是"什么都没选"）：chip 与弹层「当前」那一节
    // 读法一致，不给它留一个"选择工作空间"的空态去假装它不是个值；药丸形态保持原样。
    if (!chip) return '选择目录'
    return defaultWS ? workspaceLabel(defaultWS) : '平台分配的目录'
  })()

  const close = () => setOpen(false)

  const applyWorkspace = (ws: Workspace) => {
    onChange({ id: ws.id })
    close()
  }

  /**
   * 「添加工作空间…」：**一个动作、两态**（v14.4，对齐 dsh 的 `directory-picker-auto`）。
   *
   *  - 宿主能抬系统面板（本机浏览器与桌面窗口都算）→ 调宿主端点抬原生选目录器，拿到路径即登记；
   *  - 否则（远程 / SSH / 无显示会话）→ 开应用内对话框。
   *
   * 菜单在这一步就要收起（原生选择器是**另开一个窗口**，菜单留在下面会显得点击没反应）。
   */
  const chooseDirectory = async () => {
    close()
    if (directoryPickerKind() === 'native') {
      const picked = await pickDirectoryNative()
      // 取消不是错误：静默收手（与 dsh 的 `path === null → onCancel` 同义）。
      if ('cancelled' in picked) return
      await adoptPicked(picked.path)
      return
    }
    setBrowsing(true)
  }

  /** 目录对话框里选定：**登记 + 选中**（ADR：「添加工作空间只有一条路」）。 */
  const adoptPicked = async (path: string) => {
    setBusy(true)
    try {
      const ws = await createWorkspace(path)
      onChange({ id: ws.id })
      setBrowsing(false)
      close()
    } catch (err) {
      // 对话框是模态的：失败用 toast 报（它关闭后没有"留在原地"可谈），原因用服务端 msg。
      showToast(toErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const menu = (
    <div className="fg-ws-pop" style={{ width: 380 }}>
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
          {/* 能不能建由这一面说了算：对话框页面只切换既有项，所以指路到左栏，不在这里假装有入口。 */}
          {allowCreate ? '选一个目录作为你的第一个工作空间。' : '可在左栏工作空间标题的「＋」添加。'}
        </div>
      )}
      {items.map((ws) => {
        const broken = ws.dir_status !== 'ok'
        return (
          <button
            key={ws.id}
            type="button"
            className="fg-ws-item"
            title={ws.is_default ? workspaceLabel(ws) : ws.path}
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
              {/* 默认工作空间不展示绝对路径（需求 4）：那行只说明"这是平台给的目录"，
                  路径本身在「设置 · 工作空间」里才是主题。 */}
              <span className="fg-ws-item-sub">{ws.is_default ? '平台分配' : ws.path}</span>
            </span>
            {broken && <span className="fg-ws-item-meta">目录不可用</span>}
            {ws.id === value?.id && <CheckOutlined className="fg-ws-item-check" />}
          </button>
        )
      })}

      {/* 添加工作空间：**由调用方决定这一面有没有创建权**（对话框页面没有），
          其次 writable=false（memory 持久层）时也整项不出现（设计 ADR-11）——两处都不给
          一个必然失败的按钮，后者底下用一句原因说明为什么没有它。 */}
      {allowCreate && writable && (
        <>
          <div className="fg-ws-sep" />
          <button
            type="button"
            className="fg-ws-item"
            disabled={busy}
            onClick={() => void chooseDirectory()}
          >
            <PlusOutlined className="fg-ws-item-icon" />
            <span className="fg-ws-item-label">
              <span className="fg-ws-item-name">添加工作空间…</span>
            </span>
          </button>
        </>
      )}
      {allowCreate && !writable && workspaces !== null && (
        <div className="fg-ws-hint">当前持久层为 memory，工作空间不可创建（登记表只读）。</div>
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
              onChange({ id: null })
              close()
            }}
          >
            <FolderOpenOutlined className="fg-ws-item-icon" />
            <span className="fg-ws-item-label">
              <span className="fg-ws-item-name">
                使用{defaultWS ? `「${workspaceLabel(defaultWS)}」` : '平台分配的目录'}
              </span>
              <span className="fg-ws-item-sub">
                {defaultWS ? '平台分配' : '未指定工作空间时由平台分配'}
              </span>
            </span>
          </button>
        </>
      )}
    </div>
  )

  return (
    <>
      <Popover
        open={open}
        onOpenChange={setOpen}
        trigger="click"
        // chip 在输入卡**上沿**，弹层必须朝下开；药丸在底栏，朝上开（各自贴着自己那侧）。
        placement={chip ? 'bottomLeft' : 'topLeft'}
        content={menu}
        arrow={false}
        styles={{ content: { padding: 6 } }}
      >
        <Tooltip
          title={
            currentPath
              ? currentPath
              : defaultWS
                ? `未指定：会话落在「${workspaceLabel(defaultWS)}」`
                : '未指定：每个会话在创建时由平台分配一个独立工作目录'
          }
          // chip 的弹层朝下，tooltip 朝上会被挡；两者错开。
          placement={chip ? 'bottom' : 'top'}
        >
          <button
            type="button"
            className={chip ? 'fg-ws-chip' : 'fg-ws-pill'}
            disabled={disabled}
            aria-expanded={open}
            aria-haspopup="menu"
          >
            {currentPath ? (
              <FolderOpenOutlined style={{ fontSize: chip ? 16 : 12, color: chip ? tokens.textSecondary : tokens.textPlaceholder }} />
            ) : (
              <FolderOutlined style={{ fontSize: chip ? 16 : 12, color: chip ? tokens.textSecondary : tokens.textPlaceholder }} />
            )}
            <span className={chip ? 'fg-ws-chip-name' : 'fg-ws-pill-name'}>{currentName}</span>
            {chip && <DownOutlined style={{ fontSize: 11, color: tokens.textPlaceholder }} />}
          </button>
        </Tooltip>
      </Popover>
      {/* 目录对话框与菜单分开：菜单关闭后它仍在（选定失败要能继续挑别的目录）。
          start 传当前选中目录：从"我上次选的那一层"开始，而不是每次都回到家目录。 */}
      {allowCreate && browsing && (
        <DirectoryBrowser
          start={currentPath || undefined}
          title="添加工作空间"
          pickLabel={busy ? '登记中…' : '使用此目录'}
          busy={busy}
          onPick={(p) => void adoptPicked(p)}
          onCancel={() => setBrowsing(false)}
        />
      )}
    </>
  )
}

/**
 * 只读的工作空间 chip（会话页输入卡左上角）。
 *
 * 与 `WorkspacePicker` 的 chip 形态**同一个类**（`.fg-ws-chip`），但它是**事实回声**而不是控件：
 * 已建会话的工作空间在创建时定稿、之后不可变（它是底座 ACP session 的 cwd，也是原生权限
 * `workspace-write` 的沙箱边界），所以这里没有下拉、没有 hover 反馈——要换目录就新开一个会话。
 *
 * 形态对齐 DSH：它的工作空间 chip 在首条消息之后也是锁定的静态回声（`.workspace:disabled`）。
 */
export function WorkspaceChipStatic({ path, temporary, title }: { path: string; temporary?: boolean; title?: string }) {
  // `temporary` 是服务端的权威标志（= 落在默认工作空间上），不是从路径形状反推的。
  // 默认工作空间在**所有面向用户的名字位**读作「默认」，且不回显绝对路径（需求 4）。
  return (
    <Tooltip
      title={
        <span style={{ fontFamily: tokens.fontMono, fontSize: 11 }}>
          {temporary ? '默认工作空间 · 平台默认目录' : title ?? path}
          {temporary && (
            <>
              <br />
              <span style={{ fontFamily: tokens.fontFamily, color: tokens.textPlaceholder }}>
                平台分配：会话创建时自动分配，创建后不可更改
              </span>
            </>
          )}
        </span>
      }
      placement="top"
    >
      <span className="fg-ws-chip fg-ws-chip-static">
        <FolderOpenOutlined style={{ fontSize: 16, color: tokens.textSecondary }} />
        <span className="fg-ws-chip-name">
          {temporary ? '默认' : title || splitDirPath(path).name || path}
        </span>
      </span>
    </Tooltip>
  )
}

/**
 * 工作空间分组行上的管理入口（左栏用；**选择器里不做管理**，见设计 §5.1 的关键分线：
 * 左栏是浏览面、新建面板是选择面，两处共用同一份登记表但职责不重叠）。
 *
 * v14.1：四个行内图标按钮收成一颗 `⋯` 菜单（对齐 dsh 左栏工作区行的右侧交互：
 * `⋯` = 管理、`⊕` = 在此新建；两个动作**并排**但词与职责都不同）。收进菜单的另一个好处是
 * 主行只剩"名字 + 会话数"，长工作空间名不再被四颗图标挤掉。
 *
 * 顺序调整本期仍只做「上移/下移」两个方向（拖拽要处理"拖到后代上"这类兼容判定，
 * 成本与收益不匹配——设计 §5.5 与 ADR-12）。边界方向禁用，不给一个必然无效的菜单项；
 * 默认工作空间不可移除（设计 ADR-4），同样以 disabled 表达而不是点了再报错。
 */
export function WorkspaceManageMenu({
  ws,
  canMoveUp,
  canMoveDown,
  onMoveUp,
  onMoveDown,
  onRename,
  onDelete,
  onOpenChange,
}: {
  ws: Workspace
  canMoveUp: boolean
  canMoveDown: boolean
  onMoveUp: () => void
  onMoveDown: () => void
  /** 重命名由调用方接管（要开一个对话框，见 `WorkspaceGroupPane`）。 */
  onRename: () => void
  onDelete: () => void
  /**
   * 菜单开合上报。调用方据此给所在行挂 `fg-menu-open`：鼠标移进浮层后行不再 hover，
   * 没有这条锚点按钮会跟着隐形（浮层还在、锚点空了）。
   */
  onOpenChange?: (open: boolean) => void
}) {
  const items: MenuProps['items'] = [
    { key: 'rename', label: '重命名', icon: <EditOutlined /> },
    { type: 'divider' },
    { key: 'up', label: '上移', icon: <ArrowUpOutlined />, disabled: !canMoveUp },
    { key: 'down', label: '下移', icon: <ArrowDownOutlined />, disabled: !canMoveDown },
    { type: 'divider' },
    {
      key: 'delete',
      // 措辞对齐 dsh 的「删除工作区」（v14.2）。菜单项只有一个词的位置，说不清
      // "删的是登记不是数据"——保留边界由 WorkspaceGroupPane 的确认框负责。
      label: '删除工作区',
      icon: <DeleteOutlined />,
      danger: true,
      disabled: ws.is_default,
    },
  ]
  const onClick: MenuProps['onClick'] = ({ key }) => {
    if (key === 'up') onMoveUp()
    else if (key === 'down') onMoveDown()
    else if (key === 'rename') onRename()
    else if (key === 'delete') onDelete()
  }
  return (
    <Dropdown menu={{ items, onClick }} trigger={['click']} placement="bottomRight" onOpenChange={onOpenChange}>
      <button
        type="button"
        className="fg-ws-icon-btn"
        title="更多操作"
        aria-label="更多操作"
        onClick={(e) => e.stopPropagation()}
      >
        <MoreOutlined />
      </button>
    </Dropdown>
  )
}
