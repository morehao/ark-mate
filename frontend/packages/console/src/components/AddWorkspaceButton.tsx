import { useState } from 'react'
import { Dropdown, Input, Modal } from 'antd'
import { FolderAddOutlined, FolderOpenOutlined, PlusOutlined } from '@ant-design/icons'
import { toErrorMessage } from '@ark-mate/api'
import { useMate } from '../store'
import { directoryPickerKind, pickDirectoryNative } from '../directoryPicker'
import { DirectoryBrowser } from './DirectoryBrowser'

/**
 * 区头「添加工作区」（v14.2 / ADR-2）：**两条路**，二选一。
 *
 * - 「选择已有目录…」= 登记一个**已存在**的目录（现状，宿主两态：系统面板 / 应用内浏览）。
 * - 「新建工作空间…」= 在落盘根（`create_root`）下建一个同名目录并登记，**一步**（ADR-2 的
 *   `name` 模式）。
 *
 * 三条纪律：
 *
 *  1. **落盘根为空时第二项不渲染**（ADR-3 的入口纪律）：那是一个必然失败的按钮。此时这一颗
 *     按钮直接抬起选目录流程（回到 v14.2 的单动作形态）——菜单里只有一个选项的菜单不是菜单。
 *  2. **「选择已有目录…」这一半完全不动**：判定仍在 `../directoryPicker`（读服务端
 *     `/capabilities` 的 `native-dialog`），不在这里加任何"客户端能不能看见屏幕"的判据。
 *  3. 名字的合法性由**服务端**判（`ValidateWorkspaceName`，与目录浏览面的「新建文件夹」同一
 *     条实现）：弹层不做第二套前端正则——两套的接受集一定会分叉，而分叉的表现是"界面上说没问题、
 *     点下去报错"。
 */
export function AddWorkspaceButton() {
  const showToast = useMate((s) => s.showToast)
  const createWorkspace = useMate((s) => s.createWorkspace)
  const createNamedWorkspace = useMate((s) => s.createNamedWorkspace)
  const workspaces = useMate((s) => s.workspaces)

  /** 应用内目录浏览（宿主抬不起系统面板时的第二态）。 */
  const [open, setOpen] = useState(false)
  /** 新建工作空间弹层。 */
  const [naming, setNaming] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [nameError, setNameError] = useState<string | null>(null)

  const createRoot = workspaces?.create_root ?? ''

  const adopt = async (path: string) => {
    setBusy(true)
    try {
      await createWorkspace(path)
      setOpen(false)
      // 不加成功 toast：工作空间登记成功后左栏列表会立即刷新出现该条目，这本身就是回执；
      // 再叠一条「已添加工作空间…」既冗余、又会在点完的瞬间抢视线（见 2026-10-30 反馈）。
    } catch (e) {
      showToast(toErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  /** 「选择已有目录…」：一个动作，两态（原生可用就只用原生；否则开应用内对话框）。 */
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

  /** 「新建工作空间…」：服务端一步建目录 + 登记；错误**就地**显示在弹层里（不吞成 toast）。 */
  const submitName = async () => {
    const name = draft.trim()
    if (!name) {
      setNameError('请输入名字')
      return
    }
    setBusy(true)
    setNameError(null)
    try {
      await createNamedWorkspace(name)
      setNaming(false)
      setDraft('')
      // 同上：新建成功后左栏立即出现该工作空间，弹层关闭 + 列表更新已足够，不再叠成功 toast。
    } catch (e) {
      setNameError(toErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const menu = {
    items: [
      { key: 'pick', icon: <FolderOpenOutlined />, label: '选择已有目录…' },
      { key: 'name', icon: <PlusOutlined />, label: '新建工作空间…' },
    ],
    onClick: (info: { key: string }) => {
      if (info.key === 'pick') void choose()
      if (info.key === 'name') {
        setDraft('')
        setNameError(null)
        setNaming(true)
      }
    },
  }

  return (
    <>
      {createRoot ? (
        <Dropdown menu={menu} trigger={['click']} placement="bottomRight">
          <button type="button" className="fg-rail-sechead-icon" title="添加工作区" aria-label="添加工作区" disabled={busy}>
            <FolderAddOutlined />
          </button>
        </Dropdown>
      ) : (
        // 落盘根未声明：只留「选择已有目录」这一个动作（不给一个必然失败的菜单项）。
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
      )}
      {open && (
        <DirectoryBrowser
          title="添加工作空间"
          pickLabel={busy ? '添加中…' : '添加此目录'}
          busy={busy}
          onPick={(p) => void adopt(p)}
          onCancel={() => setOpen(false)}
        />
      )}
      <Modal
        open={naming}
        title="新建工作空间"
        okText={busy ? '创建中…' : '创建'}
        cancelText="取消"
        width={420}
        confirmLoading={busy}
        onOk={() => void submitName()}
        onCancel={() => setNaming(false)}
        destroyOnHidden
      >
        <div style={{ fontSize: 12.5 }}>
          <div style={{ marginBottom: 5 }}>名称</div>
          <Input
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value)
              setNameError(null)
            }}
            onPressEnter={() => void submitName()}
            placeholder="例如：支付重构"
            autoFocus
            maxLength={128}
            status={nameError ? 'error' : undefined}
          />
          {/* 位置：落盘根由服务端下发，只在这里**如实显示**（不在前端拼路径）。 */}
          <div style={{ marginTop: 8, fontSize: 11.5, color: 'var(--fg-text-secondary, #8c8c8c)', lineHeight: 1.7 }}>
            位置：<code>{createRoot}</code>
          </div>
          <div style={{ marginTop: 6, fontSize: 11.5, color: 'var(--fg-text-secondary, #8c8c8c)', lineHeight: 1.7 }}>
            会在这个位置建一个同名文件夹并加入列表（一步完成）。同名目录已存在时会提示换个名字，不会复用已有目录。
          </div>
          {nameError && (
            <div style={{ marginTop: 8, fontSize: 11.5, color: '#d4380d', lineHeight: 1.7 }}>{nameError}</div>
          )}
        </div>
      </Modal>
    </>
  )
}
