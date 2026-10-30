import { useCallback, useEffect, useState } from 'react'
import {
  createExpert,
  deleteExpert,
  getExpert,
  listExperts,
  listMcpServers,
  listModels,
  listSkills,
  setExpertEnabled,
  toErrorMessage,
  updateExpert,
} from '@ark-mate/api'
import type { Expert, ExpertWriteBody } from '@ark-mate/types'
import { useMate } from '../../store'
import { writeErrorText } from '../../console/capabilityView'
import type { ExpertRefOption } from '../../console/expertView'

/** 弹窗里引用选择器的候选（技能 / MCP / 模型）。清单取不到时留空并允许留空保存。 */
export interface ExpertOptions {
  skills: ExpertRefOption[]
  mcps: ExpertRefOption[]
  models: ExpertRefOption[]
}

const EMPTY_OPTIONS: ExpertOptions = { skills: [], mcps: [], models: [] }

/**
 * 专家数据的唯一加载 / 变更入口（设计「模块划分」：面板只画，写面语义交给纯函数与这里）。
 *
 * - 列表：`listExperts()`（含禁用，**不含 instructions 正文**）；
 * - 正文：打开弹窗时 `getExpert(id)` 才拉 instructions（列表刻意不带正文，避免首屏拉 N 份长文本）；
 * - 写面：create/update/delete/toggle 全部复用现有 `/experts` 六接口，**零后端改动**；
 * - 保存后重新 `listExperts` 刷新列表，不用本地乐观状态覆盖服务端。
 */
export function useExpertData() {
  const showToast = useMate((s) => s.showToast)
  const [experts, setExperts] = useState<Expert[]>([])
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState<string | null>(null)
  const [options, setOptions] = useState<ExpertOptions>(EMPTY_OPTIONS)

  // 弹窗状态：created=true 表示新建；否则按 targetId 拉详情正文。
  const [open, setOpen] = useState(false)
  const [created, setCreated] = useState(false)
  const [targetId, setTargetId] = useState<string | null>(null)
  const [detail, setDetail] = useState<Expert | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [toggleBusy, setToggleBusy] = useState<string | null>(null)
  const [delBusy, setDelBusy] = useState<string | null>(null)

  const loadExperts = useCallback(async (): Promise<void> => {
    setLoading(true)
    try {
      setExperts(await listExperts())
      setListError(null)
    } catch (err) {
      setListError(toErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  // 引用选择器候选：任一取不到就留空（专家仍可只做模型/展示），不阻断页面。
  const loadOptions = useCallback(async (): Promise<void> => {
    const [skillsR, mcpsR, modelsR] = await Promise.allSettled([listSkills(), listMcpServers(), listModels()])
    setOptions({
      skills: skillsR.status === 'fulfilled' ? skillsR.value.map((s) => ({ id: s.id, name: s.name || s.id })) : [],
      mcps: mcpsR.status === 'fulfilled' ? mcpsR.value.map((m) => ({ id: m.id, name: m.name || m.id })) : [],
      models:
        modelsR.status === 'fulfilled' ? modelsR.value.models.map((m) => ({ id: m.id, name: m.name || m.model })) : [],
    })
  }, [])

  useEffect(() => {
    void loadExperts()
    void loadOptions()
  }, [loadExperts, loadOptions])

  const refresh = (): void => {
    void loadExperts()
    void loadOptions()
  }

  const fetchDetail = useCallback(
    async (id: string): Promise<void> => {
      setDetailLoading(true)
      setDetailError(null)
      setDetail(null)
      try {
        setDetail(await getExpert(id))
      } catch (err) {
        setDetailError(writeErrorText(err))
      } finally {
        setDetailLoading(false)
      }
    },
    [],
  )

  const openNew = (): void => {
    setCreated(true)
    setTargetId(null)
    setDetail(null)
    setDetailError(null)
    setFormError(null)
    setDetailLoading(false)
    setOpen(true)
  }

  const openEdit = (e: Expert): void => {
    setCreated(false)
    setTargetId(e.id)
    setDetailError(null)
    setFormError(null)
    setOpen(true)
    void fetchDetail(e.id)
  }

  const retryDetail = (): void => {
    if (targetId) void fetchDetail(targetId)
  }

  const closeDialog = (): void => {
    setOpen(false)
    setBusy(false)
  }

  /** 保存：created 决定 create / update（编辑 body 里不带 id，路径已带）。 */
  const save = async (body: ExpertWriteBody): Promise<void> => {
    setBusy(true)
    setFormError(null)
    try {
      if (created) {
        const res = await createExpert(body)
        showToast(`已创建专家「${res.id}」`)
      } else {
        // 一次「打开又保存」不该写盘：变化键为空直接关掉。
        if (Object.keys(body).length === 0) {
          showToast('没有改动')
          setOpen(false)
          return
        }
        const id = detail?.id ?? targetId ?? ''
        const res = await updateExpert(id, body)
        showToast(`已保存专家「${res.id}」`)
      }
      setOpen(false)
      await loadExperts()
    } catch (err) {
      // 错误留在弹窗里：用户刚填的内容还在，改一处就能重试。
      setFormError(writeErrorText(err))
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (id: string, enabled: boolean): Promise<void> => {
    setToggleBusy(id)
    try {
      await setExpertEnabled(id, enabled)
      showToast(`${enabled ? '已启用' : '已停用'}专家「${id}」${enabled ? '' : '（仅影响新选择，不影响历史会话）'}`)
      await loadExperts()
      // 同步弹窗里的 detail.enabled（不重新拉正文，避免把正在编辑的草稿冲掉）。
      setDetail((cur) => (cur && cur.id === id ? { ...cur, enabled } : cur))
    } catch (err) {
      showToast(`切换「${id}」失败：${writeErrorText(err)}`)
    } finally {
      setToggleBusy(null)
    }
  }

  const remove = async (id: string): Promise<void> => {
    setDelBusy(id)
    try {
      const res = await deleteExpert(id)
      showToast(res.deleted ? `已删除专家「${id}」` : `专家「${id}」本就不存在（已同步列表）`)
      if (open && !created && targetId === id) setOpen(false)
      await loadExperts()
    } catch (err) {
      showToast(`删除 ${id} 失败：${writeErrorText(err)}`)
    } finally {
      setDelBusy(null)
    }
  }

  return {
    experts,
    loading,
    listError,
    options,
    dialog: { open, created, detail, detailLoading, detailError },
    busy,
    formError,
    toggleBusy,
    delBusy,
    refresh,
    openNew,
    openEdit,
    closeDialog,
    retryDetail,
    save,
    toggle,
    remove,
  }
}

export type ExpertData = ReturnType<typeof useExpertData>
