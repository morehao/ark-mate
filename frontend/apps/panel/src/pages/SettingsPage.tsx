import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Input, Modal, Space, Spin, Tag, Tooltip } from 'antd'
import { DeleteOutlined, FolderOpenOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark/ui'
import { getWorkspaceSettings, toErrorMessage, updateWorkspaceSettings } from '@ark/api'
import type { WorkspaceSettings } from '@ark/types'
import { useForge } from '../store'
import { WorkspaceBrowser } from '../components/WorkspaceBrowser'

/**
 * 设置页（Settings）——目前只有一节：**工作区目录白名单**。
 *
 * 这一节要解决的实际痛点不是"能不能改"，而是"改了到底算不算数"：白名单的最终来源可能是
 * `config.json`，也可能是优先级更高的 env 文件 / 进程 `FORGE_*` / CLI flag。后者存在时写
 * `config.json` **必然被覆盖**，所以服务端直接拒绝写并回 `locked_by`（`settings_locked`）——
 * 面板据此转只读并显示锁定来源，而不是给一个必然失败的保存按钮。
 *
 * 另一条容易误解的语义也在界面上说明：**平台代建目录恒在生效名单内**（`provision_included`），
 * 它不是用户配的、也删不掉；`configured` 才是这里可编辑的那一份。
 *
 * 自定义 Agent / provider 配置不在这里（那是「Agents」页的职责），避免同一个设置有两个入口。
 */

/** 计划中的能力（本页曾以静态清单承载，保留以免信息丢失；权威清单见 docs/design-and-roadmap.md §5.3）。 */
const P3_ITEMS = [
  '能力基线 / 权限请求裁剪入口（工具权限交互面）',
  '消息搜索分页与全文高亮（服务端 messages 表已落库）',
  '多 Agent 编排与批量调度面',
]

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

function CardHeader({ title, hint, extra }: { title: string; hint: string; extra?: ReactNode }) {
  return (
    <header style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 16px', borderBottom: `1px solid ${tokens.border}` }}>
      <b style={{ fontSize: 13.5, fontWeight: 600 }}>{title}</b>
      <span style={{ flex: 1, fontSize: 11.5, color: tokens.textPlaceholder }}>{hint}</span>
      {extra}
    </header>
  )
}

/** 是否绝对路径（与 WorkspacePicker 同规则：POSIX / UNC / Windows 盘符）——只做本地预检，真校验在服务端。 */
function isAbsolutePath(p: string): boolean {
  return p.startsWith('/') || p.startsWith('\\\\') || /^[A-Za-z]:[\\/]/.test(p)
}

export function SettingsPage() {
  const showToast = useForge((s) => s.showToast)
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
  // 未保存的改动：白名单是"要么画边界、要么不画"，按集合比较即可（顺序不同不算改动）。
  const dirty = snap != null && [...draft].sort().join('\n') !== [...(snap.configured ?? [])].sort().join('\n')

  const addPath = (p: string) => {
    const v = p.trim()
    if (!v) return
    if (!isAbsolutePath(v)) {
      showToast('白名单目录必须是绝对路径')
      return
    }
    if (draft.includes(v)) {
      showToast('该目录已在白名单中')
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
      showToast(roots.length > 0 ? '已保存：目录浏览与新建会话都收在白名单内' : '已保存：恢复为不限制')
    } catch (err) {
      // 锁定 / 校验失败都以服务端文案为准（它点明了是哪一项、该去改哪里）。
      showToast(toErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const provisionNote = snap?.provision_dir
    ? `平台代建目录 ${snap.provision_dir} 自动包含（每个会话在其下按会话 id 建独立目录）`
    : '平台代建已关闭（workspace_dir 为空）：新建会话必须显式指定工作目录'

  return (
    <PageContainer
      title="设置"
      description="目录白名单决定「哪些目录能被当作工作空间」——它同时约束新建会话的 cwd 与面板里的目录浏览。自定义 Agent / provider 配置在「Agents」页。"
      extra={
        <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>
          刷新
        </Button>
      }
    >
      <CardHeader
        title="工作区目录白名单"
        hint={snap == null ? '读取中…' : snap.restricted ? '受限：只有名单内的目录可浏览、可作为工作空间' : '不限制：任意可读目录都可浏览与使用'}
        extra={
          snap && (
            <Space size={6}>
              <Tag style={{ marginInlineEnd: 0, fontSize: 11 }} color={snap.restricted ? 'blue' : 'default'}>
                {snap.restricted ? '受限' : '不限制'}
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
            message={`白名单由 ${snap.locked_by} 锁定，面板只读`}
            description="该来源的优先级高于配置文件，从面板写入会被它覆盖（所以服务端直接拒绝）。要在这里维护白名单，请去掉该来源里的 FORGE_WORKSPACE_ROOTS。"
          />
        )}
        {snap && !snap.locked_by && snap.source === 'unavailable' && (
          <Alert
            type="info"
            showIcon
            message="当前进程不可持久化设置"
            description="该进程没有配置目录（如 acp-serve / demo）：白名单只能由 FORGE_WORKSPACE_ROOTS 或配置文件设置。"
          />
        )}

        <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.7 }}>{provisionNote}</div>

        {/* 名单：只读时不给删除按钮（不是禁用一个必然失败的按钮，而是不给这个入口）。 */}
        {draft.length === 0 ? (
          <div style={{ padding: '10px 0', fontSize: 12.5, color: tokens.textPlaceholder }}>
            名单为空 = 不限制。加入目录后，目录浏览与新建会话都会被收在这些根之内。
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
            <Button icon={<FolderOpenOutlined />} onClick={() => setPicking(true)}>
              浏览…
            </Button>
          </Space.Compact>
        )}

        {/* 保存：全量替换语义（服务端 PUT 即"这份名单就是新名单"），因此按钮文案直接说清楚。 */}
        {!readonly && (
          <Space>
            <Button type="primary" loading={saving} disabled={!dirty} onClick={() => void save(draft)}>
              保存白名单
            </Button>
            {snap?.restricted && (
              <Tooltip title="清空白名单：目录浏览与新建会话恢复为不限制">
                <Button loading={saving} onClick={() => void save([])}>
                  取消限定
                </Button>
              </Tooltip>
            )}
            {dirty && <span style={{ fontSize: 12, color: tokens.textPlaceholder }}>有未保存的改动</span>}
          </Space>
        )}
      </div>

      <CardHeader
        title="仍属后续计划"
        hint="见 docs/design-and-roadmap.md §5.3"
      />
      <div style={{ padding: '12px 16px' }}>
        <ul style={{ margin: 0, paddingLeft: 20, color: tokens.text, fontSize: 13, lineHeight: 2 }}>
          {P3_ITEMS.map((t) => (
            <li key={t}>{t}</li>
          ))}
        </ul>
      </div>

      {picking && (
        <Modal open title="选择一个目录加入白名单" footer={null} onCancel={() => setPicking(false)} width={420} destroyOnHidden>
          {/* 与新建会话的「选择目录…」共用同一份浏览实现：选定动作不同（这里往白名单加一条），
              浏览能力不分叉。 */}
          <WorkspaceBrowser
            pickLabel="加入白名单"
            onPick={(p) => {
              addPath(p)
              setPicking(false)
            }}
          />
        </Modal>
      )}
    </PageContainer>
  )
}
