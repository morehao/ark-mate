package doctor

import (
	"context"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/persistence/store"
)

// ——— `mate doctor` 的**附件**体检（v32）———
//
// 分工：本文件只**采集只读事实**，`internal/bootstrap/maintenance` 只负责翻成三档结论。
//
// 判据（设计 §3.5）：`attachments` 有行，但
//   - `core_file_upload` 里找不到对应 upload 行（或指向的 `core_file` 缺失）→ `missing`（悬挂）；
//   - upload 行在、但磁盘 blob 文件不在 → `unreadable`（可重建性被破坏）。
//
// 只读：不建表、不写盘、不构造 writable 驱动（doctor 以 mode=ro 打开库）。因此这里用
// `attachmentstore.InspectBlob` 直接 SELECT core_file* + os.Stat blob，而不 New 一个 FileStore
// （后者的 local 驱动构造会 MkdirAll、且 Migrate 会 AutoMigrate——都是写）。

// AttachmentDoctorFacts `mate doctor --json` 的 `attachments` 段（键名稳定）。
type AttachmentDoctorFacts struct {
	// Total `attachments` 表总行数。
	Total int64 `json:"total"`
	// Pending / Linked / Orphan 按 state 分组的行数。
	Pending int64 `json:"pending"`
	Linked  int64 `json:"linked"`
	Orphan  int64 `json:"orphan"`
	// Missing `core_file_upload` 行找不到（或 core_file 缺失）的附件数。
	Missing int64 `json:"missing"`
	// Unreadable upload 行在、磁盘 blob 缺失的附件数。
	Unreadable int64 `json:"unreadable"`
	// Note 采集不到事实时的说明（库不可读等）；非空即"无法校验"。
	Note string `json:"note,omitempty"`
}

// CollectAttachmentDoctorFacts 采集附件体检事实（只读）。库不可用 → Note 并返回零值。
//
// baseDir = `<MATE_HOME>/attachments`（与运行期构造一致）。st 由调用方以 mode=ro 打开。
func CollectAttachmentDoctorFacts(ctx context.Context, st *store.Store, baseDir string) AttachmentDoctorFacts {
	var f AttachmentDoctorFacts
	if st == nil {
		f.Note = "库不可读：跳过附件体检"
		return f
	}
	rows, err := st.AllAttachments(ctx)
	if err != nil {
		f.Note = "读取 attachments 失败：" + err.Error()
		return f
	}
	f.Total = int64(len(rows))
	for _, a := range rows {
		switch a.State {
		case "pending":
			f.Pending++
		case "linked":
			f.Linked++
		case "orphan":
			f.Orphan++
		}
		blob := attachmentstore.InspectBlob(ctx, st.DB(), baseDir, a.UploadID)
		if blob.Error != nil {
			// 单行采集失败不中断整段：把它算进 unreadable 的"读不动"，但记 Note 提醒。
			f.Unreadable++
			if f.Note == "" {
				f.Note = "部分附件采集失败：" + blob.Error.Error()
			}
			continue
		}
		if !blob.UploadRowFound {
			f.Missing++
			continue
		}
		if !blob.FileFound {
			f.Unreadable++
		}
	}
	return f
}

// AttachmentBaseDir 返回附件 blob 根目录（`<home>/attachments`），与运行期构造同源。
//
// 单独成函数是为了让 doctor 的采集点与装配根用**同一个**派生式；两边各手抄一份会在某次改名时
// 静默失配（doctor 指向空目录 → 全报 unreadable）。
func AttachmentBaseDir(home string) string { return filepath.Join(home, "attachments") }
