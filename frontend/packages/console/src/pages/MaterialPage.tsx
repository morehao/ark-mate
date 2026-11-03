import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Button,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Space,
  Spin,
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
  HomeOutlined,
  LinkOutlined,
  TableOutlined,
  UploadOutlined,
  Html5Outlined,
  CloudUploadOutlined,
} from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import {
  createMaterialNode,
  deleteMaterialNode,
  getMaterialNode,
  listMaterialNodes,
  materialViewUrl,
  patchMaterialNode,
  publishMaterialPage,
  searchMaterial,
  toErrorMessage,
  uploadMaterialFile,
} from '@ark-mate/api'
import type { MaterialNode, MaterialNodeDetail, MaterialNodeKind, MaterialSearchHit } from '@ark-mate/types'

// ——— 资料库页（v33 P5）———
//
// 三件事：**看得见**（目录树逐级浏览）、**找得到**（关键词搜索，不是语义）、**放得进**（上传 +
// 新建文件夹）。详情抽屉按 kind 展示正文 / 表格 / 页面预览；drive 给"打开/下载"。
//
// 本期范围：只做资料库、不做知识库——搜索框标「关键词搜索」，页面上**不出现**「智能/语义/知识库」。

const KIND_META: Record<MaterialNodeKind, { label: string; icon: React.ReactNode; color: string }> = {
  folder: { label: '文件夹', icon: <FolderOutlined />, color: tokens.textSecondary },
  doc: { label: '文档', icon: <FileTextOutlined />, color: '#3b82f6' },
  table: { label: '表格', icon: <TableOutlined />, color: '#10b981' },
  link: { label: '网页剪藏', icon: <LinkOutlined />, color: '#8b5cf6' },
  drive: { label: '文件', icon: <CloudUploadOutlined />, color: '#f59e0b' },
  page: { label: '网页', icon: <Html5Outlined />, color: '#ec4899' },
}

interface Crumb {
  id: string
  title: string
}

export function MaterialPage() {
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
        const resp = await listMaterialNodes({ parent, limit: 200 })
        setNodes(resp.nodes)
      } catch (e) {
        message.error(toErrorMessage(e))
        setNodes([])
      } finally {
        setLoading(false)
      }
    },
    [],
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

  const columns: ColumnsType<MaterialNode> = [
    {
      title: '名称',
      dataIndex: 'title',
      render: (_v, row) => {
        const meta = KIND_META[row.node_kind]
        return (
          <Space size={8} style={{ cursor: 'pointer' }} onClick={() => (row.node_kind === 'folder' ? enterFolder(row) : void openNode(row.id))}>
            <span style={{ color: meta.color }}>{meta.icon}</span>
            <span style={{ fontWeight: 500 }}>{row.title || '未命名'}</span>
            <Tag style={{ marginInlineStart: 4 }}>{meta.label}</Tag>
          </Space>
        )
      },
    },
    { title: '大小', dataIndex: 'size_bytes', width: 120, render: (v: number) => (v > 0 ? fmtBytes(v) : '—') },
    { title: '更新时间', dataIndex: 'updated_at', width: 200, render: (v: string) => v.replace('T', ' ').replace('Z', '').slice(0, 19) },
    {
      title: '',
      width: 96,
      render: (_v, row) => (
        <Space>
          <Button size="small" type="text" onClick={() => void openNode(row.id)}>
            打开
          </Button>
          <Popconfirm title="删除？" description={row.node_kind === 'folder' ? '文件夹将连同子节点一起删除' : '确认删除该节点？'} onConfirm={() => void doDelete(row.id)}>
            <Button size="small" type="text" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

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

  const breadcrumb = useMemo(
    () => (
      <Space size={4} wrap>
        <a onClick={() => gotoCrumb(-1)}>
          <HomeOutlined /> 资料库
        </a>
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
    <PageContainer
      title="资料库"
      description="存放文档、表格、网页剪藏、文件与页面；可按目录浏览、按关键词查找。"
      extra={
        <Space>
          <Input.Search
            allowClear
            placeholder="关键词搜索"
            style={{ width: 240 }}
            value={searchQ}
            onChange={(e) => {
              setSearchQ(e.target.value)
              if (!e.target.value.trim()) setHits(null)
            }}
            onSearch={() => void doSearch()}
          />
          <Button onClick={newFolder}>新建文件夹</Button>
          <AntUpload accept="*" showUploadList={false} beforeUpload={(f) => doUpload(f as File)}>
            <Button type="primary" icon={<UploadOutlined />}>
              上传
            </Button>
          </AntUpload>
        </Space>
      }
    >
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
      <Spin spinning={loading}>
        {hits !== null ? (
          hits.length ? (
            <Table rowKey="node_id" columns={hitColumns} dataSource={hits} pagination={false} />
          ) : (
            <Empty style={{ padding: 40 }} description="没有找到匹配的资料，可换关键词或逐级浏览目录" />
          )
        ) : nodes.length ? (
          <Table rowKey="id" columns={columns} dataSource={nodes} pagination={false} />
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
  const [rows, setRows] = useState<string>('')
  const [editTitle, setEditTitle] = useState('')

  useEffect(() => {
    if (detail) {
      setRows(detail.rows ?? '')
      setEditTitle(detail.title)
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
