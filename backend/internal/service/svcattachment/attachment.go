// Package svcattachment 承载附件面的用例：上传（multipart → 类型探测 → 落字节 + 落业务索引行）、
// 回读元数据、字节流回读（预览）、撤下待挂附件。
//
// 两条硬边界（设计 §3.1 / §4.3）：
//   - 字节落盘/去重归 golib filestore（经 BlobStore 端口），本包不碰磁盘布局；
//   - 业务索引（归属/kind/state/生命周期）归 store（经 AttachmentStore 端口），本包不 import store/dao
//     （层表：service L4 不得 import persistence/store·dao）。
package svcattachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/dto/dtoattachment"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// AttachmentStore 附件业务索引的**窄端口**（v3.1 P4：用例层不 import store）。
// 由 *store.Store 结构上满足，装配根交下来时**现判 nil**。
type AttachmentStore interface {
	InsertAttachment(ctx context.Context, a model.AttachmentEntity) error
	AttachmentByID(ctx context.Context, id string) (model.AttachmentEntity, bool, error)
	DeleteAttachmentRow(ctx context.Context, id string) error
}

// BlobStore 附件字节存储的**窄端口**（实现在 internal/infra/attachmentstore，内部持 golib filestore）。
type BlobStore interface {
	UploadAndRecord(ctx context.Context, req BlobUploadRequest) (BlobDetail, error)
	Open(ctx context.Context, uploadID string) (io.ReadCloser, BlobDetail, error)
	// GetFile 按 upload_id 取元数据（供 Register 从已落字节建索引，不重读字节）。
	GetFile(ctx context.Context, uploadID string) (BlobDetail, error)
	DeleteFile(ctx context.Context, uploadID string) error
}

// BlobUploadRequest 写字节的入参（与 svcattachment 解耦 filestore 的具体类型）。
type BlobUploadRequest struct {
	ContentHash string
	Name        string
	Size        int64
	MimeType    string
	Reader      io.Reader
	Scene       string
}

// BlobDetail 字节写入/回流的元数据（只取本域需要的字段）。
type BlobDetail struct {
	FileUploadID string // = core_file_upload.id（attachments.upload_id 指向它）
	ContentHash  string
	Size         int64
	MimeType     string
	// Name 上传时的原始文件名（Register 用它兜底展示名）。
	Name string
}

// Limits 单文件上限（防呆/防灌盘，**不是类型约束**）。
type Limits struct {
	// MaxBytes 单附件上限（默认 20 MiB，MATE_ATTACHMENT_MAX_BYTES）。
	MaxBytes int64
}

// AttachmentSvc 附件面用例。
type AttachmentSvc interface {
	// Upload 上传一个文件（multipart 单文件）：探测真实类型 → 落字节 + 落一条 pending 行。
	//
	// **注意**：本方法是**字节收口前**的进程内上传能力，产物溯源 P0′ 后 **HTTP `POST /attachments`
	// 不再走它**（改走 `POST /files` 收字节 + `Register` 建索引）。现在只给**进程内**消费者
	// （MCP `library_upload` 经 svcmaterial.Upload）用——它不经过本机回环 HTTP。
	Upload(ctx context.Context, fileName, declaredName, declaredMime string, r io.Reader) (*dtoattachment.AttachmentResp, error)
	// Register 从**已落 filestore 的字节**建一条附件业务索引（收口后的 HTTP `POST /attachments` 用它）：
	// 按 fileID 取 FileDetail → 探测真实类型（伪装图在此拒，字节已落但不入索引）→ 落 pending 行。
	Register(ctx context.Context, fileID, declaredName string) (*dtoattachment.AttachmentResp, error)
	// Delete 撤下一条待挂附件；linked → attachment_linked(40901)；不存在 → deleted=false（幂等）。
	Delete(ctx context.Context, id string) (*dtoattachment.AttachmentDeleteResp, error)
	// Get 回读元数据。
	Get(ctx context.Context, id string) (*dtoattachment.AttachmentResp, error)
	// OpenContent 回读字节流（预览）。
	OpenContent(ctx context.Context, id string) (io.ReadCloser, string, error)
}

type attachmentSvc struct {
	st     AttachmentStore
	blobs  BlobStore
	limits Limits
}

// New 构造附件用例。st / blobs 由装配根交下来（sqlite 模式才有；memory 模式为 nil）。
func New(st AttachmentStore, blobs BlobStore, limits Limits) AttachmentSvc {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 20 << 20
	}
	return &attachmentSvc{st: st, blobs: blobs, limits: limits}
}

// image MIME 白名单（这四个是 dsh image block 与 promptCapabilities.image 共同支持的集合）。
var imageMimeWhitelist = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// Upload 上传一个文件。
//
// 顺序（fail-closed）：读全字节到内存（受 MaxBytes 约束，边读边计数超限即拒）→ 判空 → 真实类型探测
// （magic bytes；图片严格白名单，文档不设白名单）→ 服务端算 sha256 → filestore 落字节并去重 →
// 落一条 attachments{pending} 行。
//
// **文件字节在内存里过一遍**：首版单文件 ≤ 20 MiB，内存可承受；换来的是"先判类型再落盘"（伪装图不落 blob）。
// 若将来上限放大，改成 spool 到临时文件再判。
func (s *attachmentSvc) Upload(ctx context.Context, fileName, declaredName, declaredMime string, r io.Reader) (*dtoattachment.AttachmentResp, error) {
	// 1) 读全字节（边读边计数，超上限即停，避免无边界占用内存/磁盘）。
	data, err := readAllLimited(r, s.limits.MaxBytes)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return nil, code.Errorf(code.InvalidRequest, "attachment_too_large: 文件超过上限 %d 字节", s.limits.MaxBytes)
		}
		return nil, code.Errorf(code.Internal, "读取上传文件失败: %v", err)
	}
	// 2) 空文件拒。
	if len(data) == 0 {
		return nil, code.Errorf(code.InvalidRequest, "attachment_empty: 空文件不可上传")
	}
	// 3) 真实类型探测（不信客户端声明）。图片严格白名单，文档不设白名单。
	kind, probedMime := probeType(data, declaredMime)
	if kind == "invalid_image" {
		return nil, code.Errorf(code.InvalidRequest,
			"attachment_unsupported_type: 图片必须是 png/jpeg/webp/gif 之一（按文件头判定，声明=%q）", declaredMime)
	}
	// 4) 文件名：展示用；缺省回落到上传字段名。
	name := sanitizeName(declaredName)
	if name == "" {
		name = sanitizeName(fileName)
	}
	if name == "" {
		name = "attachment"
	}
	// 5) 服务端算 sha256（去重键；也是 handle 里的摘要）。
	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:])

	// 6) 落字节（filestore 边写边校验哈希、按内容去重）。
	detail, err := s.blobs.UploadAndRecord(ctx, BlobUploadRequest{
		ContentHash: contentHash,
		Name:        name,
		Size:        int64(len(data)),
		MimeType:    probedMime,
		Reader:      strings.NewReader(string(data)),
	})
	if err != nil {
		return nil, code.Errorf(code.Internal, "保存附件字节失败: %v", err)
	}
	// 7) 落一条 pending 业务索引行。
	entity := model.AttachmentEntity{
		ID:        objid.New(objid.Attachment),
		UploadID:  detail.FileUploadID,
		SessionID: "",
		Name:      name,
		MimeType:  probedMime,
		SizeBytes: detail.Size,
		Sha256:    detail.ContentHash,
		Kind:      kind,
		State:     model.AttachmentStatePending,
		CreatedAt: model.NowTSTime(),
	}
	if err := s.st.InsertAttachment(ctx, entity); err != nil {
		// 行落不了 → 回滚已写字节（尽力；删记录，blob 是否真删由 filestore 去重决定）。
		_ = s.blobs.DeleteFile(ctx, detail.FileUploadID)
		return nil, code.Errorf(code.Internal, "登记附件失败: %v", err)
	}
	return toResp(entity), nil
}

// Register 从已落 filestore 的字节建附件业务索引（收口后的 HTTP `POST /attachments {file_id}`）。
//
// 顺序（fail-closed）：按 fileID 取 FileDetail（不存在 → attachment_not_found）→ 打开字节探测真实类型
// （伪装图在此按 magic 拒：attachment_unsupported_type；字节已落 filestore，但不入索引）→ 落 pending 行。
// **不重算哈希**：去重与 sha256 已由 filestore 在上传时算好（ContentHash），这里直接用。
func (s *attachmentSvc) Register(ctx context.Context, fileID, declaredName string) (*dtoattachment.AttachmentResp, error) {
	if strings.TrimSpace(fileID) == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: file_id 不能为空")
	}
	detail, err := s.blobs.GetFile(ctx, fileID)
	if err != nil {
		return nil, code.Errorf(code.NotFound, "attachment_not_found: 文件 %s 不存在或不可读: %v", fileID, err)
	}
	// 打开字节判真实类型（不信上传时声明的 MIME）。只读前 512 字节判魔数：
	// 注意必须用 readHead（截断读），**不能**用 readAllLimited——后者对超限返回 (nil, errTooLarge)，
	// 会把"文件比 512 字节长"误判成"读不到文件头"，进而把任何正常大图当成伪装图拒掉。
	rc, _, err := s.blobs.Open(ctx, fileID)
	if err != nil {
		return nil, code.Errorf(code.NotFound, "attachment_not_found: 文件 %s 字节不可读: %v", fileID, err)
	}
	head, err := readHead(rc, 512)
	_ = rc.Close()
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取文件头部失败: %v", err)
	}
	kind, probedMime := probeType(head, detail.MimeType)
	if kind == "invalid_image" {
		return nil, code.Errorf(code.InvalidRequest,
			"attachment_unsupported_type: 图片必须是 png/jpeg/webp/gif 之一（按文件头判定，声明=%q）", detail.MimeType)
	}
	name := sanitizeName(declaredName)
	if name == "" {
		name = sanitizeName(detail.Name)
	}
	if name == "" {
		name = "attachment"
	}
	entity := model.AttachmentEntity{
		ID:        objid.New(objid.Attachment),
		UploadID:  detail.FileUploadID,
		SessionID: "",
		Name:      name,
		MimeType:  probedMime,
		SizeBytes: detail.Size,
		Sha256:    detail.ContentHash,
		Kind:      kind,
		State:     model.AttachmentStatePending,
		CreatedAt: model.NowTSTime(),
	}
	if err := s.st.InsertAttachment(ctx, entity); err != nil {
		return nil, code.Errorf(code.Internal, "登记附件失败: %v", err)
	}
	return toResp(entity), nil
}

// Delete 撤下待挂附件。linked → 40901 attachment_linked；不存在 → deleted=false（幂等）。
func (s *attachmentSvc) Delete(ctx context.Context, id string) (*dtoattachment.AttachmentDeleteResp, error) {
	entity, ok, err := s.st.AttachmentByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取附件失败: %v", err)
	}
	if !ok {
		return &dtoattachment.AttachmentDeleteResp{Deleted: false}, nil
	}
	if entity.State == model.AttachmentStateLinked {
		return nil, code.Errorf(code.Conflict, "attachment_linked: 已挂到某一轮的附件不可撤")
	}
	// 先删字节（记录），再删业务行；DeleteFile 对"记录已不在"幂等——这里 best-effort 忽略错误，
	// 行删掉后即使 blob 残留也由孤儿清扫/下一轮同内容上传重建兜住。
	_ = s.blobs.DeleteFile(ctx, entity.UploadID)
	if err := s.st.DeleteAttachmentRow(ctx, id); err != nil {
		return nil, code.Errorf(code.Internal, "删除附件失败: %v", err)
	}
	return &dtoattachment.AttachmentDeleteResp{Deleted: true}, nil
}

// Get 回读元数据。
func (s *attachmentSvc) Get(ctx context.Context, id string) (*dtoattachment.AttachmentResp, error) {
	entity, ok, err := s.st.AttachmentByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取附件失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "attachment_not_found: 附件不存在")
	}
	return toResp(entity), nil
}

// OpenContent 回读字节流（预览）。返回 (流, Content-Type, err)。
func (s *attachmentSvc) OpenContent(ctx context.Context, id string) (io.ReadCloser, string, error) {
	entity, ok, err := s.st.AttachmentByID(ctx, id)
	if err != nil {
		return nil, "", code.Errorf(code.Internal, "读取附件失败: %v", err)
	}
	if !ok {
		return nil, "", code.Errorf(code.NotFound, "attachment_not_found: 附件不存在")
	}
	rc, _, err := s.blobs.Open(ctx, entity.UploadID)
	if err != nil {
		return nil, "", code.Errorf(code.NotFound, "attachment_not_found: 附件字节不可读: %v", err)
	}
	return rc, entity.MimeType, nil
}

// toResp 实体 → 对外响应。
func toResp(e model.AttachmentEntity) *dtoattachment.AttachmentResp {
	return &dtoattachment.AttachmentResp{
		ID:        e.ID,
		Name:      e.Name,
		MimeType:  e.MimeType,
		SizeBytes: e.SizeBytes,
		Sha256:    e.Sha256,
		Kind:      e.Kind,
		State:     e.State,
		CreatedAt: model.FormatTime(e.CreatedAt),
	}
}
