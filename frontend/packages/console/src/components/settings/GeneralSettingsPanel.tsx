import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Input, Space, Spin, Tag, Tooltip } from 'antd'
import { DeleteOutlined, FolderOpenOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { getWorkspaceSettings, toErrorMessage, updateWorkspaceSettings } from '@ark-mate/api'
import type { WorkspaceSettings } from '@ark-mate/types'
import { useMate } from '../../store'
import { directoryPickerKind, pickDirectoryNative } from '../../directoryPicker'
import { DirectoryBrowser } from '../DirectoryBrowser'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 通用（工作区**起始点偏好**，v14.5 前的名字是"目录白名单"）——**作为居中弹窗的内容体**渲染。
 *
 * 设置不是页面：左栏 footer 的「设置」点开二级菜单，菜单项点开这个居中的 Modal
 * （见 `settings/SettingsModals.tsx`）。所以这里只出内容，不再自带 PageContainer 骨架。
 *
 * **它不再是边界**：选工作区是全系统（任何已存在的目录都能浏览、都能当工作空间），这份名单只决定
 * "目录浏览从哪儿起步"与"面板里列哪些快捷入口"。改名的理由就是原文案会让用户以为名单外的目录不能用。
 *
 * 这一节要解决的实际痛点不是"能不能改"，而是"改了到底算不算数"：这条偏好的最终来源可能是
 * `config.json`，也可能是优先级更高的 env 文件 / 进程 `MATE_*` / CLI flag。后者存在时写
 * `config.json` **必然被覆盖**，所以服务端直接拒绝写并回 `locked_by`（`settings_locked`）——
 * 面板据此转只读并显示锁定来源，而不是给一个必然失败的保存按钮。
 *
 * 另一条容易误解的语义也在界面上说明：**平台代建目录恒在生效名单内**（`provision_included`），
 * 它不是用户配的、也删不掉；`configured` 才是这里可编辑的那一份。
 *
 * provider 配置不在这里（那是同级「模型」那一节的职责），避免同一个设置有两个入口。
 */

/** 来源标签：把服务端的 source 直译成"这个值现在从哪来"。 */
const SOURCE_LABEL: Record<string, string> = {
  default: '默认值',
  'config.json': '配置文件',
  env: 'env 文件',
  'env(process)': '进程环境变量',
  flag: '命令行参数',
  unavailable: '不可持久化',
}

const mono = { fontFamily: tokens.fontMono, fontSize: 12.5 } as const

/** 是否绝对路径（与 WorkspacePicker 同规则：POSIX / UNC / Windows 盘符）——只做本地预检，真校验在服务端。 */
function isAbsolutePath(p: string): boolean {
  return p.startsWith('/') || p.startsWith('\\\\') || /^[A-Za-z]:[\\/]/.test(p)
}

export function GeneralSettingsPanel() {
  const showToast = useMate((s) => s.showToast)
  // 默认工作区（只读展示，原型 §H）：取自登记表的 default_id 那一项（平台分配的落地目录）。
  const workspaces = useMate((s) => s.workspaces)
  const [snap, setSnap] = useState<WorkspaceSettings | null>(null)
  const [draft, setDraft] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [manual, setManual] = useState('')
  const [picking, setPicking] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const s = await getWorkspaceSettings()
      setSnap(s)
      setDraft(s.configured ?? [])
    } catch (err) {
      showToast(toErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [showToast])

  useEffect(() => {
    void load()
  }, [load])

  const readonly = !snap?.writable
  // 未保存的改动：按集合比较即可（顺序不同不算改动）。
  const dirty = snap != null && [...draft].sort().join('\n') !== [...(snap.configured ?? [])].sort().join('\n')

  const addPath = (p: string) => {
    const v = p.trim()
    if (!v) return
    if (!isAbsolutePath(v)) {
      showToast('起始目录必须是绝对路径')
      return
    }
    if (draft.includes(v)) {
      showToast('该目录已在列表中')
      return
    }
    setDraft([...draft, v])
    setManual('')
  }

  const save = async (roots: string[]) => {
    setSaving(true)
    try {
      const s = await updateWorkspaceSettings(roots)
      setSnap(s)
      setDraft(s.configured ?? [])
      showToast(roots.length > 0 ? '已保存：目录浏览从第一个位置开始，并列成快捷入口' : '已保存：未设置起点（从服务进程家目录开始）', 'success')
    } catch (err) {
      // 锁定 / 校验失败都以服务端文案为准（它点明了是哪一项、该去改哪里）。
      showToast(toErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  /**
   * 「浏览…」：**一个动作、两态**（v14.4，对齐 dsh 的 `directory-picker-auto`）。
   *
   * 宿主能抬系统面板 → 原生选目录器；否则 → 应用内对话框。
   *
   * 与另两处的差别只是"拿到路径之后干什么"：这里把它加进**起点偏好**列表（另两处是登记工作空间）。
   */
  const chooseDirectory = async () => {
    if (directoryPickerKind() === 'native') {
      const picked = await pickDirectoryNative()
      if ('cancelled' in picked) return
      addPath(picked.path)
      return
    }
    setPicking(true)
  }

  const provisionNote = snap?.provision_dir
    ? `平台代建目录 ${snap.provision_dir} 自动包含（每个会话在其下有一个独立子目录）`
    : '平台代建已关闭：新建会话时必须指定工作目录'

  // 默认工作区路径（原型 §H 只读行）：默认登记项的路径；取不到就显示占位。
  const defaultWs = workspaces?.items.find((w) => w.id === workspaces.default_id) ?? workspaces?.items.find((w) => w.is_default)

  return (
    <div className="fg-setsec">
      {/* 默认工作区（只读，原型 §H）：平台分配给每个新会话的落地目录。 */}
      <div className="fg-setsec-card">
        <CardHeader title="默认工作区" hint="每个会话都在一个「工作区」里工作——目录就是助手能读写的边界。这里是平台分配的默认工作区路径。" />
        <div style={{ padding: '14px 16px', display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ fontSize: 12.5, color: tokens.textSecondary, flex: 'none' }}>默认工作区</span>
          <span style={{ ...mono, color: tokens.text, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {defaultWs?.path || '（默认工作区未登记）'}
          </span>
        </div>
      </div>

      <div className="fg-setsec-head">
        <span className="fg-setsec-desc">
          这几项是**目录浏览的起点**，不是允许范围：面板与新建会话都能选到系统里的任何目录，
          这里列出的位置只是让你少点几层（以及"选目录"时从哪儿开始看）。
        </span>
        <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>
          刷新
        </Button>
      </div>

      <div className="fg-setsec-card">
      <CardHeader
        title="工作区起始位置"
        hint={snap == null ? '读取中…' : snap.prefer_roots ? '已设置起点：目录浏览从第一个位置开始，并在面板里列成快捷入口' : '未设置起点：目录浏览从服务进程家目录开始'}
        extra={
          snap && (
            <Space size={6}>
              <Tag style={{ marginInlineEnd: 0, fontSize: 11 }} color={snap.prefer_roots ? 'blue' : 'default'}>
                {snap.prefer_roots ? '已设起点' : '未设起点'}
              </Tag>
              <Tooltip title={`来源：${SOURCE_LABEL[snap.source] ?? snap.source}`}>
                <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{SOURCE_LABEL[snap.source] ?? snap.source}</Tag>
              </Tooltip>
            </Space>
          )
        }
      />
      <div style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }}>
        {loading && snap == null && (
          <div style={{ padding: '12px 0' }}>
            <Spin size="small" />
          </div>
        )}

        {/* 锁定 / 不可写：先说清"为什么改不了、去哪改"，再展示只读内容——
            否则用户会以为是面板坏了。 */}
        {snap?.locked_by && (
          <Alert
            type="warning"
            showIcon
            message={`起始位置由 ${snap.locked_by} 锁定，面板只读`}
            description="该来源的优先级高于配置文件，从面板写入会被它覆盖（所以服务端直接拒绝）。要在这里维护它，请去掉该来源里的 MATE_WORKSPACE_ROOTS。"
          />
        )}
        {snap && !snap.locked_by && snap.source === 'unavailable' && (
          <Alert
            type="info"
            showIcon
            message="当前进程不可持久化设置"
            description="该进程没有配置目录（如 demo 冒烟进程）：起始位置只能由 MATE_WORKSPACE_ROOTS 或配置文件设置。"
          />
        )}

        <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.7 }}>{provisionNote}</div>

        {/* 名单：只读时不给删除按钮（不是禁用一个必然失败的按钮，而是不给这个入口）。 */}
        {draft.length === 0 ? (
          <div style={{ padding: '10px 0', fontSize: 12.5, color: tokens.textPlaceholder }}>
            列表为空 = 没有起点偏好：目录浏览从服务进程家目录开始。加入位置后，浏览会从第一个开始，
            并把它们列成快捷入口——**任何目录仍然都能选**。
          </div>
        ) : (
          <div style={{ border: `1px solid ${tokens.border}`, borderRadius: tokens.radiusSm, overflow: 'hidden' }}>
            {draft.map((p, i) => (
              <div
                key={p}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '8px 12px',
                  borderBottom: i === draft.length - 1 ? 'none' : `1px solid ${tokens.border}`,
                }}
              >
                <FolderOpenOutlined style={{ color: tokens.textPlaceholder, fontSize: 13 }} />
                <span style={{ flex: 1, ...mono, overflowWrap: 'anywhere' }}>{p}</span>
                {!readonly && (
                  <Button
                    size="small"
                    type="text"
                    icon={<DeleteOutlined />}
                    title="从名单中移除（保存后生效）"
                    onClick={() => setDraft(draft.filter((x) => x !== p))}
                  />
                )}
              </div>
            ))}
          </div>
        )}

        {/* 加入目录：手输绝对路径（快）或浏览挑选（不必知道路径）。只读时整块隐藏。 */}
        {!readonly && (
          <Space.Compact style={{ width: '100%' }}>
            <Input
              value={manual}
              placeholder="输入目录绝对路径（例如 /srv/docs）"
              style={mono}
              onChange={(e) => setManual(e.target.value)}
              onPressEnter={() => addPath(manual)}
            />
            <Button icon={<PlusOutlined />} onClick={() => addPath(manual)} disabled={!manual.trim()}>
              加入
            </Button>
            <Button icon={<FolderOpenOutlined />} onClick={() => void chooseDirectory()}>
              浏览…
            </Button>
          </Space.Compact>
        )}

        {/* 保存：全量替换语义（服务端 PUT 即"这份名单就是新名单"），因此按钮文案直接说清楚。 */}
        {!readonly && (
          <Space>
            <Button type="primary" loading={saving} disabled={!dirty} onClick={() => void save(draft)}>
              保存起点
            </Button>
            {snap?.prefer_roots && (
              <Tooltip title="清空列表：目录浏览恢复为从服务进程家目录开始">
                <Button loading={saving} onClick={() => void save([])}>
                  取消限定
                </Button>
              </Tooltip>
            )}
            {dirty && <span style={{ fontSize: 12, color: tokens.textPlaceholder }}>有未保存的改动</span>}
          </Space>
        )}
      </div>

      {picking && (
        <DirectoryBrowser
          title="选择一个目录作为起点"
          pickLabel="加入起点"
          onPick={(p) => {
            addPath(p)
            setPicking(false)
          }}
          onCancel={() => setPicking(false)}
        />
      )}
      </div>
    </div>
  )
}
