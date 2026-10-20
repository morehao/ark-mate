// Package row 是工作空间域的**登记行值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// 它与 `workspace.WorkspaceItem` 的分工：`Item` 是一张 `workspaces` 行的完整形状（纯值，零行为）；
// `WorkspaceItem` 是对外读面的投影（带会话计数、目录状态、最近使用等现算字段）。
// store 适配器只认 `Item`，投影留在域里做。
package row

import "time"

// DefaultTitle 默认工作空间的语言中立标题（`workspaces.title` 列的一个取值）。
//
// 存的是这个串而不是「默认工作空间」：一次安装跨语言切换时库里的值不变，只有屏幕上的字
// 跟着变（前端按 `is_default` 渲染本地化名字）。**改这个串等于改库里已有行的语义**——
// 老安装的行仍是旧值，展示回落会认不出它，因此它不是可随意重命名的东西。
const DefaultTitle = "default-workspace"

// Item 一条工作空间登记（一张 `workspaces` 行的完整形状）。
//
// 不变量（由域的端口实现与执行者保证，`Item` 自己只是形状）：
//   - 一个规范化目录至多一条登记（`path` 唯一索引）；
//   - 有且只有一个默认工作空间（`is_default` 部分唯一索引）；
//   - `list_order` 升序即展示顺序，且是 `0..n-1` 的一个排列。
type Item struct {
	// ID 平台 uuid（对外稳定标识，不是路径）。
	ID string
	// Path 规范化后的绝对路径（EvalSymlinks + Clean 之后）。
	Path string
	// Title 展示名（'' = 未命名 → 前端按末级目录名回落渲染）。
	Title string
	// IsDefault 是否平台默认工作空间（有且只有一个；删不掉、身份不可改）。
	IsDefault bool
	// DefaultCapabilityJSON 该工作空间新建会话时的默认能力选定（空 = 没有默认值）。
	DefaultCapabilityJSON string
	// DefaultPermissionMode 该工作空间新建会话时的默认权限模式（空 = 没有默认值）。
	DefaultPermissionMode string
	// ListOrder 左栏展示顺序（升序；新登记的追加到尾部）。
	ListOrder int64
	// CreatedAt 登记时刻。
	CreatedAt time.Time
	// UpdatedAt 最后一次改写时刻（改名/重排都会刷新它）。
	UpdatedAt time.Time
}

// DisplayTitle 把存值转成**展示名**：默认工作空间的哨兵标题读作「默认工作空间」，
// 其余原样透出（空串由前端按末级目录名回落）。
//
// 这条判定必须与写入侧用同一个哨兵：分叉的表现是默认工作空间在界面上显示成
// `default-workspace` 这个原始串（`TestDefaultWorkspaceCopy` 钉的就是它）。
func (it Item) DisplayTitle() string {
	if it.Title == DefaultTitle {
		return "默认工作空间"
	}
	return it.Title
}
