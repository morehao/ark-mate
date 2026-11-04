// 产物面（v34/v35）的对外类型：会话里「哪个工具写出了哪个文件」的溯源索引。
//
// 字节**不在这里**：file 类产物用 `upload_id` 指向 filestore（内容寻址）；`path` 是工作区相对
// 路径，只用于 dirty 判定（工作区原文件是否已被改动）与展示，不是回读字节的入口。

/** 产物载体种类（`artifacts.kind`）。 */
export type ArtifactKind = 'file' | 'text' | 'table' | 'link'

/** 产物来源（`artifacts.origin`）。 */
export type ArtifactOrigin = 'tool_write' | 'tool_edit' | 'upload' | 'inline' | 'manual'

/** `GET /sessions/:id/artifacts` 的元素（也用于 `GET /artifacts/:id`）。 */
export interface ArtifactMeta {
  id: string
  session_id: string
  run_id?: string
  tool_call_id?: string
  origin: ArtifactOrigin
  kind: ArtifactKind
  title: string
  /** 工作区相对路径（仅工具写工作区文件的产物有；dirty 判定靠它回定位原文件）。 */
  path?: string
  mime_type?: string
  size_bytes: number
  /** file 类产物的字节句柄（= 上传面 `POST /files` 的 file_id）。 */
  upload_id?: string
  content_text?: string
  content_sha256?: string
  /** 工作区原文件与生成时快照不一致（被改动/删除）。详情读面惰性计算，列表页返回存量。 */
  dirty: boolean
  created_at: string
  updated_at: string
}

/** `GET /sessions/:id/artifacts` 的响应（含未被收录进资料库的产物）。 */
export interface ArtifactListResp {
  artifacts: ArtifactMeta[]
  total: number
}
