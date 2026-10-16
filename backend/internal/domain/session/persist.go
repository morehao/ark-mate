package session

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/glog"
)

// persistSession 把会话记录同步写库（best-effort：失败告警不阻断会话流程）。
func (pl *Plane) persistSession(rec *SessionRec) {
	if pl.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := pl.st.UpsertSession(ctx, model.SessionEntity{
		ID:                 rec.ID,
		Cwd:                rec.Cwd,
		WorkspaceID:        rec.WorkspaceID,
		WorkspaceTemporary: rec.WorkspaceTemporary,
		ModelID:            rec.ModelID,
		RuntimeID:          rec.RuntimeID,
		RuntimeSessionID:   rec.RuntimeSessionID,
		Title:              rec.Title,
		Pinned:             rec.Pinned,
		Archived:           rec.Archived,
		Status:             rec.Status,
		Origin:             rec.Origin,
		CapabilityJSON:     rec.CapabilityJSON,
		// **钉住的指纹**（代际）：普通持久化写的就是记录里的那个值（会话一旦建立就不再重新解析），
		// 所以这里不会"每次落库都把指纹刷成当前值"；只有 M3-6 的轮次边界迁移会显式改写它。
		CapabilityFingerprint: rec.Fingerprint,
		CreatedAt:             rec.CreatedAt,
		UpdatedAt:             rec.UpdatedAt,
	})
	if err != nil {
		glog.Warnw(context.Background(), "store: upsert session failed", "session", rec.ID, "err", err)
	}
}
