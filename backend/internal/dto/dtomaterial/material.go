// Package dtomaterial 承载资料库（Material Library）面的对外契约。
//
// 本期范围：只做资料库、不做知识库——因此**没有任何语义检索/向量/问答字段**。
// 「找」= 目录浏览 + 关键词子串匹配（`GET /materials/search`）。
package dtomaterial

// NodeResp 目录树里一个节点的**列表项**元数据（`GET /materials/nodes` 的元素）。
//
// 不含正文/载体指针：列表只给展示所需的最小字段；要看正文/载体走 `NodeDetailResp`
// （`GET /materials/nodes/:id`）。
type NodeResp struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	NodeKind  string `json:"node_kind"`
	Title     string `json:"title"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
}

// ListNodesResp `GET /materials/nodes` 的响应体（一层子节点 + 分页）。
type ListNodesResp struct {
	Nodes   []NodeResp `json:"nodes"`
	HasMore bool       `json:"has_more"`
	Offset  int        `json:"offset"`
	Limit   int        `json:"limit"`
}

// NodeDetailResp `GET /materials/nodes/:id` 的响应体：元数据 + 按 kind 裁剪的载体定位。
//
// 按 kind 只填对应字段（其余为空串），与 domain/material 的「一列一义」一致：
//   - doc/page/link：Body 有值（link 另有 SourceURL）；
//   - table：TableID 有值（另附 schema）；
//   - drive：DriveUploadID 有值（另附 DownloadPath 给本地只读绝对路径）。
type NodeDetailResp struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	NodeKind string `json:"node_kind"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	TableID  string `json:"table_id,omitempty"`
	// Schema 仅 table 种类：字段定义 JSON 原样透出。
	Schema string `json:"schema,omitempty"`
	// Rows 仅 table 种类：行数据 JSON 数组原样透出。
	Rows string `json:"rows,omitempty"`
	// DriveUploadID 仅 drive 种类：附件链路的 core_file_upload.id（内部指针，一般前端用不到）。
	DriveUploadID string `json:"drive_upload_id,omitempty"`
	SourceURL     string `json:"source_url,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// CreateNodeReq `POST /materials/nodes` 的请求体（建 folder/doc/table/link/page；drive 走上传）。
type CreateNodeReq struct {
	ParentID  string `json:"parent_id"`
	NodeKind  string `json:"node_kind"` // folder|doc|table|link|page
	Title     string `json:"title"`
	Body      string `json:"body"`       // doc/page/link
	Schema    string `json:"schema"`     // table: 字段定义 JSON
	SourceURL string `json:"source_url"` // link
	// SpaceID 可选；本期只支持默认个人空间，非默认值 → 40501 space_not_supported。
	SpaceID string `json:"space_id"`
}

// PatchNodeReq `PATCH /materials/nodes/:id` 的请求体（指针语义：字段缺失 = 不改）。
type PatchNodeReq struct {
	Title    *string `json:"title"`
	ParentID *string `json:"parent_id"`
	Body     *string `json:"body"`
}

// DeleteNodeResp `DELETE /materials/nodes/:id` 的响应体（幂等：不存在 → deleted=false 且 code=0）。
type DeleteNodeResp struct {
	Deleted bool `json:"deleted"`
}

// SearchResp `GET /materials/search` 的响应体（**关键词检索，非语义**）。
type SearchResp struct {
	Hits    []SearchHit `json:"hits"`
	Total   int         `json:"total"`
	HasMore bool        `json:"has_more"`
	// Note 固定为人话提示（如空结果时），前端可直接展示。
	Note string `json:"note,omitempty"`
}

// SearchHit 一条关键词命中。
type SearchHit struct {
	NodeID   string `json:"node_id"`
	Title    string `json:"title"`
	NodeKind string `json:"node_kind"`
	Snippet  string `json:"snippet"`
}

// SetRowsReq `PUT /materials/nodes/:id/rows` 的请求体（整表行数据替换；rows_json 为 JSON 数组字符串）。
//
// 首版用整体替换（单列 JSON）：最小可用、语义清晰（一次写就是一次快照）。逐行增删改在 UI 侧解析
// 数组后整体回写。
type SetRowsReq struct {
	Rows string `json:"rows"`
}

// RowsResp `GET/PUT /materials/nodes/:id/rows` 的响应体。
type RowsResp struct {
	NodeID string `json:"node_id"`
	Schema string `json:"schema"`
	Rows   string `json:"rows"`
}

// PublishPageResp `POST /materials/nodes/:id/publish` 的响应体。
type PublishPageResp struct {
	NodeID string `json:"node_id"`
	// ViewPath 本地只读查看路径（`/materials/nodes/:id/view`，只绑回环，非公网 URL）。
	ViewPath string `json:"view_path"`
	// FilePath 落盘后的本地绝对路径（便于用户直接打开；不进对外链接语义）。
	FilePath string `json:"file_path"`
}
