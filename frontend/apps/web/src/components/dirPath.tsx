import type { ReactNode } from 'react'

/**
 * 目录路径的展示原语（v14.3 从被移除的 `WorkspaceBrowser` 里提出来）。
 *
 * 它们与"选目录"的交互无关，只被三处展示面共用——选择器的工作空间列表、左栏分组、
 * 能力选择器的小节标题，所以单独成文件：否则删掉旧的浏览面板会连带删掉这三处的依赖。
 */

/** 目录名 + 上级路径（对齐 goose DirSwitcher 的 DirNameLabel）。 */
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
export function Label({ children, right }: { children: ReactNode; right?: ReactNode }) {
  return (
    <div className="fg-ws-label">
      <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{children}</span>
      {right}
    </div>
  )
}
