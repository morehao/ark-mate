/**
 * 设置 · 模型（第 4 节）：**两个平级的管理对象** —— ① 供应商凭据 ② 模型。
 *
 * 命名纪律（A13 守卫钉住）：面向用户说「供应商」「模型」；`provider` / `model_id` / `config_write`
 * 这类词只出现在**指向具体资源/接口**的地方（表格列、错误出路），**不当标题**。
 * 也不许把运行时的**不透明 value**（`["p","m"]` / `p/m`）直接摆给用户看——用户看 `model_id` 与显示名。
 *
 * 两条诚实性约束（本屏与初稿最大的差别，改文案前先读）：
 *  ① 「已保存」**不等于**「已生效」：模型配置在**新进程启动时**读，所以保存成功的提示只能说
 *     「已保存，将在下一个会话进程生效」，并且顶部常驻一条配置状态条（路径 / 生成时间 / pending）。
 *  ② 「生效状态列」是按**对账差集**算出来的三态（已生效 / 待生效 / 底座独有），不是按"我们发过一次写"。
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert, Button, Collapse, Form, Input, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  ExperimentOutlined, PlusOutlined, ReloadOutlined, SyncOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import {
  applyModels,
  createModel,
  createProvider,
  deleteModel,
  deleteProvider,
  listModels,
  listProviders,
  listRuntimes,
  patchModel,
  probeProvider,
  toErrorMessage,
  upsertProvider,
} from '@ark-mate/api'
import type {
  ModelRow, ModelsResp, Provider, ProviderCheck, ProviderProbeResp, Runtime, RuntimeConfigResp,
} from '@ark-mate/types'
import { useMate } from '../../store'
import { CardHeader } from './CardHeader'

/** 一个 runtime 的读面（清单 + 事实 + 差集 + 物化状态）。 */
type RuntimeView = ModelsResp | null

const PROVIDER_ID_RE = /^[A-Za-z0-9._-]{1,64}$/

interface ProviderFormValues {
  id?: string
  type?: string
  note?: string
  key?: string
  /**
   * 逐 runtime 的运行时参数（H3）。键是 `rt:field`（形如 `opencode:baseURL`），
   * 值是表单文本；object 类字段按 **JSON 文本**编辑，提交前解析（解析失败当场报错，不静默丢弃）。
   */
  [projectionKey: string]: unknown
}

/** 表单字段名：`<runtime>:<field>`（AntD 的 name 路径不支持嵌套的 runtime 名做动态键，故用扁平键）。 */
const projectionFieldName = (runtime: string, field: string): string => `${runtime}:${field}`

interface ModelFormValues {
  provider_id: string
  model_id: string
  name?: string
  hidden?: boolean
  pinned?: boolean
  is_default?: boolean
}

/** 生效状态三态：**按对账差集**算，不按"我们发过写"。 */
type EffectState = 'live' | 'pending' | 'runtime-only'

const EFFECT_META: Record<EffectState, { label: string; color: string }> = {
  live: { label: '已生效', color: tokens.success },
  pending: { label: '待生效', color: tokens.warning },
  'runtime-only': { label: '底座独有', color: tokens.textPlaceholder },
}

export function ModelSettingsPanel() {
  const agents = useMate((s) => s.agents)
  const showToast = useMate((s) => s.showToast)

  const [providers, setProviders] = useState<Provider[]>([])
  const [runtimes, setRuntimes] = useState<Runtime[]>([])
  const [providerId, setProviderId] = useState<string>('')
  // runtime 候选从**已注册的 agent** 的 `runtimes[]` 并集里取（平台没有单独的 runtime 列表面，
  // 而 agent 的 runtimes 就是"哪些底座真的在用"的权威投影）。dsh/opencode 两个内置 agent 恒在。
  const runtimeOptions = useMemo(() => {
    const set = new Set<string>()
    for (const a of agents) for (const rt of a.runtimes ?? []) set.add(rt)
    const ids = Array.from(set).sort()
    return ids.length ? ids : ['deepseekharness', 'opencode']
  }, [agents])
  const [runtimeId, setRuntimeId] = useState<string>('opencode')
  const [view, setView] = useState<RuntimeView>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [probe, setProbe] = useState<{ id: string; data: ProviderProbeResp | null; err: string | null } | null>(null)
  const [providerModal, setProviderModal] = useState<{ mode: 'create' } | { mode: 'edit'; row: Provider } | null>(null)
  const [modelModal, setModelModal] = useState<{ mode: 'create' } | { mode: 'edit'; row: ModelRow } | null>(null)
  const [busy, setBusy] = useState(false)
  const [providerForm] = Form.useForm<ProviderFormValues>()
  const [modelForm] = Form.useForm<ModelFormValues>()

  const loadProviders = useCallback(async () => {
    try {
      const rows = await listProviders()
      setProviders(rows)
      setProviderId((cur) => cur || rows[0]?.id || '')
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }, [showToast])

  // 运行时参数 schema 由后端派生（`GET /runtimes` 的 provider_projection）——前端不硬编码字段：
  // 只渲染「该底座声明了 schema」的那几块，加一个底座不需要改这里。
  const schemaRuntimes = useMemo(
    () => runtimes.filter((r) => r.provider_projection && (r.provider_projection.fields?.length ?? 0) > 0),
    [runtimes],
  )

  const loadView = useCallback(async (rt: string, refresh = false) => {
    if (!rt) return
    if (refresh) setRefreshing(true)
    try {
      const resp = await listModels(undefined, rt, refresh)
      // 旧形态回落（未声明落点的 runtime）也能喂进来：这里只用到 declared/diff/config，
      // 缺字段时按空数组处理，不额外分支。
      setView(resp as ModelsResp)
    } catch (err) {
      showToast(toErrorMessage(err))
    } finally {
      if (refresh) setRefreshing(false)
    }
  }, [showToast])

  useEffect(() => {
    void loadProviders()
    void (async () => {
      try {
        setRuntimes(await listRuntimes())
      } catch {
        // 拿不到底座列表 = 运行时参数区不渲染（其余照常）；不打断设置页。
        setRuntimes([])
      }
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!runtimeId) return
    void loadView(runtimeId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runtimeId])

  const guard = async <T,>(p: Promise<T>): Promise<T | undefined> => {
    setBusy(true)
    try {
      return await p
    } catch (err) {
      showToast(toErrorMessage(err))
      return undefined
    } finally {
      setBusy(false)
    }
  }

  /** 写面之后的统一收尾：提示语**只说"已保存、将在下一个会话进程生效"**，绝不说"已生效"。 */
  const afterModelWrite = async (note: string): Promise<void> => {
    showToast(note)
    await loadView(runtimeId, false)
  }

  // ——— ① 供应商 ———

  const openProviderModal = (target: { mode: 'create' } | { mode: 'edit'; row: Provider }): void => {
    setProviderModal(target)
    if (target.mode === 'edit') {
      providerForm.setFieldsValue({ id: target.row.id, type: target.row.type, note: target.row.note, key: '' })
      // 回填运行时参数：object 类字段按 JSON 文本回显（与提交时的解析口径对称）。
      const projections = target.row.projections ?? {}
      const filled: Record<string, unknown> = {}
      for (const rt of schemaRuntimes) {
        const frag = projections[rt.id] ?? {}
        for (const f of rt.provider_projection?.fields ?? []) {
          const v = frag[f.name]
          if (v === undefined) continue
          filled[projectionFieldName(rt.id, f.name)] = f.type === 'object' ? JSON.stringify(v, null, 2) : String(v)
        }
      }
      providerForm.setFieldsValue(filled)
    } else {
      providerForm.resetFields()
    }
  }

  /** 把表单里的运行时参数收集成 `{runtime: fragment}`；object 字段解析失败即抛（不静默丢弃）。 */
  const collectProjections = (values: Record<string, unknown>): Record<string, Record<string, unknown>> => {
    const out: Record<string, Record<string, unknown>> = {}
    for (const rt of schemaRuntimes) {
      const frag: Record<string, unknown> = {}
      for (const f of rt.provider_projection?.fields ?? []) {
        const raw = values[projectionFieldName(rt.id, f.name)]
        if (raw === undefined || raw === null || raw === '') continue
        if (f.type === 'object') {
          try {
            frag[f.name] = JSON.parse(String(raw))
          } catch (err) {
            throw new Error(`${rt.id} 的 ${f.name} 不是合法 JSON：${toErrorMessage(err)}`)
          }
        } else {
          frag[f.name] = String(raw)
        }
      }
      if (Object.keys(frag).length > 0) out[rt.id] = frag
    }
    return out
  }

  const submitProvider = async (): Promise<void> => {
    const v = await providerForm.validateFields()
    let projections: Record<string, Record<string, unknown>>
    try {
      projections = collectProjections(v as unknown as Record<string, unknown>)
    } catch (err) {
      showToast(toErrorMessage(err))
      return
    }
    if (providerModal?.mode === 'create') {
      if (!PROVIDER_ID_RE.test(v.id ?? '')) {
        showToast('id 只能含字母数字与 . _ -（1-64 位）：它会成为密钥文件名的一部分')
        return
      }
      const created = await guard(createProvider({
        id: v.id ?? '', type: v.type ?? '', note: v.note, key: v.key || undefined,
        projections: Object.keys(projections).length ? projections : undefined,
      }))
      if (!created) return
      showToast('凭据已保存（密钥不读回）。改密钥要等下一个轮次边界才会被会话拿到')
    } else if (providerModal?.mode === 'edit') {
      const row = providerModal.row
      const patch: {
        type?: string
        note?: string
        key?: string
        projections?: Record<string, Record<string, unknown>>
      } = {}
      if (v.type !== row.type) patch.type = v.type
      if ((v.note ?? '') !== (row.note ?? '')) patch.note = v.note ?? ''
      // 密钥留空 = 不改（避免"编辑备注顺手把密钥清掉"）。
      if (v.key) patch.key = v.key
      // 运行时参数：只在**真的改了**时才提交（逐 runtime 比较，避免"打开就保存"把空对象写成删除）。
      const before = row.projections ?? {}
      for (const [rt, frag] of Object.entries(projections)) {
        if (JSON.stringify(before[rt] ?? {}) !== JSON.stringify(frag)) {
          patch.projections = { ...(patch.projections ?? {}), [rt]: frag }
        }
      }
      if (Object.keys(patch).length === 0) {
        showToast('没有改动')
        return
      }
      const updated = await guard(upsertProvider(row.id, patch))
      if (!updated) return
      const eff = updated.effect
      showToast(eff && eff.effect === 'none'
        ? '已保存：这些字段不驱动行为，本轮与后续轮次都不变'
        : `已保存：${eff?.note ?? '引用它的会话会在下一个轮次边界拿到新凭据'}`)
    }
    setProviderModal(null)
    await loadProviders()
  }

  const removeProvider = async (row: Provider): Promise<void> => {
    const resp = await guard(deleteProvider(row.id))
    if (!resp) return
    if (!resp.deleted) {
      showToast('这条凭据已经不存在（幂等）')
    } else if (resp.vault_ciphertext_left) {
      showToast(`凭据已删除，但密文残留：${resp.vault_ciphertext_left}（mate doctor 的孤儿密文段可见）`)
    } else {
      showToast('凭据已删除')
    }
    if (providerId === row.id) setProviderId('')
    await loadProviders()
  }

  const runProbe = async (row: Provider, upstream: boolean): Promise<void> => {
    setProbe({ id: row.id, data: null, err: null })
    try {
      const data = await probeProvider(row.id, upstream)
      setProbe({ id: row.id, data, err: null })
    } catch (err) {
      setProbe({ id: row.id, data: null, err: toErrorMessage(err) })
    }
  }

  // ——— ② 模型 ———

  const providerOptions = useMemo(
    () => providers.map((p) => ({ value: p.id, label: `${p.id}（${p.type}）` })),
    [providers],
  )

  /** 生效状态：两侧都有 = 已生效；平台有、底座没有 = 待生效；底座有、平台没有 = 底座独有。 */
  const stateOf = (value: string): EffectState => {
    const inDeclared = (view?.declared ?? []).some((d) => d.value === value)
    const inAdvertised = (view?.advertised ?? []).some((a) => a.value === value)
    if (inDeclared && inAdvertised) return 'live'
    if (inDeclared) return 'pending'
    return 'runtime-only'
  }

  const openModelModal = (target: { mode: 'create' } | { mode: 'edit'; row: ModelRow }): void => {
    setModelModal(target)
    if (target.mode === 'edit') {
      modelForm.setFieldsValue({
        provider_id: target.row.provider_id, model_id: target.row.model_id, name: target.row.name,
        hidden: target.row.hidden, pinned: target.row.pinned, is_default: target.row.is_default,
      })
    } else {
      modelForm.resetFields()
      modelForm.setFieldsValue({ provider_id: providerOptions[0]?.value })
    }
  }

  const submitModel = async (): Promise<void> => {
    const v = await modelForm.validateFields()
    if (modelModal?.mode === 'create') {
      const created = await guard(createModel({
        runtime: runtimeId, provider_id: v.provider_id, model_id: v.model_id,
        name: v.name, hidden: v.hidden, pinned: v.pinned, is_default: v.is_default,
      }))
      if (!created) return
      // **文案纪律**：清单写面在下一进程生效，绝不说"已生效"。
      await afterModelWrite('已保存，将在下一个会话进程生效（配置在进程启动时读）')
    } else if (modelModal?.mode === 'edit') {
      const row = modelModal.row
      const updated = await guard(patchModel(row.id, {
        name: v.name, hidden: !!v.hidden, pinned: !!v.pinned,
        is_default: v.is_default && !row.is_default ? true : undefined,
      }))
      if (!updated) return
      await afterModelWrite(v.is_default && !row.is_default
        ? '已设为该底座的默认模型：新会话创建时立即下发；既有会话在下一个会话进程读到它'
        : '已保存，将在下一个会话进程生效（配置在进程启动时读）')
    }
    setModelModal(null)
  }

  const removeModel = async (row: ModelRow): Promise<void> => {
    const resp = await guard(deleteModel(row.id))
    if (!resp) return
    if (!resp.deleted) {
      showToast('这条模型条目已经不存在（幂等）')
    } else {
      showToast('已删除：既有会话不受影响（底座已保存的取值仍可继续发请求）')
    }
    await loadView(runtimeId, false)
  }

  /**
   * 置/取消默认。两个方向都走同一条 PATCH——**取消默认必须真的能取消**：只置不撤的话，
   * 用户想让底座回到自己的默认就只能删掉整条模型，而删掉又会丢掉 `extra`/`hidden` 那些留痕。
   * 文案分开，因为两件事的后果不同（置默认影响新会话下发的取值；取消默认让新会话回到底座默认）。
   */
  const setDefault = async (row: ModelRow, next: boolean): Promise<void> => {
    const updated = await guard(patchModel(row.id, { is_default: next }))
    if (!updated) return
    await afterModelWrite(next
      ? '已设为默认：新会话创建时立即下发；既有会话在下一个会话进程读到它'
      : '已取消默认：新会话按底座自己的默认起；已有会话的取值不变')
  }

  const applyNow = async (): Promise<void> => {
    const resp = await guard(applyModels(runtimeId))
    if (!resp) return
    const st = resp.applied.find((a) => a.runtime === runtimeId)
    showToast(st?.note ?? '已重新物化。**此刻还拿不到底座的新目录**（要新进程）——生效状态列稍后会显示"已生效"')
    await loadView(runtimeId, true)
  }

  const config: RuntimeConfigResp | undefined = view?.config
  const declaredColumns: ColumnsType<ModelRow> = [
    {
      title: '模型', dataIndex: 'model_id', key: 'model_id',
      render: (_: unknown, row: ModelRow) => (
        <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
          <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{row.model_id}</span>
          {row.pinned && <Tag color="blue">置顶</Tag>}
          {row.hidden && <Tag>已隐藏</Tag>}
        </span>
      ),
    },
    { title: '显示名', dataIndex: 'name', key: 'name', width: 160, render: (v?: string) => v || <span style={{ color: tokens.textPlaceholder }}>—</span> },
    {
      title: '供应商', dataIndex: 'provider_id', key: 'provider_id', width: 140,
      render: (v: string) => <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{v}</span>,
    },
    {
      title: '默认', key: 'default', width: 72,
      render: (_: unknown, row: ModelRow) => (
        <Tooltip title={row.is_default
          ? '该底座的默认模型；点一下**取消**默认（取消后新会话按底座自己的默认起）'
          : '点一下把它设为该底座的默认模型'}>
          {row.is_default ? (
            <Popconfirm
              title="取消这条默认模型？"
              description="取消后新会话按底座自己的默认起；已经有会话的取值不变。"
              okText="取消默认"
              cancelText="算了"
              onConfirm={() => void setDefault(row, false)}
            >
              <Button
                type="text"
                size="small"
                disabled={busy}
                style={{ color: tokens.warning, fontSize: 15 }}
                aria-label="取消默认模型"
              >
                ★
              </Button>
            </Popconfirm>
          ) : (
            <Button
              type="text"
              size="small"
              disabled={busy}
              onClick={() => void setDefault(row, true)}
              style={{ color: tokens.textPlaceholder, fontSize: 15 }}
              aria-label="设为默认模型"
            >
              ☆
            </Button>
          )}
        </Tooltip>
      ),
    },
    {
      title: '生效状态', key: 'effect', width: 200,
      render: (_: unknown, row: ModelRow) => {
        const st = stateOf(row.value)
        const meta = EFFECT_META[st]
        return (
          <Tooltip title={st === 'pending'
            ? '平台已声明，底座还没广告它：配置文件在**新进程**启动时读——点上面的「立即应用」或新开一个会话'
            : st === 'runtime-only'
              ? '底座自带的条目（平台没有声明它）：平台不管它，如实展示、不删'
              : '两侧都有：已生效'}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 6, height: 6, borderRadius: 3, background: meta.color }} />
              <span style={{ fontSize: 12 }}>{meta.label}</span>
            </span>
          </Tooltip>
        )
      },
    },
    {
      title: '操作', key: 'actions', width: 150,
      render: (_: unknown, row: ModelRow) => (
        <Space size={4}>
          <Button type="link" size="small" onClick={() => openModelModal({ mode: 'edit', row })}>编辑</Button>
          <Popconfirm
            title="删除这条模型条目？"
            description="既有会话不受影响（底座已保存的取值仍可继续发请求）；但清单里没有它之后，新会话不会再自动用它。"
            onConfirm={() => void removeModel(row)}
          >
            <Button type="link" size="small" danger>删除</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const advertisedRows = useMemo(() => {
    const declared = new Set((view?.declared ?? []).map((d) => d.value))
    return (view?.advertised ?? [])
      .filter((a) => !declared.has(a.value))
      .map((a, i) => ({ key: `${a.value}-${i}`, ...a }))
  }, [view])

  return (
    <div className="fg-model-panel">
      {/* ——— ① 供应商 ——— */}
      <section className="fg-card" aria-label="供应商">
        <CardHeader
          title="供应商"
          hint="上游凭据：登记密钥、声明它在各底座上怎么写、自检能不能连通"
          extra={(
            <Space size={6}>
              <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadProviders()}>刷新</Button>
              <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => openProviderModal({ mode: 'create' })}>
                新建凭据
              </Button>
            </Space>
          )}
        />
        <div className="fg-card-body">
          {providers.length === 0 && (
            <Alert
              type="info"
              showIcon
              message="还没有供应商凭据"
              description="模型条目必须挂靠在一条凭据下：先在这里建一条（填上游类型与密钥），再到下面「模型」块里登记模型。"
            />
          )}
          {providers.length > 0 && (
            <Table<Provider>
              size="small"
              rowKey="id"
              pagination={false}
              dataSource={providers}
              columns={[
                {
                  title: 'id', dataIndex: 'id', key: 'id',
                  render: (v: string) => <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{v}</span>,
                },
                { title: '上游类型', dataIndex: 'type', key: 'type', width: 130 },
                {
                  title: '密钥', dataIndex: 'has_key', key: 'has_key', width: 90,
                  render: (v: boolean) => (v ? <Tag color="green">已存</Tag> : <Tag color="orange">未设置</Tag>),
                },
                {
                  title: '被引用', key: 'refs', width: 220,
                  render: (_: unknown, row: Provider) => (
                    <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                      {row.refs?.agents?.length ? `Agent ${row.refs.agents.join('、')}` : '暂无 Agent 使用'}
                      {row.refs?.runtimes?.length ? `（${row.refs.runtimes.join('、')}）` : ''}
                    </span>
                  ),
                },
                {
                  title: '本地体检', key: 'problems', width: 240,
                  render: (_: unknown, row: Provider) => (row.problems?.length
                    ? <Tooltip title={row.problems.join('；')}><Tag color="orange">{row.problems.length} 项待处理</Tag></Tooltip>
                    : <span style={{ fontSize: 12, color: tokens.textPlaceholder }}>正常</span>),
                },
                {
                  title: '操作', key: 'actions', width: 260,
                  render: (_: unknown, row: Provider) => (
                    <Space size={2} wrap>
                      <Button type="link" size="small" onClick={() => openProviderModal({ mode: 'edit', row })}>编辑</Button>
                      <Button type="link" size="small" icon={<ExperimentOutlined />} onClick={() => void runProbe(row, false)}>
                        自检
                      </Button>
                      <Popconfirm
                        title="删除这条凭据？"
                        description="被 Agent 或模型条目引用时会被拒绝——那是为了避免静默换掉凭据来源。"
                        onConfirm={() => void removeProvider(row)}
                      >
                        <Button type="link" size="small" danger>删除</Button>
                      </Popconfirm>
                    </Space>
                  ),
                },
              ]}
            />
          )}

          {/* 自检结果就地展开（pass 绿 / fail 红 / warn·unknown 灰，每条带可执行的 remedy）。 */}
          {probe && (
            <div className="fg-probe" aria-label="自检结果">
              <div className="fg-probe-head">
                <b>自检 · {probe.id}</b>
                {probe.data && (
                  <span style={{ color: probe.data.ok ? tokens.success : tokens.error, fontSize: 12 }}>
                    {probe.data.ok ? '本地段无失败项' : '有失败项'}
                  </span>
                )}
                <span style={{ flex: 1 }} />
                <Button size="small" type="link" onClick={() => void runProbe(
                  providers.find((p) => p.id === probe.id) ?? ({ id: probe.id } as Provider), true,
                )}>
                  连一次上游（会把密钥发给第三方）
                </Button>
                <Button size="small" type="link" onClick={() => setProbe(null)}>收起</Button>
              </div>
              {probe.err && <Alert type="error" showIcon message={probe.err} />}
              {probe.data?.checks?.map((c: ProviderCheck, i: number) => (
                <div key={`${c.name}-${i}`} className="fg-probe-row">
                  <span className={`fg-probe-dot fg-${c.status}`} aria-hidden />
                  <span className="fg-probe-name">{c.name}</span>
                  <span className="fg-probe-detail">{c.detail}</span>
                  {c.remedy && <span className="fg-probe-remedy">{c.remedy}</span>}
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {/* ——— ② 模型 ——— */}
      <section className="fg-card" aria-label="模型">
        <CardHeader
          title="模型"
          hint="清单、默认模型与生效对账（每个底座一份清单、一个默认）"
          extra={(
            <Space size={6}>
              <Select
                size="small"
                value={runtimeId}
                style={{ width: 150 }}
                onChange={setRuntimeId}
                options={runtimeOptions.map((id) => ({ value: id, label: id }))}
              />
              <Button size="small" icon={<ReloadOutlined />} loading={refreshing} onClick={() => void loadView(runtimeId, true)}>
                刷新目录
              </Button>
              <Button size="small" icon={<SyncOutlined />} disabled={busy} onClick={() => void applyNow()}>
                立即应用
              </Button>
              <Button
                size="small" type="primary" icon={<PlusOutlined />}
                disabled={providers.length === 0}
                onClick={() => openModelModal({ mode: 'create' })}
              >
                新建模型
              </Button>
            </Space>
          )}
        />
        <div className="fg-card-body">
          {/* 配置状态条：路径 / 生成时间 / pending。**摘要对不上就报出来，不静默覆盖。** */}
          {config && (
            <div className="fg-cfgbar">
              <span className={`fg-cfgbar-dot${config.pending ? ' fg-pending' : ''}`} aria-hidden />
              <span>
                {config.declared
                  ? (config.writable
                    ? `配置文件：${config.path ?? '—'}`
                    : `该底座尚未实测：只读（平台会生成候选，但不落盘）`)
                  : '该底座未声明模型配置落点：平台清单对它不可达'}
              </span>
              {config.generated_at && <span className="fg-cfgbar-dim">生成于 {config.generated_at}</span>}
              <span className="fg-cfgbar-dim">通道 {config.channel ?? '—'}</span>
              {config.pending && <Tag color="orange">待生效（新进程启动时读）</Tag>}
            </div>
          )}
          {config?.note && <Alert type="info" showIcon message={config.note} style={{ marginBottom: 8 }} />}

          {view && (
            <>
              <Table<ModelRow>
                size="small"
                rowKey="id"
                pagination={false}
                loading={busy}
                dataSource={view.declared ?? []}
                locale={{ emptyText: '还没有声明任何模型。先建一条供应商凭据，再点右上「新建模型」。' }}
                columns={declaredColumns}
              />

              <div className="fg-diffnote">
                <b>生效说明</b>：新会话默认用它；会话内可另选（立即生效）。「保存成功」只表示**已写入平台清单**，
                底座的配置文件在**新进程启动时**才读——所以状态列才是"到底生效了没有"的唯一答案。
              </div>

              {/* 差集：如实报出"写了没生效"与"底座有平台没声明"。 */}
              {(view.diff?.declared_not_advertised?.length ?? 0) > 0 && (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginBottom: 8 }}
                  message={`有 ${view.diff.declared_not_advertised.length} 条平台已声明但底座还没广告`}
                  description={(
                    <ul className="fg-difflist">
                      {view.diff.declared_not_advertised.map((d) => (
                        <li key={d.value}>
                          <code>{d.model_id || d.value}</code>
                          <span className={`fg-sev fg-${d.severity}`}>{d.severity}</span>
                          <span>{d.reason}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                />
              )}

              {advertisedRows.length > 0 && (
                <div className="fg-advertised">
                  <div className="fg-advertised-head">
                    底座独有（{advertisedRows.length} 条，平台不声明也不删）
                    <Tooltip title="这些是底座自己的目录里的条目（opencode 的内置 models.dev 目录会一直在，那是常态不是故障）">
                      <span className="fg-diffnote-inline">为什么会有？</span>
                    </Tooltip>
                  </div>
                  <div className="fg-advertised-list">
                    {advertisedRows.slice(0, 40).map((a) => (
                      <span key={a.key} className="fg-advertised-item">
                        <code>{a.value}</code>
                        {a.unavailable && <span className="fg-cfgbar-dim">运行时标为不可用</span>}
                      </span>
                    ))}
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      </section>

      {/* ——— 供应商表单 ——— */}
      <Modal
        open={providerModal !== null}
        title={providerModal?.mode === 'create' ? '新建供应商凭据' : '编辑供应商凭据'}
        onCancel={() => setProviderModal(null)}
        onOk={() => void submitProvider()}
        confirmLoading={busy}
        okText="保存"
        destroyOnHidden
      >
        <Form form={providerForm} layout="vertical" preserve={false}>
          <Form.Item label="id" name="id" rules={[{ required: true, message: 'id 必填（它会成为密钥文件名的一部分）' }]}>
            <Input disabled={providerModal?.mode === 'edit'} placeholder="例如 deepseek-main（仅字母数字与 . _ -）" />
          </Form.Item>
          <Form.Item label="上游类型" name="type" rules={[{ required: providerModal?.mode === 'create', message: '类型决定注入哪个环境变量' }]}>
            <Select
              showSearch
              placeholder="例如 deepseek / openai / anthropic"
              options={['deepseek', 'openai', 'anthropic', 'openrouter', 'groq'].map((v) => ({ value: v, label: v }))}
            />
          </Form.Item>
          <Form.Item
            label={providerModal?.mode === 'edit' ? '密钥（留空 = 不改）' : '密钥'}
            name="key"
            extra="密钥只写不读回；保存后要等下一个轮次边界才会被会话拿到"
          >
            <Input.Password placeholder="sk-…" autoComplete="off" />
          </Form.Item>
          <Form.Item label="备注" name="note">
            <Input placeholder="例如「主账号」" />
          </Form.Item>
          {schemaRuntimes.length > 0 && (
            <Collapse
              ghost
              size="small"
              items={schemaRuntimes.map((rt) => ({
                key: rt.id,
                label: `运行时参数 · ${rt.name || rt.id}`,
                children: (
                  <div className="fg-projfields">
                    <div className="fg-diffnote">
                      {rt.provider_projection?.note}
                      「未知键」会原样保留，平台只校验它认识的字段。
                    </div>
                    {(rt.provider_projection?.fields ?? []).map((f) => (
                      <Form.Item
                        key={f.name}
                        label={`${f.name}${f.required ? '' : '（可选）'}${f.type === 'object' ? ' · JSON' : ''}`}
                        name={projectionFieldName(rt.id, f.name)}
                        extra={f.note}
                      >
                        {f.type === 'object'
                          ? <Input.TextArea rows={3} placeholder='{"key":"value"}' autoComplete="off" />
                          : <Input placeholder={f.note} autoComplete="off" />}
                      </Form.Item>
                    ))}
                  </div>
                ),
              }))}
            />
          )}
        </Form>
      </Modal>

      {/* ——— 模型表单 ——— */}
      <Modal
        open={modelModal !== null}
        title={modelModal?.mode === 'create' ? `新建模型 · ${runtimeId}` : '编辑模型'}
        onCancel={() => setModelModal(null)}
        onOk={() => void submitModel()}
        confirmLoading={busy}
        okText="保存"
        destroyOnHidden
      >
        <Form form={modelForm} layout="vertical" preserve={false}>
          <Form.Item label="供应商" name="provider_id" rules={[{ required: true, message: '必须挂靠在一条凭据下' }]}>
            <Select
              disabled={modelModal?.mode === 'edit'}
              placeholder="选择一条供应商凭据"
              options={providerOptions}
            />
          </Form.Item>
          <Form.Item label="模型 id" name="model_id" rules={[{ required: true, message: '模型 id 必填' }]}>
            <Input disabled={modelModal?.mode === 'edit'} placeholder="底座侧认的模型 id，例如 deepseek-v4-flash" />
          </Form.Item>
          <Form.Item label="显示名" name="name" extra="只影响平台呈现与配置里的 name；改不动底座候选里的路由前缀">
            <Input placeholder="可选" />
          </Form.Item>
          <Space size={24}>
            <Form.Item label="在该底座隐藏" name="hidden" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item label="置顶" name="pinned" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item label="设为默认" name="is_default" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
          <div className="fg-diffnote">
            「隐藏」只影响平台的选择器，**不会**把模型从底座删掉；「置顶」只改平台列表顺序。
            两者都不下发。
          </div>
        </Form>
      </Modal>
    </div>
  )
}
