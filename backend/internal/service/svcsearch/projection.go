package svcsearch

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/contract/dtosearch"
)

// ——— 会话域搜索视图 → 对外 DTO（v3.2 刀 9）———
func messageSearchResp(v *session.MessageSearchResult) *dtosearch.MessageSearchResp {
	if v == nil {
		return nil
	}
	hits := make([]dtosearch.MessageSearchHit, 0, len(v.Hits))
	for _, h := range v.Hits {
		hits = append(hits, dtosearch.MessageSearchHit{
			SessionID: h.SessionID, Title: h.Title, RuntimeID: h.RuntimeID, Role: h.Role,
			Seq: h.Seq, SeqHi: h.SeqHi, MessageID: h.MessageID, RunID: h.RunID,
			Snippet: h.Snippet, CreatedAt: h.CreatedAt,
		})
	}
	return &dtosearch.MessageSearchResp{
		Query: v.Query, Source: v.Source, Hits: hits, Total: v.Total, HasMore: v.HasMore,
	}
}
