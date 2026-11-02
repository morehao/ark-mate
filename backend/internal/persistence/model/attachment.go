package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameAttachment 平台附件业务索引（指向 golib filestore 的 upload 行）。
	//
	// 分工（附件链路设计 §3.1）：字节落盘 / 内容寻址去重 / `core_file`、`core_file_upload`
	// 两表归 golib `filestore`；本表只承载 filestore 不知道的**平台语义**——会话归属、
	// 投递形态（kind）、生命周期（state）、探测后的真实 MIME、用户可见文件名。
	TableNameAttachment = "attachments"
)

// 投递形态（kind）：决定这条附件走哪个内容块下发（设计 §5）。
//
// 一列一义纪律：kind 只表达**投递形态**，不表达 MIME（MIME 只在 mime_type）。
const (
	// AttachmentKindImage 图片（进 image 内容块 / read_image）。
	AttachmentKindImage = "image"
	// AttachmentKindDocument 文档（不走内容块，只拼一行 handle 文本给 read）。
	AttachmentKindDocument = "document"
)

// 生命周期（state）。
const (
	// AttachmentStatePending 已上传、尚未挂到某一轮。
	AttachmentStatePending = "pending"
	// AttachmentStateLinked 已挂到某一轮（不可撤）。
	AttachmentStateLinked = "linked"
	// AttachmentStateOrphan 超过 TTL 仍未挂载，由清扫置此态并删 blob。
	AttachmentStateOrphan = "orphan"
)

// AttachmentEntity 是 attachments 表的一行（平台附件业务索引）。
//
// 迁移由自管迁移内核的 v32 步建表（字面量 DDL，见 store/migrate_v32.go）；本实体与那步
// **手工逐列一致**（列名/类型），改列时两处必须同步。`core_file*` 不由本实体建（事实源在 golib）。
type AttachmentEntity struct {
	// ID 平台附件 id，`att_<26位ULID>`（internal/core/object/objid.New(objid.Attachment)）。
	ID string `gorm:"column:id;primaryKey"`
	// UploadID 指向 `core_file_upload.id`（golib FileDetail.FileUploadID）；读字节走 filestore.Open(upload_id)。
	UploadID string `gorm:"column:upload_id;not null"`
	// SessionID 归属会话（pending 阶段可空，挂载时补）。指向 sessions.id。
	SessionID string `gorm:"column:session_id;not null;default:''"`
	// Name 用户可见原始文件名（只作展示 / 写进 handle，不落目录——blob 按 sha256 存）。
	Name string `gorm:"column:name;not null"`
	// MimeType 探测后的真实 MIME（不是客户端声明值，见设计 §4.3）。
	MimeType string `gorm:"column:mime_type;not null"`
	// SizeBytes 字节数（与 filestore 记录一致）。
	SizeBytes int64 `gorm:"column:size_bytes;not null"`
	// Sha256 内容摘要（= filestore content_hash；去重/审计，本表也留一份便于按内容查附件）。
	Sha256 string `gorm:"column:sha256;not null"`
	// Kind 投递形态：image | document（见 AttachmentKind*）。
	Kind string `gorm:"column:kind;not null"`
	// State 生命周期：pending | linked | orphan（见 AttachmentState*）。
	State string `gorm:"column:state;not null"`
	// CreatedAt RFC3339Nano(UTC) 文本（落库格式硬契约，见 model/time.go / format_test.go）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (AttachmentEntity) TableName() string { return TableNameAttachment }

// AttachmentEntityList 是 AttachmentEntity 切片类型（gormdao 泛型参数要求具名切片）。
type AttachmentEntityList []AttachmentEntity
