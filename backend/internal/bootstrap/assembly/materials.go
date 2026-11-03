package assembly

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/materialmcp"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
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

// materialBlobReader 把 store（附件行）+ attachmentstore（字节/路径）适配成
// svcmaterial.AttachmentReader：attachment id → upload_id → 磁盘绝对路径。
type materialBlobReader struct {
	rows  *store.Store
	blobs *attachmentstore.Store
}

func (a materialBlobReader) LocalPathByAttachmentID(ctx context.Context, attachmentID string) (string, error) {
	if a.rows == nil || a.blobs == nil {
		return "", errAttachmentsUnsupported()
	}
	row, ok, err := a.rows.AttachmentByID(ctx, attachmentID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", code.Errorf(code.NotFound, "attachment_not_found: 附件 %s 不存在", attachmentID)
	}
	return a.blobs.ReadPath(ctx, row.UploadID)
}

// buildMaterialSvc 构造资料库用例。memory 模式（Store 为 nil）时 NodeStore 交 nil，
// 用例内 requireStore 报 40501；上传适配器包住已构造的附件用例。
func (f Facts) buildMaterialSvc(attach svcattachment.AttachmentSvc) svcmaterial.MaterialSvc {
	var nodeStore svcmaterial.NodeStore
	if f.Store != nil {
		nodeStore = f.Store
	}
	return svcmaterial.New(nodeStore, materialUploadAdapter{attach: attach}, materialBlobReader{rows: f.Store, blobs: f.AttachStore}, pagePublisher{root: f.Cfg.HomeDir})
}

// 断言：*store.Store 满足 svcmaterial.NodeStore（编译期钉住端口形状漂移）。
var _ svcmaterial.NodeStore = (*store.Store)(nil)

// 断言：materialBlobReader 满足 svcmaterial.AttachmentReader。
var _ svcmaterial.AttachmentReader = materialBlobReader{}

// 确保 model 包被引用（NodeStore 端口签名用到 model 类型；此文件仅在需要时用）。
var _ = model.LibraryNodeEntity{}

// ——— `mate mcp material` 的装配入口（CLI 维护面用它，避免 maintenance 直接 import 用例层）———
//
// maintenance 与 materialmcp 都不允许 import `svc*`（层规：只有装配根与控制器可以）。所以
// 在这里（装配根）构造 svcmaterial，并把它包成 materialmcp.Service 交给 Handler。CLI 只拿
// 一个 `mcp.Handler` + 一个关闭函数，对用例层零认识。

// MaterialMCPHandler 以给定配置构造资料库 MCP 工具面的 Handler（含它依赖的库/字节存储/用例）。
//
// 返回的 cleanup 负责关库；调用方（`mate mcp material`）在进程退出前调用它。
func MaterialMCPHandler(cfg config.Config) (mcp.Handler, func() error, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if err := attachmentstore.Migrate(st.DB()); err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	blobs, err := attachmentstore.New(st.DB(), attachmentstore.Options{
		BaseDir:        filepath.Join(cfg.HomeDir, "attachments"),
		MaxUploadBytes: cfg.AttachmentMaxBytes,
	})
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	attachSvc := svcattachment.New(st, assemblyBlobStore{st: blobs}, svcattachment.Limits{MaxBytes: cfg.AttachmentMaxBytes})
	svc := svcmaterial.New(st, assemblyUploadAdapter{attach: attachSvc}, materialBlobReader{rows: st, blobs: blobs}, pagePublisher{root: cfg.HomeDir})
	return materialmcp.New(svc), func() error { return st.Close() }, nil
}

// assemblyBlobStore 把 *attachmentstore.Store 适配成 svcattachment.BlobStore（翻译 filestore 类型）。
// 与 materials.go 的 attachmentBlobAdapter 同形，但这里给 CLI 装配入口用（assembly 内部）。
type assemblyBlobStore struct {
	st *attachmentstore.Store
}

func (a assemblyBlobStore) UploadAndRecord(ctx context.Context, req svcattachment.BlobUploadRequest) (svcattachment.BlobDetail, error) {
	detail, err := a.st.UploadAndRecord(ctx, attachmentstore.UploadAndRecordRequest{
		ContentHash: req.ContentHash, Name: req.Name, Size: req.Size,
		MimeType: req.MimeType, Reader: req.Reader, Scene: req.Scene,
	})
	if err != nil {
		return svcattachment.BlobDetail{}, err
	}
	return svcattachment.BlobDetail{FileUploadID: detail.FileUploadID, ContentHash: detail.ContentHash, Size: detail.Size, MimeType: detail.MimeType}, nil
}

func (a assemblyBlobStore) Open(ctx context.Context, uploadID string) (io.ReadCloser, svcattachment.BlobDetail, error) {
	rc, detail, err := a.st.Open(ctx, uploadID)
	if err != nil {
		return nil, svcattachment.BlobDetail{}, err
	}
	return rc, svcattachment.BlobDetail{FileUploadID: detail.FileUploadID, ContentHash: detail.ContentHash, Size: detail.Size, MimeType: detail.MimeType}, nil
}

func (a assemblyBlobStore) GetFile(ctx context.Context, uploadID string) (svcattachment.BlobDetail, error) {
	detail, err := a.st.GetFile(ctx, uploadID)
	if err != nil {
		return svcattachment.BlobDetail{}, err
	}
	return svcattachment.BlobDetail{FileUploadID: detail.FileUploadID, ContentHash: detail.ContentHash, Size: detail.Size, MimeType: detail.MimeType, Name: detail.Name}, nil
}

func (a assemblyBlobStore) DeleteFile(ctx context.Context, uploadID string) error {
	return a.st.DeleteFile(ctx, uploadID)
}

// assemblyUploadAdapter 把 svcattachment 的上传投影成 svcmaterial.UploadedAttachment
// （materials.go 里的 materialUploadAdapter 已存在同名；这里区别命名避免重复）。
type assemblyUploadAdapter struct {
	attach svcattachment.AttachmentSvc
}

func (a assemblyUploadAdapter) Upload(ctx context.Context, fileName, declaredName, declaredMime string, r io.Reader) (svcmaterial.UploadedAttachment, error) {
	resp, err := a.attach.Upload(ctx, fileName, declaredName, declaredMime, r)
	if err != nil {
		return svcmaterial.UploadedAttachment{}, err
	}
	return svcmaterial.UploadedAttachment{ID: resp.ID, Name: resp.Name, MimeType: resp.MimeType, SizeBytes: resp.SizeBytes}, nil
}

var _ svcattachment.BlobStore = assemblyBlobStore{}

// pagePublisher 把 page 节点的 HTML 落盘到 `<root>/material_pages/<nodeID>/index.html`（本机可打开）。
//
// 只绑本机（不进公网语义）：`view` 由控制器回环读 body；落盘文件只是「用户能直接双击打开」的便利。
type pagePublisher struct {
	root string
}

func (p pagePublisher) WritePage(_ context.Context, nodeID, html string) (string, error) {
	dir := filepath.Join(p.root, "material_pages", nodeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	abs := filepath.Join(dir, "index.html")
	if err := os.WriteFile(abs, []byte(html), 0o644); err != nil {
		return "", err
	}
	return abs, nil
}

var _ svcmaterial.PagePublisher = pagePublisher{}
