package session

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/object/objtime"
)

// ——— 会话检索（标题 / runtime / 会话 id / 用户消息正文） ———
//
// P3b-2b：从 manager 搬进域（`SearchSessions` + `scanTranscriptText` + `foldPreview`）。
// 它读的是**本域的两份事实**：内存会话表（`pl.sessions`）与 transcript 目录（`pl.tx.dir`），
// 所以留在 manager 只会让 manager 反向认识域的旁路文件布局。

// SearchSessions 按关键词搜索会话（标题 / Agent / ID / 用户消息正文；含归档）。
// 元数据命中计 1 次；正文命中逐条累计（片段取首条命中用户消息）。
// 排序：updated_at 降序，截断 limit（调用方已校验 >0）。
// 复杂度说明：本地工具态直接扫 transcript 文件（文件小、量级低）；不引入索引/DB（决策范围外）。
func (pl *Plane) SearchSessions(_ context.Context, q string, limit int) ([]SessionSearchHitItem, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	qLower := strings.ToLower(q)

	// `Snapshot` 给的是副本（域外读面用）；排序/过滤都在副本上做，不持锁。
	snap := pl.Snapshot()
	recs := make([]*SessionRec, 0, len(snap))
	for i := range snap {
		recs = append(recs, &snap[i])
	}
	dir := pl.transcriptDir()
	st := pl.st

	type cand struct {
		rec     *SessionRec
		snippet string
		matches int
	}
	var out []cand
	for _, r := range recs {
		metaHit := strings.Contains(strings.ToLower(r.ID), qLower) ||
			strings.Contains(strings.ToLower(r.RuntimeID), qLower) ||
			(r.Title != "" && strings.Contains(strings.ToLower(r.Title), qLower))
		snippet, bodyMatches := "", 0
		if st != nil {
			// sqlite 模式：正文检索走 messages 表（user 行原文；读模型，逐轮物化）。
			if hits, err := st.SearchUserMessages(context.Background(), r.ID, q); err == nil {
				for _, h := range hits {
					bodyMatches++
					if snippet == "" {
						snippet = foldPreview(h.Text)
					}
				}
			}
			// 过渡期兜底：DB 尚无 messages（如仅 migrate events 未物化 messages）时回扫 transcript。
			if bodyMatches == 0 && dir != "" {
				if sc, n, ok := scanTranscriptText(dir, r.ID, qLower); ok {
					snippet, bodyMatches = sc, n
				}
			}
		} else if dir != "" {
			if sc, n, ok := scanTranscriptText(dir, r.ID, qLower); ok {
				snippet, bodyMatches = sc, n
			}
		}
		matches := bodyMatches
		if metaHit {
			matches++
		}
		if matches == 0 {
			continue
		}
		out = append(out, cand{rec: r, snippet: snippet, matches: matches})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].rec.UpdatedAt.After(out[j].rec.UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}

	hits := make([]SessionSearchHitItem, 0, len(out))
	for _, c := range out {
		r := c.rec
		hits = append(hits, SessionSearchHitItem{
			SessionItem: SessionItem{
				ID:        r.ID,
				RuntimeID: r.RuntimeID,
				Status:    r.Status,
				Title:     r.Title,
				Pinned:    r.Pinned,
				Archived:  r.Archived,
				CreatedAt: objtime.Milli(r.CreatedAt),
				UpdatedAt: objtime.Milli(r.UpdatedAt),
			},
			Snippet: c.snippet,
			Matches: c.matches,
		})
	}
	return hits, nil
}

// scanTranscriptText 在单会话 transcript 文件中搜索 qLower（大小写不敏感、子串），
// 返回（首条命中用户消息文本的折叠预览, 命中用户消息条数, 是否有正文命中）。
// 文件缺失/不可读 → ok=false（调用方按「无正文命中」处理）。
func scanTranscriptText(dir, sessionID, qLower string) (string, int, bool) {
	f, err := os.Open(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		return "", 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	hdrSeen := false
	snippet := ""
	matches := 0
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
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type != BusUserMessage {
			continue
		}
		text, _ := ev.Payload["text"].(string)
		if text == "" || !strings.Contains(strings.ToLower(text), qLower) {
			continue
		}
		matches++
		if snippet == "" {
			snippet = foldPreview(text)
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, false
	}
	return snippet, matches, matches > 0
}

// foldPreview 折叠空白 + 截断（≤160 rune，省略号收尾），用于命中片段预览。
func foldPreview(text string) string {
	folded := strings.Join(strings.Fields(text), " ")
	runes := []rune(folded)
	const max = 160
	if len(runes) <= max {
		return folded
	}
	return string(runes[:max]) + "…"
}
