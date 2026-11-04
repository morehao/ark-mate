import { http } from '../request'
import type { ArtifactListResp, ArtifactMeta } from '@ark-mate/types'

// ——— 产物面（v34）的 API 客户端 ———
//
// 产物是「哪个会话/哪一轮/哪个工具产出了哪个文件」的溯源索引；字节在 filestore（`upload_id`）。
// 进资料库是**显式动作**：`POST /materials/import-artifact`（见 resources/material.ts 的 importArtifact），
// 不是自动全量入库——登记在 artifacts 里的东西不必然出现在资料库目录树中。

/** 列某会话的全部产物（含未被收录进资料库的；`created_at` 降序，列表不算 dirty）。 */
export const listSessionArtifacts = (sessionId: string): Promise<ArtifactListResp> =>
  http.get<ArtifactListResp>(`/mate/sessions/${encodeURIComponent(sessionId)}/artifacts`)

/** 产物详情（`dirty` 在此惰性计算并回写：工作区原文件被改动/删除 → true）。 */
export const getArtifact = (id: string): Promise<ArtifactMeta> =>
  http.get<ArtifactMeta>(`/mate/artifacts/${encodeURIComponent(id)}`)
