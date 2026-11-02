package attachmentstore

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// fakeRows 内存版 RowPort（记下被置 orphan 的 id）。
type fakeRows struct {
	pending []model.AttachmentEntity
	orphan  []string
}

func (f *fakeRows) ListPendingAttachmentsBefore(_ context.Context, _ string) ([]model.AttachmentEntity, error) {
	return f.pending, nil
}
func (f *fakeRows) MarkAttachmentOrphan(_ context.Context, id string) error {
	f.orphan = append(f.orphan, id)
	return nil
}

// TestSweepOnceMarksOrphan sweepOnce 把超时 pending 置 orphan（blobs=nil 时只置状态）。
func TestSweepOnceMarksOrphan(t *testing.T) {
	rows := &fakeRows{pending: []model.AttachmentEntity{
		{ID: "att_1", UploadID: "upl_1"},
		{ID: "att_2", UploadID: "upl_2"},
	}}
	sw := NewSweeper(rows, nil, 24*time.Hour, time.Hour)
	sw.sweepOnce(context.Background())
	if len(rows.orphan) != 2 {
		t.Fatalf("应置 2 条 orphan，got %v", rows.orphan)
	}
}

// TestSweeperDisabledInterval 关闭的 sweeper 不启动后台循环，Stop 幂等。
func TestSweeperDisabledInterval(t *testing.T) {
	rows := &fakeRows{}
	sw := NewSweeper(rows, nil, 24*time.Hour, 0) // 0 = 关闭
	sw.Start(context.Background())
	sw.Stop() // 未启动也应立即返回
	if len(rows.orphan) != 0 {
		t.Fatalf("关闭状态不该扫任何东西")
	}
}
