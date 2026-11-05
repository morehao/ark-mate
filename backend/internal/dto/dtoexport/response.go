package dtoexport

// ExportFile 单会话导出产物（content 为可直接落盘的正文）。
type ExportFile struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Filename  string `json:"filename"`
	Format    string `json:"format"`
	Events    int    `json:"events"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"` // 事件数超上限（明确标注，不静默截断）
	Content   string `json:"content"`
}

// ExportResp 批量导出响应（skipped 列出无数据/未知会话，不静默丢弃）。
type ExportResp struct {
	GeneratedAt int64        `json:"generated_at"`
	Format      string       `json:"format"`
	Exported    int          `json:"exported"`
	TotalBytes  int          `json:"total_bytes"`
	Skipped     []string     `json:"skipped,omitempty"`
	Files       []ExportFile `json:"files"`
}
