import { useEffect, useState } from 'react'
import { Input, Modal } from 'antd'
import { toErrorMessage } from '@ark-mate/api'
import { tokens } from '@ark-mate/ui'
import type { Workspace } from '@ark-mate/types'
import { useMate } from '../store'
import { permLabel } from '../console/permissionView'
import { isPermMode, PermissionPicker, type PermMode } from './PermissionPicker'

/**
 * 工作空间「默认设置…」Modal（design §6.4）。
 *
 * 两块内容：
 *   - **默认权限**（主链路，完整可用）：三档选择器，复用 `PermissionPicker`（选 danger 走二次确认）。
 *     danger 走的是 Picker 内部的确认框——"确认后"才把档位写进本地 state；本 Modal 的「保存」
 *     再把这份 state 通过 `patchWorkspaceDefaults` 落库。
 *   - **默认能力**（简化版，避免过度设计）：工作空间默认能力的**原样 JSON 编辑**。它只做
 *     "读出 → 原样编辑 → 写回"这条最小闭环（不报错即可），交互打磨留 TODO。为什么不在
 *     这里复用 `CapabilityBar`：那套需要异步装配专家/技能/连接器全集并转成物化 id 列表，
 *     成本远高于本设置页的价值，而 Q2 允许能力部分从简（优先保证权限主链路）。
 *
 * 读：初值来自 `Workspace.default_permission_mode` / `Workspace.default_capability`。
 * 写：`PATCH /workspaces/:id`（补丁语义，只带改动过的键）。
 */
export function WorkspaceDefaultsModal({
  workspace,
  onClose,
}: {
  workspace: Workspace | null
  onClose: () => void
}) {
  const patchWorkspaceDefaults = useMate((s) => s.patchWorkspaceDefaults)

  const [perm, setPerm] = useState<PermMode>('workspace-write')
  // 能力 JSON 草稿（原样字符串；空 = 未设默认）。
  const [capabilityText, setCapabilityText] = useState('')
  const [capabilityError, setCapabilityError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // 打开（或切到另一条工作空间）时用读面初值重置草稿。
  useEffect(() => {
    if (!workspace) return
    setPerm(isPermMode(workspace.default_permission_mode) ? workspace.default_permission_mode : 'workspace-write')
    setCapabilityText(workspace.default_capability ?? '')
    setCapabilityError(null)
    setError(null)
  }, [workspace])

  if (!workspace) return null

  const save = async () => {
    setError(null)
    // 能力：空串 = 清掉默认；非空必须是合法 JSON（否则写回一个坏值，下次读不回来）。这里只在
    // 前端做"形状"体检，语义（字段含义）交给后端——本版不解析结构，只保证能把串写回/读出。
    const capTrim = capabilityText.trim()
    if (capTrim) {
      try {
        JSON.parse(capTrim)
      } catch {
        setCapabilityError('不是合法的 JSON')
        return
      }
    }
    setCapabilityError(null)
    setSaving(true)
    try {
      // 补丁语义：只带要改的键。权限永远带（用户在设置页的表态）；能力只在有变化时带，
      // 避免无谓覆盖（读面没给 default_capability 时就别动它）。
      const prim = workspace.default_permission_mode ?? ''
      const patch: { default_permission_mode?: string | null; default_capability?: string | null } = {}
      if (prim !== perm) patch.default_permission_mode = perm
      const capOriginal = workspace.default_capability ?? ''
      if (capabilityText !== capOriginal) patch.default_capability = capTrim
      // 没有任何改动时不发请求，直接关。
      if (patch.default_permission_mode === undefined && patch.default_capability === undefined) {
        onClose()
        return
      }
      await patchWorkspaceDefaults(workspace.id, patch)
      onClose()
    } catch (err) {
      // 失败留在 Modal 里就地显示，用户可改可重试。
      setError(toErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={workspace !== null}
      title={`默认设置 · ${workspace.title || workspace.path}`}
      okText="保存"
      cancelText="取消"
      width={480}
      confirmLoading={saving}
      onOk={() => void save()}
      onCancel={onClose}
      destroyOnHidden
    >
      <div style={{ fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 18 }}>
        {/* 默认权限（主链路） */}
        <div>
          <div style={{ marginBottom: 6, fontWeight: 600, color: tokens.text }}>默认权限</div>
          <div style={{ marginBottom: 10, fontSize: 11.5, color: tokens.textPlaceholder, lineHeight: 1.7 }}>
            新建会话时预选这一档（仍可在新建面板里改）。当前：{permLabel(perm)}。
            选「完全权限」需二次确认；默认值只是便利，不是授权。
          </div>
          <PermissionPicker value={perm} onChange={setPerm} disabled={saving} />
        </div>

        {/* 默认能力（简化版：原样 JSON 编辑，TODO 交互打磨） */}
        <div>
          <div style={{ marginBottom: 6, fontWeight: 600, color: tokens.text }}>
            默认能力 <span style={{ fontSize: 11, color: tokens.textPlaceholder }}>（简化编辑，后续打磨）</span>
          </div>
          <div style={{ marginBottom: 6, fontSize: 11.5, color: tokens.textPlaceholder, lineHeight: 1.7 }}>
            能力选定的 JSON 串；留空 = 未设默认（新建时跟随默认）。本版只做原样读写，暂不做可视化选择器。
          </div>
          {/* TODO(Q2)：后续用 CapabilityBar 的装配逻辑把这里的 JSON 换成可视化三选择器。 */}
          <Input.TextArea
            value={capabilityText}
            onChange={(e) => {
              setCapabilityText(e.target.value)
              setCapabilityError(null)
            }}
            placeholder='例如：{"expert":"","skills":["plan-first"]}（留空 = 未设默认）'
            autoSize={{ minRows: 3, maxRows: 8 }}
            style={{ fontFamily: tokens.fontMono, fontSize: 12 }}
            status={capabilityError ? 'error' : undefined}
          />
          {capabilityError && (
            <div style={{ marginTop: 4, fontSize: 11.5, color: tokens.error }}>{capabilityError}</div>
          )}
        </div>

        {error && <div style={{ fontSize: 12, color: tokens.error }}>{error}</div>}
      </div>
    </Modal>
  )
}
