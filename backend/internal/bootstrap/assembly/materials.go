package assembly

import (
	"context"
	"io"

	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcattachment"
	"github.com/morehao/ark-mate/internal/service/svcmaterial"
)

// 资料库面在装配根的接线（v33）。
//
// 三处适配（用例层不 import store/infra）：
//   - *store.Store   → svcmaterial.NodeStore（结构上基本满足，nil 时交 nil：用例内 requireStore 报 40501）；
//   - svcattachment  → svcmaterial.AttachmentUploader（把 *dtoattachment.AttachmentResp 投影成 UploadedAttachment）；
//   - *attachmentstore.Store → svcmaterial.AttachmentReader（ReadPath；memory 模式 nil）。

// materialUploadAdapter 把附件用例的上传投影成资料库认的最小返回值。
type materialUploadAdapter struct {
	attach svcattachment.AttachmentSvc
}

func (a materialUploadAdapter) Upload(ctx context.Context, fileName, declaredName, declaredMime string, r io.Reader) (svcmaterial.UploadedAttachment, error) {
	if a.attach == nil {
		return svcmaterial.UploadedAttachment{}, errAttachmentsUnsupported()
	}
	resp, err := a.attach.Upload(ctx, fileName, declaredName, declaredMime, r)
	if err != nil {
		return svcmaterial.UploadedAttachment{}, err
	}
	return svcmaterial.UploadedAttachment{
		ID:        resp.ID,
		Name:      resp.Name,
		MimeType:  resp.MimeType,
		SizeBytes: resp.SizeBytes,
	}, nil
}

// materialBlobReader 把 *attachmentstore.Store 适配成 svcmaterial.AttachmentReader（ReadPath）。
type materialBlobReader struct {
	st *attachmentstore.Store
}

func (a materialBlobReader) ReadPath(ctx context.Context, uploadID string) (string, error) {
	if a.st == nil {
		return "", errAttachmentsUnsupported()
	}
	return a.st.ReadPath(ctx, uploadID)
}

// buildMaterialSvc 构造资料库用例。memory 模式（Store 为 nil）时 NodeStore 交 nil，
// 用例内 requireStore 报 40501；上传适配器包住已构造的附件用例。
func (f Facts) buildMaterialSvc(attach svcattachment.AttachmentSvc) svcmaterial.MaterialSvc {
	var nodeStore svcmaterial.NodeStore
	if f.Store != nil {
		nodeStore = f.Store
	}
	return svcmaterial.New(nodeStore, materialUploadAdapter{attach: attach}, materialBlobReader{st: f.AttachStore})
}

// 断言：*store.Store 满足 svcmaterial.NodeStore（编译期钉住端口形状漂移）。
var _ svcmaterial.NodeStore = (*store.Store)(nil)

// 断言：*attachmentstore.Store 满足 svcmaterial.AttachmentReader。
var _ svcmaterial.AttachmentReader = (*attachmentstore.Store)(nil)

// 确保 model 包被引用（NodeStore 端口签名用到 model 类型；此文件仅在需要时用）。
var _ = model.LibraryNodeEntity{}
