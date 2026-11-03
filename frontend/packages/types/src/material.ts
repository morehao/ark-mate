// 资料库（Material Library）面的对外类型。
//
// 本期范围：只做资料库、不做知识库——**没有任何语义检索/向量/问答字段**。
// 「找」= 目录浏览 + 关键词子串匹配（`GET /materials/search`）。

/** 目录树一个节点的列表项（`GET /materials/nodes` 的元素）。 */
export interface MaterialNode {
  id: string
  parent_id: string
  node_kind: MaterialNodeKind
  title: string
  size_bytes: number
  updated_at: string
}

/** 节点种类（六种，封闭）。 */
export type MaterialNodeKind = 'folder' | 'doc' | 'table' | 'link' | 'drive' | 'page'

/** `GET /materials/nodes` 的响应（一层子节点 + 分页）。 */
export interface MaterialListResp {
  nodes: MaterialNode[]
  has_more: boolean
  offset: number
  limit: number
}

/** `GET /materials/nodes/:id` 的响应（元数据 + 按 kind 裁剪的载体）。 */
export interface MaterialNodeDetail {
  id: string
  parent_id: string
  node_kind: MaterialNodeKind
  title: string
  body?: string
  table_id?: string
  schema?: string
  rows?: string
  drive_upload_id?: string
  source_url?: string
  size_bytes: number
  created_at: string
  updated_at: string
}

/** `POST /materials/nodes` 的请求体。 */
export interface CreateMaterialNodeReq {
  parent_id?: string
  node_kind: MaterialNodeKind
  title: string
  body?: string
  schema?: string
  source_url?: string
  space_id?: string
}

/** `PATCH /materials/nodes/:id` 的请求体（指针语义）。 */
export interface PatchMaterialNodeReq {
  title?: string
  parent_id?: string
  body?: string
}

/** `DELETE /materials/nodes/:id` 的响应。 */
export interface DeleteMaterialNodeResp {
  deleted: boolean
}

/** `GET /materials/search` 的一条关键词命中（**非语义**）。 */
export interface MaterialSearchHit {
  node_id: string
  title: string
  node_kind: MaterialNodeKind
  snippet: string
}

/** `GET /materials/search` 的响应。 */
export interface MaterialSearchResp {
  hits: MaterialSearchHit[]
  total: number
  has_more: boolean
  note?: string
}

/** `GET/PUT /materials/nodes/:id/rows` 的响应。 */
export interface MaterialRowsResp {
  node_id: string
  schema: string
  rows: string
}

/** `POST /materials/nodes/:id/publish` 的响应。 */
export interface MaterialPublishResp {
  node_id: string
  view_path: string
  file_path: string
}
