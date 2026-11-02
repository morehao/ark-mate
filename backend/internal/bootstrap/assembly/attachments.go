package assembly

import (
	"context"
	"io"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/service/svcattachment"
)

// 附件面在装配根的接线（v32）。
//
// 三条边界：
//   - 用例层（svcattachment）不 import store / infra（层表）；它只认两个窄端口
//     `AttachmentStore`（= *store.Store 结构上满足）与 `BlobStore`（= *attachmentstore.Store 经本适配器）。
//   - memory 模式没有库、也没有字节存储：AttachStore 为 nil 时交下去的是 **nil-safe 适配器**，
//     调用即 40501，而不是 typed-nil（typed-nil 装进接口不是 nil，会让"附件面不支持"变成运行期 panic）。
//   - 字节层（*attachmentstore.Store）返回 filestore 类型，用例层认本域值 → 在**这一层**翻译。

// attachmentBlobAdapter 把 *attachmentstore.Store 适配成 svcattachment.BlobStore。
type attachmentBlobAdapter struct {
	st *attachmentstore.Store
}

// errAttachmentsUnsupported memory 模式下附件面不可用（无库无字节存储）。
func errAttachmentsUnsupported() error {
	return code.Errorf(code.NotSupported, "attachments 需要 sqlite 持久层（memory 模式不支持附件）")
}

func (a attachmentBlobAdapter) UploadAndRecord(ctx context.Context, req svcattachment.BlobUploadRequest) (svcattachment.BlobDetail, error) {
	if a.st == nil {
		return svcattachment.BlobDetail{}, errAttachmentsUnsupported()
	}
	detail, err := a.st.UploadAndRecord(ctx, attachmentstore.UploadAndRecordRequest{
		ContentHash: req.ContentHash,
		Name:        req.Name,
		Size:        req.Size,
		MimeType:    req.MimeType,
		Reader:      req.Reader,
		Scene:       req.Scene,
	})
	if err != nil {
		return svcattachment.BlobDetail{}, err
	}
	return svcattachment.BlobDetail{
		FileUploadID: detail.FileUploadID,
		ContentHash:  detail.ContentHash,
		Size:         detail.Size,
		MimeType:     detail.MimeType,
	}, nil
}

func (a attachmentBlobAdapter) Open(ctx context.Context, uploadID string) (io.ReadCloser, svcattachment.BlobDetail, error) {
	if a.st == nil {
		return nil, svcattachment.BlobDetail{}, errAttachmentsUnsupported()
	}
	rc, detail, err := a.st.Open(ctx, uploadID)
	if err != nil {
		return nil, svcattachment.BlobDetail{}, err
	}
	return rc, svcattachment.BlobDetail{
		FileUploadID: detail.FileUploadID,
		ContentHash:  detail.ContentHash,
		Size:         detail.Size,
		MimeType:     detail.MimeType,
	}, nil
}

func (a attachmentBlobAdapter) DeleteFile(ctx context.Context, uploadID string) error {
	if a.st == nil {
		return errAttachmentsUnsupported()
	}
	return a.st.DeleteFile(ctx, uploadID)
}

// buildAttachmentSvc 构造附件用例。AttachStore 为 nil（memory 模式）时交 nil-safe 适配器；
// AttachmentStore 端口在 memory 模式同样没有实现——用一个 nil-safe 的 wrapper 包住 *store.Store，
// 若为 nil 则插入/读取即 40501。
func (f Facts) buildAttachmentSvc() svcattachment.AttachmentSvc {
	limits := svcattachment.Limits{MaxBytes: f.Cfg.AttachmentMaxBytes}
	var rowStore svcattachment.AttachmentStore
	if f.Store != nil {
		rowStore = f.Store
	} else {
		rowStore = unsupportedAttachmentStore{}
	}
	return svcattachment.New(rowStore, attachmentBlobAdapter{st: f.AttachStore}, limits)
}

// unsupportedAttachmentStore memory 模式下的空实现（调用即 40501）。
type unsupportedAttachmentStore struct{}

func (unsupportedAttachmentStore) InsertAttachment(context.Context, model.AttachmentEntity) error {
	return errAttachmentsUnsupported()
}

func (unsupportedAttachmentStore) AttachmentByID(context.Context, string) (model.AttachmentEntity, bool, error) {
	return model.AttachmentEntity{}, false, errAttachmentsUnsupported()
}

func (unsupportedAttachmentStore) DeleteAttachmentRow(context.Context, string) error {
	return errAttachmentsUnsupported()
}
