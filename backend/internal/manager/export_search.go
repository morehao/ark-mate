package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtoexport"
	"github.com/morehao/ark-forge/internal/dto/dtosearch"
	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/pkg/acl"
)

// 导出上限（超出显式标注 truncated，不静默截断）。
const (
	maxExportIDs       = 50
	maxExportEvents    = 20000
	maxExportBytesEach = 4 << 20 // 单会话正文上限 4 MiB
)

// SearchMessages 跨会话消息级检索（M3 ②：全局消息搜索 + 锚点跳转）。
//
// 数据面：sqlite 模式走 messages 读模型（角色不限）；memory 模式回扫 transcript 的用户消息与
// agent 消息（逐行解析事件，取 seq 作锚点）。两者都返回 seq，前端可据锚点滚动定位。
func (s *Service) SearchMessages(_ context.Context, q string, limit int) (*dtosearch.MessageSearchResp, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_search_query", "搜索关键词 q 不能为空 [invalid_search_query]", nil))
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	resp := &dtosearch.MessageSearchResp{Query: q, Hits: []dtosearch.MessageSearchHit{}}

	if s.st != nil {
		resp.Source = "sqlite"
		hits, err := s.st.SearchMessages(context.Background(), q, limit+1)
		if err != nil {
			return nil, s.restErr(acl.NewError(acl.ClassService, "message_search_failed",
				fmt.Sprintf("消息检索失败: %v [message_search_failed]", err), err))
		}
		if len(hits) > limit {
			resp.HasMore = true
			hits = hits[:limit]
		}
		for _, h := range hits {
			resp.Hits = append(resp.Hits, dtosearch.MessageSearchHit{
				ConversationID: h.ConversationID,
				Title:          h.ConversationTitle,
				AgentID:        h.AgentID,
				Role:           h.Role,
				Seq:            h.Seq,
				SeqHi:          h.SeqHi,
				MessageID:      h.MessageID,
				RunID:          h.RunID,
				Snippet:        foldPreview(h.Text),
				CreatedAt:      h.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		resp.Total = len(resp.Hits)
		return resp, nil
	}

	// memory 模式：回扫 transcript（无 DB 时的明确降级面，标注 source=transcript）。
	resp.Source = "transcript"
	if s.tx == nil || s.tx.dir == "" {
		return resp, nil
	}
	s.mu.Lock()
	recs := make([]*ConversationRec, 0, len(s.conversations))
	for _, r := range s.conversations {
		recs = append(recs, r)
	}
	s.mu.Unlock()
	sort.Slice(recs, func(i, j int) bool { return recs[i].UpdatedAt.After(recs[j].UpdatedAt) })

	type hit struct {
		at  time.Time
		out dtosearch.MessageSearchHit
	}
	var all []hit
	qLower := strings.ToLower(q)
	for _, r := range recs {
		for _, th := range scanTranscriptMessages(s.tx.dir, r.ID, qLower) {
			all = append(all, hit{at: r.UpdatedAt, out: dtosearch.MessageSearchHit{
				ConversationID: r.ID,
				Title:          r.Title,
				AgentID:        r.AgentID,
				Role:           th.Role,
				Seq:            th.Seq,
				Snippet:        foldPreview(th.Text),
				CreatedAt:      r.UpdatedAt.UTC().Format(time.RFC3339),
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
func openTranscript(dir, conversationID string) (*os.File, error) {
	return os.Open(filepath.Join(dir, conversationID+".jsonl"))
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
func scanTranscriptMessages(dir, conversationID, qLower string) []transcriptMessageHit {
	f, err := openTranscript(dir, conversationID)
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

// ExportConversations 批量导出会话（M3 ②）。事实源与前端渲染一致：事件序列（sqlite=events 表；
// memory=eventbus/transcript 回放），因此两模式产物一致。未知/无事件会话进 skipped 明确列出。
func (s *Service) ExportConversations(ctx context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown", "text", "json":
	default:
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_export_format",
			fmt.Sprintf("不支持的导出格式 %q（markdown|text|json）[invalid_export_format]", req.Format), nil))
	}
	ids := make([]string, 0, len(req.IDs))
	seen := map[string]bool{}
	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "invalid_export_ids", "ids 不能为空 [invalid_export_ids]", nil))
	}
	if len(ids) > maxExportIDs {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "export_too_many_ids",
			fmt.Sprintf("一次最多导出 %d 个会话（收到 %d）[export_too_many_ids]", maxExportIDs, len(ids)), nil))
	}

	resp := &dtoexport.ExportResp{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Format: format, Files: []dtoexport.ExportFile{}}
	for _, id := range ids {
		meta, ok := s.conversationMeta(id)
		if !ok {
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		events, truncated := s.exportEvents(ctx, id)
		if len(events) == 0 {
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		content := renderExport(meta, events, format)
		if len(content) > maxExportBytesEach {
			content = content[:maxExportBytesEach]
			truncated = true
		}
		resp.Files = append(resp.Files, dtoexport.ExportFile{
			ConversationID: id,
			Title:          meta.title,
			AgentID:        meta.agentID,
			Status:         meta.status,
			Filename:       exportFilename(id, meta.title, format),
			Format:         format,
			Events:         len(events),
			Bytes:          len(content),
			Truncated:      truncated,
			Content:        content,
		})
		resp.TotalBytes += len(content)
	}
	resp.Exported = len(resp.Files)
	return resp, nil
}

// exportMeta 导出用会话元数据（sqlite 走库，memory 走内存镜像）。
type exportMeta struct {
	id, title, agentID, status string
	created, updated           time.Time
}

func (s *Service) conversationMeta(id string) (exportMeta, bool) {
	if s.st != nil {
		c, ok, err := s.st.GetConversation(context.Background(), id)
		if err != nil || !ok {
			return exportMeta{}, false
		}
		return exportMeta{id: c.ID, title: c.Title, agentID: c.AgentID, status: c.Status,
			created: c.CreatedAt, updated: c.UpdatedAt}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.conversations[id]
	if !ok {
		return exportMeta{}, false
	}
	return exportMeta{id: r.ID, title: r.Title, agentID: r.AgentID, status: r.Status,
		created: r.CreatedAt, updated: r.UpdatedAt}, true
}

// exportEvents 取会话事件（升序；截断上限明确返回 truncated）。
func (s *Service) exportEvents(ctx context.Context, id string) ([]eventbus.Event, bool) {
	if s.st != nil {
		var out []eventbus.Event
		after := int64(0)
		for {
			batch, more, err := s.st.EventsAfter(ctx, id, after, 1000)
			if err != nil {
				return out, false
			}
			for _, e := range batch {
				if len(out) >= maxExportEvents {
					return out, true
				}
				out = append(out, eventbus.Event{
					Type: e.Type, Seq: e.Seq, AggregID: e.ConversationID,
					Payload: decodePayload(e.PayloadJSON),
				})
				after = e.Seq
			}
			if !more || len(batch) == 0 {
				break
			}
		}
		return out, false
	}
	all := s.bus.EventsAfter(0)
	out := make([]eventbus.Event, 0, len(all))
	for _, e := range all {
		if e.AggregID != id {
			continue
		}
		if len(out) >= maxExportEvents {
			return out, true
		}
		out = append(out, e)
	}
	return out, false
}

// exportFilename 生成安全文件名（去除路径分隔与控制字符；含会话短 id 防重名）。
func exportFilename(id, title, format string) string {
	ext := map[string]string{"markdown": "md", "text": "txt", "json": "json"}[format]
	base := strings.TrimSpace(title)
	if base == "" {
		base = "conversation"
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < 0x20:
			return '-'
		default:
			return r
		}
	}, base)
	runes := []rune(base)
	if len(runes) > 40 {
		base = string(runes[:40])
	}
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s-%s.%s", base, short, ext)
}

// renderExport 按格式渲染（事件序列 → 单文件正文）。
func renderExport(meta exportMeta, events []eventbus.Event, format string) string {
	if format == "json" {
		doc := map[string]any{
			"conversation": map[string]any{
				"id": meta.id, "title": meta.title, "agent_id": meta.agentID, "status": meta.status,
				"created_at": meta.created.UTC().Format(time.RFC3339),
				"updated_at": meta.updated.UTC().Format(time.RFC3339),
			},
			"events": events,
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return ""
		}
		return string(b) + "\n"
	}

	md := format == "markdown"
	var b strings.Builder
	title := meta.title
	if title == "" {
		title = meta.id
	}
	if md {
		fmt.Fprintf(&b, "# %s\n\n", title)
		fmt.Fprintf(&b, "- 会话：`%s`\n- Agent：`%s`\n- 状态：`%s`\n- 创建：%s\n- 更新：%s\n- 事件数：%d\n\n---\n\n",
			meta.id, meta.agentID, meta.status,
			meta.created.UTC().Format(time.RFC3339), meta.updated.UTC().Format(time.RFC3339), len(events))
	} else {
		fmt.Fprintf(&b, "%s\n会话: %s\nAgent: %s\n状态: %s\n创建: %s\n更新: %s\n事件数: %d\n%s\n\n",
			title, meta.id, meta.agentID, meta.status,
			meta.created.UTC().Format(time.RFC3339), meta.updated.UTC().Format(time.RFC3339), len(events),
			strings.Repeat("-", 60))
	}
	for _, ev := range events {
		payload, _ := ev.Payload.(map[string]any)
		text := payloadString(payload, "text")
		switch ev.Type {
		case BusUserMessage:
			if md {
				fmt.Fprintf(&b, "## 用户\n\n%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "[用户] %s\n\n", text)
			}
		case BusMessage:
			if md {
				fmt.Fprintf(&b, "## 助手\n\n%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "[助手] %s\n\n", text)
			}
		case BusThought:
			if text == "" {
				continue
			}
			if md {
				fmt.Fprintf(&b, "> 思考：%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "(思考) %s\n\n", text)
			}
		case BusToolCall, BusToolUpdate:
			t := payloadString(payload, "title")
			kind := payloadString(payload, "kind")
			status := payloadString(payload, "status")
			line := fmt.Sprintf("工具 %s", t)
			if kind != "" {
				line += fmt.Sprintf("（%s）", kind)
			}
			if status != "" {
				line += fmt.Sprintf(" [%s]", status)
			}
			if md {
				fmt.Fprintf(&b, "- %s\n", line)
			} else {
				fmt.Fprintf(&b, "· %s\n", line)
			}
		case BusTurnEnded:
			if md {
				fmt.Fprintf(&b, "\n*轮次结束*\n\n")
			} else {
				fmt.Fprintf(&b, "\n[轮次结束]\n\n")
			}
		case BusTurnError:
			msg := payloadString(payload, "error")
			if msg == "" {
				msg = payloadString(payload, "msg")
			}
			if md {
				fmt.Fprintf(&b, "\n**轮次失败**：%s\n\n", msg)
			} else {
				fmt.Fprintf(&b, "\n[轮次失败] %s\n\n", msg)
			}
		case BusRunCancelled:
			if md {
				fmt.Fprintf(&b, "\n*已取消*\n\n")
			} else {
				fmt.Fprintf(&b, "\n[已取消]\n\n")
			}
		}
	}
	return b.String()
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}
