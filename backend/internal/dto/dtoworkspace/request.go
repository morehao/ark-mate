package dtoworkspace

// WorkspaceCreateReq `POST /workspaces` 的请求体：把一个**已存在的目录**登记成工作空间。
//
// 只有一条路（选一个宿主目录，新的或已存在的）：目录的建立交给**目录浏览面**里的
// "新建文件夹"，不给第二条"输入一个还不存在的路径"的入口——那会带来"要不要替用户建目录"
// 这类判据，而 dsh 的同一决策也是"添加工作空间只有一条路"。
// 两种模式**二选一**（ADR-2）：`path` 登记一个已存在的目录；`name` 在落盘根下建一个新目录并
// 登记。都给 / 都不给都是 40001（`workspace_invalid`）——不"猜用户想干嘛"。
type WorkspaceCreateReq struct {
	// Path 目录绝对路径（`path` 模式）。服务端按**与建会话同一条校验**处理（绝对 / 存在 / 是目录 +
	// 符号链接规范化），所以"能登记"与"能建会话"不可能出现一个能过一个不能过。
	Path string `json:"path"`
	// Name 新建目录的**单段名**（`name` 模式，ADR-2）。在 `create_root` 下 `Mkdir` 再登记，
	// **一步**完成（不再需要前端串「新建文件夹」+「登记」两次可失败的往返）。
	//
	// 合法性走 `ValidateWorkspaceName`（与目录浏览面的「新建文件夹」**同一条实现**）：
	// 空 / 分隔符 / `.` / `..` / 绝对路径 / 超长 → 40001；同名目录已存在 → 40901，
	// **绝不静默复用**。`create_root` 未声明 → 40001（`workspace_create_root_unset`）。
	Name string `json:"name"`
	// Title 展示名（可选；空 = 未命名，界面按末级目录名渲染）。
	//
	// 它**不是**身份：改名不改 id、不改路径，也不影响该目录下已有会话的归属。
	Title string `json:"title,omitempty"`
	// Strict 为 true 时，路径已登记 → 报 workspace_exists(40901)；缺省（false）= get-or-create
	// 返回既有那一行。
	//
	// 为什么默认是 get-or-create：前端的"选一个目录建工作空间"会被重复触发（多按一次、
	// 「最近使用（未登记）」点两下），报错会让一次幂等意图看起来像失败（设计 ADR-8）。
	Strict bool `json:"strict,omitempty"`
}

// WorkspacePatchReq `PATCH /workspaces/:id` 的请求体。
//
// 目前只有一个可改字段。**没有**"设为默认"：默认工作空间由装配期钉死（设计 ADR-4），
// 把作废的键留在请求体里只会让下一个人以为平台支持它。
type WorkspacePatchReq struct {
	// Title 新展示名（空串 = 清空，回落到末级目录名渲染）。
	Title string `json:"title"`
	// DefaultCapability **该工作空间新建会话时的默认能力选定**（v27；`nil` = 不改）。
	//
	// 用指针区分"没写"与"写了空串"：后者是**清掉**该档默认值（回到平台缺省），
	// 而前者是"这次只改标题"。两者在 wire 上都长得像"没有这个键"，合成一个就会出
	// 「只想改标题，结果把默认能力集清空了」。
	DefaultCapability *string `json:"default_capability"`
	// DefaultPermissionMode 默认权限模式（同上：`nil` = 不改，空串 = 清掉）。
	DefaultPermissionMode *string `json:"default_permission_mode"`
	// Path **改（登记）路径**（W4；`nil` = 不改）。
	//
	// 它改的是"这个工作空间指向哪个目录"——那是身份的一半，所以它比改名危险得多：
	// 服务端按与建会话**同一条**校验（绝对 / 存在 / 是目录 / 规范化）处理，且
	// **既有会话的 `cwd` 一个字节都不动**（它们的历史就在那个目录里）。
	Path *string `json:"path"`
}

// WorkspaceMoveReq `POST /workspaces/:id/move` 的请求体（DOM insertBefore 语义）。
type WorkspaceMoveReq struct {
	// BeforeID 锚点工作空间；空 = 追加到尾部。锚点是自身 = 空操作（不写库）。
	BeforeID string `json:"before_id,omitempty"`
}

// WorkspaceDirCreateReq `POST /workspaces/dirs` 的请求体：在某个**已存在**的目录下建一个子目录。
//
// 刻意只有单段名（不含路径分隔符、不接受 "." / ".."）：多级创建与"相对父目录的路径"会把
// 越界判定摊到每一段上，而这条接口的边界判据只有一条——父目录合法 + 建出来的真身仍在根内。
type WorkspaceDirCreateReq struct {
	// Parent 已存在的父目录绝对路径（按与建会话同一条校验处理，必须落在工作区根白名单内）。
	Parent string `json:"parent" binding:"required"`
	// Name 新目录名（单段；不含 "/" 与 "\"；不得为 "." 或 ".."）。
	Name string `json:"name" binding:"required"`
}
