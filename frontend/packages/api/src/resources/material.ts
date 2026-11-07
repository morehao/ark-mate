import { http } from '../request'
import { uploadFile } from './mate'
import type {
  CreateMaterialNodeReq,
  DeleteMaterialNodeResp,
  MaterialListResp,
  MaterialNodeDetail,
  MaterialPublishResp,
  MaterialRowsResp,
  MaterialSearchResp,
  PatchMaterialNodeReq,
} from '@ark-mate/types'

// ——— 资料库（Material Library）面的 API 客户端（v33）———
//
// 命名口径：REST 前缀是 `/mate/materials`（**不是** `/mate/library`——后者是「能力清单」读模型）。
// 本期只做资料库、不做知识库：`search` 是**关键词子串匹配**，不是语义检索。

/** 列目录（一层子节点）。parent 空 = 空间根。 */
export const listMaterialNodes = (params: { parent?: string; category?: string; limit?: number; offset?: number } = {}): Promise<MaterialListResp> => {
  const qs = new URLSearchParams()
  if (params.parent) qs.set('parent', params.parent)
  if (params.category) qs.set('category', params.category)
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.offset != null) qs.set('offset', String(params.offset))
  const suffix = qs.toString()
  return http.get<MaterialListResp>(`/mate/materials/nodes${suffix ? `?${suffix}` : ''}`)
}

/** 节点详情（元数据 + 按 kind 裁剪的载体）。 */
export const getMaterialNode = (id: string): Promise<MaterialNodeDetail> =>
  http.get<MaterialNodeDetail>(`/mate/materials/nodes/${encodeURIComponent(id)}`)

/** 建节点（folder/doc/table/link/page；drive 走上传）。 */
export const createMaterialNode = (req: CreateMaterialNodeReq): Promise<MaterialNodeDetail> =>
  http.post<MaterialNodeDetail>('/mate/materials/nodes', req)

/** 改名 / 移动 / 改正文。 */
export const patchMaterialNode = (id: string, req: PatchMaterialNodeReq): Promise<MaterialNodeDetail> =>
  http.patch<MaterialNodeDetail>(`/mate/materials/nodes/${encodeURIComponent(id)}`, req)

/** 删节点（folder 级联删子树；幂等）。 */
export const deleteMaterialNode = (id: string): Promise<DeleteMaterialNodeResp> =>
  http.delete<DeleteMaterialNodeResp>(`/mate/materials/nodes/${encodeURIComponent(id)}`)

/** 上传一个文件成 drive 节点（P0′ 收口后两步式：`POST /files` 拿 file_id → `POST /materials/nodes`）。 */
export const uploadMaterialFile = async (file: File, parentId?: string): Promise<MaterialNodeDetail> => {
  const up = await uploadFile(file)
  return createMaterialNode({
    node_kind: 'drive',
    title: up.name || file.name,
    parent_id: parentId,
    file_id: up.file_id,
  })
}

/**
 * 把一条会话产物**显式收录**进资料库（`POST /materials/import-artifact`，v34）。
 *
 * 引用而非复制：建 `category='artifact'` 的节点并指回 `provenance_id=artifact_id`。幂等——同一
 * 产物重复收录返回既有节点。产物默认只进 artifacts，**不自动全量入库**（目录会被会话垃圾淹没）。
 */
export const importArtifact = (artifactId: string, parentId?: string): Promise<MaterialNodeDetail> =>
  http.post<MaterialNodeDetail>('/mate/materials/import-artifact', {
    artifact_id: artifactId,
    parent_id: parentId,
  })

/** 关键词检索（**非语义**；title/body 子串匹配）。 */
export const searchMaterial = (q: string, params: { limit?: number; offset?: number } = {}): Promise<MaterialSearchResp> => {
  const qs = new URLSearchParams({ q })
  if (params.limit != null) qs.set('limit', String(params.limit))
  if (params.offset != null) qs.set('offset', String(params.offset))
  return http.get<MaterialSearchResp>(`/mate/materials/search?${qs.toString()}`)
}

/** 取表格行（schema + rows）。 */
export const getMaterialRows = (id: string): Promise<MaterialRowsResp> =>
  http.get<MaterialRowsResp>(`/mate/materials/nodes/${encodeURIComponent(id)}/rows`)

/** 整表替换表格行（rows 为 JSON 数组字符串）。 */
export const setMaterialRows = (id: string, rows: string): Promise<MaterialRowsResp> =>
  http.put<MaterialRowsResp>(`/mate/materials/nodes/${encodeURIComponent(id)}/rows`, { rows })

/** 发布 page（落盘 + 本地查看路径）。 */
export const publishMaterialPage = (id: string): Promise<MaterialPublishResp> =>
  http.post<MaterialPublishResp>(`/mate/materials/nodes/${encodeURIComponent(id)}/publish`, {})

/** page 的本地查看 URL（只绑回环；浏览器直接打开）。 */
export const materialViewUrl = (id: string): string => `/v1/mate/materials/nodes/${encodeURIComponent(id)}/view`
