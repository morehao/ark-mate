import { useCallback, useEffect, useMemo, useState } from 'react'
import { fmtDateTime } from '../console/time'
import { CardToolbar } from '../components/CardToolbar'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  Button,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Space,
  Spin,
  Segmented,
  Table,
  Tag,
  Typography,
  Upload as AntUpload,
  message,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  DeleteOutlined,
  FileTextOutlined,
  FolderOutlined,
  LinkOutlined,
  TableOutlined,
  UploadOutlined,
  Html5Outlined,
  CloudUploadOutlined,
} from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import { IconAction } from '../components/capability/shared'
import {
  createMaterialNode,
  deleteMaterialNode,
  getArtifact,
  getMaterialNode,
  listMaterialNodes,
  materialViewUrl,
  patchMaterialNode,
  publishMaterialPage,
  searchMaterial,
  toErrorMessage,
  uploadMaterialFile,
} from '@ark-mate/api'
import type { ArtifactMeta, MaterialNode, MaterialNodeDetail, MaterialNodeKind, MaterialSearchHit } from '@ark-mate/types'

// ——— 资料库页（v33 P5）———
//
// 三件事：**看得见**（目录树逐级浏览）、**找得到**（关键词搜索，不是语义）、**放得进**（上传 +
// 新建文件夹）。详情抽屉按 kind 展示正文 / 表格 / 页面预览；drive 给"打开/下载"。
//
// 本期范围：只做资料库、不做知识库——搜索框标「关键词搜索」，页面上**不出现**「智能/语义/知识库」。

// 品类图标一律走中性灰（精致冷白的「强调色克制」）：层级由文字承担，
// 不再给每种类型一种彩头（五色图标会把克制的表面打散）。
const KIND_META: Record<MaterialNodeKind, { label: string; icon: React.ReactNode; color: string }> = {
  folder: { label: '文件夹', icon: <FolderOutlined />, color: tokens.textSecondary },
  doc: { label: '文档', icon: <FileTextOutlined />, color: tokens.textSecondary },
  table: { label: '表格', icon: <TableOutlined />, color: tokens.textSecondary },
  link: { label: '网页剪藏', icon: <LinkOutlined />, color: tokens.textSecondary },
  drive: { label: '文件', icon: <CloudUploadOutlined />, color: tokens.textSecondary },
  page: { label: '网页', icon: <Html5Outlined />, color: tokens.textSecondary },
}

interface Crumb {
  id: string
  title: string
}

export function MaterialPage() {
  // 分类分栏走 URL（`?category=artifact`）：会话详情里「已收录 · 去查看」要能直接落到产物分栏，
  // 否则用户点完还是停在「全部」，得自己再点一次分栏才找得到刚收录的东西。
  const [searchParams, setSearchParams] = useSearchParams()
  const categoryParam = searchParams.get('category')
  const category: '' | 'material' | 'artifact' =
    categoryParam === 'material' || categoryParam === 'artifact' ? categoryParam : ''
  const setCategory = (v: '' | 'material' | 'artifact') => {
    const next = new URLSearchParams(searchParams)
    if (v) next.set('category', v)
    else next.delete('category')
    setSearchParams(next, { replace: true })
  }
  const [stack, setStack] = useState<Crumb[]>([]) // 面包屑；末项 = 当前目录（空栈 = 根）
  const [nodes, setNodes] = useState<MaterialNode[]>([])
  const [loading, setLoading] = useState(false)
  const [searchQ, setSearchQ] = useState('')
  const [hits, setHits] = useState<MaterialSearchHit[] | null>(null) // 非 null = 处于搜索结果视图
  const [detail, setDetail] = useState<MaterialNodeDetail | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)

  const currentParent = stack.length ? stack[stack.length - 1].id : ''

  const loadDir = useCallback(
    async (parent: string) => {
      setLoading(true)
      try {
        const resp = await listMaterialNodes({ parent, category: category || undefined, limit: 200 })
        setNodes(resp.nodes)
      } catch (e) {
        message.error(toErrorMessage(e))
        setNodes([])
      } finally {
        setLoading(false)
      }
    },
    [category],
  )

  useEffect(() => {
    if (hits === null) void loadDir(currentParent)
  }, [currentParent, hits, loadDir])

  const doSearch = async () => {
    const q = searchQ.trim()
    if (!q) {
      setHits(null)
      return
    }
    setLoading(true)
    try {
      const resp = await searchMaterial(q, { limit: 200 })
      setHits(resp.hits)
      if (resp.total === 0 && resp.note) message.info(resp.note)
    } catch (e) {
      message.error(toErrorMessage(e))
    } finally {
      setLoading(false)
    }
  }

  const openNode = async (id: string) => {
    try {
      const d = await getMaterialNode(id)
      setDetail(d)
      setDetailOpen(true)
    } catch (e) {
      message.error(toErrorMessage(e))
    }
  }

  const enterFolder = (n: MaterialNode) => {
    setHits(null)
    setSearchQ('')
    setStack((s) => [...s, { id: n.id, title: n.title }])
  }

  const gotoCrumb = (idx: number) => {
    // idx = -1 表示根
    setHits(null)
    setSearchQ('')
    setStack((s) => s.slice(0, idx + 1))
  }

  const doDelete = async (id: string) => {
    try {
      await deleteMaterialNode(id)
      message.success('已删除')
      if (hits !== null) {
        setHits((h) => (h ? h.filter((x) => x.node_id !== id) : h))
      } else {
        void loadDir(currentParent)
      }
    } catch (e) {
      message.error(toErrorMessage(e))
    }
  }

  const doUpload = async (file: File) => {
    try {
      await uploadMaterialFile(file, currentParent)
      message.success(`已上传 ${file.name}`)
      if (hits !== null) setHits(null)
      else void loadDir(currentParent)
    } catch (e) {
      message.error(toErrorMessage(e))
    }
    return false // 阻止 antd 默认行为（我们自己发请求）
  }

  const newFolder = () => {
    let title = ''
    Modal.confirm({
      title: '新建文件夹',
      content: <Input placeholder="文件夹名称" onChange={(e) => (title = e.target.value)} />,
      onOk: async () => {
        if (!title.trim()) {
          message.warning('请填写文件夹名称')
          return
        }
        await createMaterialNode({ node_kind: 'folder', title: title.trim(), parent_id: currentParent })
        if (hits !== null) setHits(null)
        else void loadDir(currentParent)
      },
    })
  }

  // 节点卡行（原型 §E `.mat-row`）：kind 图标 + 名称/类型·大小 + 更新时间 + ⋯（打开/删除）。
  const renderMatRow = (row: MaterialNode) => {
    const meta = KIND_META[row.node_kind]
    const open = () => (row.node_kind === 'folder' ? enterFolder(row) : void openNode(row.id))
    return (
      <div className="fg-mat-row" key={row.id}>
        <div className="fg-mat-ico">{meta.icon}</div>
        <div className="fg-mat-main" onClick={open} style={{ cursor: 'pointer' }}>
          <div className="fg-mat-name">{row.title || '未命名'}</div>
          <div className="fg-mat-meta">
            {meta.label}
            {row.size_bytes > 0 ? ` · ${fmtBytes(row.size_bytes)}` : ''}
          </div>
        </div>
        <div className="fg-mat-when">{fmtDateTime(row.updated_at)}</div>
        <Popconfirm
          title="删除？"
          description={row.node_kind === 'folder' ? '文件夹将连同子节点一起删除' : '确认删除该节点？'}
          okText="删除"
          okButtonProps={{ danger: true }}
          cancelText="取消"
          onConfirm={() => void doDelete(row.id)}
        >
          <span>
            <IconAction icon={<DeleteOutlined />} title="删除" danger />
          </span>
        </Popconfirm>
      </div>
    )
  }


  const hitColumns: ColumnsType<MaterialSearchHit> = [
    {
      title: '命中',
      dataIndex: 'title',
      render: (_v, row) => {
        const meta = KIND_META[row.node_kind]
        return (
          <Space size={8} style={{ cursor: 'pointer' }} onClick={() => void openNode(row.node_id)}>
            <span style={{ color: meta.color }}>{meta.icon}</span>
            <span style={{ fontWeight: 500 }}>{row.title || '未命名'}</span>
          </Space>
        )
      },
    },
    { title: '片段', dataIndex: 'snippet', render: (v: string) => <span style={{ color: tokens.textSecondary }}>{v}</span> },
  ]

  // 面包屑：根目录不再显示「🏠 资料库」那一行（与页面标题重复，2026-11-06 去掉）；
  // 只有进入子文件夹后才出路径，首项用「全部」回根。
  const breadcrumb = useMemo(
    () =>
      stack.length === 0 ? null : (
        <Space size={4} wrap>
          <a onClick={() => gotoCrumb(-1)}>全部</a>
          {stack.map((c, i) => (
            <span key={c.id}>
              <span style={{ color: tokens.textSecondary }}>/</span>{' '}
              <a onClick={() => gotoCrumb(i)}>{c.title || '未命名'}</a>
            </span>
          ))}
        </Space>
      ),
    [stack],
  )

  return (
    <PageContainer title="资料库" description="存放文档、表格、网页剪藏、文件与页面；可按目录浏览、按关键词查找。">
      {/* 工具进内容区工具行（与扩展三页同一版式）：页头只留标题+描述。 */}
      <CardToolbar
        left={
          /* 筛选（分类分段）放左、动作（搜索/新建/上传）放右：与扩展三页「左组右工具」同一节奏。 */
          <Segmented
            value={category}
            onChange={(v) => {
              setCategory(v as '' | 'material' | 'artifact')
              setHits(null)
            }}
            options={[
              { label: '全部', value: '' },
              { label: '资料', value: 'material' },
              { label: '产物', value: 'artifact' },
            ]}
          />
        }
        tools={
          <>
            <Input.Search
              allowClear
              placeholder="关键词搜索"
              style={{ width: 220 }}
              value={searchQ}
              onChange={(e) => {
                setSearchQ(e.target.value)
                if (!e.target.value.trim()) setHits(null)
              }}
              onSearch={() => void doSearch()}
            />
            <Button size="small" onClick={newFolder}>
              新建文件夹
            </Button>
            <AntUpload accept="*" showUploadList={false} beforeUpload={(f) => doUpload(f as File)}>
              <Button size="small" type="primary" icon={<UploadOutlined />}>
                上传
              </Button>
            </AntUpload>
          </>
        }
      />
      {/* 根目录不显示面包屑行（原来只有「🏠 资料库」一个重复节点）；搜索态或进入子目录时才出这一条。 */}
      {(hits !== null || breadcrumb !== null) && (
        <div style={{ padding: '10px 14px', borderBottom: `1px solid ${tokens.border}` }}>
          {hits !== null ? (
            <Space>
              <span style={{ color: tokens.textSecondary }}>搜索「{searchQ}」</span>
              <a onClick={() => { setHits(null); setSearchQ('') }}>返回目录</a>
            </Space>
          ) : (
            breadcrumb
          )}
        </div>
      )}
      <Spin spinning={loading}>
        {hits !== null ? (
          hits.length ? (
            <Table rowKey="node_id" columns={hitColumns} dataSource={hits} pagination={false} />
          ) : (
            <Empty style={{ padding: 40 }} description="没有找到匹配的资料，可换关键词或逐级浏览目录" />
          )
        ) : nodes.length ? (
          <div className="fg-mat-list">{nodes.map(renderMatRow)}</div>
        ) : (
          <Empty style={{ padding: 40 }} description="这里还没有资料，点右上角上传或新建文件夹" />
        )}
      </Spin>
      <NodeDetailDrawer
        detail={detail}
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        onChanged={() => {
          if (hits !== null) void doSearch()
          else void loadDir(currentParent)
        }}
      />
    </PageContainer>
  )
}

function NodeDetailDrawer(props: {
  detail: MaterialNodeDetail | null
  open: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const { detail, open, onClose, onChanged } = props
  const navigate = useNavigate()
  const [rows, setRows] = useState<string>('')
  const [editTitle, setEditTitle] = useState('')
  // 会话产物回跳（v35）：产物节点详情要能回到产出它的会话（provenance_id → artifacts.session_id）。
  const [srcArtifact, setSrcArtifact] = useState<ArtifactMeta | null>(null)

  useEffect(() => {
    if (detail) {
      setRows(detail.rows ?? '')
      setEditTitle(detail.title)
    }
  }, [detail])

  useEffect(() => {
    setSrcArtifact(null)
    if (!detail || detail.category !== 'artifact' || !detail.provenance_id) return
    let alive = true
    // 读失败不影响详情展示：回跳只是便利入口。
    void getArtifact(detail.provenance_id)
      .then((a) => {
        if (alive) setSrcArtifact(a)
      })
      .catch(() => {
        if (alive) setSrcArtifact(null)
      })
    return () => {
      alive = false
    }
  }, [detail])

  if (!detail) return null
  const meta = KIND_META[detail.node_kind]

  const rename = async () => {
    if (!editTitle.trim() || editTitle === detail.title) return
    try {
      await patchMaterialNode(detail.id, { title: editTitle.trim() })
      message.success('已重命名')
      onChanged()
    } catch (e) {
      message.error(toErrorMessage(e))
    }
  }

  const publish = async () => {
    try {
      const r = await publishMaterialPage(detail.id)
      message.success('已发布，可在浏览器打开查看')
      window.open(materialViewUrl(detail.id), '_blank')
      void r
    } catch (e) {
      message.error(toErrorMessage(e))
    }
  }

  return (
    <Modal open={open} onCancel={onClose} footer={null} width={720} title={<Space>{meta.icon}{meta.label}：{detail.title || '未命名'}</Space>}>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        {detail.category === 'artifact' && (
          <Space style={{ alignSelf: 'flex-start' }} wrap>
            <Tag color="purple" style={{ marginInlineEnd: 0 }}>
              会话产物{detail.provenance_id ? ` · ${detail.provenance_id}` : ''}
            </Tag>
            {srcArtifact?.session_id && (
              <Button
                size="small"
                type="link"
                style={{ fontSize: 12, paddingInline: 0 }}
                onClick={() => navigate(`/sessions/${encodeURIComponent(srcArtifact.session_id)}`)}
              >
                查看来源会话
              </Button>
            )}
          </Space>
        )}
        <Space>
          <Input value={editTitle} onChange={(e) => setEditTitle(e.target.value)} style={{ width: 280 }} />
          <Button onClick={() => void rename()} disabled={!editTitle.trim() || editTitle === detail.title}>
            重命名
          </Button>
        </Space>

        {detail.node_kind === 'doc' && (
          <pre style={{ whiteSpace: 'pre-wrap', background: tokens.surface, border: `1px solid ${tokens.border}`, borderRadius: 8, padding: 12, maxHeight: 420, overflow: 'auto' }}>
            {detail.body || '（空文档）'}
          </pre>
        )}

        {detail.node_kind === 'link' && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Typography.Text type="secondary">来源：{detail.source_url}</Typography.Text>
            <pre style={{ whiteSpace: 'pre-wrap', background: tokens.surface, border: `1px solid ${tokens.border}`, borderRadius: 8, padding: 12, maxHeight: 380, overflow: 'auto' }}>
              {detail.body || '（无快照）'}
            </pre>
          </Space>
        )}

        {detail.node_kind === 'table' && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Typography.Text type="secondary">字段：{detail.schema || '（无 schema）'}</Typography.Text>
            <Typography.Paragraph code style={{ whiteSpace: 'pre-wrap', maxHeight: 320, overflow: 'auto' }}>
              {rows || '[]'}
            </Typography.Paragraph>
          </Space>
        )}

        {detail.node_kind === 'page' && (
          <Space direction="vertical">
            <Button type="primary" onClick={() => void publish()}>
              发布并在浏览器打开
            </Button>
            <Typography.Text type="secondary">页面内容：</Typography.Text>
            <iframe title="page-preview" srcDoc={detail.body || ''} style={{ width: '100%', height: 360, border: `1px solid ${tokens.border}`, borderRadius: 8 }} />
          </Space>
        )}

        {detail.node_kind === 'drive' && (
          <Typography.Text type="secondary">文件已存于资料库，可在「资料库」目录中通过 Agent 读取，或经附件接口下载。</Typography.Text>
        )}
      </Space>
    </Modal>
  )
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
