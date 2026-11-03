package material

import "errors"

// ErrKindMismatch 节点种类与载体列不匹配（写面校验失败 → 40001 material_node_kind_mismatch）。
var ErrKindMismatch = errors.New("material: node kind does not match its carrier")

// Carrier 是一条节点记录里与「载体」相关的列（只取判断一致性所需的字段，避免 domain 依赖
// persistence/model 的完整实体）。
type Carrier struct {
	Body          string
	TableID       string
	DriveUploadID string
	SourceURL     string
}

// ValidateCarrier 校验「节点种类 ↔ 载体列」的一致性（一列一义纪律的机器判据之一，M-4）。
//
// 规则（与设计 §4.1 表一致）：
//   - folder：三种载体列都应为空（纯容器）；
//   - doc：Body 非空，TableID/DriveUploadID 为空；
//   - table：TableID 非空，Body/DriveUploadID 为空；
//   - link：Body 非空（快照 md），SourceURL 非空，TableID/DriveUploadID 为空；
//   - drive：DriveUploadID 非空，Body/TableID 为空；
//   - page：Body 非空（HTML），TableID/DriveUploadID 为空。
//
// 未知种类直接判为 mismatch（fail-closed：种类不在封闭集合内，写面本应先拦，这里再兜一道）。
func ValidateCarrier(k NodeKind, c Carrier) error {
	hasBody := c.Body != ""
	hasTable := c.TableID != ""
	hasDrive := c.DriveUploadID != ""
	hasURL := c.SourceURL != ""

	switch k {
	case KindFolder:
		if hasBody || hasTable || hasDrive {
			return ErrKindMismatch
		}
	case KindDoc:
		if !hasBody || hasTable || hasDrive {
			return ErrKindMismatch
		}
	case KindTable:
		if !hasTable || hasBody || hasDrive {
			return ErrKindMismatch
		}
	case KindLink:
		if !hasBody || !hasURL || hasTable || hasDrive {
			return ErrKindMismatch
		}
	case KindDrive:
		if !hasDrive || hasBody || hasTable {
			return ErrKindMismatch
		}
	case KindPage:
		if !hasBody || hasTable || hasDrive {
			return ErrKindMismatch
		}
	default:
		return ErrKindMismatch
	}
	return nil
}
