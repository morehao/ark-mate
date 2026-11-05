// Package dtoartifact 承载产物面的对外契约。
//
// 产物**字节不在这里**（file 类产物的字节在 filestore，用 upload_id 指向）；本包只暴露元数据与溯源。
package dtoartifact

// RegisterReq `POST /artifacts` 的请求体（**只收 file_id，不收文件流**；P0′ 收口后字节统一走 `POST /files`）。
//
// 内联产物（text/table/link）不传 FileID，改传 ContentText。
type RegisterReq struct {
	// FileID 上传面 `POST /files` 返回的 file_id（= core_file_upload.id）；file 类产物必填。
	FileID string `json:"file_id"`
	// ContentText 内联内容（text/table/link 类；file 类不传）。
	ContentText string `json:"content_text"`
	// SessionID 归属会话（必有）。
	SessionID string `json:"session_id"`
	// RunID 哪一轮产出（可选）。
	RunID string `json:"run_id"`
	// ToolCallID 产出它的工具调用（可选）。
	ToolCallID string `json:"tool_call_id"`
	// Origin 来源：tool_write|tool_edit|upload|inline|manual。
	Origin string `json:"origin"`
	// Kind 载体种类：file|text|table|link。
	Kind string `json:"kind"`
	// Title 展示名。
	Title string `json:"title"`
	// Path 工作区相对路径（可选；仅「工具写工作区文件」得来的 file 类产物有值，供 dirty 判定定位原文件）。
	Path string `json:"path"`
	// MimeType file 类的 MIME（可选）。
	MimeType string `json:"mime_type"`
	// SizeBytes file 类的字节数（可选；缺省由 filestore 补）。
	SizeBytes int64 `json:"size_bytes"`
}

// ArtifactResp 一条产物的元数据（`POST/GET /artifacts/:id` 的响应体）。
type ArtifactResp struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	RunID         string `json:"run_id,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	Origin        string `json:"origin"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Path          string `json:"path,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	UploadID      string `json:"upload_id,omitempty"`
	ContentText   string `json:"content_text,omitempty"`
	ContentSha256 string `json:"content_sha256,omitempty"`
	Dirty         bool   `json:"dirty"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// ListResp `GET /sessions/:id/artifacts` 的响应体。
type ListResp struct {
	Artifacts []ArtifactResp `json:"artifacts"`
	Total     int            `json:"total"`
}
