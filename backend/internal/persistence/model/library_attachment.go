package model

import "time"

// TableNameLibraryAttachment 资料库节点 ↔ 附件关联表（v33）。
//
// **不改造 `attachments` 表**（它是会话投递语义，v32）。本表只是「某个资料库节点挂了哪些附件」的
// 关联：字节仍在 filestore，附件行仍在 `attachments`。一节点可挂多附件；一个附件也可被多节点引用
// （去重共享同一 blob）。
const (
	// TableNameLibraryAttachment 资料库节点 ↔ 附件关联表（v33）。
	TableNameLibraryAttachment = "library_attachments"
)

// 关联角色（role）。
const (
	// LibraryAttachmentRolePrimary drive 节点的内容本体（一个 drive 节点恰有一条 primary）。
	LibraryAttachmentRolePrimary = "primary"
	// LibraryAttachmentRoleAttachment 附属文件（如给 doc 挂一份 PDF）。
	LibraryAttachmentRoleAttachment = "attachment"
)

// LibraryAttachmentEntity 是 library_attachments 表的一行。
//
// 复合主键 `(node_id, attachment_id)`：同一附件不能重复挂到同一节点；不同节点可共享同一附件。
// 迁移由 v33 步建表（store/migrate_v33.go），本实体与其逐列一致。
type LibraryAttachmentEntity struct {
	// NodeID → library_nodes.id
	NodeID string `gorm:"column:node_id;primaryKey"`
	// AttachmentID → attachments.id（字节在 filestore）
	AttachmentID string `gorm:"column:attachment_id;primaryKey"`
	// Role primary | attachment（见 LibraryAttachmentRole*）。
	Role string `gorm:"column:role;not null"`
	// CreatedAt RFC3339Nano(UTC) 文本。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (LibraryAttachmentEntity) TableName() string { return TableNameLibraryAttachment }

// LibraryAttachmentEntityList 是 LibraryAttachmentEntity 切片类型。
type LibraryAttachmentEntityList []LibraryAttachmentEntity
