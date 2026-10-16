package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/domain/session"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
)

// 导出上限（超出显式标注 truncated，不静默截断）。
const (
	maxExportIDs       = 50
	maxExportEvents    = 20000
	maxExportBytesEach = 4 << 20 // 单会话正文上限 4 MiB
)

// ExportSessions 批量导出会话（M3 ②）。事实源与前端渲染一致：事件序列（sqlite=events 表；
// memory=eventbus/transcript 回放），因此两模式产物一致。未知/无事件会话进 skipped 明确列出。
func (s *Service) ExportSessions(ctx context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown", "text", "json":
	default:
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_export_format",
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
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "invalid_export_ids", "ids 不能为空 [invalid_export_ids]", nil))
	}
	if len(ids) > maxExportIDs {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "export_too_many_ids",
			fmt.Sprintf("一次最多导出 %d 个会话（收到 %d）[export_too_many_ids]", maxExportIDs, len(ids)), nil))
	}

	resp := &dtoexport.ExportResp{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Format: format, Files: []dtoexport.ExportFile{}}
	for _, id := range ids {
		meta, ok := s.sessionMeta(id)
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
			SessionID: id,
			Title:     meta.title,
			RuntimeID: meta.runtimeID,
			Status:    meta.status,
			Filename:  exportFilename(id, meta.title, format),
			Format:    format,
			Events:    len(events),
			Bytes:     len(content),
			Truncated: truncated,
			Content:   content,
		})
		resp.TotalBytes += len(content)
	}
	resp.Exported = len(resp.Files)
	return resp, nil
}

// exportMeta 导出用会话元数据（sqlite 走库，memory 走内存镜像）。
type exportMeta struct {
	id, title, runtimeID, status string
	created, updated             time.Time
}

func (s *Service) sessionMeta(id string) (exportMeta, bool) {
	if s.st != nil {
		c, ok, err := s.st.GetSession(context.Background(), id)
		if err != nil || !ok {
			return exportMeta{}, false
		}
		return exportMeta{id: c.ID, title: c.Title, runtimeID: c.RuntimeID, status: c.Status,
			created: c.CreatedAt, updated: c.UpdatedAt}, true
	}
	// **不持 `s.mu`**（P2b 守卫）：`Lookup` 自己取会话域的锁并返回副本。
	r, ok := s.sessionPlane.Lookup(id)
	if !ok {
		return exportMeta{}, false
	}
	return exportMeta{id: r.ID, title: r.Title, runtimeID: r.RuntimeID, status: r.Status,
		created: r.CreatedAt, updated: r.UpdatedAt}, true
}

// exportEvents 取会话事件（升序；截断上限明确返回 truncated）。
func (s *Service) exportEvents(ctx context.Context, id string) ([]eventbus.Event, bool) {
	if s.st != nil {
		var out []eventbus.Event
		after := int64(-1)
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
					ID: e.ID, Type: e.Type, Seq: e.Seq, AggregID: e.SessionID,
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
	all := s.bus.EventsAfter(id, -1)
	out := make([]eventbus.Event, 0, len(all))
	for _, e := range all {
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
		base = "session"
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
			"session": map[string]any{
				"id": meta.id, "title": meta.title, "runtime_id": meta.runtimeID, "status": meta.status,
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
		fmt.Fprintf(&b, "- 会话：`%s`\n- 底座：`%s`\n- 状态：`%s`\n- 创建：%s\n- 更新：%s\n- 事件数：%d\n\n---\n\n",
			meta.id, meta.runtimeID, meta.status,
			meta.created.UTC().Format(time.RFC3339), meta.updated.UTC().Format(time.RFC3339), len(events))
	} else {
		fmt.Fprintf(&b, "%s\n会话: %s\n底座: %s\n状态: %s\n创建: %s\n更新: %s\n事件数: %d\n%s\n\n",
			title, meta.id, meta.runtimeID, meta.status,
			meta.created.UTC().Format(time.RFC3339), meta.updated.UTC().Format(time.RFC3339), len(events),
			strings.Repeat("-", 60))
	}
	for _, ev := range events {
		payload, _ := ev.Payload.(map[string]any)
		text := payloadString(payload, "text")
		switch ev.Type {
		case session.BusUserMessage:
			if md {
				fmt.Fprintf(&b, "## 用户\n\n%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "[用户] %s\n\n", text)
			}
		case session.BusMessage:
			if md {
				fmt.Fprintf(&b, "## 助手\n\n%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "[助手] %s\n\n", text)
			}
		case session.BusThought:
			if text == "" {
				continue
			}
			if md {
				fmt.Fprintf(&b, "> 思考：%s\n\n", text)
			} else {
				fmt.Fprintf(&b, "(思考) %s\n\n", text)
			}
		case session.BusToolCall, session.BusToolUpdate:
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
		case session.BusRunStarted:
			// 轮次分节：没有它时两轮之间只有一个"轮次结束"，读起来像同一轮的停顿。
			if md {
				fmt.Fprintf(&b, "\n---\n\n*轮次开始*\n\n")
			} else {
				fmt.Fprintf(&b, "\n[轮次开始]\n\n")
			}
		case session.BusRunEnded:
			if md {
				fmt.Fprintf(&b, "\n*轮次结束*\n\n")
			} else {
				fmt.Fprintf(&b, "\n[轮次结束]\n\n")
			}
		case session.BusRunError:
			msg := payloadString(payload, "error")
			if msg == "" {
				msg = payloadString(payload, "msg")
			}
			if md {
				fmt.Fprintf(&b, "\n**轮次失败**：%s\n\n", msg)
			} else {
				fmt.Fprintf(&b, "\n[轮次失败] %s\n\n", msg)
			}
		case session.BusRunCancelled:
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
