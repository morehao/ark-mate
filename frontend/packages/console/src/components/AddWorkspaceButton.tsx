import { useState } from 'react'
import { FolderAddOutlined } from '@ant-design/icons'
import { toErrorMessage } from '@ark-mate/api'
import { useMate } from '../store'
import { directoryPickerKind, pickDirectoryNative } from '../directoryPicker'
import { workspaceLabel } from './WorkspacePicker'
import { DirectoryBrowser } from './DirectoryBrowser'

/**
 * 区头「添加工作区」（v14.2）：一颗按钮、**一个动作**——直接抬起选目录流程，中间不过菜单
 * （对齐 dsh：*Adding is the header button's one action, so it raises the directory flow
 * with no menu in between*）。
 *
 * v14.5 起这一个动作有**两态**（对齐 dsh 的 `directory-picker-auto`，互斥不并存）：
 *   - 宿主能抬系统面板 → 调 `POST /workspaces/pick-directory`（面板由**核心进程** spawn，
 *     所以本机浏览器入口也走这条）；
 *   - 否则 → 开应用内 `DirectoryBrowser`（服务端只读枚举 + 建目录写面）。
 * 判定只有一份：`../directoryPicker`（读服务端 `/capabilities` 的 `native-dialog`）。
 * macOS 的原生面板自带「新建文件夹」，所以原生那一态不缺这一步。
 *
 * 失败用 toast 报（对话框是**模态**的：它关闭后没有"当前浏览位置"可留在原地）。
 */
export function AddWorkspaceButton() {
  const showToast = useMate((s) => s.showToast)
  const createWorkspace = useMate((s) => s.createWorkspace)

  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)

  const adopt = async (path: string) => {
    setBusy(true)
    try {
      const ws = await createWorkspace(path)
      setOpen(false)
      showToast(`已添加工作空间「${workspaceLabel(ws)}」`)
    } catch (e) {
      showToast(toErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  /** 一个动作，两态：原生可用就只用原生；否则开应用内对话框。 */
  const choose = async () => {
    if (directoryPickerKind() === 'native') {
      const picked = await pickDirectoryNative()
      // 取消不是错误：静默收手（与 dsh 的 `path === null → onCancel` 同义）。
      if ('cancelled' in picked) return
      await adopt(picked.path)
      return
    }
    setOpen(true)
  }

  return (
    <>
      <button
        type="button"
        className="fg-rail-sechead-icon"
        title="添加工作区"
        aria-label="添加工作区"
        disabled={busy}
        onClick={() => void choose()}
      >
        <FolderAddOutlined />
      </button>
      {open && (
        <DirectoryBrowser
          title="添加工作空间"
          pickLabel={busy ? '添加中…' : '添加此目录'}
          busy={busy}
          onPick={(p) => void adopt(p)}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  )
}
