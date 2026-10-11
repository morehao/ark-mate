import { useState } from 'react'
import { Popover } from 'antd'
import { FolderAddOutlined, WarningOutlined } from '@ant-design/icons'
import { toErrorMessage } from '@ark-mate/api'
import { useMate } from '../store'
import { workspaceLabel } from './WorkspacePicker'
import { WorkspaceBrowser } from './WorkspaceBrowser'

/**
 * 区头「添加工作区」（v14.2）：一颗按钮、**一个动作**——直接抬起目录浏览，中间不过菜单
 * （对齐 dsh：*Adding is the header button's one action, so it raises the directory flow
 * with no menu in between*）。
 *
 * 只有一条路：选一个宿主目录（新的或已存在的）。dsh 的选目录器自带「新建文件夹」，本仓的浏览
 * 是**服务端只读枚举**，没有这个能力——所以也不给第二条「新建空目录」的入口（没有的能力就不给入口，
 * 与选择器里那两条被裁掉的理由同源）。
 *
 * 失败**就地报错、不关弹层**（设计 §5.2 状态 E）：用户当前浏览位置还在，换一个目录即可，
 * 而不是把目录树重新点到那一层。原因用服务端 msg，不自己编文案。
 */
export function AddWorkspaceButton() {
  const showToast = useMate((s) => s.showToast)
  const createWorkspace = useMate((s) => s.createWorkspace)

  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const adopt = async (path: string) => {
    setBusy(true)
    setErr(null)
    try {
      const ws = await createWorkspace(path)
      setOpen(false)
      showToast(`已添加工作空间「${workspaceLabel(ws)}」`)
    } catch (e) {
      setErr(toErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setErr(null)
      }}
      trigger="click"
      // 触发器贴着左栏右缘，向下向左开：弹层伸进主区而不是越过窗口左缘被裁掉。
      placement="bottomLeft"
      arrow={false}
      styles={{ content: { padding: 6 } }}
      content={
        <>
          {err && (
            <div className="fg-ws-error" role="alert">
              <WarningOutlined /> <span>{err}</span>
            </div>
          )}
          {/* 不传 `onBack`：区头按钮直接就是浏览这一层，没有"上一级菜单"可回。 */}
          <WorkspaceBrowser onPick={(p) => void adopt(p)} pickLabel={busy ? '添加中…' : '添加此目录'} />
        </>
      }
    >
      <button type="button" className="fg-rail-sechead-icon" title="添加工作区" aria-label="添加工作区">
        <FolderAddOutlined />
      </button>
    </Popover>
  )
}
