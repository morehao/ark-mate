// Package dtoattachment 承载附件面的对外契约（响应体）。
//
// 上传是不走 JSON body 的 multipart（`file` 字段 + 可选 `name`），但响应与回读都走
// `AttachmentResp`；字节另走 `GET /attachments/:id/content`（不回 JSON）。
package dtoattachment

// AttachmentResp 一条附件的元数据（`POST/GET /attachments` 的响应体）。
//
// 字段与 `model.AttachmentEntity` 对齐但**不含 upload_id / session_id**：upload_id 是内部指针
// （指向 golib core_file_upload），session_id 归属由会话详情读面给出——对外只暴露平台附件 id。
// **不含 path / 字节**：预览走 `GET /attachments/:id/content`（设计 §5.4）。
type AttachmentResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Sha256    string `json:"sha256"`
	// Kind 投递形态：image | document。
	Kind string `json:"kind"`
	// State 生命周期：pending | linked | orphan。
	State     string `json:"state"`
	CreatedAt int64  `json:"created_at"`
}

// AttachmentDeleteResp `DELETE /attachments/:id` 的响应体（幂等：不存在 → deleted=false 且 code=0）。
type AttachmentDeleteResp struct {
	Deleted bool `json:"deleted"`
}
