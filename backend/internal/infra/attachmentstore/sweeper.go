package attachmentstore

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/glog"
)

// RowPort 孤儿清扫需要的**附件行**窄端口（由 *store.Store 结构上满足；本包不 import store）。
type RowPort interface {
	// ListPendingAttachmentsBefore 取 created_at 早于 before（RFC3339Nano 文本）的 pending 附件。
	ListPendingAttachmentsBefore(ctx context.Context, before string) ([]model.AttachmentEntity, error)
	// MarkAttachmentOrphan 把一条附件置 orphan（幂等，只影响仍 pending 的行）。
	MarkAttachmentOrphan(ctx context.Context, id string) error
}

// Sweeper 孤儿附件清扫循环（设计 §6）：扫到 `state=pending AND created_at < now-TTL` 的附件
// → 置 orphan + 删 blob（记录级）+ WARN 台账。被别的附件引用的同内容 blob 由 filestore 去重规则保留。
type Sweeper struct {
	rows    RowPort
	blobs   *Store // 删字节（DeleteFile）；nil = 只置状态不删（理论上不出现）
	ttl     time.Duration
	every   time.Duration
	started bool
	stop    chan struct{}
	stopped chan struct{}
}

// NewSweeper 构造清扫循环。every<=0 表示关闭（不启动）。
func NewSweeper(rows RowPort, blobs *Store, ttl, every time.Duration) *Sweeper {
	return &Sweeper{rows: rows, blobs: blobs, ttl: ttl, every: every,
		stop: make(chan struct{}), stopped: make(chan struct{})}
}

// Start 启动后台 ticker（非阻塞）。every<=0 时不启动，stopped 保持未关闭。
func (s *Sweeper) Start(ctx context.Context) {
	if s == nil || s.rows == nil || s.every <= 0 {
		return
	}
	s.started = true
	go s.loop(ctx)
}

// loop 单 goroutine ticker：每个 tick 扫一次，ctx/stop 结束即退。
func (s *Sweeper) loop(ctx context.Context) {
	defer close(s.stopped)
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce 扫一轮：置 orphan + 删 blob + WARN。单条失败不中断其余，但要逐条 WARN（不静默）。
func (s *Sweeper) sweepOnce(ctx context.Context) {
	before := nowUTC().Add(-s.ttl).Format(time.RFC3339Nano)
	rows, err := s.rows.ListPendingAttachmentsBefore(ctx, before)
	if err != nil {
		glog.Warnw(ctx, "附件孤儿清扫：读取待扫列表失败", "err", err)
		return
	}
	for _, r := range rows {
		if err := s.rows.MarkAttachmentOrphan(ctx, r.ID); err != nil {
			glog.Warnw(ctx, "附件孤儿清扫：置 orphan 失败", "attachment", r.ID, "err", err)
			continue
		}
		if s.blobs != nil {
			if derr := s.blobs.DeleteFile(ctx, r.UploadID); derr != nil {
				// 记录删了、对象删失败：可重建（下次同内容上传覆盖/运维按 core_file 比对清）。
				glog.Warnw(ctx, "附件孤儿清扫：删字节失败（已置 orphan）", "attachment", r.ID, "upload", r.UploadID, "err", derr)
				continue
			}
		}
		glog.Warnw(ctx, "附件孤儿清扫：已清理超时未挂载附件", "attachment", r.ID, "upload", r.UploadID)
	}
}

// Stop 停止循环并等待退出（未启动时立即返回）。
func (s *Sweeper) Stop() {
	if s == nil || !s.started {
		return
	}
	select {
	case <-s.stopped:
		return // 已退出
	default:
	}
	close(s.stop)
	<-s.stopped
}

// nowUTC 便于测试替换的时钟（此包内联）。
var nowUTC = func() time.Time { return time.Now().UTC() }
