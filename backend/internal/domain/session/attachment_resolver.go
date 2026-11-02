package session

import "context"

// ——— 附件解析窄端口（v32 附件链路，P3 引入）———
//
// 为什么要有它（对齐 `store_port.go` 的窄端口纪律 + 本仓 G3）：会话域/编排层在发一轮 prompt 时
// 要把 `att_…` 解析成可投递的形态（小图 → 字节+image part；文档/大图 → 只读路径 handle），
// 但它**不该认识** `internal/persistence/dao` 或字节存储实现。把"按 id 取附件"收成一个端口，
// 由装配根把实现（dao + filestore）交下来，本层只认这个接口。
//
// **G3**：`sessionruntime` 携带附件 id 时必须经 `AttachmentResolver.Resolve` 解析，不得直接用 dao。
//
// 现判 nil 的纪律同 `store_port`：内存模式交 nil（附件面整体 40501）；调用点必须能处理 nil。

// AttachmentKind 附件的投递形态（与 `model.AttachmentKind*` 同值，但在 domain 层复述一份
// 以避免 domain import persistence/model 的具体常量约定——两值由 archtest G1/用例钉住一致）。
type AttachmentKind string

const (
	// AttachmentKindImage 图片（小图直投 image 块；大图走 handle + read_image）。
	AttachmentKindImage AttachmentKind = "image"
	// AttachmentKindDocument 文档（一律 handle 文本 + read）。
	AttachmentKindDocument AttachmentKind = "document"
)

// ResolvedAttachment 一个被解析出来的附件（投递所需的最小事实）。
type ResolvedAttachment struct {
	ID        string
	Kind      AttachmentKind
	MimeType  string
	Name      string
	SizeBytes int64
	// Sha256 内容摘要（handle 文本里用到前 16 位；不进事件）。
	Sha256 string
	// Data 仅**小图**携带（供 ACP 层编 base64）；大图/文档为 nil（走 ReadPath）。
	// 该值只在「sessionruntime → ACP」这一跳里存活，**不进事件、不落库**。
	Data []byte
	// ReadPath 本地只读绝对路径（文档/大图 handle 用）；小图可为空。
	ReadPath string
}

// AttachmentResolver 按平台附件 id 批量解析（顺序与入参一致；任一不存在/越权即整体报错，
// 由调用方 fail-closed 拒收整条 prompt——零副作用）。
type AttachmentResolver interface {
	Resolve(ctx context.Context, ids []string) ([]ResolvedAttachment, error)
}

// AttachmentMeta 附件元数据（只含可进事件的字段：id/name/mime_type/size_bytes/kind）。
//
// 它是 `session.user_message.attachments[]` 载荷的元素类型（v32 设计 §5.4）。**刻意不含**
// Data/ReadPath/内容摘要之外的任何路径**：事件是持久 transcript 的一部分，"本机绝对路径"
// 不该灌进去（同 dsh「durable session events exclude local storage paths」）。G6 钉住这一点。
type AttachmentMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Kind      string `json:"kind"`
}
