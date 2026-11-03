package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameArtifact 产物登记册（v34）。**不存字节**：字节复用 golib filestore 的
	// core_file/core_file_upload（file 类产物用 upload_id 指向它）；本表只承载**溯源与归属**
	// ——哪次会话/哪一轮/哪个工具调用产出的、生成时内容哈希快照、后来是否被改动。
	TableNameArtifact = "artifacts"
)

// 产物种类（kind）：封闭集合，决定载体列（upload_id vs content_text）。一列一义。
const (
	// ArtifactKindFile 文件产物：upload_id 指向 core_file_upload.id（字节在 filestore）。
	ArtifactKindFile = "file"
	// ArtifactKindText 内联文本：content_text 存正文。
	ArtifactKindText = "text"
	// ArtifactKindTable 内联表格：content_text 存 JSON 数组。
	ArtifactKindTable = "table"
	// ArtifactKindLink 网页剪藏：content_text 存快照，title 存 url 展示。
	ArtifactKindLink = "link"
)

// 产出来源（origin）：怎么来的。与 kind 正交（kind 说载体，origin 说来源）。
const (
	ArtifactOriginToolWrite = "tool_write"
	ArtifactOriginToolEdit  = "tool_edit"
	ArtifactOriginUpload    = "upload"
	ArtifactOriginInline    = "inline"
	ArtifactOriginManual    = "manual"
)

// ArtifactEntity 是 artifacts 表的一行（产物溯源登记）。
//
// 迁移由自管迁移内核的 v34 步建表（字面量 DDL，见 store/migrate_v34.go）；本实体与那步
// **手工逐列一致**（列名/类型），改列时两处必须同步（守卫 A-2 交叉断言）。
type ArtifactEntity struct {
	// ID 产物 id，`art_<26位ULID>`（internal/core/object/objid.New(objid.Artifact)）。
	ID string `gorm:"column:id;primaryKey"`
	// SessionID 归属会话（必有；产物天然属于某次会话）。
	SessionID string `gorm:"column:session_id;not null"`
	// RunID 哪一轮产出（→ runs.id；空 = 工具外/补录）。
	RunID string `gorm:"column:run_id;not null;default:''"`
	// ToolCallID 产出它的工具调用（对齐 events 的 tool_call_id；空 = 非工具产出）。
	ToolCallID string `gorm:"column:tool_call_id;not null;default:''"`
	// Origin 来源：tool_write|tool_edit|upload|inline|manual（见 ArtifactOrigin*）。
	Origin string `gorm:"column:origin;not null"`
	// Kind 载体种类：file|text|table|link（见 ArtifactKind*）。
	Kind string `gorm:"column:kind;not null"`
	// Title 展示名（文件名/摘要/url）。
	Title string `gorm:"column:title;not null"`
	// MimeType file 类的 MIME（其余可为空）。
	MimeType string `gorm:"column:mime_type;not null;default:''"`
	// SizeBytes file 类的字节数（其余 0）。
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0"`
	// UploadID file 类 → core_file_upload.id（字节在 filestore；其余为空）。
	UploadID string `gorm:"column:upload_id;not null;default:''"`
	// ContentText text/table/link 类的内联内容（file 类为空）。
	ContentText string `gorm:"column:content_text;not null;default:''"`
	// ContentSha256 生成时内容快照哈希（回答「还在生成时那样吗」）。
	ContentSha256 string `gorm:"column:content_sha256;not null;default:''"`
	// Dirty 1 = 磁盘/内容已与快照不一致（后续被改动）；0 = 未改动。
	Dirty int `gorm:"column:dirty;not null;default:0"`
	// CreatedAt / UpdatedAt RFC3339Nano(UTC) 文本。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (ArtifactEntity) TableName() string { return TableNameArtifact }

// ArtifactEntityList 是 ArtifactEntity 切片类型（gormdao 泛型参数要求具名切片）。
type ArtifactEntityList []ArtifactEntity
