package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameWorkspace 工作空间登记表（v14）：一条记录 = 一个规范化目录 + 展示名 + 顺序。
	//
	// 它是「用户的工作目录」这条线的**持久身份**（设计 ADR-1）：在这之前目录名只从会话
	// 派生（`DISTINCT cwd`），没有可写对象、也就没有"创建工作空间"这件事可落。
	TableNameWorkspace = "workspaces"
)

// WorkspaceEntity 是 workspaces 表的一行（v14）：**工作空间登记项**。
//
// 它是「用户在哪个目录里工作」这条线的持久实体（设计 §3.3 / ADR-1）。三个不变量：
//
//   - **一个规范化目录至多一条登记**：`path` 上的唯一索引是唯一执行者。为什么不在 Go 侧
//     判重：并发两次 POST 会各自读到"不存在"然后各插一行，库级唯一索引是唯一能挡住它的东西；
//   - **有且只有一个默认工作空间**：`is_default = 1` 的部分唯一索引保证。它是「建会话未指定
//     cwd」这条路的落点（设计 ADR-4），删掉它等于让那条路无路可走；
//   - **顺序是这一列的整数**：`list_order` 升序即左栏展示顺序。设计 ADR-3 说明了为什么不用
//     独立顺序表——规模是"用户手动登记的项目数"，区间 ±1 的代价可以忽略。
//
// **刻意没有的列**（每条都是判据，不是遗漏）：
//   - `session_ids` / 任何成员账：成员关系由 `sessions.workspace_id` 表达（设计 ADR-5）。
//     dsh 的登记行里有顺序化的 `sessionIds`，那是因为它的会话账独立于会话行、会漂移；
//     本仓归属就在会话行自己身上，结构上不可能漂移，再存一份账只会多一条会不一致的路径；
//   - 目录状态（是否存在/可读）：**不缓存**。外部进程随时可能删/移目录，缓存只会让界面
//     显示一个过期的好状态；读面每次现算。
type WorkspaceEntity struct {
	// ID 平台 uuid（对外稳定标识）。**不是路径**：改名/搬迁不该让 id 漂移。
	ID string `gorm:"column:id;primaryKey"`
	// Path 规范化后的绝对路径（EvalSymlinks + Clean 之后）。
	// 唯一索引落在这一列上，"同一目录一个工作空间"由库保证。
	Path string `gorm:"column:path;not null"`
	// Title 展示名（'' = 未命名 → 前端按末级目录名回落渲染）。
	//
	// 服务端**不**在空标题时替前端编一个名字：那会把"未命名"这个状态永久写死成某个语言
	// 的字符串（与默认工作空间的语言中立口径冲突，见 IsDefault）。
	Title string `gorm:"column:title;not null;default:''"`
	// IsDefault 是否平台默认工作空间（有且只有一个；删不掉、身份不可改）。
	//
	// 它的标题在库里存的是语言中立的 `default`，**展示时**才按界面语言渲染成「默认工作空间」
	// ——与 dsh 的 workspaceDisplayTitle 同一手法：一次安装跨语言切换时磁盘路径与库里
	// 的标题都不变，只有屏幕上的字跟着变。
	IsDefault bool `gorm:"column:is_default;serializer:arkmate_flag"`
	// ListOrder 左栏展示顺序（升序；新登记的追加到尾部）。
	ListOrder int64 `gorm:"column:list_order;not null"`
	// CreatedAt 登记时刻（RFC3339Nano(UTC)）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// UpdatedAt 最后一次改写时刻（改名/重排都会刷新它）。
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (WorkspaceEntity) TableName() string { return TableNameWorkspace }

// WorkspaceEntityList 是 WorkspaceEntity 切片类型（gormdao 泛型参数要求具名切片）。
type WorkspaceEntityList []WorkspaceEntity
