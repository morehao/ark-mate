import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ArrowLeftOutlined,
  ArrowUpOutlined,
  CheckOutlined,
  FileOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  HomeOutlined,
} from '@ant-design/icons'
import { browseWorkspaces, workspaceFiles } from '@ark/api'
import type { WorkspaceBrowseResp, WorkspaceEntry, WorkspaceFileEntry } from '@ark/types'
import { useMate } from '../store'

interface Props {
  /** 起始目录（缺省 = 服务端首个工作区根，未配白名单时 = 服务进程家目录）。 */
  start?: string
  /** 选定**当前所在目录**。 */
  onPick: (path: string) => void
  /** 返回上一级菜单；缺省 = 不显示返回按钮（本组件被直接当页面用时没有"上一级菜单"）。 */
  onBack?: () => void
  /** 返回按钮的 tooltip 文案。 */
  backTitle?: string
  /** 选定按钮的文案（"使用此目录" / "加入白名单"）。 */
  pickLabel?: string
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

/** 小节标题（goose 的 DropdownMenuLabel）。 */
export function Label({ children, right }: { children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="fg-ws-label">
      <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{children}</span>
      {right}
    </div>
  )
}

/** 文件大小（资料场景要一眼看出"这是不是那个大文件"）。 */
function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GB`
}

/** 只显示「月-日 时:分」（完整值走 title）。 */
function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/**
 * 目录浏览视图（Web 无原生目录选择器，故由控制面提供只读枚举）。
 *
 * **唯一一份**浏览实现：新建会话的「选择目录…」（`WorkspacePicker`）与设置页的「加入白名单」
 * 共用它，两处只是选定动作不同（前者设 cwd，后者往白名单里加一条）。分开写两份必然漂移——
 * 而漂移的代价是"这处能选到、那处选不到"这种没有解释余地的 bug。
 *
 * 只列目录作为可进入项；当前目录里的**文件**只读展示（文件不是合法工作空间，点不得），
 * 因为资料场景下"这目录里到底有什么"与"能不能选它"同等重要。
 */
export function WorkspaceBrowser({ start, onPick, onBack, backTitle = '返回', pickLabel = '使用此目录' }: Props) {
  const showToast = useMate((s) => s.showToast)
  const [browse, setBrowse] = useState<WorkspaceBrowseResp | null>(null)
  const [files, setFiles] = useState<WorkspaceFileEntry[]>([])
  const [loading, setLoading] = useState(false)
  // 连点目录时后发的请求可能先回来：只认最后一次（goose DirSwitcher 的 refreshVersionRef 同款），
  // 否则会停在"点 A 显示 B"的错目录上，而选定按钮正好会把这个错目录提交上去。
  const loadVersionRef = useRef(0)

  const load = useCallback(
    async (path?: string) => {
      const version = ++loadVersionRef.current
      setLoading(true)
      try {
        const resp = await browseWorkspaces(path)
        if (version !== loadVersionRef.current) return
        setBrowse(resp)
        try {
          // 用服务端规范化后的 path 去列文件：保证"看到的文件"与"显示的目录"必然是同一个目录。
          const f = await workspaceFiles(resp.path)
          if (version !== loadVersionRef.current) return
          setFiles(f.entries)
        } catch {
          // 文件清单取不到不影响选目录（那是主路径）：少显示一节，不弹错。
          if (version !== loadVersionRef.current) return
          setFiles([])
        }
      } catch (err) {
        if (version !== loadVersionRef.current) return
        showToast(err instanceof Error ? err.message : String(err))
      } finally {
        if (version === loadVersionRef.current) setLoading(false)
      }
    },
    [showToast],
  )

  useEffect(() => {
    void load(start)
    // start 变化即重新定位（如从"工作区根"入口点进来）；依赖有意只放 start/load。
  }, [start, load])

  // 目录已在上面的 entries 里列过（那是可进入的项），这里只补文件，避免同一行出现两次。
  const browseFiles = files.filter((f) => !f.is_dir)

  return (
    <div className="fg-ws-pop" style={{ width: 360 }}>
      <div className="fg-ws-browse-head">
        {onBack && (
          <button type="button" className="fg-ws-icon-btn" title={backTitle} onClick={onBack}>
            <ArrowLeftOutlined />
          </button>
        )}
        <span className="fg-ws-label" style={{ flex: 1, minWidth: 0 }}>
          选择目录
        </span>
        <button
          type="button"
          className="fg-ws-icon-btn"
          title="上级目录"
          disabled={!browse?.parent}
          onClick={() => void load(browse?.parent)}
        >
          <ArrowUpOutlined />
        </button>
        <button
          type="button"
          className="fg-ws-icon-btn"
          title="主目录"
          disabled={!browse?.home}
          onClick={() => void load(browse?.home)}
        >
          <HomeOutlined />
        </button>
      </div>
      <div className="fg-ws-cur" title={browse?.path ?? ''}>
        {browse?.path ?? '…'}
      </div>
      {/* 已配置工作区根：把根直接列成入口。既省去"从主目录一层层点下去"，
          也让服务端收窄过的边界在界面上可见（否则"上级"按钮为什么突然灰了会是个谜）。 */}
      {browse?.roots && browse.roots.length > 0 && (
        <>
          <div className="fg-ws-sep" />
          <Label>工作区根</Label>
          {browse.roots.map((r) => (
            <button key={r} type="button" className="fg-ws-item" title={r} onClick={() => void load(r)}>
              <FolderOpenOutlined className="fg-ws-item-icon" />
              <DirLabel dir={r} />
              {r === browse.path && <CheckOutlined className="fg-ws-item-check" />}
            </button>
          ))}
        </>
      )}
      <div className="fg-ws-sep" />
      <div style={{ maxHeight: 220, overflowY: 'auto' }}>
        {!browse && loading && <div className="fg-ws-empty">读取中…</div>}
        {browse && browse.entries.length === 0 && <div className="fg-ws-empty">此目录下没有子目录</div>}
        {browse?.entries.map((e: WorkspaceEntry) => (
          <button key={e.path} type="button" className="fg-ws-item" title={e.path} onClick={() => void load(e.path)}>
            <FolderOutlined className="fg-ws-item-icon" />
            <DirLabel dir={e.path} />
            <span className="fg-ws-item-meta">进入 ›</span>
          </button>
        ))}
      </div>
      {/* 本目录里的文件：只读展示（文件不是合法工作空间，点不得）。 */}
      {browseFiles.length > 0 && (
        <>
          <div className="fg-ws-sep" />
          <Label>文件</Label>
          <div style={{ maxHeight: 160, overflowY: 'auto' }}>
            {browseFiles.map((f) => (
              <div key={f.path} className="fg-ws-item fg-ws-item-static" title={`${f.path} · ${f.modified_at}`}>
                <FileOutlined className="fg-ws-item-icon" />
                <span className="fg-ws-item-label">
                  <span className="fg-ws-item-name">{f.name}</span>
                </span>
                <span className="fg-ws-item-meta">
                  {formatSize(f.size)} · {formatTime(f.modified_at)}
                </span>
              </div>
            ))}
          </div>
        </>
      )}
      <div className="fg-ws-sep" />
      <button
        type="button"
        className="fg-ws-item"
        disabled={!browse?.path || loading}
        onClick={() => browse && onPick(browse.path)}
      >
        <CheckOutlined className="fg-ws-item-icon" />
        <span className="fg-ws-item-label">
          <span className="fg-ws-item-name">{pickLabel}</span>
        </span>
      </button>
    </div>
  )
}
