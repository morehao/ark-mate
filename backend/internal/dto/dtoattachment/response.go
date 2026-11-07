// Package dtoattachment 承载附件面的对外契约（响应体）。
//
// 上传是不走 JSON body 的 multipart（`file` 字段 + 可选 `name`），但响应与回读都走
// `AttachmentResp`；字节另经签名通路（presign-url + /objects）回读（不回 JSON）。
package dtoattachment

// AttachmentResp 一条附件的元数据（`POST/GET /attachments` 的响应体）。
//
// 字段与 `model.AttachmentEntity` 对齐但**不含 session_id**：session_id 归属由会话详情读面给出——
// 对外只暴露平台附件 id，外加 file_id（= core_file_upload.id，前端预览/下载走签名通路要用它）。
// **不含 path / 字节**：预览走签名通路（设计 §5.4）。
type AttachmentResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// FileID 附件字节的 filestore 句柄（= core_file_upload.id）；前端凭它换 presign-url。
	FileID    string `json:"file_id"`
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
