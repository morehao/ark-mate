package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameLibraryNode 资料库目录树主表（v33）。
	//
	// 分工（设计 §3）：字节落盘 / 内容寻址去重 / `core_file`、`core_file_upload` 两表归 golib
	// `filestore`，附件行归 `attachments`（v32）；本表只承载**目录树语义**——归属空间、父子关系、
	// 节点种类（node_kind）、载体定位（body / table_id / drive_upload_id）、原始 url。
	TableNameLibraryNode = "library_nodes"
)

// 节点种类（node_kind）：封闭集合，决定这条节点走哪个载体列 / 哪个读取入口（设计 §4.1）。
//
// 一列一义纪律：node_kind 只表达**种类**，不表达载体——载体由「body / table_id / drive_upload_id
// 谁非空」决定，写面用 `domain/material.RejectKindMismatch` 校验两者一致。
const (
	// LibraryNodeKindFolder 纯容器目录，无正文。
	LibraryNodeKindFolder = "folder"
	// LibraryNodeKindDoc 文档（body 存 Markdown/富文本）。
	LibraryNodeKindDoc = "doc"
	// LibraryNodeKindTable 结构化表格（table_id → library_tables.node_id）。
	LibraryNodeKindTable = "table"
	// LibraryNodeKindLink 网页剪藏（body 存快照 md，source_url 存原地址）。
	LibraryNodeKindLink = "link"
	// LibraryNodeKindDrive 网盘文件（drive_upload_id → core_file_upload.id，字节在 filestore）。
	LibraryNodeKindDrive = "drive"
	// LibraryNodeKindPage HTML 页面（body 存 HTML）。
	LibraryNodeKindPage = "page"
)

// 默认个人空间 id（本期唯一空间；团队空间恒拒，见设计 §4.4）。
const (
	// LibrarySpaceDefault 默认个人空间。本期只建这一个；写面收到非此值的 space_id 报
	// `space_not_supported`，不静默接受。
	LibrarySpaceDefault = "default"
)

// 节点分类（category，v34）：与 node_kind 正交，表达「从哪来/归属类别」。
const (
	// LibraryNodeCategoryMaterial 用户自建资料（默认）；provenance_id 恒为空。
	LibraryNodeCategoryMaterial = "material"
	// LibraryNodeCategoryArtifact 会话产物收录；provenance_id 必非空（→ artifacts.id）。
	LibraryNodeCategoryArtifact = "artifact"
)

// LibraryNodeEntity 是 library_nodes 表的一行（资料库目录树节点）。
//
// 迁移由自管迁移内核的 v33 步建表（字面量 DDL，见 store/migrate_v33.go）；本实体与那步
// **手工逐列一致**（列名/类型），改列时两处必须同步。
type LibraryNodeEntity struct {
	// ID 资料库节点 id，`libnd_<26位ULID>`（internal/core/object/objid.New(objid.LibraryNode)）。
	ID string `gorm:"column:id;primaryKey"`
	// SpaceID 所属空间（本期恒为 LibrarySpaceDefault）。
	SpaceID string `gorm:"column:space_id;not null"`
	// ParentID 父节点 id；空字符串 = 空间根。
	ParentID string `gorm:"column:parent_id;not null;default:''"`
	// NodeKind 节点种类（见 LibraryNodeKind*）。
	NodeKind string `gorm:"column:node_kind;not null"`
	// Title 展示标题。
	Title string `gorm:"column:title;not null"`
	// Body doc/page/link 的正文快照（drive/table/folder 为空）。
	Body string `gorm:"column:body;not null;default:''"`
	// TableID table 种类 → library_tables.node_id（其它种类为空）。
	TableID string `gorm:"column:table_id;not null;default:''"`
	// DriveUploadID drive 种类 → core_file_upload.id（其它种类为空）。
	DriveUploadID string `gorm:"column:drive_upload_id;not null;default:''"`
	// SourceURL link 种类的原始 url（其它种类为空）。
	SourceURL string `gorm:"column:source_url;not null;default:''"`
	// SizeBytes 便于列表展示的字节数（drive 取附件大小；多附件取主附件大小；其余 0）。
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0"`
	// Category 分类维度（v34）：'material'（用户自建资料）| 'artifact'（会话产物收录）。
	//
	// 与 node_kind 正交：node_kind 说「长什么样」（载体），category 说「从哪来/归属类别」。
	Category string `gorm:"column:category;not null;default:'material'"`
	// ProvenanceID 引用的产物主键（v34）：→ artifacts.id（空串 = 非产物）。
	//
	// **引用而非复制**：产物身份以 artifacts 为唯一事实源；资料库节点只是收录/组织。
	// 不变量：category='artifact' ⟺ provenance_id 非空（写面校验 material_provenance_mismatch）。
	ProvenanceID string `gorm:"column:provenance_id;not null;default:''"`
	// CreatedAt / UpdatedAt RFC3339Nano(UTC) 文本（落库格式硬契约，见 model/time.go / format_test.go）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (LibraryNodeEntity) TableName() string { return TableNameLibraryNode }

// LibraryNodeEntityList 是 LibraryNodeEntity 切片类型（gormdao 泛型参数要求具名切片）。
type LibraryNodeEntityList []LibraryNodeEntity
