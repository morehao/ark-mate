import { useEffect, useRef, useState } from 'react'
import { Checkbox, Popover, Spin, Tooltip } from 'antd'
import { ApiOutlined, PlusOutlined, ThunderboltOutlined, UserOutlined } from '@ant-design/icons'
import { listExperts, listLibrary, listMcpServers } from '@ark-mate/api'
import type { Expert, LibraryItem, McpServer, SessionCapabilitiesPatch } from '@ark-mate/types'
import { useMate } from '../store'
import { Label } from './dirPath'
import { mcpOptionView, mcpRows, skillOptionView, type CapabilityChoice } from '../console/capabilityView'
import { capabilityChips, expertSwitchPatch, selectionPatchFor } from '../console/capabilityEdit'

type PanelKind = 'expert' | 'skills' | 'mcps'
/** 面板此刻从哪儿弹出：`add` = `+` 的位置（还没有 chip）；`chip` = 对应 chip 的位置。 */
type Panel = { kind: PanelKind; from: 'add' | 'chip' }

interface Props {
  /** 当前选定（**唯一事实**：会话内来自读面快照，创建页来自本地草稿）。 */
  value: CapabilityChoice
  /**
   * 某一格被改动 → 只带那一格的**三态补丁**（键缺失 = 不改、`null` = 回未声明、`[]` = 一个都不要）。
   *
   * 谁来落库由调用方决定：会话内是 `PATCH /sessions/:id/capabilities`（下一轮生效），
   * 创建页是本地草稿（随 `POST /sessions` 的**首轮**一起提交）。
   */
  onChange: (patch: SessionCapabilitiesPatch) => void
  /** 入口不可用（会话底座对不上 / 正在创建）。 */
  disabled?: boolean
  /** 一次补丁在途：期间禁用勾选（补丁是相对当前值合并的，连点会互相覆盖）。 */
  saving?: boolean
  /**
   * 生效口径的一句话（只影响说明文案，不影响行为）：
   * `next-round` = 会话内（下一条消息生效）；`first-round` = 创建页（随本次会话首轮生效）。
   */
  applyHint?: 'next-round' | 'first-round'
}

/** 一条可选行（技能/MCP 合流后的渲染形态）。 */
interface OptionRow {
  id: string
  name: string
  selectable: boolean
  note: string
  /** 属于「缺省路径本来就会投」的集合——切到「自定义」时用它预填。 */
  inDefault: boolean
}

const MENU: { kind: PanelKind; label: string; hint: string; icon: React.ReactNode }[] = [
  { kind: 'expert', label: '专家', hint: '换一个角色：连同它的技能/连接器预设一起换', icon: <UserOutlined /> },
  { kind: 'skills', label: '技能', hint: '只让这个会话用其中几个技能', icon: <ThunderboltOutlined /> },
  { kind: 'mcps', label: '连接器', hint: '这个会话使用哪些连接器', icon: <ApiOutlined /> },
]

const TITLE: Record<PanelKind, string> = { expert: '专家', skills: '技能', mcps: '连接器' }

/**
 * 能力入口的**统一形态**：`+` 菜单 + 专家/技能/连接器三个选择器 + 选中后才出现的 chip。
 *
 * ## 为什么做成一个可复用组件
 * 「这条会话用哪些能力」在**首轮之前**（新建会话）与**首轮之后**（会话内）是同一件事，界面语言
 * 就该是同一套。历史上前者是「能力药丸 + 一个大面板」、后者是 `+`/chip，两套并存没有产品理由
 * （只是两块面板各自长出来的）。这里把**表现**收成一份，落库方式交给调用方：
 *
 *   - `ComposerCapabilities`（会话内）：`PATCH /sessions/:id/capabilities`，**下一轮**边界生效；
 *   - 新建会话面板：只改本地草稿，随 `POST /sessions` 的**首轮**一起提交（首轮就用这套能力）。
 *
 * ## 三条界面纪律
 *  1. **默认什么都不显示**：只有被声明过的那一格才出现 chip（未声明 = 跟随默认）。默认态与
 *     "跟随默认"本来就该长得一样。
 *  2. **不出现 pending 横幅/代际号/diff**：选择即写入，生效口径只用一句话说清（`applyHint`）。
 *  3. **异常由调用方呈现**：本组件不认识网络——失败提示与"回滚到服务端快照"都在调用方。
 *
 * ## 三态不糊成两态
 * 技能/MCP 各有「跟随默认 / 自定义」两档：跟随默认 → 补丁里那格是 `null`（不限制）；自定义 →
 * 非空数组是白名单、`[]` 是"一个都不要"。这两档在服务端是**两个代际**，界面上就不能画成一样。
 */
export function CapabilityBar({ value, onChange, disabled, saving, applyHint = 'next-round' }: Props) {
  const showToast = useMate((s) => s.showToast)
  const [menuOpen, setMenuOpen] = useState(false)
  const [panel, setPanel] = useState<Panel | null>(null)
  const [library, setLibrary] = useState<LibraryItem[] | null>(null)
  const [servers, setServers] = useState<McpServer[]>([])
  const [experts, setExperts] = useState<Expert[] | null>(null)
  const [loading, setLoading] = useState(false)

  const choice = value

  // 专家 chip 要显示名字：只有钉住了具体专家时才需要这份清单（否则一次请求都不发）。
  const needExpertName = choice.expert !== null && choice.expert !== ''
  useEffect(() => {
    if (!needExpertName) return
    let alive = true
    listExperts()
      .then((list) => {
        if (alive) setExperts(list)
      })
      .catch(() => {
        // 取不到名字就用 id 兜底：chip 的**语义**（钉了哪个专家）不依赖这份清单。
      })
    return () => {
      alive = false
    }
  }, [needExpertName])

  // 面板每次打开都取最新：能力清单是"现在有什么"，缓存一份旧快照只会让"不可选"的原因过期。
  // `/library` 给逐 runtime 可达性（勾选判据的唯一来源），`/mcps` 给定义级开关与传输方式，
  // `/experts` 给专家列表——少取一个就会把"服务端一定会拒"的选项画成可勾选。
  const panelOpen = panel !== null
  useEffect(() => {
    if (!panelOpen) return
    let alive = true
    setLoading(true)
    Promise.all([listLibrary(), listMcpServers(), listExperts()])
      .then(([lib, ss, ex]) => {
        if (!alive) return
        setLibrary(lib)
        setServers(ss)
        setExperts(ex)
      })
      .catch((err) => {
        if (!alive) return
        setLibrary(null)
        setServers([])
        showToast(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [panelOpen, showToast])

  const apply = (patch: SessionCapabilitiesPatch) => {
    if (saving || disabled) return
    onChange(patch)
  }

  // 点外面收起（含输入框、消息区、左栏）：antd 的 Popover 只有在配了 click 触发时才自带这条，
  // 而这里刻意不用它（见下面 `trigger={[]}` 的注释），所以自己挂一个文档级 mousedown。
  const barRef = useRef<HTMLSpanElement | null>(null)
  const panelOpenNow = menuOpen || panel !== null
  useEffect(() => {
    if (!panelOpenNow) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Element | null
      if (!t) return
      // 弹层挂在 body 下（antd 默认 portal），所以"点在弹层里"要单独判。
      if (barRef.current?.contains(t) || t.closest?.('.ant-popover')) return
      setMenuOpen(false)
      setPanel(null)
    }
    document.addEventListener('mousedown', onDown, true)
    return () => document.removeEventListener('mousedown', onDown, true)
  }, [panelOpenNow])

  const inDefaultOf = (applicability: LibraryItem['applicability'], enabled: boolean): boolean =>
    enabled !== false && applicability.length > 0 && applicability.every((a) => a.ok)

  const rowsOf = (kind: 'skills' | 'mcps'): OptionRow[] => {
    if (kind === 'skills') {
      return (library ?? [])
        .filter((it) => it.kind === 'skill')
        .map((s) => ({
          id: s.id,
          name: s.name || s.id,
          inDefault: inDefaultOf(s.applicability, s.enabled),
          ...skillOptionView(s),
        }))
    }
    return mcpRows(servers, library ?? []).map((m) => {
      const view = mcpOptionView(m)
      return { id: m.id, name: m.name || m.id, inDefault: view.selectable && inDefaultOf(m.applicability, m.defEnabled), ...view }
    })
  }

  const toggle = (kind: 'skills' | 'mcps', id: string, checked: boolean) => {
    const cur = (kind === 'skills' ? choice.skills : choice.mcps) ?? []
    const next = checked ? [...cur, id] : cur.filter((x) => x !== id)
    // 保持清单顺序（服务端也会规范化，但请求体可读性好一点）。
    const order = rowsOf(kind).map((r) => r.id)
    next.sort((a, b) => order.indexOf(a) - order.indexOf(b))
    apply(selectionPatchFor(kind, next))
  }

  const setMode = (kind: 'skills' | 'mcps', custom: boolean) => {
    if (!custom) {
      apply(selectionPatchFor(kind, null))
      return
    }
    // 切到自定义时用「缺省本来就会投」的集合预填：用户的常见动作是"减掉几个"。
    const def = rowsOf(kind)
      .filter((r) => r.inDefault)
      .map((r) => r.id)
    apply(selectionPatchFor(kind, def))
  }

  const section = (kind: 'skills' | 'mcps') => {
    const list = rowsOf(kind)
    const selected = kind === 'skills' ? choice.skills : choice.mcps
    const custom = selected !== null
    const title = TITLE[kind]
    return (
      <div>
        <div className="fg-cap-head">
          <Label>{title}</Label>
          <span className="fg-cap-mode">
            <button
              type="button"
              className={`fg-cap-mode-btn${custom ? '' : ' fg-on'}`}
              disabled={saving}
              onClick={() => setMode(kind, false)}
            >
              跟随默认
            </button>
            <button
              type="button"
              className={`fg-cap-mode-btn${custom ? ' fg-on' : ''}`}
              disabled={saving}
              onClick={() => setMode(kind, true)}
            >
              自定义
            </button>
          </span>
        </div>
        {!custom ? (
          <div className="fg-cap-note">
            {list.filter((r) => r.inDefault).length === 0
              ? `当前没有可用的${title}`
              : `按默认启用（当前 ${list.filter((r) => r.inDefault).length} 个）`}
          </div>
        ) : (
          <>
            <div className="fg-cap-note">
              <span>
                只投勾选的 {selected.length} 个{selected.length === 0 ? '（一个都不要）' : ''}
              </span>
              <span>
                <button
                  type="button"
                  className="fg-cap-mini"
                  disabled={saving}
                  onClick={() => apply(selectionPatchFor(kind, list.filter((r) => r.inDefault).map((r) => r.id)))}
                >
                  全选
                </button>
                <button type="button" className="fg-cap-mini" disabled={saving} onClick={() => apply(selectionPatchFor(kind, []))}>
                  清空
                </button>
              </span>
            </div>
            <div className="fg-cap-list">
              {list.length === 0 && <div className="fg-cap-note">清单为空</div>}
              {list.map((row) => {
                const checked = selected.includes(row.id)
                return (
                  <div key={row.id} className={`fg-cap-opt${row.selectable ? '' : ' fg-off'}`}>
                    <Checkbox
                      checked={checked}
                      disabled={!row.selectable || saving}
                      onChange={(e) => toggle(kind, row.id, e.target.checked)}
                    >
                      <span className="fg-cap-opt-name">{row.name}</span>
                    </Checkbox>
                    {row.note && (
                      <Tooltip title={row.note}>
                        <span className="fg-cap-opt-why">{row.selectable ? '部分不可用' : '不可选'}</span>
                      </Tooltip>
                    )}
                  </div>
                )
              })}
            </div>
          </>
        )}
        <div className="fg-cap-note">
          {applyHint === 'first-round'
            ? '这套能力随本次会话的首轮一起生效（不用等下一轮）。'
            : '改动在下一条消息生效；正在跑的这一轮不受影响。'}
        </div>
      </div>
    )
  }

  const expertSection = () => {
    const list = experts ?? []
    return (
      <div>
        <div className="fg-cap-head">
          <Label>专家</Label>
          <span className="fg-cap-mode">
            <button
              type="button"
              className={`fg-cap-mode-btn${choice.expert === null ? ' fg-on' : ''}`}
              disabled={saving}
              onClick={() => {
                apply(expertSwitchPatch(null))
                setPanel(null)
              }}
            >
              跟随默认
            </button>
          </span>
        </div>
        <div className="fg-cap-list">
          <button
            type="button"
            className={`fg-cap-opt${choice.expert === '' ? ' fg-on' : ''}`}
            disabled={saving}
            onClick={() => {
              apply(expertSwitchPatch(''))
              setPanel(null)
            }}
          >
            <span className="fg-cap-opt-name">通用</span>
            <span className="fg-cap-opt-id">不用专家（技能/连接器使用默认设置）</span>
          </button>
          {list.map((e) => (
            <button
              key={e.id}
              type="button"
              className={`fg-cap-opt${choice.expert === e.id ? ' fg-on' : ''}${e.enabled ? '' : ' fg-off'}`}
              disabled={saving}
              onClick={() => {
                apply(expertSwitchPatch(e.id))
                setPanel(null)
              }}
            >
              <span className="fg-cap-opt-name">{e.name || e.id}</span>
              <span className="fg-cap-opt-id">
                {e.skills?.length ?? 0} 技能 · {e.mcps?.length ?? 0} MCP{e.has_instructions ? ' · 角色设定' : ''}
                {e.enabled ? '' : '（已禁用）'}
              </span>
            </button>
          ))}
        </div>
        {/* 与创建页同一句口径：人格只在首轮注入；会话中途换专家只换能力预设。 */}
        <div className="fg-cap-note">
          换专家会连同它的技能/连接器预设一起换；角色设定在新会话开始时生效。
        </div>
      </div>
    )
  }

  const panelBody = (kind: PanelKind) => (
    <div className="fg-cap-pop">
      {loading && !library ? (
        <div className="fg-cap-loading">
          <Spin size="small" /> 正在取能力清单…
        </div>
      ) : kind === 'expert' ? (
        expertSection()
      ) : (
        section(kind)
      )}
    </div>
  )

  const menu = (
    <div className="fg-cap-menu" role="menu" aria-label="添加能力">
      {MENU.map((item) => (
        <button
          key={item.kind}
          type="button"
          role="menuitem"
          className="fg-cap-menu-item"
          title={item.hint}
          onClick={() => {
            setMenuOpen(false)
            setPanel({ kind: item.kind, from: 'add' })
          }}
        >
          <span className="fg-cap-menu-ico">{item.icon}</span>
          <span className="fg-cap-menu-label">{item.label}</span>
          <span className="fg-cap-menu-chev" aria-hidden="true">
            ›
          </span>
        </button>
      ))}
    </div>
  )

  const chips = capabilityChips(choice, experts?.find((e) => e.id === choice.expert)?.name)

  // 面板从 `+` 弹出（还没有 chip 时）或从对应 chip 弹出（已选中之后）。
  const addPopoverOpen = menuOpen || (panel !== null && panel.from === 'add')
  const closeAll = () => {
    setMenuOpen(false)
    setPanel(null)
  }

  return (
    <span className="fg-cap-bar" ref={barRef}>
      <Popover
        open={addPopoverOpen}
        onOpenChange={(v) => {
          if (!v) closeAll()
        }}
        // 自己管开关：chip 在同一个锚点区域里，交给 antd 的 click 触发会和 chip 的点击互相打架。
        trigger={[]}
        placement="topLeft"
        arrow={false}
        content={panel ? panelBody(panel.kind) : menu}
        styles={{ content: { padding: 6 } }}
      >
        <button
          type="button"
          className="fg-cap-add"
          disabled={disabled}
          aria-label="添加能力"
          // 原生 title 而不是 antd Tooltip：两者都从按钮**向上**弹，Tooltip 会盖住菜单的第三项
          // （实测截图里「连接器」被压住）。菜单项自己带 hint，这里只需要一个可发现的名字。
          title={disabled ? undefined : '添加能力：专家 / 技能 / 连接器'}
          onClick={() => {
            if (addPopoverOpen) {
              closeAll()
              return
            }
            setPanel(null)
            setMenuOpen(true)
          }}
        >
          <PlusOutlined style={{ fontSize: 12 }} />
        </button>
      </Popover>
      {chips.map((chip) => (
        <Popover
          key={chip.kind}
          open={panel?.kind === chip.kind && panel.from === 'chip'}
          onOpenChange={(v) => {
            if (!v) setPanel(null)
          }}
          trigger={[]}
          placement="topLeft"
          arrow={false}
          content={panelBody(chip.kind)}
          styles={{ content: { padding: 6 } }}
        >
          <button
            type="button"
            className="fg-cap-chip"
            title={chip.title}
            disabled={disabled}
            onClick={() => {
              if (panel?.kind === chip.kind && panel.from === 'chip') {
                setPanel(null)
                return
              }
              setMenuOpen(false)
              setPanel({ kind: chip.kind, from: 'chip' })
            }}
          >
            {chip.avatar ? (
              <span className="fg-cap-chip-avatar">{chip.avatar}</span>
            ) : (
              <span className="fg-cap-chip-ico">
                {chip.kind === 'skills' ? <ThunderboltOutlined /> : <ApiOutlined />}
              </span>
            )}
            <span className="fg-cap-chip-label">{chip.label}</span>
            {chip.count !== undefined && <span className="fg-cap-chip-count">{chip.count}</span>}
            <span className="fg-cap-chip-chev" aria-hidden="true">
              ▾
            </span>
          </button>
        </Popover>
      ))}
    </span>
  )
}
