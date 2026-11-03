// Package artifact 是「产物」领域的**纯值对象与判决**（leaf 包）：种类（kind）/来源（origin）枚举、
// 载体一致性校验。它不 import 应用内核的任何包（不 import `internal/runtime`、不 import
// `persistence/model`、不 import 任何 `svc*`）——这一层只表达「一条产物记录长什么样、什么写法不合法」。
//
// 产物溯源设计 .dsh/docs/specs/2026-11-03-artifact-provenance-design.md §4：产物的**字节**复用 golib
// filestore（file 类用 upload_id 指向 core_file_upload）；本域只管「种类 ↔ 载体列」的一致性判决。
package artifact

import "errors"

// ErrKindMismatch 产物种类与载体列不匹配（写面校验失败 → 40001 artifact_kind_unsupported）。
var ErrKindMismatch = errors.New("artifact: kind does not match its carrier")

// Kind 产物种类（封闭集合，决定载体列）。
type Kind string

const (
	// KindFile 文件产物：UploadID 非空（字节在 filestore），ContentText 空。
	KindFile Kind = "file"
	// KindText 内联文本：ContentText 非空，UploadID 空。
	KindText Kind = "text"
	// KindTable 内联表格：ContentText 非空（JSON 数组），UploadID 空。
	KindTable Kind = "table"
	// KindLink 网页剪藏：ContentText 非空（快照），UploadID 空。
	KindLink Kind = "link"
)

// AllKinds 合法种类集合。
var AllKinds = []Kind{KindFile, KindText, KindTable, KindLink}

// IsValidKind 判断一个种类是否在封闭集合内。
func IsValidKind(k Kind) bool {
	for _, v := range AllKinds {
		if v == k {
			return true
		}
	}
	return false
}

// Origin 产出来源（怎么来的）。与 Kind 正交。
type Origin string

const (
	OriginToolWrite Origin = "tool_write"
	OriginToolEdit  Origin = "tool_edit"
	OriginUpload    Origin = "upload"
	OriginInline    Origin = "inline"
	OriginManual    Origin = "manual"
)

// AllOrigins 合法来源集合。
var AllOrigins = []Origin{OriginToolWrite, OriginToolEdit, OriginUpload, OriginInline, OriginManual}

// IsValidOrigin 判断一个来源是否在封闭集合内。
func IsValidOrigin(o Origin) bool {
	for _, v := range AllOrigins {
		if v == o {
			return true
		}
	}
	return false
}

// Carrier 是一条产物记录里与「载体」相关的列（只取判断一致性所需字段）。
type Carrier struct {
	UploadID    string
	ContentText string
}

// ValidateCarrier 校验「产物种类 ↔ 载体列」一致性（一列一义）。
//
//   - file：UploadID 非空，ContentText 为空；
//   - text/table/link：ContentText 非空，UploadID 为空。
//
// 未知种类直接判 mismatch（fail-closed）。
func ValidateCarrier(k Kind, c Carrier) error {
	hasUpload := c.UploadID != ""
	hasText := c.ContentText != ""
	switch k {
	case KindFile:
		if !hasUpload || hasText {
			return ErrKindMismatch
		}
	case KindText, KindTable, KindLink:
		if !hasText || hasUpload {
			return ErrKindMismatch
		}
	default:
		return ErrKindMismatch
	}
	return nil
}
