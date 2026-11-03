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

// fakeBlob 内存版 BlobStore（按 uploadID 存字节）。
type fakeBlob struct {
	data   map[string][]byte
	nextID int
}

func newFakeBlob() *fakeBlob { return &fakeBlob{data: map[string][]byte{}} }

func (f *fakeBlob) UploadAndRecord(_ context.Context, req BlobUploadRequest) (BlobDetail, error) {
	b, err := io.ReadAll(req.Reader)
	if err != nil {
		return BlobDetail{}, err
	}
	f.nextID++
	id := "upl_" + string(rune('0'+f.nextID))
	f.data[id] = b
	return BlobDetail{FileUploadID: id, ContentHash: req.ContentHash, Size: int64(len(b)), MimeType: req.MimeType}, nil
}
func (f *fakeBlob) Open(_ context.Context, uploadID string) (io.ReadCloser, BlobDetail, error) {
	b, ok := f.data[uploadID]
	if !ok {
		return nil, BlobDetail{}, io.EOF
	}
	return io.NopCloser(bytes.NewReader(b)), BlobDetail{FileUploadID: uploadID, Size: int64(len(b))}, nil
}
func (f *fakeBlob) GetFile(_ context.Context, uploadID string) (BlobDetail, error) {
	b, ok := f.data[uploadID]
	if !ok {
		return BlobDetail{}, io.EOF
	}
	return BlobDetail{FileUploadID: uploadID, Size: int64(len(b))}, nil
}
func (f *fakeBlob) DeleteFile(_ context.Context, uploadID string) error { delete(f.data, uploadID); return nil }

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
	return New(st, bl, Limits{MaxBytes: 20 << 20}), st, bl
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
	svc := New(st, bl, Limits{MaxBytes: 4})
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

func TestContentRoundTrip(t *testing.T) {
	svc, _, _ := newSvc()
	payload := []byte("attachment binary payload \x00\x01\x02")
	resp, err := svc.Upload(context.Background(), "a.bin", "", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	rc, ct, err := svc.OpenContent(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("回读字节与上传不一致")
	}
	if ct != "application/octet-stream" {
		t.Fatalf("Content-Type 应 application/octet-stream，got %q", ct)
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
