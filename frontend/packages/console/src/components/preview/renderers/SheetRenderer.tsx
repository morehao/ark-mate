// ─────────────────────────────────────────────────────────────────────────────
// renderers/SheetRenderer.tsx —— 电子表格正文（xlsx / xls / csv / tsv，只读网格）
//
// 契约见 PreviewBodyProps：sheet 定义 loading='bytes-complete'，即 PreviewProvider 已把
// 整块字节取好，经 props.content 注入（本组件不再自己 fetch）。csv / tsv 同样走
// bytes-complete，content 为 Uint8Array（纯文本内容但以字节下发），xlsx / xls 是二进制。
//
// 实现要点：
//   · 用 xlsx(SheetJS) 的 read(content, { type: 'array' }) 解析。该模块**只在本组件**
//     import，配合 routes.ts 里 body 的 React.lazy() → xlsx 只进 SheetRenderer 的懒 chunk，
//     不进主包（重库）。本组件本身不额外做动态 import，避免重复拆分。
//   · 只读：不提供任何编辑 / 保存入口；不改动传入的字节，不写回文件。
//   · 多 sheet：xlsx 工作簿可有多个工作表；用 antd Segmented 顶部切换，默认第一个。
//   · 解析失败（损坏文件 / 非表格字节）→ catch 后落失败态，展示错误原因。
//
// 【大表性能】xlsx(SheetJS) 的读取是整表解析（无法流式），本组件在渲染层做限流：
//   · 单表最多渲染 MAX_RENDER_ROWS(=200) 行、MAX_RENDER_COLS(=100) 列；超出部分截断
//     并在底部提示「仅显示前 N 行 / M 列」，避免几千上万个 <td> 卡死 DOM。
//   · 使用简单 <table>（未引 antd Table）：虚拟化 / 列宽自适应对只读预览是过度设计，
//     原生 table 渲染更轻；固定表头 + 容器滚动即可满足预览。
//   · 待办：若后续要做整表浏览 / 虚拟滚动，再引入虚拟列表（一期不做）。
// ─────────────────────────────────────────────────────────────────────────────
import { useMemo, useState } from 'react'
import { Segmented } from 'antd'
import * as XLSX from 'xlsx'
import { tokens } from '@ark-mate/ui'
import type { PreviewBodyProps } from '../routes'

/** 单表最多渲染的行 / 列数（大表截断，见文件头「大表性能」说明）。 */
const MAX_RENDER_ROWS = 200
const MAX_RENDER_COLS = 100

/** 解析结果：每张表一个二维字符串网格（已按 MAX_RENDER_ROWS/COLS 截断）。 */
interface ParsedSheet {
  name: string
  /** 表头行（若原表无表头则整表当数据，表头为空）。 */
  header: string[]
  /** 数据行。 */
  rows: string[][]
  /** 原始真实行列数（用于截断提示）。 */
  rawRows: number
  rawCols: number
}

type ParseResult = { ok: true; sheets: ParsedSheet[] } | { ok: false; error: string }

/** 把 content 统一成 ArrayBuffer（xlsx.read type:'array' 接受 ArrayBuffer / Uint8Array）。 */
function toArrayBuffer(content: PreviewBodyProps['content']): ArrayBuffer | null {
  if (content instanceof Uint8Array) {
    // slice() 拷贝一份，避免底层 buffer 被复用 / detach；同时保证从 0 偏移开始。
    const copy = content.slice()
    return copy.buffer as ArrayBuffer
  }
  if (typeof content === 'string') {
    // 误传 string 时的兜底：当作 csv 文本编码成字节。
    return new TextEncoder().encode(content).buffer as ArrayBuffer
  }
  return null
}

/** 把单元格值转成适合展示的字符串（null/undefined → 空串，数字/日期按 xlsx 已格式化文本用）。 */
function cellText(value: unknown): string {
  if (value === null || value === undefined) return ''
  return String(value)
}

/** 解析工作簿为多张二维网格。 */
function parseWorkbook(content: PreviewBodyProps['content']): ParseResult {
  const data = toArrayBuffer(content)
  if (!data) return { ok: false, error: '表格内容不可用' }
  if (data.byteLength === 0) return { ok: false, error: '表格内容为空' }

  try {
    const wb = XLSX.read(data, { type: 'array' })
    if (!wb.SheetNames || wb.SheetNames.length === 0) {
      return { ok: false, error: '工作簿中没有工作表' }
    }

    const sheets: ParsedSheet[] = wb.SheetNames.map((name) => {
      const ws = wb.Sheets[name]
      // 原始完整二维数组（raw:false → 用单元格格式化显示文本，贴近 Excel 观感）。
      const aoa: unknown[][] = ws
        ? XLSX.utils.sheet_to_json<unknown[]>(ws, { header: 1, raw: false, defval: '', blankrows: false })
        : []

      const rawRows = aoa.length
      const rawCols = aoa.reduce((max, row) => Math.max(max, row.length), 0)

      const cappedRows = aoa.slice(0, MAX_RENDER_ROWS + 1) // +1：第一行当表头
      // 表头取第一行；其余为数据。空表则两者皆空。
      const headerRow = cappedRows[0] ?? []
      const restRows = cappedRows.slice(1)

      // 每行按列数补齐到 min(rawCols, MAX) 长度，保证网格对齐。
      const colCount = Math.min(rawCols, MAX_RENDER_COLS)
      const padRow = (row: unknown[]): string[] => {
        const out: string[] = []
        for (let i = 0; i < colCount; i += 1) out.push(cellText(row[i]))
        return out
      }

      return {
        name,
        header: padRow(headerRow),
        rows: restRows.map(padRow),
        rawRows,
        rawCols,
      }
    })

    return { ok: true, sheets }
  } catch (e) {
    const detail = e instanceof Error ? e.message : String(e)
    return { ok: false, error: detail || '解析失败' }
  }
}

/** 表头单元格样式（sticky，固定表头）。 */
const thStyle: React.CSSProperties = {
  position: 'sticky',
  top: 0,
  zIndex: 1,
  padding: '6px 10px',
  textAlign: 'left',
  fontWeight: 600,
  color: tokens.text,
  background: tokens.softFill,
  borderBottom: `1px solid ${tokens.borderStrong}`,
  whiteSpace: 'nowrap',
}

const tdStyle: React.CSSProperties = {
  padding: '5px 10px',
  color: tokens.text,
  borderBottom: `1px solid ${tokens.border}`,
  whiteSpace: 'nowrap',
  maxWidth: 320,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
}

/** 单张表的只读网格。 */
function SheetGrid({ sheet, active }: { sheet: ParsedSheet; active: string }) {
  const truncatedRows = Math.max(0, sheet.rawRows - 1 - sheet.rows.length) // 减表头行
  const truncatedCols = Math.max(0, sheet.rawCols - sheet.header.length)

  if (sheet.header.length === 0 && sheet.rows.length === 0) {
    return (
      <div style={{ padding: 24, color: tokens.textSecondary, fontSize: 13 }}>
        工作表「{sheet.name || active}」为空
      </div>
    )
  }

  return (
    <div style={{ height: '100%', overflow: 'auto', background: tokens.bg }}>
      <table
        style={{
          borderCollapse: 'collapse',
          fontSize: 12.5,
          fontFamily: tokens.fontFamily,
          minWidth: '100%',
        }}
      >
        <thead>
          <tr>
            {/* 行号列（read-only 参考位） */}
            <th style={{ ...thStyle, width: 44, color: tokens.textSecondary }}>#</th>
            {sheet.header.map((cell, ci) => (
              <th key={ci} style={thStyle}>
                {cell}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sheet.rows.map((row, ri) => (
            <tr key={ri}>
              <td style={{ ...tdStyle, color: tokens.textPlaceholder, textAlign: 'right' }}>{ri + 1}</td>
              {row.map((cell, ci) => (
                <td key={ci} style={tdStyle} title={cell}>
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {(truncatedRows > 0 || truncatedCols > 0) && (
        <div style={{ padding: '8px 12px', fontSize: 12, color: tokens.textSecondary }}>
          为保证性能，仅显示前 {Math.min(sheet.rawRows - 1, MAX_RENDER_ROWS)} 行
          {truncatedCols > 0 ? ` / 前 ${MAX_RENDER_COLS} 列` : ''}
          {truncatedRows > 0 ? `（共 ${sheet.rawRows - 1} 行）` : ''}
          {truncatedCols > 0 ? `（共 ${sheet.rawCols} 列）` : ''}
        </div>
      )}
    </div>
  )
}

export default function SheetRenderer({ content }: PreviewBodyProps) {
  // 解析结果随 content 缓存；content 变化才重新解析（大表解析较重，别放在渲染里）。
  const result = useMemo(() => parseWorkbook(content), [content])
  const [activeName, setActiveName] = useState<string>('')

  if (!result.ok) {
    return (
      <div
        style={{
          height: '100%',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: 24,
          color: tokens.error,
          fontSize: 13,
        }}
      >
        无法预览表格：{result.error}
      </div>
    )
  }

  const { sheets } = result
  // activeName 可能因切换 content 失效；回落到第一张表。
  const current = sheets.find((s) => s.name === activeName) ?? sheets[0]

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      {sheets.length > 1 && (
        // 多 sheet 切换：顶部 Segmented。
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '6px 12px',
            borderBottom: `1px solid ${tokens.border}`,
            background: tokens.surface,
            flexWrap: 'wrap',
          }}
        >
          <Segmented
            size="small"
            value={current?.name}
            onChange={(v) => setActiveName(String(v))}
            options={sheets.map((s) => ({ label: s.name || '(未命名)', value: s.name }))}
          />
        </div>
      )}
      <div style={{ flex: 1, minHeight: 0 }}>
        {current ? <SheetGrid sheet={current} active={activeName} /> : null}
      </div>
    </div>
  )
}
