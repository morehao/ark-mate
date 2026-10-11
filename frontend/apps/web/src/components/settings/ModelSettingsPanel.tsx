/**
 * 设置 · 模型（第 4 节）：**两个平级的管理对象** —— ① 供应商 ② 模型。
 *
 * 命名纪律（A13 守卫钉住）：这一整节面向用户的字（标题 / 正文 / toast / tooltip / 空态 /
 * 错误出路 / 状态标签）只说「供应商」「模型」「已生效 / 待生效」「已保存」。底座名与 `provider`
 * / `runtime` 这类代码词一个都不许出现在**用户可见的字**里；它们只允许出现在指向接口的映射代码
 * （对象键、字段访问）里——那不是给用户看的字。
 *
 * 两条诚实性约束（改文案前先读）：
 *  ① 「已保存」**不等于**「已生效」：生效结论只读聚合字段 `effective` / `effective_at`
 *     （applied | pending | failed；now | next_session | next_launch），
 *     **不渲染** `sync[]` 里的逐后端明细（那是机器面）。
 *  ② `effective_at` 取**所有后端里最晚的那个**（保守）：改密钥在某一侧可能这一轮就生效，
 *     页面仍会说「下次启动应用生效」。这是拍板接受的代价，**不要"优化"掉它**，
 *     也不要在页面上解释差异（那必然要把后端名带回来）。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert, Button, Checkbox, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Tooltip,
} from 'antd'
import type { InputRef } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  ExperimentOutlined, PlusOutlined, ReloadOutlined, SyncOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import {
  createModel,
  createProvider,
  deleteModel,
  deleteProvider,
  listModels,
  listProviders,
  patchModel,
  patchProvider,
  probeProvider,
  syncModels,
  toErrorMessage,
} from '@ark-mate/api'
import type {
  ModelRow, ModelsResp, Provider, ProviderCheck, ProviderPatch, ProviderProbeResp,
} from '@ark-mate/types'
import { useMate } from '../../store'
import { CardHeader } from './CardHeader'

/** 供应商 id 的词法（与 `POST /providers` 的校验一致；它会进凭据引用名）。 */
const VENDOR_ID_RE = /^[A-Za-z0-9._-]{1,64}$/

/** 本期只有一种协议（枚举，不做自由串）。 */
const PROTOCOLS = [{ value: 'openai', label: 'openai' }]

interface ProviderFormValues {
  id?: string
  name?: string
  protocol?: string
  base_url?: string
  api_key?: string
  /** 勾上 = 清掉已存密钥（密钥只写不读回，"留空"与"清除"必须分得开）。 */
  api_key_clear?: boolean
  note?: string
}

interface ModelFormValues {
  /** 供应商选择（wire 字段是 `provider_id`，只在提交时以对象键拼出来，不进用户可见的字）。 */
  vendor?: string
  model_id?: string
  name?: string
  note?: string
  /** 勾上 = 这条模型可接收图片（落成 `['text','image']`；不勾 = 默认的 `['text']`）。 */
  image_input?: boolean
}

/** 平台声明的输入模态 → 落库/上行的数组形态（两态：`['text']` | `['text','image']`）。 */
function modalitiesOf(imageInput: boolean | undefined): string[] {
  return imageInput ? ['text', 'image'] : ['text']
}

/** 反解：一条模型当前是否声明了图片输入。 */
function hasImageInput(row: ModelRow): boolean {
  return (row.input_modalities ?? []).includes('image')
}

/**
 * 提交时以**框里真正显示的那一份**密钥为准（`domValue` 优先，表单值兜底）。
 *
 * 为什么不能只信表单值（2026-10-11 实测定位）：浏览器密码管理器与部分扩展会把
 * 值**只写进 DOM**，React 的 `onChange` 不触发，于是表单里一直是空串——用户明明看见框里有一串
 * 密钥、点「保存」却被判成"没有改动"，**一个请求都不发**（CDP 直接写 `input.value` 后点保存：
 * 0 个请求、模态框不关；同一次真键入则照常发 PATCH）。密钥是只写不读回的字段，**"我以为我填了"
 * 与"其实没填"看起来一模一样**，所以这里按用户的眼睛对齐：框里有什么就存什么。
 *
 * `autoComplete="new-password"` 是第一道防线（让浏览器别往这个框里塞东西），这里是第二道。
 */
function visibleProviderKey(domValue: string | undefined, formValue: string | undefined): string {
  return domValue ?? formValue ?? ''
}

/** 聚合生效结论的展示映射（**只**用 `effective` / `effective_at`，不碰 `sync[]`）。 */
const EFFECT_LABEL: Record<string, string> = {
  applied: '已生效',
  pending: '待生效',
  failed: '同步失败',
}

const EFFECT_AT_LABEL: Record<string, string> = {
  now: '现在',
  next_session: '下次会话',
  next_launch: '下次启动应用',
}

const effectColor = (effective: string): string => {
  if (effective === 'applied') return tokens.success
  if (effective === 'failed') return tokens.error
  return tokens.warning
}

export function ModelSettingsPanel() {
  const showToast = useMate((s) => s.showToast)

  const [providers, setProviders] = useState<Provider[]>([])
  const [view, setView] = useState<ModelsResp | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [probe, setProbe] = useState<{ id: string; data: ProviderProbeResp | null; err: string | null } | null>(null)
  const [providerModal, setProviderModal] = useState<{ mode: 'create' } | { mode: 'edit'; row: Provider } | null>(null)
  const [modelModal, setModelModal] = useState<{ mode: 'create' } | { mode: 'edit'; row: ModelRow } | null>(null)
  const [busy, setBusy] = useState(false)
  const [providerForm] = Form.useForm<ProviderFormValues>()
  const [modelForm] = Form.useForm<ModelFormValues>()
  // 密钥框的原生 input（`submitProvider` 以"框里看见的那一份"为准，理由见 visibleProviderKey）。
  const providerKeyRef = useRef<InputRef>(null)

  const loadProviders = useCallback(async () => {
    try {
      setProviders(await listProviders())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }, [showToast])

  const loadModels = useCallback(async (refresh = false) => {
    if (refresh) setRefreshing(true)
    try {
      setView(await listModels())
    } catch (err) {
      showToast(toErrorMessage(err))
    } finally {
      if (refresh) setRefreshing(false)
    }
  }, [showToast])

  const reloadAll = useCallback(async () => {
    await Promise.all([loadProviders(), loadModels()])
  }, [loadProviders, loadModels])

  useEffect(() => {
    void reloadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

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

  // ——— ① 供应商 ———

  const openProviderModal = (target: { mode: 'create' } | { mode: 'edit'; row: Provider }): void => {
    setProviderModal(target)
  }

  // 表单初值用 **`initialValues`**，不是在 openProviderModal 里命令式地 `setFieldsValue`。
  //
  // 为什么（2026-10-10 实测，踩了两轮）：`Modal` 带 `destroyOnHidden`，关着时里面的 `Form`
  // 根本没挂载。无论是"打开弹窗的那一刻"直接写值，还是退一步放进 `useEffect`，都**早于**
  // rc-dialog 真正把内容挂上去（实测：加了 useEffect 仍然落在一个未连接的 form 实例上，
  // 控制台留一句 `Instance created by useForm is not connected to any Form element`）。
  // 后果是**静默的**：新建供应商时那条默认 `protocol` 不生效，点保存只报「协议必填」——
  // 用户以为是自己没填。因为内容每次关闭都销毁、每次打开都重新挂载，`initialValues`
  // 每开一次都会重新生效，这正是它该待的地方。
  const providerInitialValues: ProviderFormValues = providerModal?.mode === 'edit'
    ? {
        id: providerModal.row.id,
        name: providerModal.row.name ?? '',
        protocol: providerModal.row.protocol,
        base_url: providerModal.row.base_url ?? '',
        api_key: '',
        api_key_clear: false,
        note: providerModal.row.note ?? '',
      }
    : ({ protocol: PROTOCOLS[0].value } as ProviderFormValues)

  const submitProvider = async (): Promise<void> => {
    // 校验失败时 antd 会 reject：**接住它**。不接的话控制台会留下一条
    // `Uncaught (in promise)`——行内错误提示照样会画出来，所以用户看不出差别，
    // 但"页面无未捕获异常"这条验收判据会被它判红，而且真出问题时它会把真相淹掉。
    const v = await providerForm.validateFields().catch(() => undefined)
    if (!v) return
    // 密钥取"框里真正显示的那一份"（浏览器/扩展只写 DOM 时表单值是空的，见函数注释）。
    const apiKey = visibleProviderKey(providerKeyRef.current?.input?.value, v.api_key)
    if (providerModal?.mode === 'create') {
      if (!VENDOR_ID_RE.test(v.id ?? '')) {
        showToast('id 只能含字母数字与 . _ -（1-64 位）')
        return
      }
      const created = await guard(createProvider({
        id: v.id ?? '',
        name: v.name || undefined,
        protocol: v.protocol ?? PROTOCOLS[0].value,
        base_url: v.base_url || undefined,
        api_key: apiKey || undefined,
        note: v.note || undefined,
      }))
      if (!created) return
      showToast('供应商已保存：密钥只写不读回；生效时刻见「模型」块顶部的状态')
    } else if (providerModal?.mode === 'edit') {
      const row = providerModal.row
      const patch: ProviderPatch = {}
      if ((v.name ?? '') !== (row.name ?? '')) patch.name = v.name ?? ''
      if ((v.protocol ?? '') !== row.protocol) patch.protocol = v.protocol ?? ''
      if ((v.base_url ?? '') !== (row.base_url ?? '')) patch.base_url = v.base_url ?? ''
      if ((v.note ?? '') !== (row.note ?? '')) patch.note = v.note ?? ''
      if (apiKey) patch.api_key = apiKey
      else if (v.api_key_clear) patch.api_key = ''
      if (Object.keys(patch).length === 0) {
        // 说清"为什么算没改动"：密钥框是空的（已存密钥不读回），所以留空 = 不改。
        showToast('没有改动：密钥框为空（已存密钥不会读回；要更换就在框里填新密钥）')
        return
      }
      const updated = await guard(patchProvider(row.id, patch))
      if (!updated) return
      const eff = updated.effect
      showToast(eff && eff.effect === 'none'
        ? '已保存：这些字段不驱动行为，本轮与后续轮次都不变'
        : `已保存：${eff?.note ?? '引用它的会话会在下一个轮次边界拿到新凭据'}`)
    }
    setProviderModal(null)
    await reloadAll()
  }

  const removeProvider = async (row: Provider): Promise<void> => {
    const resp = await guard(deleteProvider(row.id))
    if (!resp) return
    if (!resp.deleted) {
      showToast('这条供应商已经不存在')
    } else if (resp.vault_ciphertext_left) {
      showToast(`供应商已删除，但密文残留：${resp.vault_ciphertext_left}`)
    } else {
      showToast('供应商已删除')
    }
    await reloadAll()
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

  const modelOptions = useMemo(
    () => (view?.models ?? []).map((m) => ({ value: m.id, label: m.name || m.model_id })),
    [view],
  )

  const vendorOptions = useMemo(
    () => providers.map((p) => ({ value: p.id, label: p.name ? `${p.name}（${p.id}）` : p.id })),
    [providers],
  )

  const openModelModal = (target: { mode: 'create' } | { mode: 'edit'; row: ModelRow }): void => {
    setModelModal(target)
  }

  // 同 provider 那份：用 `initialValues`（理由见 providerInitialValues 上面的注释）。
  const modelInitialValues: ModelFormValues = modelModal?.mode === 'edit'
    ? {
        vendor: modelModal.row.provider_id,
        model_id: modelModal.row.model_id,
        name: modelModal.row.name ?? '',
        note: modelModal.row.note ?? '',
        image_input: hasImageInput(modelModal.row),
      }
    : ({ vendor: vendorOptions[0]?.value } as ModelFormValues)

  const submitModel = async (): Promise<void> => {
    // 同 submitProvider：接住校验失败，别留 Uncaught (in promise)。
    const v = await modelForm.validateFields().catch(() => undefined)
    if (!v) return
    if (modelModal?.mode === 'create') {
      const created = await guard(createModel({
        // 对象键是 wire 字段名（不是给用户看的字）。
        provider_id: v.vendor ?? '',
        model_id: v.model_id ?? '',
        name: v.name || undefined,
        note: v.note || undefined,
        input_modalities: modalitiesOf(v.image_input),
      }))
      if (!created) return
      // **文案纪律**：清单写面在下一进程生效，绝不说"已生效"。
      showToast('模型已保存，已提交同步：生效时刻见「模型」块顶部的状态')
    } else if (modelModal?.mode === 'edit') {
      const row = modelModal.row
      const patch: { name?: string; note?: string; input_modalities?: string[] } = {}
      if ((v.name ?? '') !== (row.name ?? '')) patch.name = v.name ?? ''
      if ((v.note ?? '') !== (row.note ?? '')) patch.note = v.note ?? ''
      if (!!v.image_input !== hasImageInput(row)) patch.input_modalities = modalitiesOf(v.image_input)
      if (Object.keys(patch).length === 0) {
        showToast('没有改动')
        return
      }
      const updated = await guard(patchModel(row.id, patch))
      if (!updated) return
      showToast('模型已保存，已提交同步：生效时刻见「模型」块顶部的状态')
    }
    setModelModal(null)
    await loadModels()
  }

  /** 一键切换某条模型的图片输入声明（列表里那个「输入」标签）。 */
  const setImageInput = async (row: ModelRow, on: boolean): Promise<void> => {
    const updated = await guard(patchModel(row.id, { input_modalities: modalitiesOf(on) }))
    if (!updated) return
    showToast(on ? '已允许图片输入，已提交同步' : '已改回仅文本，已提交同步')
    await loadModels()
  }

  const removeModel = async (row: ModelRow): Promise<void> => {
    const resp = await guard(deleteModel(row.id))
    if (!resp) return
    if (!resp.deleted) {
      showToast('这条模型已经不存在')
    } else if (resp.default_cleared) {
      showToast('模型已删除；它正好是默认模型，默认已一并清空')
    } else {
      showToast('模型已删除')
    }
    await loadModels()
  }

  /**
   * 设/取消**唯一**默认模型。两个方向都走同一条 PATCH：
   *  - 选一条 → `is_default: true`（服务端会先清掉旧的那条）；
   *  - 清空 → 对当前默认那条 `is_default: false`（"取消默认"必须真的能取消）。
   */
  const setDefaultModel = async (id: string | undefined): Promise<void> => {
    const current = view?.default_model ?? ''
    if (id === current) return
    if (!id) {
      if (!current) return
      const updated = await guard(patchModel(current, { is_default: false }))
      if (!updated) return
      showToast('已取消默认模型：新会话按会话自己的默认起')
    } else {
      const target = (view?.models ?? []).find((m) => m.id === id)
      if (!target) return
      const updated = await guard(patchModel(id, { is_default: true }))
      if (!updated) return
      showToast(`已把「${target.name || target.model_id}」设为默认模型`)
    }
    await loadModels()
  }

  const resync = async (): Promise<void> => {
    const resp = await guard(syncModels())
    if (!resp) return
    showToast('已提交同步：生效时刻见「模型」块顶部的状态')
    await loadModels(true)
  }

  // ——— 渲染 ———

  const providerColumns: ColumnsType<Provider> = [
    {
      title: 'id', dataIndex: 'id', key: 'id', width: 150,
      render: (v: string) => <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{v}</span>,
    },
    { title: '展示名', dataIndex: 'name', key: 'name', width: 140, render: (v?: string) => v || <span style={{ color: tokens.textPlaceholder }}>—</span> },
    { title: '协议', dataIndex: 'protocol', key: 'protocol', width: 90 },
    {
      title: '端点', key: 'endpoint', width: 220,
      render: (_: unknown, row: Provider) => (row.base_url
        ? <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{row.base_url}</span>
        : <Tag color="orange">缺端点</Tag>),
    },
    {
      title: '密钥', dataIndex: 'has_key', key: 'has_key', width: 80,
      render: (v: boolean) => (v ? <Tag color="green">已存</Tag> : <Tag color="orange">未设置</Tag>),
    },
    { title: '模型数', dataIndex: 'model_count', key: 'model_count', width: 76 },
    { title: '备注', dataIndex: 'note', key: 'note', width: 120, render: (v?: string) => v || <span style={{ color: tokens.textPlaceholder }}>—</span> },
    {
      title: '本地体检', key: 'problems', width: 150,
      render: (_: unknown, row: Provider) => (row.problems?.length
        ? <Tooltip title={row.problems.join('；')}><Tag color="orange">{row.problems.length} 项待处理</Tag></Tooltip>
        : <span style={{ fontSize: 12, color: tokens.textPlaceholder }}>正常</span>),
    },
    {
      title: '操作', key: 'actions', width: 210,
      render: (_: unknown, row: Provider) => (
        <Space size={2} wrap>
          <Button type="link" size="small" onClick={() => openProviderModal({ mode: 'edit', row })}>编辑</Button>
          <Button type="link" size="small" icon={<ExperimentOutlined />} onClick={() => void runProbe(row, false)}>
            自检
          </Button>
          <Popconfirm
            title="删除这条供应商？"
            description="被 Agent 或模型引用时会被拒绝——那是为了避免静默换掉凭据来源。"
            onConfirm={() => void removeProvider(row)}
          >
            <Button type="link" size="small" danger>删除</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const modelColumns: ColumnsType<ModelRow> = [
    {
      title: '模型', key: 'model_id',
      render: (_: unknown, row: ModelRow) => (
        <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
          <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{row.model_id}</span>
          {row.id === (view?.default_model ?? '') && <Tag color="blue">默认</Tag>}
        </span>
      ),
    },
    { title: '显示名', dataIndex: 'name', key: 'name', width: 150, render: (v?: string) => v || <span style={{ color: tokens.textPlaceholder }}>—</span> },
    {
      title: '供应商', key: 'vendor', width: 150,
      render: (_: unknown, row: ModelRow) => (
        <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{row.provider_id}</span>
      ),
    },
    { title: '备注', dataIndex: 'note', key: 'note', width: 140, render: (v?: string) => v || <span style={{ color: tokens.textPlaceholder }}>—</span> },
    {
      title: '输入', key: 'modalities', width: 150,
      // 默认 `text` = **图片不会被接受**（这是决定，不是缺陷）：所以这里必须看得见、改得动，
      // 否则用户会在一个本来能收图的模型上莫名失败（而且失败形态取决于运行环境，更难排查）。
      render: (_: unknown, row: ModelRow) => {
        if (hasImageInput(row)) {
          return (
            <Tooltip title="已声明图片输入：可在会话里把工作区图片交给模型">
              <Tag color="blue" style={{ cursor: 'pointer' }} onClick={() => void setImageInput(row, false)}>文本+图片</Tag>
            </Tooltip>
          )
        }
        return (
          <Tooltip title="默认只收文本：图片不会被接受。要图片请点这里勾选">
            <Tag style={{ cursor: 'pointer' }} onClick={() => void setImageInput(row, true)}>文本</Tag>
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
            title="删除这条模型？"
            description="既有会话不受影响；但它从清单里消失后，新会话不会再自动用它。"
            onConfirm={() => void removeModel(row)}
          >
            <Button type="link" size="small" danger>删除</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const effective = view?.effective ?? ''
  const effectiveAt = view?.effective_at ?? ''

  return (
    <div className="fg-model-panel">
      {/* ——— ① 供应商 ——— */}
      <section className="fg-card" aria-label="供应商">
        <CardHeader
          title="供应商"
          hint="登记一个 OpenAI 兼容端点与它的密钥；模型挂在供应商下面"
          extra={(
            <Space size={6}>
              <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadProviders()}>刷新</Button>
              <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => openProviderModal({ mode: 'create' })}>
                新建供应商
              </Button>
            </Space>
          )}
        />
        <div className="fg-card-body">
          {providers.length === 0 && (
            <Alert
              type="info"
              showIcon
              message="还没有供应商"
              description="模型条目必须挂在一个供应商下：先在这里新建一条（填端点与密钥），再到下面「模型」块里登记模型。"
            />
          )}
          {providers.length > 0 && (
            <Table<Provider>
              size="small"
              rowKey="id"
              pagination={false}
              dataSource={providers}
              columns={providerColumns}
            />
          )}

          {/* 自检结果就地展开（每条带可执行的 remedy）。 */}
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
          hint="清单、默认模型与生效结论"
          extra={(
            <Space size={6}>
              {/* 唯一的默认模型选择器（A14：结构断言按这个 aria-label 数，恰好一个）。 */}
              <Select
                size="small"
                allowClear
                aria-label="默认模型"
                placeholder="默认模型"
                value={view?.default_model || undefined}
                style={{ width: 190 }}
                loading={busy}
                options={modelOptions}
                onChange={(v?: string) => void setDefaultModel(v)}
              />
              <Button size="small" icon={<ReloadOutlined />} loading={refreshing} onClick={() => void loadModels(true)}>
                刷新
              </Button>
              <Button size="small" icon={<SyncOutlined />} disabled={busy} onClick={() => void resync()}>
                重新同步
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
          {/* 生效结论：只读聚合字段。逐后端的明细属机器面，这里一律不渲染。 */}
          {view && (
            <div className="fg-cfgbar">
              <span className="fg-cfgbar-dot" aria-hidden style={{ background: effectColor(effective) }} />
              <span>同步状态：{EFFECT_LABEL[effective] ?? '待生效'}</span>
              {effective === 'pending' && EFFECT_AT_LABEL[effectiveAt] && (
                <span className="fg-cfgbar-dim">{EFFECT_AT_LABEL[effectiveAt]}</span>
              )}
            </div>
          )}
          {/* 失败时的出路：聚合结论只知道"有失败的项"，具体逐项留在诊断面——所以这里给可执行动作。 */}
          {effective === 'failed' && (
            <Alert
              type="error"
              showIcon
              message="有同步失败的项"
              description="先到上面的供应商块点「自检」核对端点与密钥（它会逐条给出待处理项），修好后点「重新同步」。"
            />
          )}
          {view?.note && <Alert type="info" showIcon message={view.note} style={{ marginBottom: 8 }} />}

          {view && (
            <>
              <Table<ModelRow>
                size="small"
                rowKey="id"
                pagination={false}
                loading={busy}
                dataSource={view.models ?? []}
                locale={{ emptyText: '还没有登记任何模型。先建一条供应商，再点右上「新建模型」。' }}
                columns={modelColumns}
              />

              <div className="fg-diffnote">
                <b>生效说明</b>：「保存成功」只表示**已写入平台清单**；清单要等新的会话进程启动才被读到，
                所以顶部的同步状态才是"到底生效了没有"的答案。默认模型在新建会话时下发。
              </div>
            </>
          )}
        </div>
      </section>

      {/* ——— 供应商表单 ——— */}
      <Modal
        open={providerModal !== null}
        title={providerModal?.mode === 'create' ? '新建供应商' : '编辑供应商'}
        onCancel={() => setProviderModal(null)}
        onOk={() => void submitProvider()}
        confirmLoading={busy}
        okText="保存"
        destroyOnHidden
      >
        <Form form={providerForm} layout="vertical" preserve={false} initialValues={providerInitialValues}>
          <Form.Item label="id" name="id" rules={[{ required: true, message: 'id 必填' }]}>
            <Input disabled={providerModal?.mode === 'edit'} placeholder="例如 my-gateway（仅字母数字与 . _ -）" />
          </Form.Item>
          <Form.Item label="展示名" name="name">
            <Input placeholder="可选，例如「内网网关」" />
          </Form.Item>
          <Form.Item label="协议" name="protocol" rules={[{ required: true, message: '协议必填' }]}>
            <Select options={PROTOCOLS} />
          </Form.Item>
          <Form.Item label="端点" name="base_url" extra="OpenAI 兼容端点；留空 = 缺端点，界面会提示，且清单不会下发">
            <Input placeholder="https://llm.internal/v1" autoComplete="off" />
          </Form.Item>
          <Form.Item
            label={providerModal?.mode === 'edit' ? '密钥（留空 = 不改）' : '密钥'}
            name="api_key"
            extra="密钥只写不读回；是否需要等下一轮由平台如实回带"
          >
            {/* `autoComplete="new-password"`：页面里只有一个 password 框时，Chrome 会把**记住的
                密码**填进来（`off` 它不认）——填进来的值不经过 React，用户以为"框里已经有密钥了"，
                而表单里是空的（点保存被判成"没有改动"）。详见 visibleProviderKey。 */}
            <Input.Password ref={providerKeyRef} placeholder="sk-…" autoComplete="new-password" />
          </Form.Item>
          {providerModal?.mode === 'edit' && (
            <Form.Item name="api_key_clear" valuePropName="checked">
              <Checkbox>清除已存密钥</Checkbox>
            </Form.Item>
          )}
          <Form.Item label="备注" name="note">
            <Input placeholder="例如「主账号」" />
          </Form.Item>
        </Form>
      </Modal>

      {/* ——— 模型表单 ——— */}
      <Modal
        open={modelModal !== null}
        title={modelModal?.mode === 'create' ? '新建模型' : '编辑模型'}
        onCancel={() => setModelModal(null)}
        onOk={() => void submitModel()}
        confirmLoading={busy}
        okText="保存"
        destroyOnHidden
      >
        <Form form={modelForm} layout="vertical" preserve={false} initialValues={modelInitialValues}>
          <Form.Item label="供应商" name="vendor" rules={[{ required: true, message: '必须挂在一个供应商下' }]}>
            <Select disabled={modelModal?.mode === 'edit'} placeholder="选择一个供应商" options={vendorOptions} />
          </Form.Item>
          <Form.Item label="上游模型 id" name="model_id" rules={[{ required: true, message: '模型 id 必填' }]}>
            <Input disabled={modelModal?.mode === 'edit'} placeholder="发给端点的模型 id，例如 gpt-4o-mini" />
          </Form.Item>
          <Form.Item label="展示名" name="name" extra="只影响平台呈现；留空则显示上游模型 id">
            <Input placeholder="可选" />
          </Form.Item>
          <Form.Item label="备注" name="note">
            <Input placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
