package dtoexport

// ExportReq 批量导出请求（ids 上限 50；format 默认 markdown）。
type ExportReq struct {
	IDs    []string `json:"ids" binding:"required"`
	Format string   `json:"format"` // markdown | text | json
}
