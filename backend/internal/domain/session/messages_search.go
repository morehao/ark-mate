package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objtime"
)

// ——— 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转） ———
//
// P3b-2b：从 manager 搬进域（`SearchMessages` + transcript 回扫那半）。
// 数据面：sqlite 模式走 messages 读模型（角色不限）；memory 模式回扫 transcript 的用户消息与
// agent 消息（逐行解析事件，取 seq 作锚点）。两者都返回 seq，前端可据锚点滚动定位。

// SearchMessages 跨会话消息级检索。
func (pl *Plane) SearchMessages(_ context.Context, q string, limit int) (*MessageSearchResult, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, code.FromError(failure.NewError(failure.ClassRequest, "invalid_search_query", "搜索关键词 q 不能为空 [invalid_search_query]", nil))
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	resp := &MessageSearchResult{Query: q, Hits: []MessageSearchHit{}}

	if pl.st != nil {
		resp.Source = "sqlite"
		hits, err := pl.st.SearchMessages(context.Background(), q, limit+1)
		if err != nil {
			return nil, code.FromError(failure.NewError(failure.ClassService, "message_search_failed",
				fmt.Sprintf("消息检索失败: %v [message_search_failed]", err), err))
		}
		if len(hits) > limit {
			resp.HasMore = true
			hits = hits[:limit]
		}
		for _, h := range hits {
			// 内部（模型网关）会话的正文不进用户检索：它们不是用户在聊的会话。
			if pl.isInternalSession(h.SessionID) {
				continue
			}
			resp.Hits = append(resp.Hits, MessageSearchHit{
				SessionID: h.SessionID,
				Title:     h.SessionTitle,
				Role:      h.Role,
				Seq:       h.Seq,
				SeqHi:     h.SeqHi,
				MessageID: h.MessageID,
				RunID:     h.RunID,
				Snippet:   foldPreview(h.Text),
				CreatedAt: objtime.Milli(h.CreatedAt),
			})
		}
		resp.Total = len(resp.Hits)
		return resp, nil
	}

	// memory 模式：回扫 transcript（无 DB 时的明确降级面，标注 source=transcript）。
	resp.Source = "transcript"
	dir := pl.transcriptDir()
	if dir == "" {
		return resp, nil
	}
	snap := pl.Snapshot()
	recs := make([]*SessionRec, 0, len(snap))
	for i := range snap {
		recs = append(recs, &snap[i])
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].UpdatedAt.After(recs[j].UpdatedAt) })

	type hit struct {
		at  time.Time
		out MessageSearchHit
	}
	var all []hit
	qLower := strings.ToLower(q)
	for _, r := range recs {
		if IsInternalOrigin(r.Origin) {
			continue
		}
		for _, th := range scanTranscriptMessages(dir, r.ID, qLower) {
			all = append(all, hit{at: r.UpdatedAt, out: MessageSearchHit{
				SessionID: r.ID,
				Title:     r.Title,
				RuntimeID: r.RuntimeID,
				Role:      th.Role,
				Seq:       th.Seq,
				Snippet:   foldPreview(th.Text),
				CreatedAt: objtime.Milli(r.UpdatedAt),
			}})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			return all[i].out.Seq > all[j].out.Seq
		}
		return all[i].at.After(all[j].at)
	})
	if len(all) > limit {
		resp.HasMore = true
		all = all[:limit]
	}
	for _, h := range all {
		resp.Hits = append(resp.Hits, h.out)
	}
	resp.Total = len(resp.Hits)
	return resp, nil
}

// openTranscript 打开单会话 transcript 文件（路径约定 <dir>/<会话id>.jsonl）。
func openTranscript(dir, sessionID string) (*os.File, error) {
	return os.Open(filepath.Join(dir, sessionID+".jsonl"))
}

// readTranscriptEvents 解析 transcript 正文行（跳过 schema header；不可解析行跳过）。
func readTranscriptEvents(f *os.File) []wireTranscriptLine {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	hdrSeen := false
	var out []wireTranscriptLine
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if !hdrSeen {
			var h wireTranscriptHeader
			if err := json.Unmarshal(line, &h); err == nil && h.Schema == wireTranscriptSchema && h.SessionID != "" {
				hdrSeen = true
			}
			continue
		}
		var ev wireTranscriptLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// transcriptMessageHit 一条 transcript 命中（memory 模式搜索用）。
type transcriptMessageHit struct {
	Role string
	Seq  int64
	Text string
}

// scanTranscriptMessages 扫单会话 transcript 的 user_message / session.message 事件，
// 返回命中（含 seq 锚点）。文件缺失/不可读 → 空结果。
func scanTranscriptMessages(dir, sessionID, qLower string) []transcriptMessageHit {
	f, err := openTranscript(dir, sessionID)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []transcriptMessageHit
	for _, ev := range readTranscriptEvents(f) {
		switch ev.Type {
		case BusUserMessage, BusMessage:
		default:
			continue
		}
		text, _ := ev.Payload["text"].(string)
		if text == "" || !strings.Contains(strings.ToLower(text), qLower) {
			continue
		}
		role := "assistant"
		if ev.Type == BusUserMessage {
			role = "user"
		}
		out = append(out, transcriptMessageHit{Role: role, Seq: ev.Seq, Text: text})
	}
	return out
}
