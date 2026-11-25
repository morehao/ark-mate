package model

// ───────────────────────── 控制面认证数据模型（ark-iam RP 接入）─────────────────────────
//
// 设计：`.dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md`（§详细设计·数据模型）。
// 关键口径（DM-2 / C1）：**身份真源唯一 = ark-iam**；下面这些表是**平台侧的映射与平台属性**，
// 不是登录真源、不是所有权真相：
//   - `control_users.user_id` = ark-iam 令牌里的 `user_id`（= ark-iam `user.id`，审计主键）；
//   - 鉴权与资源所有权一律读**实时令牌 claim**，绝不读这里的快照行；
//   - 这些行由令牌驱动**幂等开通**（首次见到即 upsert），ark-iam 侧删了成员不会立刻删这里（快照）。
//
// 时间列一律 **INTEGER Unix 毫秒**（`objtime.Milli`，对齐 apps/workspaces 等 v40+ 新表与本仓 wire 口径）。
//
// 表名用**复数**（与 `apps`/`sessions` 一致）；设计文档正文为可读用了单数 `control_tenant`，
// 此处以本仓既有多数表名为准取 `control_tenants`/`control_users`/...。

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameControlTenant 租户的平台属性（隔离档 / 配额 / 展示名）。
	TableNameControlTenant = "control_tenants"
	// TableNameControlUser 用户映射 + 开通快照（user_id = ark-iam user.id）。
	TableNameControlUser = "control_users"
	// TableNameControlIdentityAudit 认证与登出审计。
	TableNameControlIdentityAudit = "control_identity_audits"
	// TableNameControlLogoutJTI SLO 的 jti 去重（多副本部署必须落表，DM-5）。
	TableNameControlLogoutJTI = "control_logout_jtis"
)

// nameMaxLen 等长字段的取值上限（对齐 DDL 的 varchar 长度语义；SQLite 不强制，但 Go 侧校验）。
const controlIDMaxLen = 64

// ControlTenantEntity 是 control_tenants 表的一行：租户的平台属性。
//
// 与 ark-iam 的 `tenant`（成员/角色/状态）是**两回事**：ark-iam 决定「谁能登录、属于哪个租户」；
// 本行决定「登录后这个租户怎么落工作区、算多少配额」。租户身份（tenant_id）来自令牌。
type ControlTenantEntity struct {
	// TenantID = ark-iam 令牌里的 `tenant_id`（非空；主键）。
	TenantID string `gorm:"column:tenant_id;primaryKey"`
	// DisplayName 展示名（空 → ''）。
	DisplayName string `gorm:"column:display_name;not null;default:''"`
	// IsolationLevel 隔离档：os_user | sandboxed（默认 sandboxed，承总纲 A2）。具名枚举取值见常量。
	IsolationLevel string `gorm:"column:isolation_level;not null;default:'sandboxed'"`
	// QuotaCPU 配额硬限（CPU）。
	QuotaCPU int64 `gorm:"column:quota_cpu;not null;default:0"`
	// QuotaDiskGB 配额硬限（磁盘 GiB）。
	QuotaDiskGB int64 `gorm:"column:quota_disk_gb;not null;default:0"`
	// CreatedAt / UpdatedAt Unix 毫秒。
	CreatedAt int64 `gorm:"column:created_at;not null"`
	UpdatedAt int64 `gorm:"column:updated_at;not null"`
}

// 隔离档取值（`control_tenants.isolation_level`）。
const (
	IsolationOSUser    = "os_user"
	IsolationSandboxed = "sandboxed"
)

// 用户类型取值（`control_users.user_type`）。
const (
	ControlUserTypeNormal  = "normal"  // 人令牌（HasPerson）
	ControlUserTypeService = "service" // 机器凭证（IsMachine）
)

// ControlUserEntity 是 control_users 表的一行：ark-iam 用户到平台的**映射 + 开通快照**。
//
// 复合唯一 `(tenant_id, user_id)`：同一人同一租户只一行；幂等 upsert 复用。
// person_id 是自然人跨租户标识（`Identity.PersonID`），仅用于「此人跨租户有哪些平台行」的对账。
type ControlUserEntity struct {
	// TenantID = 令牌 tenant_id。
	TenantID string `gorm:"column:tenant_id;primaryKey"`
	// UserID = ark-iam user.id（审计主键；与 tenant_id 组成复合主键）。
	UserID string `gorm:"column:user_id;primaryKey"`
	// PersonID = Identity.PersonID（自然人；机器凭证为空）。
	PersonID string `gorm:"column:person_id;not null;default:''"`
	// UserType normal | service。
	UserType string `gorm:"column:user_type;not null;default:'normal'"`
	// StatusSnapshot 开通时成员状态快照；**唯一读者＝运营对账/展示，鉴权绝不读它**（读实时令牌）。
	StatusSnapshot string `gorm:"column:status_snapshot;not null;default:'enable'"`
	// ActedAt 最近一次见到（Unix 毫秒）。
	ActedAt int64 `gorm:"column:acted_at;not null;default:0"`
	// CreatedAt Unix 毫秒。
	CreatedAt int64 `gorm:"column:created_at;not null"`
}

// ControlIdentityAuditEntity 是 control_identity_audits 表的一行：认证/登出审计。
type ControlIdentityAuditEntity struct {
	ID          int64  `gorm:"column:id;primaryKey;autoIncrement"`
	TenantID    string `gorm:"column:tenant_id;not null;default:''"`
	ActorUserID string `gorm:"column:actor_user_id;not null;default:''"`
	// Action login | logout | deny。
	Action string `gorm:"column:action;not null"`
	// Result allow | deny。
	Result string `gorm:"column:result;not null"`
	SrcIP  string `gorm:"column:src_ip;not null;default:''"`
	At     int64  `gorm:"column:at;not null"` // Unix 毫秒
}

// ControlLogoutJTIEntity 是 control_logout_jtis 表的一行：SLO logout_token 的 jti 去重。
//
// 存**摘要**（`logout.HashJTI`），不存原文；`expires_at` 对齐 logout_token 的 exp，过期可清理。
type ControlLogoutJTIEntity struct {
	// JTIHash = logout.HashJTI(jti) 摘要（主键）。
	JTIHash string `gorm:"column:jti_hash;primaryKey"`
	// ExpiresAt logout_token exp（Unix 毫秒）。
	ExpiresAt int64 `gorm:"column:expires_at;not null"`
	// CreatedAt Unix 毫秒。
	CreatedAt int64 `gorm:"column:created_at;not null"`
}

// ───────────────────────── gorm 表名与列表类型（R1/R2/R3：实体 ↔ 表名常量一一对应）─────────────────────────

// TableName 实现 gorm.Tabler，返回本表名常量（R2：经常量，不写字面量）。
func (ControlTenantEntity) TableName() string { return TableNameControlTenant }

// TableName 实现 gorm.Tabler。
func (ControlUserEntity) TableName() string { return TableNameControlUser }

// TableName 实现 gorm.Tabler。
func (ControlIdentityAuditEntity) TableName() string { return TableNameControlIdentityAudit }

// TableName 实现 gorm.Tabler。
func (ControlLogoutJTIEntity) TableName() string { return TableNameControlLogoutJTI }

// ControlTenantEntityList 是 ControlTenantEntity 的具名切片（gormdao 泛型参数要求，R1）。
type ControlTenantEntityList []ControlTenantEntity

// ControlUserEntityList 是 ControlUserEntity 的具名切片（R1）。
type ControlUserEntityList []ControlUserEntity

// ControlIdentityAuditEntityList 是 ControlIdentityAuditEntity 的具名切片（R1）。
type ControlIdentityAuditEntityList []ControlIdentityAuditEntity

// ControlLogoutJTIEntityList 是 ControlLogoutJTIEntity 的具名切片（R1）。
type ControlLogoutJTIEntityList []ControlLogoutJTIEntity
