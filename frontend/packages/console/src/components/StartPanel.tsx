import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from 'antd'
import { ArrowUpOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { advertisedModelValues, modelValues, undeclaredModelValues, isPlatformModelID } from '@ark-mate/types'
import { useMate } from '../store'
import { ModelSelect } from './ModelSelect'
import { WorkspacePicker, type WorkspaceChoice } from './WorkspacePicker'
import { mostRecentlyUsedWorkspaceId } from '../console/workspaceView'
import { CapabilityBar } from './CapabilityBar'
import { PermissionChip } from './PermissionChip'
import { AttachmentChip } from './AttachmentChip'
import { useAttachmentDraft } from '../console/useAttachmentDraft'
import { FOLLOW_DEFAULT, selectionPayload, type CapabilityChoice } from '../console/capabilityView'
import { mergeCapabilityPatch } from '../console/capabilityEdit'
import { shouldSendKey } from '../console/sendKey'

// 新建会话时记住的「上次所选模型」——存的是**平台模型行 id**（`mdl_…`；v26 起）。
// 键名前缀随 M0-1 的前端产品包改名统一为 `mate.web.*`（与其它前端持久键同前缀）；
// 旧键名不回填——代价是既有本地选择在改名后一次性回到默认模型。
//
// v26 顺带改了一次**值**的形态（旧值是不透明串），于是这个键有了两层自愈（见 `readStoredModel`
// 与下面的 effect）：**形状不对的无条件清掉**（不等目录），形状对但已不在候选里的等目录到了再清。
// 为什么要分两层：2026-10-15 的真实事故是"等目录"等成了一个死锁——目录为空的那些天里，
// 旧值一直留着，而每次发送都被平台以 40401 拒（下拉里又没有别的候选可换）。
const LAST_MODEL_KEY = 'mate.web.lastModel'

// 本地记住的模型 → 平台行 id 或 `null`。
//
// 形状不对（v26 之前的运行时 value，如 `["yygu","deepseek-flash"]`）**当场丢弃**：平台只接受
// 自己声明过的模型，这种值发出去必被拒，而它在界面上与合法值长得一模一样。
const readStoredModel = (): string | null => {
  const v = localStorage.getItem(LAST_MODEL_KEY)?.trim()
  if (!v) return null
  if (!isPlatformModelID(v)) {
    localStorage.removeItem(LAST_MODEL_KEY)
    return null
  }
  return v
}

// 示例指令（参照 AionUi /guid「试试这些指令」：`pages/guid/GuidPage.tsx`）
const EXAMPLES = [
  '了解一下这个项目，用大白话告诉我它是做什么的、从哪里入手',
  '帮我找出这个文件夹里重复和没用的文件，列出来让我确认',
  '帮我做一个精美的个人主页网页，做好后直接打开给我看',
]

/**
 * 新建会话面板（hero + 内嵌输入卡 + 示例指令）。
 * 未选中会话时的问答区就是这个面板——不再有独立的「开始问答」空态页，
 * 也不再单独占一条路由（`/sessions` 无 id 即新建）。
 *
 * v21：形制对齐 DSH 的 composerStack——**工作空间 chip 贴在输入卡上沿左上角**（不再是底栏里的
 * 一颗药丸 + 一个「在项目中工作」标签），卡片内部只剩输入与底栏。同一版删掉了这里原来那行底座
 * 信息：底座由构建期固定、用户没有可选项，对话框页面就不必常驻报它（底座事实在会话详情与会话头
 * 仍可见）。工作空间这一面**只切换既有登记项**（`allowCreate={false}`）——建目录要抬宿主目录面板，
 * 那是左栏「＋」与设置的事。
 */
export function StartPanel() {
  const navigate = useNavigate()
  const createAndRun = useMate((s) => s.createAndRun)
  const modelCatalog = useMate((s) => s.modelCatalog)
  const loadModelCatalog = useMate((s) => s.loadModelCatalog)

  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  // 初值只认**形状合法**的本地记忆（`readStoredModel`：形状不对的当场丢弃，不等目录）。
  const [model, setModel] = useState<string | null>(readStoredModel)
  // 工作空间选择（v14）：`null` = 未指定（会话落在**默认工作空间**）；否则是选中的登记 id
  // （v14.3 起这里只剩 id：选择面删掉「输入路径」后，前端不再产生裸绝对路径这种值）。
  //
  // v14.1 改口径：新建会话**默认预选「最近使用的工作空间」**（用户明确要求，取代 v14 的
  // "每次都从未指定开始"）。理由不冲突——工作空间确实是会话的沙箱边界，所以这里只做**预选**：
  // 选择器在面板上如实显示这个目录，用户看得见、发送前随时能改；不预选反而会出现
  // "我上一轮就在 ark-mate 里干活，这一轮却又落回默认目录"这种需要用户自己重新翻一遍的默认值。
  // 判定与优先级只有一份：`console/workspaceView.ts`；拿不到登记表就退回"未指定"。
  const [workspace, setWorkspace] = useState<WorkspaceChoice>({ id: null })
  // 用户显式选过之后（含左栏「＋」预选），默认值不再回写——否则一次列表刷新
  // 就会把用户刚选好的目录顶掉，而那看起来像"我的选择没生效"。
  const [workspacePicked, setWorkspacePicked] = useState(false)
  const pickWorkspace = (next: WorkspaceChoice) => {
    setWorkspacePicked(true)
    setWorkspace(next)
  }
  const workspaces = useMate((s) => s.workspaces)
  const preferredWorkspaceId = useMate((s) => s.preferredWorkspaceId)
  const setPreferredWorkspace = useMate((s) => s.setPreferredWorkspace)

  // 左栏分组行的「＋」把工作空间预选进来（设计 §5.1 两个面的唯一桥）。
  //
  // 消费后**立即清空**：它是一次性意图——不清的话，用户手动改成别的目录、再打开一次新建面板，
  // 会被旧值顶回去，而这看起来像"我的选择没生效"。
  useEffect(() => {
    if (!preferredWorkspaceId) return
    setWorkspacePicked(true)
    setWorkspace({ id: preferredWorkspaceId })
    setPreferredWorkspace(null)
  }, [preferredWorkspaceId, setPreferredWorkspace])

  // 扩展 · 技能页「通过对话创建」把提示词放进 `pendingPrompt`，这里消费并清空（D2）。
  // 与工作空间预选同一条纪律：它是一次性意图，读了不清会让下一次打开新建面板时
  // 被上一轮的提示词顶掉用户刚输入的内容。
  const pendingPrompt = useMate((s) => s.pendingPrompt)
  const consumePendingPrompt = useMate((s) => s.consumePendingPrompt)
  useEffect(() => {
    if (!pendingPrompt) return
    setDraft(pendingPrompt)
    consumePendingPrompt()
  }, [pendingPrompt, consumePendingPrompt])

  // 默认值：登记表到达后（首帧它可能还在路上）预选最近使用的那一条。
  useEffect(() => {
    if (workspacePicked) return
    const id = mostRecentlyUsedWorkspaceId(workspaces)
    if (!id) return
    setWorkspace({ id })
  }, [workspaces, workspacePicked])

  // 能力选定（M3）：缺省 = 两个 kind 都不限制（请求体里不加 capabilities 键，逐字节等同旧行为）。
  //
  // 与工作空间相反，这个选择**保留上一次的**：它不是沙箱边界，而更像"这次任务的工具箱"——
  // 连着做同一类任务时每次重选一遍是纯负担；且它不影响新任务能读到/改到哪些文件，
  // 复用不会造成"静默继承上一个任务的边界"那类问题。不想要了就点回「跟随默认」。
  //
  // **这里是本地草稿**：新建会话还没有会话记录可 PATCH，所以 `+`/chip 改的是这份草稿，
  // 随 `POST /sessions` 的**首轮**一起提交（首轮就用这套能力）。界面与"会话内改能力"同一套
  // 组件（`CapabilityBar`），落库时机不同而已——"新建会话就是首轮"，没有理由长成另一个样子。
  const [capabilities, setCapabilities] = useState<CapabilityChoice>(FOLLOW_DEFAULT)

  // v19：建会话不再需要客户端知道任何底座事实（原来要在缺省时挑出"当前 Agent"并传 agent_id，
  // 现在后端按构建期固定的那个底座自己填）。因此这里也不再拿"agents 列表是否为空"当发送门禁——
  // 那条门禁当初只是为了让"必然失败的提交"点不下去，而现在提交不依赖任何取不到的前置事实。

  // 图片门禁（v32）：当前路由能否收图 = `GET /models/catalog` 的连接期事实 `image_input`
  // （ADR-14 的唯一判据；`undefined` = 取不到 → 不拦，交给服务端 `model_no_image_input` 兜底）。
  //
  // 注意这里**只读连接期事实**、不读静态能力基线：与 `Composer` 里那条门禁同一条纪律。
  // 新建页拿不到"会话模型"（会话还没建），所以用路由级事实——它是"这条路由的收图能力"的上界，
  // 恰好是首轮发送前能知道的那一层；逐条模型的 `input_modalities` 由服务端在受理时再判。
  const imageSupported = modelCatalog?.image_input
  const { pendingAtts, uploading, dragOver, setDragOver, fileRef, addFiles, removeAtt, clear } =
    useAttachmentDraft(imageSupported)

  // v32：有附件（哪怕正文为空）也可以发——正文与附件是不同的输入，二者其一非空即可。
  const empty = draft.trim().length === 0 && pendingAtts.length === 0
  const canSend = !empty && !sending && !uploading

  // 候选的**两个面**（v27，别合并）：
  //	- `advertised`：运行时广告的全部候选 —— 只服务两条判断（目录到没到、广告了几个）；
  //	- `modelOptions`：**能选**的那些（= 广告 ∩ 平台清单）。喂给选择器的只有它——用户要的是
  //	  "设置里配过的模型"，广告里对不上平台行的候选（`model_id` 为空）既选不了也不是他要找的。
  const advertised = useMemo(() => advertisedModelValues(modelCatalog ?? undefined), [modelCatalog])
  const modelOptions = useMemo(() => modelValues(modelCatalog ?? undefined), [modelCatalog])
  // 广告了、但平台没声明过的那部分。**收窄不许是静默的**（那正是 v26 当初不肯过滤的理由）：
  // 它在这里换成一句人话，见下面的 `narrowedNote`。
  const undeclared = useMemo(() => undeclaredModelValues(modelCatalog ?? undefined), [modelCatalog])
  const catalogNote = modelCatalog?.note ?? ''
  const authHint = (modelCatalog?.auth_guidance ?? [])
    .map((g) => (g.terminal && g.command?.length ? `${g.name}：${g.command.join(' ')}` : g.name))
    .join(' ｜ ')
  // 下拉为空**至少有两种成因**，文案必须分开（同后端 `catalogEmptyNote` 的理由）：
  //	① 运行时此刻一个候选都没广告 → `catalogNote` 会说清是哪一种；
  //	② 广告了，但没有一条在平台清单里（`undeclared` 非空）——2026-10-16 截图里那种，
  //	   而"广告了 N 个"这件事只有这里看得见（收窄之后选择器里一个都不剩）。
  const narrowedNote =
    modelOptions.length === 0 && undeclared.length > 0
      ? `当前模型目录里的 ${undeclared.length} 个候选都不是「设置 · 模型」里配置过的模型：` +
        '先到那里添加供应商与模型。'
      : ''

  // 拉取模型目录（v19 起这条读面无参：取样对象就是构建期固定的那个底座，
  // 后端按运行时会话/缓存/resume 取样）。
  useEffect(() => {
    void loadModelCatalog()
  }, [loadModelCatalog])

  // 本地记住的模型的自愈分两层，**形状那一层不许等目录**：
  //
  //	① 形状不对（v26 之前的运行时 value，或别的底座的词）：无条件清掉。等目录到齐再清是个
  //	   死锁——目录为空时那句 `return` 会让旧值永远留着，于是每次发送都被 40401 拒，而下拉里
  //	   一个候选都没有、用户无从替换（2026-10-15 的真实事故）；
  //	② 形状对但已不在运行时候选里（换了底座 / 模型行被删）：等目录到了再判——目录为空只代表
  //	   "还不知道"，不代表"没有"。比的是候选的 **`model_id` 注解**（本地存的是平台模型行 id），
  //	   不是候选的 `value`（那是底座的词，两者比不出结果，症状是"每次打开都把记住的模型清掉"）。
  //
  // 闸门取**广告面**（`advertised`）而不是收窄后的候选（v27）：两个问题不同——"目录到没到"看广告，
  // "这条能不能选"看 `model_id` 注解。拿收窄后的列表当闸门，会把"平台清单为空、底座却广告了一堆"
  // 这一态变成"保持旧值"，而旧值正是发出去必被 40401 拒的那种——那正是 v26.1 修掉的死锁形状。
  useEffect(() => {
    if (!model) return
    if (!isPlatformModelID(model)) {
      setModel(null)
      localStorage.removeItem(LAST_MODEL_KEY)
      return
    }
    if (advertised.length === 0) return
    if (!advertised.some((v) => v.model_id === model)) {
      setModel(null)
      localStorage.removeItem(LAST_MODEL_KEY)
    }
  }, [model, advertised])

  // 选中的登记项**不在这里校验**目录是否存在：它的目录状态由 `GET /workspaces` 的 `dir_status`
  // 下发，选择器已经据此禁用了不可用的那一行（拿 id 去 `browse` 是问错了接口）。
  // v14.3 删掉了这里原先对"裸路径"的校验——选择面收敛后前端不再产生裸路径（`WorkspaceChoice`
  // 只剩 id），留着那段等于维护一条走不到的路径。

  const send = async () => {
    if (!canSend) return
    setSending(true)
    try {
      // 出口再判一次**形状**（纵深防御：状态只该由上面两条路写进来，但那两条路都不该是
      // "不合法值能发出去"的唯一保证）。形状不对 → 一个字都不带，回到「跟随默认」——
      // 平台只接受自己声明过的模型，带一个形状不对的值只会换来一次 40401。
      const picked = isPlatformModelID(model) ? model : null
      const atts = pendingAtts
      const id = await createAndRun(
        draft.trim(),
        picked ? { modelID: picked } : undefined,
        workspace.id,
        // UI 三态（null = 未限制）→ wire 形态（键缺失 = 未限制）：两个 kind 都没限制时返回
        // undefined，请求体里连 capabilities 键都不出现。转换只此一处，别的层不再判。
        selectionPayload(capabilities),
        // 首轮附件（v32）：附件 id 随 `POST /sessions` 一起提交，由后端解析成 image 块/handle。
        atts.length > 0 ? atts : undefined,
      )
      if (picked) localStorage.setItem(LAST_MODEL_KEY, picked)
      // 附件已随首轮受理：清掉待发 chip（发送失败则抛出到 finally 前，pending 原样保留）。
      clear()
      if (id) navigate(`/sessions/${id}`)
    } finally {
      setSending(false)
    }
  }

  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', background: tokens.bg }}>
      <div
        style={{
          maxWidth: 780,
          width: '100%',
          margin: '0 auto',
          padding: '11vh 28px 0',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          gap: 26,
          boxSizing: 'border-box',
        }}
      >
        {/* hero */}
        <h1 style={{ margin: 0, fontSize: 27, fontWeight: 680, letterSpacing: '-0.02em', color: tokens.text }}>
          Hi，今天有什么安排？
        </h1>

        {/* 输入区：工作空间 chip 贴在卡片**上沿左上角**，卡片本体只装输入与底栏。
            v21 删掉了这里原来那一行底座信息（RuntimeBadge + "新建会话使用构建期固定的底座
            （不可切换）"）：底座由构建期固定、用户无事可做，对话框页面就不必常驻报它——
            底座事实仍在会话详情与会话头可见。 */}
        <div style={{ width: '100%', display: 'flex', flexDirection: 'column' }}>
          {/* 内嵌输入卡（原型 §12–§13）：卡内只有 `+` … 模型 … 发送；工作区 / 权限 chip 移到卡下方一行。 */}
          <div
            className="fg-composer"
            style={{
              width: '100%',
              border: `1px solid ${tokens.border}`,
              borderRadius: tokens.radiusComposer,
              background: tokens.surface,
              padding: '14px 16px 10px',
              display: 'flex',
              flexDirection: 'column',
              gap: 10,
              boxSizing: 'border-box',
              // 拖拽落区（v32）：拖文件入卡片时高亮边框（与会话页 Composer 同一行为，走 token）。
              outline: dragOver ? `2px dashed ${tokens.primary}` : 'none',
              outlineOffset: -4,
            }}
            onDragOver={(e) => {
              if (e.dataTransfer?.types?.includes('Files')) {
                e.preventDefault()
                setDragOver(true)
              }
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => {
              if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
                e.preventDefault()
                setDragOver(false)
                void addFiles(e.dataTransfer.files)
              }
            }}
          >
            {/* 待发附件 chips（v32）：上传成功即出现，可逐个移除。首轮也随之带上。 */}
            {pendingAtts.length > 0 && (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {pendingAtts.map((a) => (
                  <AttachmentChip key={a.id} attachment={a} onRemove={() => removeAtt(a.id)} />
                ))}
                {uploading && <span style={{ fontSize: 12, color: tokens.textSecondary }}>上传中…</span>}
              </div>
            )}
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (shouldSendKey(e)) {
                  e.preventDefault()
                  void send()
                }
              }}
              onPaste={(e) => {
                if (!e.clipboardData || !e.clipboardData.types.includes('text/html')) return
                const plain = e.clipboardData.getData('text/plain')
                e.preventDefault()
                const ta = e.currentTarget
                const s = ta.selectionStart
                const en = ta.selectionEnd
                setDraft(draft.slice(0, s) + plain + draft.slice(en))
                requestAnimationFrame(() => {
                  const p = s + plain.length
                  ta.setSelectionRange(p, p)
                })
              }}
              rows={2}
              placeholder={'输入你的问题，或点下方示例…'}
              style={{
                width: '100%',
                boxSizing: 'border-box',
                border: 'none',
                outline: 'none',
                resize: 'none',
                background: 'transparent',
                color: tokens.text,
                fontSize: 15,
                lineHeight: 1.7,
                fontFamily: 'inherit',
                minHeight: 72,
              }}
            />

            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
              {/* 左栏必须可裁剪：窄容器下压到很窄时，nowrap 文案要省略号收起，
                  而不是溢出到右栏下面被选择器白底盖住（那会渲染成「文字重影」）。
                  v21：工作空间已上移到卡片上沿的 chip，这里只剩「这个会话用哪些能力」，
                  于是底栏回到单行——不再需要三行堆叠的左列。 */}
              <span
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 10,
                  flex: '1 1 auto',
                  minWidth: 0,
                  overflow: 'hidden',
                }}
              >
                {/* 隐藏的文件 input：`+` 菜单的「添加文件」与拖拽落区都走它。 */}
                <input
                  ref={fileRef}
                  type="file"
                  multiple
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    void addFiles(e.target.files)
                    e.target.value = '' // 允许再次选择同名文件
                  }}
                />
                {/* 「这个会话用哪些能力」= 会话级能力选定（M3）。与会话内输入区**同一个组件**。
                    `+` 菜单第一项是「添加文件」，走 onAddFile 触发上面的隐藏 input。 */}
                <CapabilityBar
                  value={capabilities}
                  onChange={(patch) => setCapabilities((cur) => mergeCapabilityPatch(cur, patch))}
                  disabled={sending || uploading}
                  saving={sending}
                  applyHint="first-round"
                  onAddFile={() => fileRef.current?.click()}
                />
                {/* v2.2：权限 chip 在工具条内、`+` 的右边（与 Composer 同一排布）。 */}
                <PermissionChip disabled={sending} />
              </span>

              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                {/* 会话级模型选择器：关闭态只显示名称；候选、描述与空目录提示都在下拉里
                    （下拉即「浏览全部」，不再单独开弹窗）。创建时经 set_config_option 应用。
                    候选只喂**平台清单里声明过的**那些（v27，见上面两个面的注释）——"运行时
                    无可用模型"这种说法在收窄之后是假话（运行时可能广告了一堆，只是都没配过）。
                    宽度由 .fg-model-pill 管（静息贴合文案、聚焦展开），这里不再固定 240px。 */}
                <ModelSelect
                  values={modelOptions}
                  value={model}
                  onChange={(v) => setModel(v)}
                  allowClear
                  size="small"
                  placeholder={modelOptions.length === 0 ? '模型 · 暂无可选' : '模型 · 跟随默认'}
                  emptyNote={
                    [narrowedNote, catalogNote, authHint].filter(Boolean).join(' ｜ ') ||
                    '当前没有可选的模型：到「设置 · 模型」里添加供应商与模型，再选一条默认模型。'
                  }
                />

                {/* 与输入区同一颗 32px 圆钮的外观（需求 6 上半）：这一屏还没有会话，
                    所以没有停止态，也不带速记提示以外的交互。 */}
                <Button
                  type="primary"
                  shape="circle"
                  icon={<ArrowUpOutlined />}
                  disabled={!canSend}
                  loading={sending}
                  onClick={() => void send()}
                  style={{ width: 32, height: 32, boxSizing: 'border-box', flex: 'none' }}
                />
              </span>
            </div>
          </div>
          {/* 卡下方一行（原型 §13）：工作区 chip + 权限 chip。
              2026-11-05 反馈：右下角那条发送键提示整条去掉（与输入区同一个理由）。 */}
          <div className="fg-composer-chips">
            <WorkspacePicker
              variant="chip"
              allowCreate={false}
              value={workspace}
              onChange={pickWorkspace}
              disabled={sending}
            />
          </div>
        </div>

        {/* 示例指令 */}
        <div style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div style={{ fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary, marginBottom: 6 }}>
            试试这些指令
          </div>
          {EXAMPLES.map((ex) => (
            <button
              key={ex}
              type="button"
              onClick={() => setDraft(ex)}
              style={{
                textAlign: 'left',
                background: 'transparent',
                border: 'none',
                padding: '6px 2px',
                cursor: 'pointer',
                color: tokens.textSecondary,
                fontSize: 13.5,
                lineHeight: 1.6,
                fontFamily: 'inherit',
              }}
            >
              {ex}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
