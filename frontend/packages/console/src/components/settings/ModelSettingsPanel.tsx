/**
 * 设置 · 模型（第 4 节）：**两个平级的管理对象** —— ① 供应商 ② 模型。
 *
 * 交互形态（2026-10-11 改造，语汇参照 deepseek-harness 的「自定义模型 API」卡片，
 * 见 `.dsh/docs/specs/2026-10-11-model-settings-ui-interaction-design.md`）：
 *  - **表格 → 行卡**：身份（名字 + id + 协议标签 + 状态点）在左，动作（自检 / 编辑 / 删除）在右；
 *  - **弹窗 → 就地展开的编辑卡**：字段竖排（标签在上、提示或错误在下），底部「取消 / 保存」；
 *    就地展开的前提是**上下文不消失**——用户改这条时还看得见别的供应商与模型数。
 *  - **删除 → 确认弹窗**（标题带对象名、描述说清后果），不用气泡确认；
 *  - **新建 → 列表最后一格的虚线整宽按钮**（供应商），模型则挂在每个供应商分组头上；
 *  - **保存成功 → 一行 `role="status"` 提示**（不是飘走就看不见的 toast）；「没有改动」留在卡片里。
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
 *
 * 表单字段纪律（`internal/archtest/settings_form_fields_test.go` 钉住）：`ProviderFormValues` /
 * `ModelFormValues` 的**每个键**都必须有对应的 `<Form.Item name="…">`。antd 的 `validateFields()`
 * 只返回已注册字段，没画出来的键恒为 `undefined`——"用户以为填了""用户以为没改"与"其实发的是
 * 默认值"看起来一模一样（2026-10-11 的真实事故：只改一次备注就把模型的图片能力改回仅文本）。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert, Button, Checkbox, Form, Input, Modal, Select, Space,
} from 'antd'
import type { InputRef } from 'antd'
import { ExperimentOutlined, PlusOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import {
  createModel,
  createProvider,
  deleteModel,
  deleteProvider,
  listModelCatalog,
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
  /**
   * 这条模型能接收的输入能力（多选）。**只能由用户在表单里声明**——平台不猜也探测不到：
   * 一条模型收不收图，取决于真实端点上挂着什么（自建网关后面的模型 id 未必在任何目录里）。
   */
  modalities?: string[]
}

/**
 * 输入能力取值集（**当前只有两种**：文本恒有、图片可选）。
 *
 * 为什么取值集不能再多（2026-10-11 读随包载荷确认）：模型清单里的 `input` 在运行环境那侧
 * 只认 `["text","image"]`，多写一种就是「写了、存了、什么都不发生」。等某个环境实测接受更多
 * 取值时，这里加一项、后端合法取值集加一项即可——表单形状（多选）不用动。
 *
 * 「文本」置灰常勾：它是**恒有**项而不是可关闭的开关（后端归一化同样钉住「恒含文本」）。
 */
const INPUT_CAPABILITIES = [
  { label: '文本', value: 'text', disabled: true },
  { label: '图片', value: 'image' },
]

/** 图片开关 → 落库/上行的数组形态（规范序，恒含 `text`）。 */
function modalitiesWithImage(on: boolean): string[] {
  return on ? ['text', 'image'] : ['text']
}

/** 表单勾选 → 同样的数组形态。 */
function modalitiesOf(caps: string[] | undefined): string[] {
  return modalitiesWithImage(!!caps?.includes('image'))
}

/** 反解：一条模型当前声明的输入能力（表单初值，也是「到底改没改」的比较基准）。 */
function capabilitiesOf(row: ModelRow): string[] {
  return modalitiesWithImage((row.input_modalities ?? []).includes('image'))
}

/** 一条模型当前是否声明了图片输入（行卡上那个可点的能力标签）。 */
function hasImageInput(row: ModelRow): boolean {
  return (row.input_modalities ?? []).includes('image')
}

/** 两份能力声明是否等价（逐项比较，避免顺序差异被当成"改过了"）。 */
function sameCapabilities(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i])
}

/** 供应商展示名（空则回落 id）。 */
function providerLabel(row: Provider): string {
  return row.name || row.id
}

/** 模型展示名（空则回落上游模型 id）。 */
function modelLabel(row: ModelRow): string {
  return row.name || row.model_id
}

type ProviderCardTarget = { mode: 'create' } | { mode: 'edit'; row: Provider }
type ModelCardTarget = { mode: 'create'; vendor?: string } | { mode: 'edit'; row: ModelRow }
// 待删除的对象：两个互斥的槽位（不用联合类型的判别字段——那个判别值会是一个带代码词的字符串
// 字面量，而 A13 守卫按字面量扫描这一节的全部字符串，判别字段会被它当成"用户可见的字"。
// 事实上它也确实不该出现在用户面：删除的目标由上面那张卡片决定，不由一个 tag 决定）。

/**
 * 供应商卡片的表单值（**单一事实源**：`initialValues` 与"打开时写入"都用它）。
 *
 * 编辑时 `api_key` 恒为空串——密钥只写不读回，"留空 = 不改"与"清除"必须分得开。
 */
function providerFormValues(target: ProviderCardTarget): ProviderFormValues {
  return target.mode === 'edit'
    ? {
        id: target.row.id,
        name: target.row.name ?? '',
        protocol: target.row.protocol,
        base_url: target.row.base_url ?? '',
        api_key: '',
        api_key_clear: false,
        note: target.row.note ?? '',
      }
    : { protocol: PROTOCOLS[0].value }
}

/** 模型卡片的表单值。`firstVendor` 只在新建时作为默认挂靠的供应商。 */
function modelFormValues(target: ModelCardTarget, firstVendor?: string): ModelFormValues {
  return target.mode === 'edit'
    ? {
        vendor: target.row.provider_id,
        model_id: target.row.model_id,
        name: target.row.name ?? '',
        note: target.row.note ?? '',
        modalities: capabilitiesOf(target.row),
      }
    // 新建时把每个键都**显式**给空值：`setFieldsValue` 是合并语义，省略的键会继承上一次打开
    // 残留的值（实测：新建模型时「上游模型 id」框里还留着上一条的 id）。
    : { vendor: target.vendor ?? firstVendor, model_id: '', name: '', note: '', modalities: ['text'] }
}

/**
 * 提交时以**框里真正显示的那一份**密钥为准（`domValue` 优先，表单值兜底）。
 *
 * 为什么不能只信表单值（2026-10-11 实测定位）：浏览器密码管理器与部分扩展会把
 * 值**只写进 DOM**，React 的 `onChange` 不触发，于是表单里一直是空串——用户明明看见框里有一串
 * 密钥、点「保存」却被判成"没有改动"，**一个请求都不发**（CDP 直接写 `input.value` 后点保存：
 * 0 个请求、卡片不收起；同一次真键入则照常发 PATCH）。密钥是只写不读回的字段，**"我以为我填了"
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

/** 一张就地展开的编辑卡：供应商与模型共用外壳（凹底 + 标题 + 底部动作）。 */
function EditorShell({ title, route, hint, children, actions }: {
  title: string
  /** 只读的身份标注（"我在改谁"）。 */
  route?: string
  hint?: string
  children: React.ReactNode
  actions: React.ReactNode
}) {
  return (
    <div className="fg-editor">
      <div className="fg-editor-head">
        <span className="fg-editor-title">{title}</span>
        {route ? <span className="fg-editor-route">{route}</span> : null}
      </div>
      {hint ? <p className="fg-hint">{hint}</p> : null}
      {children}
      <div className="fg-editor-actions">{actions}</div>
    </div>
  )
}

/** 供应商编辑卡的表单（新建与编辑共用一套字段；id 建好之后只读）。 */
function ProviderForm({ form, target, busy, keyRef, noopLine, onSubmit, onCancel }: {
  form: ReturnType<typeof Form.useForm<ProviderFormValues>>[0]
  target: ProviderCardTarget
  busy: boolean
  keyRef: React.RefObject<InputRef | null>
  noopLine?: string
  onSubmit: () => void
  onCancel: () => void
}) {
  const editing = target.mode === 'edit'
  return (
    <EditorShell
      title={editing ? '编辑供应商' : '新建供应商'}
      route={editing ? target.row.id : undefined}
      hint={editing
        ? undefined
        : '兼容 OpenAI 的端点、中转站、自部署服务都从这里接：填 API 地址与协议即可。'}
      actions={(
        <>
          {noopLine ? <p className="fg-noopline">{noopLine}</p> : null}
          <Button onClick={onCancel} disabled={busy}>取消</Button>
          <Button type="primary" loading={busy} onClick={onSubmit}>
            {editing ? '保存' : '创建供应商'}
          </Button>
        </>
      )}
    >
      <Form form={form} layout="vertical" preserve={false} initialValues={providerFormValues(target)}>
        <div className="fg-editor-grid">
          <Form.Item
            label="供应商 id"
            name="id"
            rules={[
              { required: true, message: '供应商 id 不能为空' },
              { pattern: VENDOR_ID_RE, message: '只能含字母、数字与 . _ -（1–64 位）' },
            ]}
            extra={editing ? '建好之后不再改动：它进请求与凭据名。' : '1–64 位字母、数字与 . _ -；进请求与凭据名。'}
          >
            <Input disabled={editing} placeholder="例如 my-gateway" autoComplete="off" />
          </Form.Item>
          <Form.Item label="展示名" name="name" extra="只影响平台呈现；留空则显示供应商 id">
            <Input placeholder="可选" autoComplete="off" />
          </Form.Item>
          <Form.Item label="API 协议" name="protocol" rules={[{ required: true, message: '协议必填' }]}>
            <Select options={PROTOCOLS} />
          </Form.Item>
          <Form.Item
            label="API 地址"
            name="base_url"
            extra="OpenAI 兼容端点；留空 = 缺端点，列表里会标出来，且清单不会下发"
          >
            <Input placeholder="https://llm.internal/v1" autoComplete="off" />
          </Form.Item>
        </div>
        <Form.Item
          label={editing ? 'API 密钥（留空 = 不改）' : 'API 密钥'}
          name="api_key"
          extra={editing
            ? '密钥只写不读回：已配置时留空表示不改，填新值表示替换。'
            : '密钥只写不读回；不需要鉴权的端点可以留空。'}
        >
          {/* `autoComplete="new-password"`：页面里只有一个 password 框时，Chrome 会把**记住的
              密码**填进来（`off` 它不认）——填进来的值不经过 React，用户以为"框里已经有密钥了"，
              而表单里是空的（点保存被判成"没有改动"）。详见 visibleProviderKey。 */}
          <Input.Password ref={keyRef} placeholder="sk-…" autoComplete="new-password" />
        </Form.Item>
        {editing
          ? (
            <Form.Item name="api_key_clear" valuePropName="checked">
              <Checkbox>清除已存密钥</Checkbox>
            </Form.Item>
          )
          : null}
        <Form.Item label="备注" name="note">
          <Input placeholder="例如「主账号」" autoComplete="off" />
        </Form.Item>
      </Form>
    </EditorShell>
  )
}

/** 模型编辑卡的表单（挂靠关系与上游 id 建好之后只读——换供应商 = 删了重建）。 */
function ModelForm({ form, target, vendorOptions, busy, noopLine, onSubmit, onCancel }: {
  form: ReturnType<typeof Form.useForm<ModelFormValues>>[0]
  target: ModelCardTarget
  vendorOptions: { value: string; label: string }[]
  busy: boolean
  noopLine?: string
  onSubmit: () => void
  onCancel: () => void
}) {
  const editing = target.mode === 'edit'
  return (
    <EditorShell
      title={editing ? '编辑模型' : '新建模型'}
      route={editing ? target.row.model_id : undefined}
      actions={(
        <>
          {noopLine ? <p className="fg-noopline">{noopLine}</p> : null}
          <Button onClick={onCancel} disabled={busy}>取消</Button>
          <Button type="primary" loading={busy} onClick={onSubmit}>
            {editing ? '保存' : '创建模型'}
          </Button>
        </>
      )}
    >
      <Form form={form} layout="vertical" preserve={false} initialValues={modelFormValues(target, vendorOptions[0]?.value)}>
        <div className="fg-editor-grid">
          <Form.Item
            label="供应商"
            name="vendor"
            rules={[{ required: true, message: '必须挂在一个供应商下' }]}
            extra={editing ? '挂靠关系建好之后不再改动：要换供应商请删掉重建。' : undefined}
          >
            <Select disabled={editing} placeholder="选择一个供应商" options={vendorOptions} />
          </Form.Item>
          <Form.Item
            label="上游模型 id"
            name="model_id"
            rules={[{ required: true, message: '模型 id 不能为空' }]}
            extra={editing ? '发给端点的模型 id，建好之后不再改动。' : '发给端点的模型 id，例如 gpt-4o-mini'}
          >
            <Input disabled={editing} placeholder="gpt-4o-mini" autoComplete="off" />
          </Form.Item>
          <Form.Item label="展示名" name="name" extra="留空则显示上游模型 id">
            <Input placeholder="可选" autoComplete="off" />
          </Form.Item>
          <Form.Item label="备注" name="note">
            <Input placeholder="可选" autoComplete="off" />
          </Form.Item>
        </div>
        {/* 输入能力：**必须由用户声明**——平台既不猜也不探测（自建网关后面的模型 id
            未必在任何目录里）。不勾图片 = 声明仅文本（图片不会被接受）。 */}
        <Form.Item
          label="输入能力"
          name="modalities"
          extra="默认只收文本（图片不会被接受）；勾上「图片」= 允许把工作区里的图片交给它。真的能不能收由端点决定。"
        >
          <Checkbox.Group options={INPUT_CAPABILITIES} />
        </Form.Item>
      </Form>
    </EditorShell>
  )
}

export function ModelSettingsPanel() {
  const showToast = useMate((s) => s.showToast)

  const [providers, setProviders] = useState<Provider[]>([])
  const [view, setView] = useState<ModelsResp | null>(null)
  /**
   * 「当前这条路由到底能不能收图」= **连接期事实**（ADR-14），`null` = 取不到（无法校验）。
   *
   * 三态而不是布尔：`false` 是"连上了、它自报不支持"，`null` 是"没连上/取不到"——把后者说成
   * "不支持"正是 ADR-14 要拦的错误结论，所以这里绝不 `?? false`。
   *
   * 为什么就地取一份而不是复用 store 里的 `modelCatalog`：那个槽位会被「会话详情」按**它那条
   * 会话**的取样覆盖，而这一节说的是**这份部署的装配事实**（构建期固定的那个底座），
   * 拿别的取样做断言就是撒谎。
   */
  const [imageInput, setRouteImageInput] = useState<boolean | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [probe, setProbe] = useState<{ id: string; data: ProviderProbeResp | null; err: string | null } | null>(null)
  const [providerCard, setProviderCard] = useState<ProviderCardTarget | null>(null)
  const [modelCard, setModelCard] = useState<ModelCardTarget | null>(null)
  const [deleteProviderRow, setDeleteProviderRow] = useState<Provider | null>(null)
  const [deleteModelRow, setDeleteModelRow] = useState<ModelRow | null>(null)
  const [busy, setBusy] = useState(false)
  /** 保存成功那一行（`role="status"`）：只报"已保存"，绝不报"已生效"。 */
  const [savedLine, setSavedLine] = useState<string | undefined>(undefined)
  /** 「没有改动」：留在卡片里说清为什么算没改动，而不是弹一个飘走的提示。 */
  const [noopLine, setNoopLine] = useState<string | undefined>(undefined)
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

  // 连接期事实（ADR-14）：取不到就保持 `null`（"无法校验"），**不**降级成"不支持"。
  // 失败不弹 toast：这一节的主线是清单管理，目录读面在没起会话时不可用是正常状态。
  useEffect(() => {
    let alive = true
    void listModelCatalog()
      .then((c) => { if (alive) setRouteImageInput(c.image_input ?? null) })
      .catch(() => { if (alive) setRouteImageInput(null) })
    return () => { alive = false }
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

  /**
   * 表单初值：**打开卡片的那一刻同步写**，同时也交给 `initialValues`（两者共用同一个纯函数，
   * 不会漂移）。
   *
   * 这段注释是三次实测换来的，改之前先读完：
   *  ① 2026-10-10：只把初值交给 `initialValues` 不写值 → 新建供应商时那条默认 `protocol` 不生效
   *     （点保存只报「协议必填」）。当时的结论是"改用 `initialValues`"，但那只修了**第一次**打开。
   *  ② 2026-10-11：`useForm()` 的 store 活在 React 树之外，字段被卸掉、值又被 `preserve={false}`
   *     清掉之后，**第二次打开**的 Form 不再是"首次初始化"，`initialValues` 不再套用——表现是
   *     **编辑卡片整个是空的**（model_id / 备注都是空串，于是"改备注"什么都不发生，或者更糟：
   *     按空表单把能力声明改掉）。
   *  ③ 2026-10-11 同日再修：改成在弹窗动画结束时 `resetFields` + `setFieldsValue` —— 仍然不对，
   *     而且更隐蔽：那个时点已经是打开动画之后，用户在动画期间敲进去的字会被一起抹掉。
   * 现在这一版把卡片做成**就地展开**（没有动画窗口），并在点击处理器里同步写值：
   *   - 关闭 → 打开：卡片是**新挂载**的，`<Form>` 首次初始化会用同一份 `initialValues`；
   *     我们提前写的值与其一致，两条路都落到同一份数据上。
   *   - 切换对象（A → B）：卡片不重新挂载，`resetFields()` + `setFieldsValue()` 是**唯一**的来源。
   * `resetFields()` 在前：`setFieldsValue` 是**合并**语义，只给一部分键时其余键会保留上一次打开
   * 的旧值（实测：新建模型时「上游模型 id」框里还留着上一条的 id，再一输入就拼成两个 id）。
   */
  const fillProviderForm = (target: ProviderCardTarget): void => {
    providerForm.resetFields()
    providerForm.setFieldsValue(providerFormValues(target))
  }

  const fillModelForm = (target: ModelCardTarget): void => {
    modelForm.resetFields()
    modelForm.setFieldsValue(modelFormValues(target, providers[0]?.id))
  }

  const openProviderCard = (target: ProviderCardTarget): void => {
    setSavedLine(undefined)
    setNoopLine(undefined)
    setProviderCard(target)
    fillProviderForm(target)
  }

  const closeProviderCard = (): void => {
    setNoopLine(undefined)
    setProviderCard(null)
  }

  const openModelCard = (target: ModelCardTarget): void => {
    setSavedLine(undefined)
    setNoopLine(undefined)
    setModelCard(target)
    fillModelForm(target)
  }

  const closeModelCard = (): void => {
    setNoopLine(undefined)
    setModelCard(null)
  }

  const submitProvider = async (): Promise<void> => {
    // 校验失败时 antd 会 reject：**接住它**。不接的话控制台会留下一条
    // `Uncaught (in promise)`——行内错误提示照样会画出来，所以用户看不出差别，
    // 但"页面无未捕获异常"这条验收判据会被它判红，而且真出问题时它会把真相淹掉。
    const v = await providerForm.validateFields().catch(() => undefined)
    if (!v) return
    // 密钥取"框里真正显示的那一份"（浏览器/扩展只写 DOM 时表单值是空的，见函数注释）。
    const apiKey = visibleProviderKey(providerKeyRef.current?.input?.value, v.api_key)
    if (providerCard?.mode === 'create') {
      const created = await guard(createProvider({
        id: v.id ?? '',
        name: v.name || undefined,
        protocol: v.protocol ?? PROTOCOLS[0].value,
        base_url: v.base_url || undefined,
        api_key: apiKey || undefined,
        note: v.note || undefined,
      }))
      if (!created) return
      closeProviderCard()
      // 名称先取出来再进模板串：这一节的守卫按**字符串字面量**扫禁词，而模板串里的
      // `${函数名}` 会被整段当成用户可见的字（`providerLabel` 里就有那个代码词）。
      const savedName = providerLabel(created)
      setSavedLine(`已保存「${savedName}」。`)
      await reloadAll()
      return
    }
    if (providerCard?.mode === 'edit') {
      const row = providerCard.row
      const patch: ProviderPatch = {}
      if ((v.name ?? '') !== (row.name ?? '')) patch.name = v.name ?? ''
      if ((v.protocol ?? '') !== row.protocol) patch.protocol = v.protocol ?? ''
      if ((v.base_url ?? '') !== (row.base_url ?? '')) patch.base_url = v.base_url ?? ''
      if ((v.note ?? '') !== (row.note ?? '')) patch.note = v.note ?? ''
      if (apiKey) patch.api_key = apiKey
      else if (v.api_key_clear) patch.api_key = ''
      if (Object.keys(patch).length === 0) {
        // 说清"为什么算没改动"：密钥框是空的（已存密钥不读回），所以留空 = 不改。
        setNoopLine('没有改动：密钥框为空（已存密钥不会读回；要更换就在框里填新密钥）。')
        return
      }
      const updated = await guard(patchProvider(row.id, patch))
      if (!updated) return
      const eff = updated.effect
      closeProviderCard()
      setSavedLine(`已保存「${v.name || row.id}」。`)
      if (eff && eff.effect === 'none') {
        showToast('这些字段不驱动行为，本轮与后续轮次都不变')
      }
      await reloadAll()
    }
  }

  const removeProvider = async (row: Provider): Promise<void> => {
    const resp = await guard(deleteProvider(row.id))
    if (!resp) return
    if (!resp.deleted) {
      showToast('这条供应商已经不存在')
    } else {
      // v20：密钥是 providers 表上的 `key` 列，随行一起消失——不再有「密文残留」这个半成功态。
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

  /**
   * 模型按**供应商分组**展示（供应商的顺序即后端读面的顺序，模型保持清单顺序）。
   *
   * 分组不是为了好看：它让"哪个供应商名下一条模型都没有"直接看得见——而空着的供应商正是
   * 用户以为自己配好了、实际新会话里选不到模型的那种状态。
   *
   * 兜底：清单里挂了但供应商读面没给出来的条目（理论上不会出现，删供应商会被引用检查拦住）
   * 单独成一组，绝不因为分组而把已有条目从界面上抹掉。
   */
  const groups = useMemo(() => {
    const models = view?.models ?? []
    const byProvider = new Map<string, ModelRow[]>()
    for (const m of models) {
      const bucket = byProvider.get(m.provider_id)
      if (bucket) bucket.push(m)
      else byProvider.set(m.provider_id, [m])
    }
    const known = new Set(providers.map((p) => p.id))
    const out: { key: string; provider?: Provider; rows: ModelRow[] }[] = providers.map((p) => ({
      key: p.id,
      provider: p,
      rows: byProvider.get(p.id) ?? [],
    }))
    const orphan = models.filter((m) => !known.has(m.provider_id))
    if (orphan.length > 0) out.push({ key: '__unlinked__', rows: orphan })
    return out
  }, [providers, view])

  const submitModel = async (): Promise<void> => {
    // 同 submitProvider：接住校验失败，别留 Uncaught (in promise)。
    const v = await modelForm.validateFields().catch(() => undefined)
    if (!v) return
    if (modelCard?.mode === 'create') {
      const created = await guard(createModel({
        // 对象键是 wire 字段名（不是给用户看的字）。
        provider_id: v.vendor ?? '',
        model_id: v.model_id ?? '',
        name: v.name || undefined,
        note: v.note || undefined,
        // 用户在表单里声明的输入能力（新建时总是显式带上，默认就是仅文本）。
        input_modalities: modalitiesOf(v.modalities),
      }))
      if (!created) return
      closeModelCard()
      // **文案纪律**：清单写面在下一进程生效，绝不说"已生效"。
      const savedName = modelLabel(created)
      setSavedLine(`已保存「${savedName}」。`)
      await loadModels()
      return
    }
    if (modelCard?.mode === 'edit') {
      const row = modelCard.row
      const patch: { name?: string; note?: string; input_modalities?: string[] } = {}
      if ((v.name ?? '') !== (row.name ?? '')) patch.name = v.name ?? ''
      if ((v.note ?? '') !== (row.note ?? '')) patch.note = v.note ?? ''
      // **只在真的改过时才发**：省略 = 不改（wire 语义）。以前写成 `!!v.image_input !== hasImageInput(row)`，
      // 而未注册字段恒为 `undefined`，于是改备注 = 把图片能力静默改回仅文本。
      const nextModalities = modalitiesOf(v.modalities)
      if (!sameCapabilities(nextModalities, capabilitiesOf(row))) patch.input_modalities = nextModalities
      if (Object.keys(patch).length === 0) {
        setNoopLine('没有改动：这个表单里的字段都还是原值。')
        return
      }
      const updated = await guard(patchModel(row.id, patch))
      if (!updated) return
      closeModelCard()
      setSavedLine(`已保存「${v.name || row.model_id}」。`)
      await loadModels()
    }
  }

  /** 一键切换某条模型的图片输入声明（行卡上那个可点的能力标签）。 */
  const setImageInput = async (row: ModelRow, on: boolean): Promise<void> => {
    const updated = await guard(patchModel(row.id, { input_modalities: modalitiesWithImage(on) }))
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
      showToast(`已把「${modelLabel(target)}」设为默认模型`)
    }
    await loadModels()
  }

  const resync = async (): Promise<void> => {
    const resp = await guard(syncModels())
    if (!resp) return
    showToast('已提交同步：生效时刻见「模型」块顶部的状态')
    await loadModels(true)
  }

  const confirmDelete = async (): Promise<void> => {
    const providerRow = deleteProviderRow
    const modelRow = deleteModelRow
    setDeleteProviderRow(null)
    setDeleteModelRow(null)
    if (providerRow) await removeProvider(providerRow)
    else if (modelRow) await removeModel(modelRow)
  }

  // ——— 渲染 ———

  const effective = view?.effective ?? ''
  const effectiveAt = view?.effective_at ?? ''

  // 删除目标只可能是两者之一（两个槽位互斥）。标题里带对象名，描述说清后果。
  //
  // 注意每个要进模板串的值都先落到一个**名字干净**的局部量上：这一节的守卫把 `${…}` 里的
  // 表达式源码也算作"用户可见的字"，而它按子串扫禁词——`${someProviderRow.x}` 会被判成语义上的
  // 代码词出现在用户面。值本身（名字、条数）没问题，标识符的名字有问题。
  const deleteName = deleteProviderRow !== null
    ? providerLabel(deleteProviderRow)
    : deleteModelRow !== null ? modelLabel(deleteModelRow) : ''
  const deleteTitle = deleteName === '' ? '' : `删除「${deleteName}」？`
  const deleteModelCount = deleteProviderRow === null ? 0 : deleteProviderRow.model_count
  const deleteDescription = deleteProviderRow !== null
    ? (deleteModelCount > 0
      // 后端**不会**连带删模型：被模型引用时删除会被拒（`provider_in_use`）。这里是预先把话说清，
      // 而不是等后端拒绝之后再解释。
      ? `它名下还有 ${deleteModelCount} 条模型：先到下面「模型」块里删掉它们，这条才会被允许删除。`
      : '删除这条供应商会移除它的端点与密钥；被模型引用时会被拒绝——那是为了避免静默换掉凭据来源。')
    : deleteModelRow !== null
      ? (deleteModelRow.id === (view?.default_model ?? '')
        ? '它正好是默认模型：删除会一并清空默认。既有会话不受影响。'
        : '既有会话不受影响；它从清单里消失后，新会话不会再自动用它。')
      : ''

  return (
    <div className="fg-model-panel">
      {/* 保存成功那一行：说"已保存"，不说"已生效"（ADR-22）。 */}
      {savedLine === undefined
        ? null
        : <p className="fg-savedline" role="status" aria-live="polite">{savedLine}</p>}

      {/* ——— ① 供应商 ——— */}
      <section className="fg-card" aria-label="供应商">
        <CardHeader
          title="供应商"
          hint="登记一个 OpenAI 兼容端点与它的密钥；模型挂在供应商下面"
          extra={(
            <Space size={6}>
              <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadProviders()}>刷新</Button>
            </Space>
          )}
        />
        <div className="fg-card-body">
          {providers.length === 0 && providerCard?.mode !== 'create' && (
            <div className="fg-emptycard">
              <b>还没有供应商</b>
              模型条目必须挂在一个供应商下：先在这里新建一条（填 API 地址与密钥），再到下面「模型」块里登记模型。
            </div>
          )}

          <ul className="fg-rows">
            {providers.map((row) => {
              const target = providerCard?.mode === 'edit' && providerCard.row.id === row.id ? providerCard : null
              const open = target !== null
              const keyLabel = row.has_key ? 'API 密钥已配置' : 'API 密钥缺失'
              return (
                <li key={row.id} className="fg-rowcard">
                  <div className="fg-rowhead">
                    <span className="fg-rowname">{providerLabel(row)}</span>
                    {row.name ? <span className="fg-rowid">{row.id}</span> : null}
                    <span className="fg-rowtag">{row.protocol}</span>
                    {row.base_url
                      ? null
                      : <span className="fg-rowtag fg-warn" title="没有 API 地址：清单不会下发">缺端点</span>}
                    <span className="fg-rowmeta">{row.model_count} 个模型</span>
                    <span
                      className={`fg-keydot ${row.has_key ? 'fg-key-on' : 'fg-key-off'}`}
                      role="img"
                      aria-label={keyLabel}
                      title={keyLabel}
                    />
                    <span className="fg-rowactions">
                      <Button
                        type="link" size="small" icon={<ExperimentOutlined />}
                        onClick={() => void runProbe(row, false)}
                      >
                        自检
                      </Button>
                      <Button size="small" onClick={() => (open ? closeProviderCard() : openProviderCard({ mode: 'edit', row }))}>
                        {open ? '收起' : '编辑'}
                      </Button>
                      <Button type="text" size="small" danger onClick={() => setDeleteProviderRow(row)}>
                        删除
                      </Button>
                    </span>
                  </div>
                  {/* 开会话之前就能看见的问题清单（后端现算的派生值）。 */}
                  {row.problems?.length
                    ? <p className="fg-rowproblems">{row.problems.join('；')}</p>
                    : null}

                  {/* 自检结果：就在这条供应商下面展开（每条各自一份，互不干扰）。 */}
                  {probe?.id === row.id && (
                    <div className="fg-probe" aria-label="自检结果">
                      <div className="fg-probe-head">
                        <b>自检 · {row.id}</b>
                        {probe.data && (
                          <span style={{ color: probe.data.ok ? tokens.success : tokens.error, fontSize: 12 }}>
                            {probe.data.ok ? '本地段无失败项' : '有失败项'}
                          </span>
                        )}
                        <span style={{ flex: 1 }} />
                        {/* 出网是**显式**动作：默认只跑本地段。 */}
                        <Button size="small" type="link" onClick={() => void runProbe(row, true)}>
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

                  {open && target !== null && (
                    <ProviderForm
                      form={providerForm}
                      target={target}
                      busy={busy}
                      keyRef={providerKeyRef}
                      noopLine={noopLine}
                      onSubmit={() => void submitProvider()}
                      onCancel={closeProviderCard}
                    />
                  )}
                </li>
              )
            })}
          </ul>

          {/* 唯一的"新建"入口：列表的最后一格（虚线整宽），不是页头那一排里的小按钮。 */}
          {providerCard?.mode === 'create'
            ? (
              <ProviderForm
                form={providerForm}
                target={providerCard}
                busy={busy}
                keyRef={providerKeyRef}
                noopLine={noopLine}
                onSubmit={() => void submitProvider()}
                onCancel={closeProviderCard}
              />
            )
            : (
              <button type="button" className="fg-addbtn" onClick={() => openProviderCard({ mode: 'create' })}>
                <PlusOutlined /> 新建供应商
              </button>
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
              {/* 连接期事实（ADR-14）：这条说的是**当前环境**收不收图，与逐条模型自己声明的
                  输入能力是两件事。`null` = 取不到（还没连上）——如实说"无法校验"，
                  绝不降级成"不支持"（那正是 ADR-14 要拦的错误结论）。 */}
              <span style={{ flex: 1 }} />
              <span
                className="fg-cfgbar-dim"
                title="这条结论来自当前环境自报的连接期能力，针对的是整条链路；每条模型自己声明的输入能力见行上的标签，两者都要满足才能把图片交给模型。"
              >
                {imageInput === true
                  ? '当前环境可收图片'
                  : imageInput === false
                    ? '当前环境不接受图片'
                    : '当前环境能否收图片：无法校验'}
              </span>
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
          {view?.note && <Alert type="info" showIcon message={view.note} />}

          {providers.length === 0 && (
            <div className="fg-emptycard">
              <b>还没有可挂靠的供应商</b>
              模型必须挂在供应商下：先在上面「供应商」块里建一条。
            </div>
          )}

          {/* 按供应商分组：空组本身就是要说的事（"这条供应商名下一条模型都没有"）。 */}
          {groups.map(({ key, provider, rows }) => {
            const createTarget = modelCard?.mode === 'create' && provider !== undefined && modelCard.vendor === provider.id
              ? modelCard
              : null
            const creating = createTarget !== null
            return (
              <div className="fg-group" key={key}>
                <div className="fg-grouphead">
                  <span className="fg-groupname">
                    {provider === undefined ? '未挂靠的条目' : providerLabel(provider)}
                  </span>
                  {provider && provider.name ? <span className="fg-rowid">{provider.id}</span> : null}
                  <span className="fg-rowmeta">{rows.length} 条</span>
                  <span style={{ flex: 1 }} />
                  {provider && (
                    <button
                      type="button"
                      className="fg-linkbtn"
                      disabled={busy}
                      onClick={() => openModelCard({ mode: 'create', vendor: provider.id })}
                    >
                      <PlusOutlined /> 添加模型
                    </button>
                  )}
                </div>

                {rows.length === 0 && !creating && (
                  <p className="fg-emptyline">这条供应商还没有模型：点右上「添加模型」登记一条。</p>
                )}

                <ul className="fg-rows">
                  {rows.map((row) => {
                    // 同供应商那份：一次算好目标并收窄类型。
                    const target = modelCard?.mode === 'edit' && modelCard.row.id === row.id ? modelCard : null
                    const open = target !== null
                    const image = hasImageInput(row)
                    const capLabel = image
                      ? `已声明图片输入（${row.model_id}）：点一下改回仅文本`
                      : `默认只收文本（${row.model_id}）：点一下允许图片`
                    return (
                      <li key={row.id} className="fg-rowcard">
                        <div className="fg-rowhead">
                          <span className="fg-modelid">{row.model_id}</span>
                          <span className="fg-rowmeta">{row.name || '—'}</span>
                          {/* 一键改输入能力：默认 `text` = **图片不会被接受**（这是决定，不是缺陷），
                              所以它必须看得见、也必须一步改得动——多两次点击会显著降低用户改对它的概率。 */}
                          <button
                            type="button"
                            className={`fg-rowtag fg-clickable${image ? ' fg-info' : ''}`}
                            aria-label={capLabel}
                            title={capLabel}
                            onClick={() => void setImageInput(row, !image)}
                          >
                            {image ? '文本+图片' : '文本'}
                          </button>
                          {row.id === (view?.default_model ?? '') && <span className="fg-rowtag fg-info">默认</span>}
                          {row.note ? <span className="fg-rowmeta">{row.note}</span> : null}
                          <span className="fg-rowactions">
                            <Button size="small" onClick={() => (open ? closeModelCard() : openModelCard({ mode: 'edit', row }))}>
                              {open ? '收起' : '编辑'}
                            </Button>
                            <Button type="text" size="small" danger onClick={() => setDeleteModelRow(row)}>
                              删除
                            </Button>
                          </span>
                        </div>
                        {open && target !== null && (
                          <ModelForm
                            form={modelForm}
                            target={target}
                            vendorOptions={vendorOptions}
                            busy={busy}
                            noopLine={noopLine}
                            onSubmit={() => void submitModel()}
                            onCancel={closeModelCard}
                          />
                        )}
                      </li>
                    )
                  })}
                </ul>

                {creating && createTarget !== null && (
                  <ModelForm
                    form={modelForm}
                    target={createTarget}
                    vendorOptions={vendorOptions}
                    busy={busy}
                    noopLine={noopLine}
                    onSubmit={() => void submitModel()}
                    onCancel={closeModelCard}
                  />
                )}
              </div>
            )
          })}

          {/* 生效说明（固定一行）：把"保存成功 ≠ 已生效"说清，免得用户以为写完了就生效了。 */}
          <div className="fg-diffnote">
            <b>生效说明</b>：「保存成功」只表示已写入平台清单；清单要等新的会话进程启动才被读到，
            所以顶部的同步状态才是"到底生效了没有"的答案。默认模型在新建会话时下发。
          </div>
        </div>
      </section>

      {/* ——— 删除确认：标题带对象名，描述说清后果 ———
          删除进行中不许点遮罩关掉（`maskClosable` 在 antd 6 已废弃，控制台会警告），
          用新的 `mask.closable` 形状。 */}
      <Modal
        open={deleteProviderRow !== null || deleteModelRow !== null}
        className="fg-confirm"
        title={deleteTitle}
        onCancel={() => { setDeleteProviderRow(null); setDeleteModelRow(null) }}
        onOk={() => void confirmDelete()}
        okText="删除"
        cancelText="取消"
        okButtonProps={{ danger: true, loading: busy }}
        cancelButtonProps={{ disabled: busy }}
        mask={{ closable: !busy }}
      >
        <p style={{ margin: 0, color: tokens.textSecondary }}>{deleteDescription}</p>
      </Modal>
    </div>
  )
}
