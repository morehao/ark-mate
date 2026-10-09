package dtoworkspace

// WorkspaceBrowseResp 目录浏览结果（只读）。
// 只列目录：工作空间必须是目录（绝对 / 存在 / is_dir 三条件之一）。
type WorkspaceBrowseResp struct {
	// Path 规范化后的当前目录绝对路径（与建会话 cwd 同一形态：已做符号链接规范化）。
	Path string `json:"path"`
	// Parent 上级目录（到达文件系统根**或已配置的工作区根**时为空，前端据此禁用"上级"）。
	Parent string `json:"parent,omitempty"`
	// Home 服务进程家目录（前端"回到主目录"用；不可得或不在工作区根白名单内时为空）。
	Home string `json:"home,omitempty"`
	// Roots 生效的工作区根白名单（MATE_WORKSPACE_ROOTS；空 = 不限制）。
	// 透出给面板是为了让"为什么上不去了"有解释，也让它能把根直接列成入口。
	Roots []string `json:"roots,omitempty"`
	// Entries 子目录列表（按名称升序；默认隐藏 "." 开头的目录；
	// 配了白名单时，真身落在根外的符号链接子项不会被列出——点了必被拒，不如不给）。
	Entries []WorkspaceEntryResp `json:"entries"`
}

// WorkspaceEntryResp 浏览结果中的一个候选子目录。
type WorkspaceEntryResp struct {
	Name string `json:"name"` // 目录名（不含路径）
	Path string `json:"path"` // 绝对路径（可直接作为建会话的 cwd）
}

// WorkspaceRecentResp 最近使用过的工作空间（从既有会话派生）。
type WorkspaceRecentResp struct {
	Path string `json:"path"`
	Name string `json:"name"` // 末级目录名（展示用）
	// LastUsedAt 最近一次使用时间（该工作空间下最新会话的 updated_at）。
	LastUsedAt string `json:"last_used_at"`
	// Conversations 该工作空间下的会话数（含归档；不含代建的临时空间自身）。
	Conversations int `json:"conversations"`
}

// WorkspaceLibraryResp 资料库 = 工作区根下的一级子目录（给"选名字而不是选路径"用）。
// 派生自 MATE_WORKSPACE_ROOTS + 文件系统，不另存登记表：见 manager.WorkspaceLibraries。
type WorkspaceLibraryResp struct {
	Path string `json:"path"` // 绝对路径（可直接作为建会话的 cwd）
	Name string `json:"name"` // 目录名即资料库名
	Root string `json:"root"` // 它属于哪个工作区根
	// LastUsedAt 该资料库下最新会话的 updated_at（从未用过则为空，不是 zero time）。
	LastUsedAt string `json:"last_used_at,omitempty"`
	// Conversations 该资料库下的会话数（含归档）。
	Conversations int `json:"conversations"`
}

// WorkspaceFilesResp 目录内容清单（只读的"看资料"面，与浏览面分工见 manager.ListWorkspaceFiles）。
type WorkspaceFilesResp struct {
	// Path 规范化后的目录绝对路径。
	Path string `json:"path"`
	// Roots 生效的工作区根白名单（空 = 不限制）。
	Roots []string `json:"roots,omitempty"`
	// Entries 目录内容（目录在前、文件在后，各自按名升序）。
	// 注意：文件条目的 Path **不可**作为建会话 cwd（工作空间必须是目录）。
	Entries []WorkspaceFileEntryResp `json:"entries"`
}

// WorkspaceFileEntryResp 目录内容中的一项（文件或子目录）。
type WorkspaceFileEntryResp struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	// Size 字节数（符号链接取真身大小）。
	Size int64 `json:"size"`
	// ModifiedAt 最后修改时间（RFC3339，UTC）。
	ModifiedAt string `json:"modified_at"`
}

// WorkspaceSettingsResp 工作区根白名单的设置快照（`GET /settings/workspace`）。
//
// 为什么要同时给 Configured 与 Roots：面板编辑的是**显式配置**那一份，而代建根是平台派生的
// （恒在生效名单内、用户删不掉）。只给生效名单，面板就永远无法表达"删掉这一条"。
type WorkspaceSettingsResp struct {
	// Configured 显式配置的白名单（= 面板可编辑的那一份；不含隐式并入的代建根）。
	Configured []string `json:"configured"`
	// Roots **生效**白名单 = Configured ∪ {ProvisionDir}（空 = 不限制：可浏览任意可读目录）。
	Roots []string `json:"roots,omitempty"`
	// Restricted 是否处于受限模式（Configured 非空）。false = 未配白名单，目录浏览不受限。
	Restricted bool `json:"restricted"`
	// ProvisionDir 平台代建工作空间的根目录（空 = 代建关闭）。
	ProvisionDir string `json:"provision_dir,omitempty"`
	// ProvisionIncluded 代建根是否已隐式并入生效白名单（受限模式下恒 true；不受限时为 false——
	// 那时"名单"根本不存在，谈不上包含）。
	ProvisionIncluded bool `json:"provision_included"`
	// Source 白名单的最终来源：default | config.json | env | env(process) | flag | unavailable。
	Source string `json:"source"`
	// LockedBy 非空 = 该键被更高优先级来源锁定（写入必被覆盖），面板应转只读并显示它。
	LockedBy string `json:"locked_by,omitempty"`
	// Writable 是否可经 `PUT /settings/workspace` 改写（LockedBy 为空且进程有持久化端口）。
	Writable bool `json:"writable"`
}
