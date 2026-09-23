import { http } from '../request'
import type { LibraryItem } from '@ark/types'

// ——— M2-4 Library 统一读模型（GET /library） ———

/**
 * 统一能力清单：skill/mcp/agent/tool 四类同构。
 *
 * 每条回带**逐 runtime 的 `applicability[]`**（`projected`/`untested`/`not_configured`/
 * `not_applicable`/`disabled` + 人话 `reason`）——**刻意不给「整体可用吗」的合并结论**：
 * 一条能力对 opencode 可用、对 dsh 不可用是有用信息，压成一个 bool 就没了。
 *
 * `kind` 未知值后端报 40000（不静默返回空表）；`q` 对 id/name/description 做大小写不敏感子串。
 */
export const listLibrary = (params: { kind?: string; q?: string } = {}): Promise<LibraryItem[]> => {
  const qs = new URLSearchParams()
  if (params.kind) qs.set('kind', params.kind)
  if (params.q) qs.set('q', params.q)
  const suffix = qs.toString()
  return http.get<LibraryItem[]>(`/forge/library${suffix ? `?${suffix}` : ''}`)
}
