import { useCallback, useEffect, useRef, useState } from 'react'
import { fmtShortDateTime } from '../console/time'
import { Button, Input, Modal, Tooltip } from 'antd'
import type { InputRef } from 'antd'
import {
  ArrowUpOutlined,
  CheckOutlined,
  RightOutlined,
  EditOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  HomeOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { browseWorkspaces, createWorkspaceDir, toErrorMessage, workspaceFiles } from '@ark-mate/api'
import type { WorkspaceBrowseResp, WorkspaceEntry, WorkspaceFileEntry } from '@ark-mate/types'

interface Props {
  /** 起始目录（缺省 = 服务端首个工作区根，未配白名单时 = 服务进程家目录）。 */
  start?: string
  /**
   * 是否提供「新建文件夹」。
   *
   * 选择目录的调用方都要它：**这是建新目录的唯一入口**——选择面删掉手输路径之后，
   * "新项目还没建出来"这件事只能在这里解决（对齐 dsh：「添加工作空间只有一条路，
   * 因为选目录器自身就有新建文件夹」）。
   */
  create?: boolean
  /** 对话框标题（两处调用方的措辞不同：选择目录 / 选择一个目录加入白名单）。 */
  title?: string
  /** 确认按钮文案（使用此目录 / 加入白名单）。 */
  pickLabel?: string
  /** 调用方的确认在途：整个对话框冻结（行不可点、按钮禁用）。 */
  busy?: boolean
  /** 选定**当前所在目录**（若选中了某一行，则是那一行）。 */
  onPick: (path: string) => void
  onCancel: () => void
}

/** 目录名 + 上级路径两行（对齐 goose DirSwitcher 的 DirNameLabel）。 */
export function splitDirPath(dir: string): { name: string; parent: string } {
  const normalized = dir.replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/)
  const name = parts.pop() || dir
  const parent = parts.join('/')
  return { name, parent }
}

/** 两行目录项（目录名 + 上级路径）。 */
export function DirLabel({ dir }: { dir: string }) {
  const { name, parent } = splitDirPath(dir)
  return (
    <span className="fg-ws-item-label">
      <span className="fg-ws-item-name">{name}</span>
      {parent && <span className="fg-ws-item-sub">{parent}</span>}
    </span>
  )
}

/** 路径末段（新建目录的默认落点名）与平台分隔符。 */
function lastSegment(path: string): string {
  return splitDirPath(path).name
}

/** 文件大小（资料场景要一眼看出"这是不是那个大文件"）。 */
function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`
}

/** 一栏目录行（Miller 视图渲染一到两栏）。 */
function DirColumn({
  entries,
  selectedPath,
  busy,
  onPick,
}: {
  entries: WorkspaceEntry[]
  selectedPath: string | null
  busy: boolean
  onPick: (e: WorkspaceEntry) => void
}) {
  return (
    <div className="fg-ws-col" role="list">
      {entries.map((e) => {
        const selected = e.path === selectedPath
        return (
          // 外层承载列表语义，内层保持原生 button（辅助技术才看得到一个可操作控件）。
          <span key={e.path} role="listitem" className="fg-ws-rowseat">
            <button
              type="button"
              className={`fg-ws-item${selected ? ' fg-ws-rowsel' : ''}`}
              title={e.path}
              aria-current={selected || undefined}
              disabled={busy}
              onClick={() => onPick(e)}
            >
              {selected ? (
                <FolderOpenOutlined className="fg-ws-item-icon fg-ws-open" />
              ) : (
                <FolderOutlined className="fg-ws-item-icon" />
              )}
              <span className="fg-ws-item-label">
                <span className="fg-ws-item-name">{e.name}</span>
              </span>
              <RightOutlined className="fg-ws-item-chev" />
            </button>
          </span>
        )
      })}
    </div>
  )
}

/**
 * 目录浏览对话框（v14.3；对齐 dsh 的 DirectoryBrowser）。
 *
 * **唯一一份**目录浏览实现：新建会话的「添加工作空间…」与设置 · 白名单的「选择目录…」
 * 共用它，两处只有标题与确认按钮文案不同。分开写两份必然漂移，而漂移的代价是
 * "这处能选到、那处选不到"这种没有解释余地的 bug。
 *
 * 与它替换掉的 `WorkspaceBrowser`（一个 360px 的内嵌小面板）的差别，都不是外观调整：
 *   - 头部从"三颗图标 + 一行路径"换成**面包屑**（服务端下发完整祖先链，逐级可跳）——
 *     用户得知道自己在哪、并且能一跳回去；
 *   - 列表从"点进去 = 整层替换"换成 **Miller 双栏**（左 = 当前层，右 = 选中项的子目录）——
 *     相邻两层的对照关系正是选目录这个动作需要的；
 *   - 新增**路径编辑**（点面包屑右侧的铅笔区即变输入框，回车一跳）与**新建文件夹**；
 *   - 「显示隐藏文件」是**开关**（服务端按 `hidden` 过滤，切换即重读）。
 *
 * 调用方以 `open && <DirectoryBrowser …/>` 挂载（而不是让它常驻 + 收一个 `open`）：每次打开都是
 * 全新会话，上一次的浏览位置、草稿与在途请求一并消失——这正是"再打开一次"应当有的语义。
 *
 * 刻意不做（与 dsh 的偏离，理由写在这里免得后来者以为是漏了）：
 *   - **不做"输入即预览"**：dsh 的路径编辑会按草稿逐段把视图跟过去（防抖 + 焦点回停的一整套草稿态机）。
 *     本仓没有键盘重度用户这条诉求，实现它会把组件复杂度翻一倍，收益不匹配；这里只做"回车提交一次跳转"。
 *   - **不做原生目录选择器**：Web 控制台与桌面壳共用同一份产物，走原生会把两态分成两套测试面。
 */
export function DirectoryBrowser({
  start,
  create = true,
  title = '选择目录',
  pickLabel = '使用此目录',
  busy = false,
  onPick,
  onCancel,
}: Props) {
  // 当前层（左栏）。null = 还在读第一层。
  const [listing, setListing] = useState<WorkspaceBrowseResp | null>(null)
  // 选中的行（右栏 = 它的子目录）。null = 单栏。
  const [selected, setSelected] = useState<WorkspaceEntry | null>(null)
  // 选中项的子目录（null = 正在读，或读失败后已退回单栏）。
  const [child, setChild] = useState<WorkspaceBrowseResp | null>(null)
  // 本目录的**文件**清单（只读展示：文件不是合法工作空间，点不得）。
  const [fileMeta, setFileMeta] = useState<WorkspaceFileEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showHidden, setShowHidden] = useState(false)
  // 路径编辑草稿（null = 面包屑态）。
  const [pathDraft, setPathDraft] = useState<string | null>(null)
  // 新建文件夹的二级对话框（null = 关闭）。
  const [folderDraft, setFolderDraft] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  // 连点目录时后发的请求可能先回来：只认最后一次（goose DirSwitcher 的 refreshVersionRef 同款），
  // 否则会停在"点 A 显示 B"的错目录上，而确认按钮正好会把这个错目录提交上去。
  const seqRef = useRef(0)
  const pathInputRef = useRef<InputRef>(null)
  // IME 组字期间的回车是"选字"，不是"提交"。
  const composingRef = useRef(false)

  /** 列一层目录（+ 该层的文件清单）。每次调用自增序号，晚到的响应自行作废。 */
  const loadLevel = useCallback(async (path: string | undefined, hidden: boolean) => {
    const seq = ++seqRef.current
    setLoading(true)
    setError(null)
    try {
      const resp = await browseWorkspaces(path, hidden)
      if (seq !== seqRef.current) return
      setListing(resp)
      try {
        // 用服务端规范化后的 path 去列文件：保证"看到的文件"与"显示的目录"必然是同一个目录。
        const f = await workspaceFiles(resp.path, hidden)
        if (seq !== seqRef.current) return
        setFileMeta(f.entries.filter((e) => !e.is_dir))
      } catch {
        // 文件清单取不到不影响选目录（那是主路径）：少显示一节，不弹错。
        if (seq !== seqRef.current) return
        setFileMeta([])
      }
    } catch (err) {
      if (seq !== seqRef.current) return
      setError(toErrorMessage(err))
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [])

  // 打开即从起点读第一层（组件按 open 挂载，所以这里等价于"每次打开都重新开始"）。
  useEffect(() => {
    void loadLevel(start, false)
  }, [start, loadLevel])

  /** 跳到一个目录（面包屑 / 上级 / 主目录 / 提交路径 / 工作区根）：回到单栏。 */
  const navigate = useCallback(
    (path: string | undefined, hidden = showHidden) => {
      setPathDraft(null)
      setSelected(null)
      setChild(null)
      void loadLevel(path, hidden)
    },
    [loadLevel, showHidden],
  )

  /** 选中一行：右栏列出它的子目录。子目录读失败 → 退回单栏（宁可少一栏，也不要一个空的右栏）。 */
  const select = useCallback(async (entry: WorkspaceEntry) => {
    const seq = ++seqRef.current
    setSelected(entry)
    // 右栏先**乐观**填上当前层的子目录集（读它要一个往返；空着会让点击像没反应），
    // 响应回来再替换成该目录真正的 children。
    setChild((prev) => (prev ? { ...prev, path: entry.path, crumbs: undefined } : null))
    setLoading(true)
    setError(null)
    try {
      const resp = await browseWorkspaces(entry.path, showHidden)
      if (seq !== seqRef.current) return
      setChild(resp)
    } catch (err) {
      if (seq !== seqRef.current) return
      setError(toErrorMessage(err))
      setSelected(null)
      setChild(null)
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [showHidden])

  /** 从右栏往下钻一层：右栏成为新的左栏，并选中被点的行。 */
  const drill = useCallback(
    (entry: WorkspaceEntry) => {
      setChild((currentChild) => {
        if (currentChild) setListing(currentChild)
        return currentChild
      })
      void select(entry)
    },
    [select],
  )

  /** 新建文件夹：建在**选中项**下（没选中就建在当前层），成功后落到该层并选中新目录。 */
  const confirmCreate = async () => {
    const name = folderDraft ?? ''
    if (name.trim() === '' || creating) return
    const parent = selected?.path ?? listing?.path
    if (!parent) return
    setCreating(true)
    setCreateError(null)
    try {
      const resp = await createWorkspaceDir(parent, name)
      setFolderDraft(null)
      setPathDraft(null)
      // 落到目标层并选中新目录（对齐 dsh：创建后的落点就是它，用户不必再点一次）。
      const fresh = await browseWorkspaces(parent, showHidden)
      seqRef.current += 1 // 这次读是"回到目标层"，不看序号
      setListing(fresh)
      await select({ name: lastSegment(resp.path), path: resp.path })
    } catch (err) {
      // 失败**就地报错**（二级对话框不关）：改名重试是这里的正常动作。
      setCreateError(toErrorMessage(err))
    } finally {
      setCreating(false)
    }
  }

  const entries = (listing?.entries ?? []).filter((e) => showHidden || !e.hidden)
  const childEntries = (child?.entries ?? [])
    .filter((e) => showHidden || !e.hidden)
    // 右栏是"选中项的 children"，左栏已经列过的是**当前层**——两者内容相同（乐观填充那一瞬），
    // 但语义不同，不去重（去掉会让右栏在响应回来前是空的，而它正是"下一步能去哪"的预览）。
    .filter((e) => e.path !== selected?.path)
  const crumbs = listing?.crumbs ?? []
  // 确认目标：选中的行优先，否则当前层（对齐 dsh 的 `selected?.path ?? parent?.path`）。
  const targetPath = selected?.path ?? listing?.path ?? null
  const draftPending = pathDraft !== null
  const frozen = busy || loading
  const home = listing?.home && listing.home !== listing.path ? listing.home : undefined
  const allRoots = listing?.roots ?? []
  // 工作区根小节的**入口意义**：它解答"为什么上不去了、允许工作的位置在哪"。
  // 只有两种情形配得上占一行：① 当前层不是根——列它下面的一级根（一跳过去）；
  // ② 不限制（没有根）——不该有这一节。已经处在某个根上时列它的**祖先**，
  // 等于给出一串点了必被拒（或跳到根外）的入口，所以不显示（对齐 dsh：菜单里不摆不可选项）。
  const rootEntries = allRoots.filter((r) => r !== listing?.path && (listing?.path === undefined || r.startsWith(`${listing.path}/`) || r.startsWith(`${listing.path}\\`)))
  // 显示用的完整路径：白名单下 crumbs 被裁到根为止，此时只拼链尾（"ws"）会让人以为这就是全文。
  // 从根拼起即可还原全部可用信息——拼法由 crumb 自身决定（POSIX 根是 "/"，Windows 盘符形如 "C:\"），
  // 前端不猜平台。
  const currentPath = draftPending ? (pathDraft ?? '') : (listing?.path ?? '')
  const twoPane = selected !== null

  return (
    <Modal
      open
      title={title}
      onCancel={onCancel}
      width={680}
      className="fg-ws-dlg"
      maskClosable={!busy}
      keyboard={!busy}
      destroyOnHidden
      footer={
        <div className="fg-ws-foot">
          {create && (
            <Button
              disabled={!listing || frozen || draftPending}
              onClick={() => {
                setFolderDraft('')
                setCreateError(null)
              }}
            >
              新建文件夹
            </Button>
          )}
          <button
            type="button"
            className={`fg-ws-toggle${showHidden ? ' fg-on' : ''}`}
            aria-pressed={showHidden}
            disabled={frozen}
            onClick={() => {
              const next = !showHidden
              setShowHidden(next)
              // 隐藏项由服务端过滤（hidden=false 时不下发），所以切换要重读一次——
              // 换来的是"打开开关就有东西"而不是"先看到一个空列表"。
              navigate(listing?.path, next)
            }}
          >
            显示隐藏文件
            {showHidden && <CheckOutlined />}
          </button>
          <span className="fg-ws-foot-gap" />
          <Button onClick={onCancel} disabled={busy}>
            取消
          </Button>
          <Button
            type="primary"
            disabled={!targetPath || frozen || draftPending}
            onClick={() => targetPath && onPick(targetPath)}
          >
            {pickLabel}
          </Button>
          {/* 上级 / 主目录与动作同一排（对齐 dsh 的 footerBar 只放确认与取消：
              本仓多这两颗是因为面包屑会被长路径挤到只剩尾段，"回去一层"必须有一个稳定入口）。 */}
          <button
            type="button"
            className="fg-ws-icon-btn"
            title="上级目录"
            disabled={!listing?.parent || frozen}
            onClick={() => navigate(listing?.parent)}
          >
            <ArrowUpOutlined />
          </button>
          <button
            type="button"
            className="fg-ws-icon-btn"
            title="主目录"
            disabled={!home || frozen}
            onClick={() => navigate(home)}
          >
            <HomeOutlined />
          </button>
        </div>
      }
    >
      <div className="fg-ws-dlg-body">
        {/* 头部：面包屑（或路径编辑框） + 上级/主目录两颗图标。 */}
        <div className="fg-ws-crumbbar">
          {draftPending ? (
            <Input
              ref={pathInputRef}
              size="small"
              className="fg-ws-pathinput"
              value={pathDraft ?? ''}
              placeholder="输入绝对路径后回车"
              autoFocus
              onChange={(e) => setPathDraft(e.target.value)}
              onCompositionStart={() => {
                composingRef.current = true
              }}
              onCompositionEnd={() => {
                composingRef.current = false
              }}
              onPressEnter={() => {
                if (!composingRef.current) {
                  const p = (pathDraft ?? '').trim()
                  if (p === '') setPathDraft(null)
                  else navigate(p)
                }
              }}
              onKeyDown={(e) => {
                if (e.key === 'Escape') {
                  e.stopPropagation() // Esc 收回编辑态，而不是关掉对话框
                  setPathDraft(null)
                }
              }}
            />
          ) : (
            <>
              <span className="fg-ws-crumbs" role="navigation">
                {crumbs.map((c, i) => (
                  <span key={c.path} className="fg-ws-crumbseat">
                    {i > 0 && <RightOutlined className="fg-ws-crumbchev" />}
                    <button
                      type="button"
                      className="fg-ws-crumb"
                      title={c.path}
                      disabled={frozen}
                      onClick={() => navigate(c.path)}
                    >
                      {c.name}
                    </button>
                  </span>
                ))}
                {crumbs.length === 0 && <span className="fg-ws-crumb">…</span>}
              </span>
              {/* 面包屑右侧的空白区就是"改路径"的入口：铅笔图示 + 整条 hover 点亮说明这一点。
                  一个必须靠猜的隐形热区是这里唯一能打字的路，所以它必须是**看得见**的。 */}
              <Tooltip title="输入路径">
                <button
                  type="button"
                  className="fg-ws-crumbedit"
                  aria-label="输入路径"
                  disabled={frozen}
                  onClick={() => {
                    // 预填当前目录并以分隔符结尾：接着打的就是子目录名（对齐 dsh 的种子行为）。
                    // 用 listing.path（**未裁剪的全文**）而不是显示用的 crumbPath：编辑框是"提交绝对路径"
                    // 的地方，被白名单裁短过的链串提交上去会落在别的目录。
                    const sep = (listing?.path ?? '').includes('\\') ? '\\' : '/'
                    setPathDraft(currentPath && !currentPath.endsWith(sep) ? currentPath + sep : currentPath)
                  }}
                >
                  <EditOutlined />
                </button>
              </Tooltip>
            </>
          )}
        </div>

        {/* 完整路径（面包屑会被长路径挤到只剩尾段，这里给一字不差的全文；太长时左右滚动）。
            显示的是**绝对路径全文**（`listing.path`），不是面包屑拼串：白名单下 crumbs 被裁到根为止，
            拿链拼出来的只是根的名字，而"我到底在磁盘的哪儿"要靠全文回答。
            编辑态下不显示：那时输入框里是**草稿**，再显示一行"当前目录"会让人分不清哪个是要提交的那个。 */}
        {!draftPending && (
          <div className="fg-ws-dlg-path" title={currentPath}>
            {currentPath || '…'}
          </div>
        )}

        {/* 已配置工作区根：把根直接列成入口。既省去"从家目录一层层点下去"，
            也让服务端收窄过的边界在界面上可见（否则"上级"按钮为什么突然灰了会是个谜）。 */}
        {!twoPane && rootEntries.length > 0 && (
          <>
            <div className="fg-ws-sep" />
            <div className="fg-ws-label">工作区根</div>
            <div className="fg-ws-roots">
              {rootEntries.map((r) => (
                <button
                  key={r}
                  type="button"
                  className={`fg-ws-item${r === listing?.path ? ' fg-ws-rowsel' : ''}`}
                  title={r}
                  disabled={frozen}
                  onClick={() => navigate(r)}
                >
                  <FolderOpenOutlined className="fg-ws-item-icon" />
                  <DirLabel dir={r} />
                  {r === listing?.path && <CheckOutlined className="fg-ws-item-check" />}
                </button>
              ))}
            </div>
          </>
        )}

        <div className={`fg-ws-miller${twoPane ? '' : ' fg-single'}`}>
          <DirColumn
            entries={entries}
            selectedPath={selected?.path ?? null}
            busy={frozen}
            onPick={(e) => void select(e)}
          />
          {twoPane && <span className="fg-ws-divider" />}
          {twoPane && <DirColumn entries={childEntries} selectedPath={null} busy={frozen} onPick={drill} />}
          {!twoPane && listing && entries.length === 0 && !loading && (
            <div className="fg-ws-empty">此目录下没有子目录</div>
          )}
        </div>

        {/* 弹簧：把两颗图标与动作条顶到最下沿（内容多寡都不改变它们的位置）。 */}
        <div className="fg-ws-grow" />

        {/* 截断必须说出来：静默少几行会让用户以为"目录就这些"。 */}
        {listing?.truncated && <div className="fg-ws-note">子目录过多，仅显示了一部分</div>}
        {error && (
          <div className="fg-ws-error" role="alert">
            <WarningOutlined /> <span>{error}</span>
          </div>
        )}

        {/* 本目录里的文件：只读展示（文件不是合法工作空间，点不得）。 */}
        {fileMeta.length > 0 && (
          <>
            <div className="fg-ws-sep" />
            <div className="fg-ws-label">文件</div>
            <div className="fg-ws-files">
              {fileMeta.map((f) => (
                <div key={f.path} className="fg-ws-item fg-ws-item-static" title={f.path}>
                  <span className="fg-ws-item-label">
                    <span className="fg-ws-item-name">{f.name}</span>
                  </span>
                  <span className="fg-ws-item-meta">
                    {formatSize(f.size)} · {fmtShortDateTime(f.modified_at)}
                  </span>
                </div>
              ))}
            </div>
          </>
        )}
      </div>

      {/* 二级对话框：在**选中项**（没选中 = 当前层）下建一个子目录。 */}
      <Modal
        open={folderDraft !== null}
        title="新建文件夹"
        width={380}
        okText="创建"
        cancelText="取消"
        confirmLoading={creating}
        okButtonProps={{ disabled: (folderDraft ?? '').trim() === '' }}
        onOk={() => void confirmCreate()}
        onCancel={() => {
          if (!creating) setFolderDraft(null)
        }}
        destroyOnHidden
      >
        <div className="fg-ws-create">
          <div className="fg-ws-create-in">在「{selected?.name ?? lastSegment(currentPath)}」下创建</div>
          <Input
            value={folderDraft ?? ''}
            placeholder="文件夹名"
            autoFocus
            disabled={creating}
            onChange={(e) => setFolderDraft(e.target.value)}
            onCompositionStart={() => {
              composingRef.current = true
            }}
            onCompositionEnd={() => {
              composingRef.current = false
            }}
            onPressEnter={() => {
              if (!composingRef.current) void confirmCreate()
            }}
          />
          {createError && (
            <div className="fg-ws-error" role="alert">
              <WarningOutlined /> <span>{createError}</span>
            </div>
          )}
        </div>
      </Modal>
    </Modal>
  )
}
