package dtoworkspace

// WorkspaceSettingsReq `PUT /settings/workspace` 的请求体（**全量替换**语义）。
//
// 字段缺省（`{}`）与显式 `{"roots": []}` 同义 = 恢复"不限制"：白名单只有"画边界"与"不画边界"
// 两种状态，没有"部分更新"的合理含义（增删一条就是一份新名单，由前端给全量）。
type WorkspaceSettingsReq struct {
	// Roots 允许作为工作空间的根目录（绝对路径；须存在且是目录）。
	Roots []string `json:"roots"`
}
