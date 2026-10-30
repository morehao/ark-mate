import type { Expert, ExpertWriteBody } from '@ark-mate/types'

// ——— 专家管理页的纯函数层 ———
//
// 只做「读模型 → 表单值 / 表单值 → 写面请求体 / 校验」的纯变换，**不发请求、不碰 React**：
// 写面语义（编辑只提交变化键、`null` 删键、未知键保留）与阈值（字节/危险标记/上限）全部在这里成文，
// 组件只负责把结果画出来。这样 `node --test` 就能逐条钉住那些「看起来工作、实际会静默改语义」的分支。
//
// 阈值与后端 `ValidateExpert`（backend/internal/core/capability/expert.go:79-127）逐项对齐；
// 前端校验只是省一次往返，**服务端才是唯一闸门**（超长/危险标记一律 40001 definition_invalid）。

/** 角色设定字节上限（UTF-8 字节，对齐后端 `MaxExpertInstructionsBytes`）。 */
export const EXPERT_INSTRUCTIONS_MAX_BYTES = 4096
/** 名称字符上限（对齐后端 `MaxExpertNameRunes`）。 */
export const EXPERT_MAX_NAME = 40
/** 描述字符上限（对齐后端 `MaxExpertDescriptionRunes`）。 */
export const EXPERT_MAX_DESCRIPTION = 120
/** 标签个数上限（对齐后端 `MaxExpertTags`）。 */
export const EXPERT_MAX_TAGS = 8
/** 单个标签字符上限（对齐后端 `MaxExpertTagRunes`）。 */
export const EXPERT_MAX_TAG_CHARS = 20
/** 技能/MCP 每类引用上限（对齐后端 `MaxExpertRefs`）。 */
export const EXPERT_MAX_REFS = 50
/** id 词法（对齐后端 `skillIDRE`）：小写字母/数字，连字符分段，≤64。 */
export const EXPERT_ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
/** 内置头像键（对齐后端 `ValidExpertAvatars`）。 */
export const EXPERT_AVATARS = ['spark', 'chart', 'code', 'pen', 'search', 'briefcase'] as const
export type ExpertAvatar = (typeof EXPERT_AVATARS)[number]

/**
 * 角色设定里的危险标记（对齐后端 `expertDangerousMarkers`）。
 *
 * 这些标记会被首轮当作**隐藏上下文注入**，含逃逸风险，因此后端一律拒。前端就地列出命中项并禁用保存，
 * 但真正落库前的拒绝仍以服务端为准。
 */
export const EXPERT_DANGEROUS_MARKERS = ['</system-reminder>', '<!--', '-->', '{{', '}}', '\x00'] as const

/** 引用选择器的一个候选项（技能 / MCP / 模型通用）。 */
export interface ExpertRefOption {
  id: string
  name: string
}

/** 专家表单值（编辑态只提交与它比较后有变化的键）。 */
export interface ExpertFormValues {
  id: string
  name: string
  description: string
  avatar: string
  model: string
  skills: string[]
  mcps: string[]
  tags: string[]
  instructions: string
  enabled: boolean
}

/** 新建专家的空白表单。 */
export function blankExpertForm(): ExpertFormValues {
  return {
    id: '',
    name: '',
    description: '',
    avatar: 'spark',
    model: '',
    skills: [],
    mcps: [],
    tags: [],
    instructions: '',
    enabled: true,
  }
}

/** 专家读模型（**详情**，带 instructions 正文）→ 表单值。列表读模型没有正文，只能用于卡片。 */
export function expertFormValues(e: Expert): ExpertFormValues {
  return {
    id: e.id,
    name: e.name ?? '',
    description: e.description ?? '',
    avatar: e.avatar ?? 'spark',
    model: e.model ?? '',
    skills: [...(e.skills ?? [])],
    mcps: [...(e.mcps ?? [])],
    tags: [...(e.tags ?? [])],
    instructions: e.instructions ?? '',
    enabled: e.enabled ?? true,
  }
}

// 集合语义（顺序不影响落库）：比较时排序。技能/MCP/tags 都由后端规范化，顺序不是语义。
function sameStringSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sa = [...a].sort()
  const sb = [...b].sort()
  return sa.every((x, i) => x === sb[i])
}

/** 新建：提交完整已知字段 body（id 必填）。 */
export function expertCreateBody(v: ExpertFormValues): ExpertWriteBody {
  return {
    id: v.id.trim(),
    name: v.name,
    description: v.description,
    avatar: v.avatar,
    model: v.model,
    skills: [...v.skills],
    mcps: [...v.mcps],
    tags: [...v.tags],
    enabled: v.enabled,
    instructions: v.instructions,
  }
}

/**
 * 编辑：**只提交发生变化的键**。
 *
 * `id` 永远不提交（路径已带）；未变的键不出现 → 后端保留原值（含用户在高级 JSON 里加的未知键）。
 * 清空 description 是显式的 `""`（清空），不是「没改」。
 */
export function expertFormPatch(initial: ExpertFormValues, next: ExpertFormValues): ExpertWriteBody {
  const patch: ExpertWriteBody = {}
  if (next.name !== initial.name) patch.name = next.name
  if (next.description !== initial.description) patch.description = next.description
  if (next.avatar !== initial.avatar) patch.avatar = next.avatar
  if (next.model !== initial.model) patch.model = next.model
  if (!sameStringSet(next.skills, initial.skills)) patch.skills = [...next.skills]
  if (!sameStringSet(next.mcps, initial.mcps)) patch.mcps = [...next.mcps]
  if (!sameStringSet(next.tags, initial.tags)) patch.tags = [...next.tags]
  if (next.instructions !== initial.instructions) patch.instructions = next.instructions
  if (next.enabled !== initial.enabled) patch.enabled = next.enabled
  return patch
}

/** 角色设定的 UTF-8 字节数（与后端 `len(e.Instructions)` 口径一致）。 */
export function instructionsByteLength(text: string | undefined | null): number {
  return new TextEncoder().encode(text ?? '').length
}

/** 角色设定命中的危险标记（按清单顺序；无命中 → 空数组）。 */
export function instructionsMarkers(text: string | undefined | null): string[] {
  const s = text ?? ''
  return EXPERT_DANGEROUS_MARKERS.filter((m) => s.includes(m))
}

/**
 * 只读注入预览：`hasPersona` 为 false 时是「仅能力预设」。
 *
 * 后端按纯文本注入、不渲染 HTML，所以预览也是纯文本，不做 Markdown 富渲染。
 */
export function instructionPreview(text: string | undefined | null): { hasPersona: boolean; text: string } {
  const s = text ?? ''
  return { hasPersona: s.trim() !== '', text: s }
}

/**
 * 引用缺失：技能/MCP 里指向不存在定义的 id（保存仍会成功，选择该专家时这些项被跳过并告警）。
 *
 * `knownSkills` / `knownMcps` 是当前已装清单的 id 集合；清单为空时**不做「全部缺失」判定**——
 * 清单取不到与「真没有这个定义」是两件事，前者不该把整个专家的引用都标黄。
 */
export function missingExpertRefs(
  e: Pick<Expert, 'skills' | 'mcps'>,
  knownSkills: Set<string>,
  knownMcps: Set<string>,
): { skills: string[]; mcps: string[] } {
  const skills = knownSkills.size === 0 ? [] : (e.skills ?? []).filter((id) => !knownSkills.has(id))
  const mcps = knownMcps.size === 0 ? [] : (e.mcps ?? []).filter((id) => !knownMcps.has(id))
  return { skills, mcps }
}

/** 卡片兜底展示用的模型短名（`mdl_` 前缀去掉并截断；无模型的调用方自己处理空）。 */
export function expertModelShort(model: string | undefined): string {
  if (!model) return ''
  return model.replace(/^mdl_/, '')
}

// ——— 校验（前端预校验；服务端 ValidateExpert 是权威） ———

function validateKnown(input: {
  created: boolean
  id: string
  name: string
  description: string
  model: string
  skills?: string[]
  mcps?: string[]
  tags?: string[]
  instructions?: string
}): string | null {
  if (input.created) {
    const id = input.id.trim()
    if (!id) return '标识必填'
    if (!EXPERT_ID_RE.test(id) || id.length > 64) {
      return '标识只能用英文小写字母/数字，用连字符分段（例如 data-analyst），≤64 位'
    }
  }
  const name = input.name.trim()
  if (!name) return '名称必填'
  if ([...name].length > EXPERT_MAX_NAME) return `名称 ≤${EXPERT_MAX_NAME} 字符`
  if ([...input.description].length > EXPERT_MAX_DESCRIPTION) return `描述 ≤${EXPERT_MAX_DESCRIPTION} 字符`
  if (input.model && !input.model.startsWith('mdl_')) return '推荐模型不在可选范围内'
  if (input.skills && input.skills.length > EXPERT_MAX_REFS) return `推荐技能最多 ${EXPERT_MAX_REFS} 个`
  if (input.mcps && input.mcps.length > EXPERT_MAX_REFS) return `推荐连接器最多 ${EXPERT_MAX_REFS} 个`
  const tags = input.tags ?? []
  if (tags.length > EXPERT_MAX_TAGS) return `标签最多 ${EXPERT_MAX_TAGS} 个`
  if (tags.some((t) => [...t].length > EXPERT_MAX_TAG_CHARS)) return `每个标签 ≤${EXPERT_MAX_TAG_CHARS} 字符`
  const bytes = instructionsByteLength(input.instructions)
  if (bytes > EXPERT_INSTRUCTIONS_MAX_BYTES) {
    return `角色设定超过 ${EXPERT_INSTRUCTIONS_MAX_BYTES} 字节（当前 ${bytes}），保存会被拒绝`
  }
  const marks = instructionsMarkers(input.instructions)
  if (marks.length > 0) return `角色设定含不支持的写法：${marks.join('、')}，保存会被拒绝`
  return null
}

/** 表单值校验（新建/编辑通用）。 */
export function validateExpertForm(v: ExpertFormValues, created: boolean): string | null {
  return validateKnown({ ...v, created })
}

/** 高级（JSON）原文 body 校验：只校验**出现过的**已知键（没写的键由后端保留原值）。 */
export function validateExpertBody(body: ExpertWriteBody, created: boolean): string | null {
  if (created) {
    const id = typeof body.id === 'string' ? body.id.trim() : ''
    if (!id) return 'id 必填（它同时是定义文件名）'
    if (!EXPERT_ID_RE.test(id) || id.length > 64) {
      return 'id 须小写字母/数字、用连字符分段（形如 data-analyst），≤64 位'
    }
  }
  // 编辑态「未出现的键 = 保留原值」，因此只有**出现过**的键才校验；新建态 name 必填。
  if ('name' in body || created) {
    const name = typeof body.name === 'string' ? body.name.trim() : ''
    if (!name) return '名称必填'
    if ([...name].length > EXPERT_MAX_NAME) return `名称 ≤${EXPERT_MAX_NAME} 字符`
  }
  if ('description' in body && typeof body.description === 'string') {
    if ([...body.description].length > EXPERT_MAX_DESCRIPTION) return `描述 ≤${EXPERT_MAX_DESCRIPTION} 字符`
  }
  if ('model' in body && typeof body.model === 'string' && body.model && !body.model.startsWith('mdl_')) {
    return '推荐模型须为平台模型行 id（mdl_…）'
  }
  if ('skills' in body && Array.isArray(body.skills) && body.skills.length > EXPERT_MAX_REFS) {
    return `推荐技能最多 ${EXPERT_MAX_REFS} 个`
  }
  if ('mcps' in body && Array.isArray(body.mcps) && body.mcps.length > EXPERT_MAX_REFS) {
    return `推荐 MCP 最多 ${EXPERT_MAX_REFS} 个`
  }
  if ('tags' in body && Array.isArray(body.tags)) {
    const tags = body.tags as string[]
    if (tags.length > EXPERT_MAX_TAGS) return `标签最多 ${EXPERT_MAX_TAGS} 个`
    if (tags.some((t) => [...t].length > EXPERT_MAX_TAG_CHARS)) return `每个标签 ≤${EXPERT_MAX_TAG_CHARS} 字符`
  }
  if ('instructions' in body && typeof body.instructions === 'string') {
    const bytes = instructionsByteLength(body.instructions)
    if (bytes > EXPERT_INSTRUCTIONS_MAX_BYTES) {
      return `角色设定超过 ${EXPERT_INSTRUCTIONS_MAX_BYTES} 字节（当前 ${bytes}），保存会被拒绝`
    }
    const marks = instructionsMarkers(body.instructions)
    if (marks.length > 0) return `角色设定含危险标记：${marks.join('、')}，保存会被拒绝`
  }
  return null
}

/** 解析高级（JSON）textarea 为专家 body（对象校验）；失败给可读错误。 */
export function parseExpertJson(text: string): { body: ExpertWriteBody | null; error: string | null } {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (err) {
    return { body: null, error: `不是合法 JSON：${err instanceof Error ? err.message : String(err)}` }
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { body: null, error: '定义必须是 JSON 对象（形如 {"id": "…"}）' }
  }
  return { body: { ...(parsed as ExpertWriteBody) }, error: null }
}
