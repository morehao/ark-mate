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
	// Roots 面板配置的**起始点偏好**（MATE_WORKSPACE_ROOTS；空 = 没有偏好）。
	//
	// **v14.5 起它不是"允许范围"**：任何已存在的目录都能浏览、都能当工作空间（选工作区是全系统）。
	// 透出给面板的用途只剩一个——把这些偏好位置列成快捷入口，省去"从家目录一层层点下去"。
	Roots []string `json:"roots,omitempty"`
	// Crumbs 从**文件系统根到当前目录（含自身）**的祖先链，逐级可跳（对齐 dsh 的 DirectoryListing.crumbs）。
	//
	// 为什么由服务端下发而不是前端按路径字符串切分：盘符 / UNC / 符号链接（macOS 的 /var → /private/var）
	// 三处都会切错，而"位置显示错了"比"选不到"更让人困惑。
	//
	// **v14.5：恒给完整链**（全系统口径下没有"不许露出的前缀"），不再按名录裁剪。
	Crumbs []WorkspaceCrumbResp `json:"crumbs,omitempty"`
	// Truncated 是否因单层条目上限而截断（前端据此提示"还有未显示的子目录"，而不是静默少几行）。
	Truncated bool `json:"truncated,omitempty"`
	// Entries 子目录列表（按名称升序；默认隐藏 "." 开头的目录；
	// 配了白名单时，真身落在根外的符号链接子项不会被列出——点了必被拒，不如不给）。
	Entries []WorkspaceEntryResp `json:"entries"`
}

// WorkspaceEntryResp 浏览结果中的一个候选子目录。
type WorkspaceEntryResp struct {
	Name string `json:"name"` // 目录名（不含路径）
	Path string `json:"path"` // 绝对路径（可直接作为建会话的 cwd）
	// Hidden 是否按宿主平台约定隐藏（POSIX 的点前缀）。
	//
	// 服务端在 includeHidden=false 时**不下发**这些行；下发时带上标记，是为了让前端能自己
	// 决定"显示隐藏项"这个开关（对齐 dsh：切换开关不重新请求，隐藏行已在手上）。
	Hidden bool `json:"hidden,omitempty"`
}

// WorkspaceCrumbResp 面包屑的一项：一个祖先目录，逐级可跳。
//
// 与 WorkspaceEntryResp 字段形状相同但**语义不同**，故不合并：面包屑表达"位置"，
// 条目表达"选项"。两者的契约注释各自成立，合并只会让后来者以为它们可以互换。
type WorkspaceCrumbResp struct {
	// Name 显示名（文件系统根用它自身的路径当名字，例如 "/"；盘符则形如 "C:\"）。
	Name string `json:"name"`
	// Path 绝对路径（跳转目标）。
	Path string `json:"path"`
	// Hidden 恒为 false（祖先链上不区分隐藏与否——那是名字的宿主约定，不是位置的属性）。
	Hidden bool `json:"hidden,omitempty"`
}

// WorkspaceDirCreateResp `POST /workspaces/dirs` 响应：新建目录的绝对路径。
//
// 回**规范化后**的路径（而不是把 parent 与 name 拼回去给前端）：前端拿到它就是可以直接
// 提交为 cwd / 加入白名单的那一个，不需要也不应该自己拼。
type WorkspaceDirCreateResp struct {
	Path string `json:"path"`
}

// WorkspaceRecentResp 最近使用过的工作空间（从既有会话派生）。
type WorkspaceRecentResp struct {
	Path string `json:"path"`
	Name string `json:"name"` // 末级目录名（展示用）
	// LastUsedAt 最近一次使用时间（该工作空间下最新会话的 updated_at）。
	LastUsedAt string `json:"last_used_at"`
	// Sessions 该工作空间下的会话数（含归档；不含代建的临时空间自身）。
	Sessions int `json:"sessions"`
}

// WorkspaceFilesResp 目录内容清单（只读的"看资料"面，与浏览面分工见 manager.ListWorkspaceFiles）。
type WorkspaceFilesResp struct {
	// Path 规范化后的目录绝对路径。
	Path string `json:"path"`
	// Roots 面板配置的起始点偏好（空 = 没有偏好；v14.5 起不是允许范围）。
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

// WorkspacePickResp `POST /workspaces/pick-directory` 响应：宿主原生选择器的结果。
//
// 只有两种结局，且**取消不是错误**（对齐参照实现的 `pickDirectory(): Promise<string | null>`）：
//   - `{path}`  用户选定；
//   - `{cancelled: true}` 用户取消（或面板超时被收回）。
//
// 刻意不回 `{path:""}` 表示取消：空串在 JSON 里与"没给这个键"看不出区别，
// 而调用方要据此决定"静默收手"还是"报错"，两者必须能分辨。
type WorkspacePickResp struct {
	Path string `json:"path,omitempty"`
	// Cancelled 用户取消（面板被 Esc / 关闭 / 超时收回）。
	Cancelled bool `json:"cancelled,omitempty"`
}

// WorkspaceResp 一条**工作空间登记**（v14；左栏分组与选择器的唯一事实源）。
//
// 与 WorkspaceRecentResp / WorkspaceLibraryResp 的区别是本轮设计的核心：那两个是**派生的只读
// 视图**（从既有会话或从白名单目录算出来的），这一条是**用户登记下来的持久实体**——它有 id、
// 有标题、有顺序，可以被改写与删除。选择器的主列表取这一份，另外两个降级成辅助入口。
type WorkspaceResp struct {
	// ID 平台 uuid（对外稳定标识；改名/搬迁不改它）。会话的 workspace_id 指向它。
	ID string `json:"id"`
	// Path 规范化后的绝对路径（可直接作为建会话的 cwd）。
	Path string `json:"path"`
	// Title 展示名（'' = 未命名，前端按末级目录名渲染；默认工作空间读作本地化的「默认工作空间」）。
	Title string `json:"title"`
	// IsDefault 是否平台默认工作空间（删不掉；「未指定 cwd 的建会话」落在它上面）。
	IsDefault bool `json:"is_default"`
	// ListOrder 展示顺序（升序）。
	ListOrder int64 `json:"list_order"`
	// DirStatus 目录现状：ok | missing | not_dir | unreadable。
	//
	// **每次读现算，不缓存**：外部进程随时可能删/移目录，缓存只会让界面显示一个过期的好状态。
	// 它不是 ok 时这一行**仍然返回**——登记不会因为目录消失就凭空不见，用户得能看见并清理它。
	DirStatus string `json:"dir_status"`
	// Sessions 该工作空间名下的会话数（含归档）。
	Sessions int `json:"sessions"`
	// LastUsedAt 该工作空间下最新会话的 updated_at（RFC3339；空 = 从未用过，不是 zero time）。
	LastUsedAt string `json:"last_used_at,omitempty"`
	// CreatedAt / UpdatedAt 登记时刻与最后一次改写时刻（RFC3339）。
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// WorkspaceListResp `GET /workspaces` 响应。
type WorkspaceListResp struct {
	// Items 按 list_order 升序的全部登记。
	Items []WorkspaceResp `json:"items"`
	// DefaultID 默认工作空间 id（空 = 该部署没有默认工作空间，例如代建关闭）。
	//
	// 前端要用它做两件事：把「未指定」的会话归到它下面、以及禁止删除它。
	DefaultID string `json:"default_id,omitempty"`
	// Writable 是否可创建/改名/删除（memory 持久层 = false）。
	//
	// 不可写时前端**不渲染**创建与管理入口（而不是给一个必然失败的按钮）——能力缺失就不给入口，
	// 不发明第二套口径（设计 ADR-11）。
	Writable bool `json:"writable"`
	// ProvisionDir 平台默认工作空间的所在目录（空 = 代建关闭）。透出是为了让"默认那个目录在哪"
	// 有答案，也是排障时的第一个锚点。
	ProvisionDir string `json:"provision_dir,omitempty"`
	// CreateRoot 新建工作空间的落盘根（ADR-3；空 = 不给「新建工作空间…」入口）。
	//
	// 放在**列表响应**上而不是只放设置响应里：左栏区头的添加菜单要据此决定渲不渲染第二项，
	// 而那一屏只读 `state.workspaces`（不读设置）。入口纪律同 ADR-11——能力缺失就不给按钮。
	CreateRoot string `json:"create_root,omitempty"`
}

// WorkspaceDeleteResp `DELETE /workspaces/:id` 响应。
//
// 刻意把 ReleasedSessions 回出来：删除登记的文案要能说清"哪些会话被放回了默认工作空间"，
// 而这句话必须由**事实**支撑，不是前端自己猜的。
type WorkspaceDeleteResp struct {
	ID string `json:"id"`
	// ReleasedSessions 归属被清空、读面归到默认工作空间的会话数（会话本身与 cwd 均未改动）。
	ReleasedSessions int64 `json:"released_sessions"`
}

// WorkspaceMoveResp `POST /workspaces/:id/move` 响应：重排后的**完整**顺序。
//
// 给完整顺序而不是只给目标行：客户端的本地顺序与库的对齐靠它比对（对齐 dsh 的
// `insertBefore` 返回完整 committed order）。
type WorkspaceMoveResp struct {
	IDs []string `json:"ids"`
}

// 目录现状取值（WorkspaceResp.DirStatus）。
const (
	// DirStatusOK 目录存在、可读、且确实是目录。
	DirStatusOK = "ok"
	// DirStatusMissing 目录不存在（被删/被移走/挂载点不在）。
	DirStatusMissing = "missing"
	// DirStatusNotDir 路径存在但不是目录（被一个同名文件顶掉了）。
	DirStatusNotDir = "not_dir"
	// DirStatusUnreadable 存在但是目录，但读不了（权限/挂载故障）——与 missing 分开，
	// 因为两者的处置不同：前者要去修权限，后者要去确认目录还在不在。
	DirStatusUnreadable = "unreadable"
)
