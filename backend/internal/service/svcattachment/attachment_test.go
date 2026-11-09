package svcattachment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/morehao/golib/gerror"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// fakeStore 内存版 AttachmentStore（按 id 存）。
type fakeStore struct {
	rows map[string]model.AttachmentEntity
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]model.AttachmentEntity{}} }

func (f *fakeStore) InsertAttachment(_ context.Context, a model.AttachmentEntity) error {
	f.rows[a.ID] = a
	return nil
}
func (f *fakeStore) AttachmentByID(_ context.Context, id string) (model.AttachmentEntity, bool, error) {
	a, ok := f.rows[id]
	return a, ok, nil
}
func (f *fakeStore) DeleteAttachmentRow(_ context.Context, id string) error {
	delete(f.rows, id)
	return nil
}

// fakeBlob 内存版 BlobStore（按 uploadID 存字节 + 上传时声明的 MIME）。
type fakeBlob struct {
	data   map[string][]byte
	mime   map[string]string
	path   map[string]string // ReadPath 用：uploadID → 磁盘路径（测试里给假的即可）
	nextID int
}

func newFakeBlob() *fakeBlob {
	return &fakeBlob{data: map[string][]byte{}, mime: map[string]string{}, path: map[string]string{}}
}

func (f *fakeBlob) UploadAndRecord(_ context.Context, req BlobUploadRequest) (BlobDetail, error) {
	b, err := io.ReadAll(req.Reader)
	if err != nil {
		return BlobDetail{}, err
	}
	f.nextID++
	id := "upl_" + string(rune('0'+f.nextID))
	f.data[id] = b
	f.mime[id] = req.MimeType
	return BlobDetail{FileUploadID: id, ContentHash: req.ContentHash, Size: int64(len(b)), MimeType: req.MimeType, Name: req.Name}, nil
}
func (f *fakeBlob) Open(_ context.Context, uploadID string) (io.ReadCloser, BlobDetail, error) {
	b, ok := f.data[uploadID]
	if !ok {
		return nil, BlobDetail{}, io.EOF
	}
	return io.NopCloser(bytes.NewReader(b)), BlobDetail{FileUploadID: uploadID, Size: int64(len(b)), MimeType: f.mime[uploadID]}, nil
}
func (f *fakeBlob) GetFile(_ context.Context, uploadID string) (BlobDetail, error) {
	b, ok := f.data[uploadID]
	if !ok {
		return BlobDetail{}, io.EOF
	}
	return BlobDetail{FileUploadID: uploadID, Size: int64(len(b)), MimeType: f.mime[uploadID]}, nil
}
func (f *fakeBlob) DeleteFile(_ context.Context, uploadID string) error {
	delete(f.data, uploadID)
	return nil
}

// ReadPath 测试用：按 uploadID 返回预置的假路径（未预置则回落到 id 本身）。
func (f *fakeBlob) ReadPath(_ context.Context, uploadID string) (string, error) {
	if p, ok := f.path[uploadID]; ok {
		return p, nil
	}
	return "/fake/blob/" + uploadID, nil
}

func codeOf(t *testing.T, err error) int {
	t.Helper()
	var ge gerror.Error
	if !errors.As(err, &ge) {
		t.Fatalf("不是带码错误：%v", err)
	}
	return ge.Code
}

// pngMagic 一个最小 PNG 头（足以通过 magic bytes 判定）。
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0}

func newSvc() (AttachmentSvc, *fakeStore, *fakeBlob) {
	st, bl := newFakeStore(), newFakeBlob()
	return New(st, bl, nil, Limits{MaxBytes: 20 << 20}), st, bl
}

func TestUploadPNGKindImage(t *testing.T) {
	svc, _, _ := newSvc()
	resp, err := svc.Upload(context.Background(), "a.png", "", "image/png", bytes.NewReader(pngMagic))
	if err != nil {
		t.Fatalf("上传 PNG 失败：%v", err)
	}
	if resp.Kind != model.AttachmentKindImage {
		t.Fatalf("PNG 应 kind=image，got %q", resp.Kind)
	}
	if resp.MimeType != "image/png" {
		t.Fatalf("mime 应 image/png，got %q", resp.MimeType)
	}
	if resp.State != model.AttachmentStatePending {
		t.Fatalf("state 应 pending，got %q", resp.State)
	}
}

func TestUploadTxtKindDocument(t *testing.T) {
	svc, _, _ := newSvc()
	resp, err := svc.Upload(context.Background(), "a.txt", "", "text/plain", strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("上传 txt 失败：%v", err)
	}
	if resp.Kind != model.AttachmentKindDocument {
		t.Fatalf("txt 应 kind=document，got %q", resp.Kind)
	}
}

func TestUploadTooLarge(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	svc := New(st, bl, nil, Limits{MaxBytes: 4})
	_, err := svc.Upload(context.Background(), "a.txt", "", "text/plain", strings.NewReader("way too large content"))
	if err == nil || codeOf(t, err) != 40001 {
		t.Fatalf("超上限应 40001，got %v", err)
	}
	if !strings.Contains(err.Error(), "attachment_too_large") {
		t.Fatalf("文案应含 attachment_too_large：%v", err)
	}
}

func TestUploadEmpty(t *testing.T) {
	svc, _, _ := newSvc()
	_, err := svc.Upload(context.Background(), "a.txt", "", "text/plain", bytes.NewReader(nil))
	if err == nil || codeOf(t, err) != 40001 || !strings.Contains(err.Error(), "attachment_empty") {
		t.Fatalf("空文件应 40001 attachment_empty，got %v", err)
	}
}

func TestUploadFakeImageRejected(t *testing.T) {
	svc, _, _ := newSvc()
	// 声明是 image/png，但字节是随机数据。
	_, err := svc.Upload(context.Background(), "fake.png", "", "image/png", bytes.NewReader([]byte("not a png at all")))
	if err == nil || codeOf(t, err) != 40001 || !strings.Contains(err.Error(), "attachment_unsupported_type") {
		t.Fatalf("伪装图应 40001 attachment_unsupported_type，got %v", err)
	}
}

// bigPNG 造一个「magic 正确、体积远超 512 字节」的 PNG 字节。
// 回归用：Register 只读前 512 字节判魔数，**不能**因为文件比前缀长就当成伪装图。
func bigPNG() []byte {
	return append(append([]byte{}, pngMagic...), bytes.Repeat([]byte{0x00}, 5000)...)
}

// TestRegisterLargeImageKindImage 钉住回归：>512 字节的真图（真实截图就是这种）必须能建索引。
func TestRegisterLargeImageKindImage(t *testing.T) {
	svc, _, bl := newSvc()
	data := bigPNG()
	detail, err := bl.UploadAndRecord(context.Background(), BlobUploadRequest{
		Name: "样机 (37).png", Size: int64(len(data)), MimeType: "image/png", Reader: bytes.NewReader(data),
	})
	if err != nil {
		t.Fatalf("落字节失败：%v", err)
	}
	resp, err := svc.Register(context.Background(), detail.FileUploadID, "样机 (37).png")
	if err != nil {
		t.Fatalf(">512 字节的真图应能建索引，got %v", err)
	}
	if resp.Kind != model.AttachmentKindImage || resp.MimeType != "image/png" {
		t.Fatalf("应 kind=image/mime=image/png，got kind=%q mime=%q", resp.Kind, resp.MimeType)
	}
	if resp.SizeBytes != int64(len(data)) {
		t.Fatalf("size 应 %d，got %d", len(data), resp.SizeBytes)
	}
}

// TestRegisterFakeImageLargeRejected 大文件同样要能判出伪装图（别为了修 bug 把门禁放宽）。
func TestRegisterFakeImageLargeRejected(t *testing.T) {
	svc, _, bl := newSvc()
	data := bytes.Repeat([]byte("not a png at all "), 40) // 680 字节，声明 image/png
	detail, err := bl.UploadAndRecord(context.Background(), BlobUploadRequest{
		Name: "fake.png", Size: int64(len(data)), MimeType: "image/png", Reader: bytes.NewReader(data),
	})
	if err != nil {
		t.Fatalf("落字节失败：%v", err)
	}
	_, err = svc.Register(context.Background(), detail.FileUploadID, "fake.png")
	if err == nil || codeOf(t, err) != 40001 || !strings.Contains(err.Error(), "attachment_unsupported_type") {
		t.Fatalf("伪装图应 40001 attachment_unsupported_type，got %v", err)
	}
}

func TestDeletePendingAndLinked(t *testing.T) {
	svc, st, _ := newSvc()
	resp, err := svc.Upload(context.Background(), "a.txt", "", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	// pending → 成功删。
	del, err := svc.Delete(context.Background(), resp.ID)
	if err != nil || !del.Deleted {
		t.Fatalf("删 pending 应成功：%v %+v", err, del)
	}
	// 再删（幂等）：deleted=false 且无错。
	del, err = svc.Delete(context.Background(), resp.ID)
	if err != nil || del.Deleted {
		t.Fatalf("重复删应幂等 deleted=false：%v %+v", err, del)
	}
	// linked → 40901 attachment_linked。
	resp2, _ := svc.Upload(context.Background(), "b.txt", "", "text/plain", strings.NewReader("y"))
	row := st.rows[resp2.ID]
	row.State = model.AttachmentStateLinked
	st.rows[resp2.ID] = row
	_, err = svc.Delete(context.Background(), resp2.ID)
	if err == nil || codeOf(t, err) != 40901 || !strings.Contains(err.Error(), "attachment_linked") {
		t.Fatalf("删 linked 应 40901 attachment_linked，got %v", err)
	}
}

// ——— ConvertToPDF（office 预览转 PDF）———

// fakeConverter 可控的 OfficeConverter。
type fakeConverter struct {
	available bool
	pdf       []byte
	err       error
	gotPath   string
	gotName   string
}

func (f *fakeConverter) Available() bool { return f.available }
func (f *fakeConverter) ConvertFile(_ context.Context, inputPath, name string) ([]byte, error) {
	f.gotPath, f.gotName = inputPath, name
	if f.err != nil {
		return nil, f.err
	}
	return f.pdf, nil
}

// 预置一条已落字节的 document 上传（返回其 uploadID）。
func seedDoc(t *testing.T, bl *fakeBlob) string {
	t.Helper()
	detail, err := bl.UploadAndRecord(context.Background(), BlobUploadRequest{
		ContentHash: "h", Name: "xian-weather.docx", Size: 4, MimeType: "application/octet-stream",
		Reader: strings.NewReader("DOCX"),
	})
	if err != nil {
		t.Fatalf("预置字节失败：%v", err)
	}
	return detail.FileUploadID
}

func TestConvertToPDFSuccess(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	conv := &fakeConverter{available: true, pdf: []byte("%PDF-1.7 fake")}
	svc := New(st, bl, conv, Limits{MaxBytes: 20 << 20})
	id := seedDoc(t, bl)

	pdf, err := svc.ConvertToPDF(context.Background(), id, "xian-weather.docx")
	if err != nil {
		t.Fatalf("转换应成功：%v", err)
	}
	if string(pdf) != "%PDF-1.7 fake" {
		t.Fatalf("应返回转换器产出的 PDF 字节，got %q", pdf)
	}
	if conv.gotName != "xian-weather.docx" {
		t.Fatalf("应把文件名传给转换器（推导后缀），got %q", conv.gotName)
	}
}

func TestConvertToPDFUnavailableDowngrades(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	// 转换器「已构造但不可用」（随包 payload 缺失）→ 明确降级码 40501，而非 500。
	conv := &fakeConverter{available: false}
	svc := New(st, bl, conv, Limits{MaxBytes: 20 << 20})
	id := seedDoc(t, bl)

	_, err := svc.ConvertToPDF(context.Background(), id, "x.docx")
	if err == nil || codeOf(t, err) != 40501 {
		t.Fatalf("转换器不可用应 40501，got %v", err)
	}
	if !strings.Contains(err.Error(), "office_preview_unsupported") {
		t.Fatalf("文案应含 office_preview_unsupported：%v", err)
	}
}

func TestConvertToPDFNilConverterDowngrades(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	svc := New(st, bl, nil, Limits{MaxBytes: 20 << 20})
	id := seedDoc(t, bl)
	_, err := svc.ConvertToPDF(context.Background(), id, "x.docx")
	if err == nil || codeOf(t, err) != 40501 {
		t.Fatalf("converter=nil 应 40501，got %v", err)
	}
}

func TestConvertToPDFNotFound(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	conv := &fakeConverter{available: true, pdf: []byte("%PDF-1.7")}
	svc := New(st, bl, conv, Limits{MaxBytes: 20 << 20})
	_, err := svc.ConvertToPDF(context.Background(), "nope", "x.docx")
	if err == nil || codeOf(t, err) != 40401 || !strings.Contains(err.Error(), "attachment_not_found") {
		t.Fatalf("文件不存在应 40401 attachment_not_found，got %v", err)
	}
}

func TestConvertToPDFConvertErrorUpstream(t *testing.T) {
	st, bl := newFakeStore(), newFakeBlob()
	conv := &fakeConverter{available: true, err: errors.New("boom")}
	svc := New(st, bl, conv, Limits{MaxBytes: 20 << 20})
	id := seedDoc(t, bl)
	_, err := svc.ConvertToPDF(context.Background(), id, "x.docx")
	if err == nil || codeOf(t, err) != 50001 || !strings.Contains(err.Error(), "office_preview_failed") {
		t.Fatalf("转换失败应 50001 office_preview_failed，got %v", err)
	}
}
