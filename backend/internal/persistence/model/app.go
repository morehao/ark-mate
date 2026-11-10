package model

// 应用定义（一期 A+Slot）两张表的行形状：`apps`（定义本体）+ `app_members`（归属关系）。
//
// 与 experts/skills/mcp_servers 的「body JSON + revision 窄列」不同（见 `model/expert.go`）：
// `apps` 是**类型化多列**表（v40 DDL，设计 §5.9 正文表）——一列一个定义字段，没有 `body` 列，
// 也**没有 `revision` 列**。写面的乐观锁因此不能读本表的 revision 列，而是以
// `definition_revisions.seq`（该 app 的最新一条审计 seq）为「当前版本」，在事务内复核后再写
// （见 `store/app.go` 与 `dao/app.go` 的 CAS 方法）。
//
// 时间列 `created_at` / `updated_at` 是 **INTEGER Unix 毫秒**（`objtime.Milli`；设计 §5.9 与
// `migrate_v40.go` 已定）——刻意不走 expert 那套 RFC3339Nano 文本 serializer，读写两侧零换算。

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameApp 应用定义本体（v40）。一条 app 一行，主键 `id`（`app_` 前缀）。
	TableNameApp = "apps"
	// TableNameAppMember app 与其成员定义的多对多**归属**关系（v40）。
	// 主键复合 (app_id, member_kind, member_id)；member_kind 指向既有 6 种定义 + entity。
	TableNameAppMember = "app_members"
)

// 成员种类常量（`app_members.member_kind`）。前四种对齐 AppMembers 的组合字段，`entity` 见设计 §5.2.1。
const (
	AppMemberKindExpert     = "expert"
	AppMemberKindAutomation = "automation"
	AppMemberKindSkill      = "skill"
	AppMemberKindMcp        = "mcp"
	AppMemberKindEntity     = "entity"
)

// AppEntity 是 `apps` 表的一行（应用定义本体）。
//
// 字段与设计 §5.9 正文表逐列对应；JSON 数组/对象列（tags_json 等）在 store 侧序列化。
// `Enabled` 用 int（0/1）而非 bool——与 DDL 的 INTEGER 直连。
type AppEntity struct {
	ID string `gorm:"column:id;primaryKey"`
	// Name 展示名（1–40 字符）。
	Name string `gorm:"column:name;not null"`
	// Description 描述（≤120 字符，空 → ''）。
	Description string `gorm:"column:description;not null;default:''"`
	// Avatar 内置图标键（空 → ''）。
	Avatar string `gorm:"column:avatar;not null;default:''"`
	// TagsJSON 标签数组的 JSON 文本（默认 '[]'）。
	TagsJSON string `gorm:"column:tags_json;not null;default:'[]'"`
	// EntitiesJSON 实体归属声明的 JSON 文本（默认 '[]'）。
	EntitiesJSON string `gorm:"column:entities_json;not null;default:'[]'"`
	// ToolDomainsJSON 工具域前缀列表的 JSON 文本（默认 '[]'）。
	ToolDomainsJSON string `gorm:"column:tool_domains_json;not null;default:'[]'"`
	// SlotsJSON 执行逻辑钩子的 JSON 文本（默认 '[]'）。
	SlotsJSON string `gorm:"column:slots_json;not null;default:'[]'"`
	// PageJSON 前端页面入口的 JSON 文本（空 → ''）。
	PageJSON string `gorm:"column:page_json;not null;default:''"`
	// SettingsSchemaJSON 应用级配置 schema 的 JSON 文本（空 → ''）。
	SettingsSchemaJSON string `gorm:"column:settings_schema_json;not null;default:''"`
	// APIVersion 面向核心的应用 API 版本（caret 范围，默认 '^1'）。
	APIVersion string `gorm:"column:api_version;not null;default:'^1'"`
	// Enabled 是否启用（0/1；缺省 1）。
	Enabled int `gorm:"column:enabled;not null;default:1"`
	// CreatedAt 创建时刻（Unix 毫秒，`objtime.Milli`）。
	CreatedAt int64 `gorm:"column:created_at;not null"`
	// UpdatedAt 最后修改时刻（Unix 毫秒，`objtime.Milli`）。
	UpdatedAt int64 `gorm:"column:updated_at;not null"`
}

// TableName 实现 gormdao.Entity。
func (AppEntity) TableName() string { return TableNameApp }

// AppEntityList 是 AppEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AppEntityList []AppEntity

// AppMemberEntity 是 `app_members` 表的一行（app 与成员定义的归属关系）。
//
// 复合主键 (AppID, MemberKind, MemberID) 由 DDL 的 PRIMARY KEY 保证去重与归属唯一。
// `Owned` 1=拥有（可随 app 删）/0=引用（默认，只解除归属，设计 Q2）。
type AppMemberEntity struct {
	AppID      string `gorm:"column:app_id;primaryKey"`
	MemberKind string `gorm:"column:member_kind;primaryKey"`
	MemberID   string `gorm:"column:member_id;primaryKey"`
	Owned      int    `gorm:"column:owned;not null;default:0"`
}

// TableName 实现 gormdao.Entity。
func (AppMemberEntity) TableName() string { return TableNameAppMember }

// AppMemberEntityList 是 AppMemberEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AppMemberEntityList []AppMemberEntity
