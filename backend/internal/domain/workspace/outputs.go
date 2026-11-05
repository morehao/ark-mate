package workspace

// ——— 工作空间域的**对外响应**形状（v3.2 刀 6）———
//
// 只放领域自己产出的数据形状：没有 json tag，也不认识 omitempty。对外形态
// （`dtoworkspace.Workspace*`）由用例层 `service/svcworkspace` 投影——改一个 JSON 字段名
// 不该动领域代码，领域测试也不该断言线上格式。字段名与 DTO 一一对应（投影是纯搬运）。
//
// 与 `view.go` 的分工：那个文件里的 `WorkspaceView` 是**登记项在域内的视图**（含目录可读性
// 判定、会话归属聚合等内部语义），这里放的是"要交给外面"的那些形状。

// DirStatus 目录可读性判定（落在一处：读面、建会话前的预检、前端提示都引用它）。
const (
	DirStatusOK         = "ok"
	DirStatusMissing    = "missing"
	DirStatusNotDir     = "not_dir"
	DirStatusUnreadable = "unreadable"
)

// CreateRequest 登记一个工作空间。
type CreateRequest struct {
	Path   string
	Name   string
	Title  string
	Strict bool
}

// PatchRequest 改一个登记项（nil = 这个字段不动）。
type PatchRequest struct {
	Title                 string
	DefaultCapability     *string
	DefaultPermissionMode *string
	Path                  *string
}

// WorkspaceItem 登记表里的一条（对外形态）。
type WorkspaceItem struct {
	ID         string
	Path       string
	Title      string
	IsDefault  bool
	ListOrder  int64
	DirStatus  string
	Sessions   int
	LastUsedAt int64

	CreatedAt int64

	UpdatedAt int64
}

// WorkspaceList 登记表读面。
type WorkspaceList struct {
	Items        []WorkspaceItem
	DefaultID    string
	Writable     bool
	ProvisionDir string
	CreateRoot   string
}

// WorkspaceDelete 删除登记项的结果。
type WorkspaceDelete struct {
	ID               string
	ReleasedSessions int64
}

// WorkspaceMove 排序结果（按新顺序的 id 列表）。
type WorkspaceMove struct {
	IDs []string
}

// WorkspaceDirCreate 建目录的结果。
type WorkspaceDirCreate struct {
	Path string
}

// RecentWorkspace 最近使用的工作空间。
type RecentWorkspace struct {
	Path       string
	Name       string
	LastUsedAt int64

	Sessions int
}

// BrowseCrumb 面包屑的一段。
type BrowseCrumb struct {
	Name   string
	Path   string
	Hidden bool
}

// BrowseEntry 目录浏览的一条子项。
type BrowseEntry struct {
	Name   string
	Path   string
	Hidden bool
}

// BrowseView 目录浏览结果。
type BrowseView struct {
	Path      string
	Parent    string
	Home      string
	Roots     []string
	Crumbs    []BrowseCrumb
	Truncated bool
	Entries   []BrowseEntry
}

// FileEntry 文件浏览的一条。
type FileEntry struct {
	Name       string
	Path       string
	IsDir      bool
	Size       int64
	ModifiedAt int64
}

// FilesView 文件浏览结果。
type FilesView struct {
	Path    string
	Roots   []string
	Entries []FileEntry
}
