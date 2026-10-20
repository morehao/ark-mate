package svcsystem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/dto/dtoexport"
)

// ——— P4a：导出的**用例层实现** ———
//
// 它拥有：参数校验与归一（格式、ids 去重、上限）、错误码出口、响应装配、渲染。
// 它**不拥有**：会话元数据与事件序列的取法（那是 `Source` 端口，见 source.go）——
// 于是这个包既不 import `manager` 也不 import `store`/`domain/session`。

// Exporter 导出用例（实现 `SystemSvc`）。
type Exporter struct{ src Source }

// New 造一个导出用例（`src` 由装配侧交出）。
func New(src Source) *Exporter { return &Exporter{src: src} }

var _ SystemSvc = (*Exporter)(nil)

// 导出上限（超出显式标注 truncated，不静默截断）。
const (
	// MaxExportIDs 单次导出的会话数上限（用例口径）。
	MaxExportIDs = 50
	// MaxExportEvents 单次导出的事件上限（**导出用例的口径**：适配器也要按它截断，
	// 否则"取数"与"渲染"会各自截一次，而成品里看不出是哪一次生效）。
	MaxExportEvents    = 20000
	maxExportBytesEach = 4 << 20 // 单会话正文上限 4 MiB
)

// ExportSessions 批量导出会话（M3 ②）。事实源与前端渲染一致：事件序列（sqlite=events 表；
// memory=eventbus/transcript 回放），因此两模式产物一致。未知/无事件会话进 skipped 明确列出。
func (e *Exporter) ExportSessions(ctx context.Context, req dtoexport.ExportReq) (*dtoexport.ExportResp, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown", "text", "json":
	default:
		return nil, code.FromError(code.RequestError("invalid_export_format", fmt.Sprintf("不支持的导出格式 %q（markdown|text|json）[invalid_export_format]", req.Format)))
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
		return nil, code.FromError(code.RequestError("invalid_export_ids", "ids 不能为空 [invalid_export_ids]"))
	}
	if len(ids) > MaxExportIDs {
		return nil, code.FromError(code.RequestError("export_too_many_ids", fmt.Sprintf("一次最多导出 %d 个会话（收到 %d）[export_too_many_ids]", MaxExportIDs, len(ids))))
	}

	resp := &dtoexport.ExportResp{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Format: format, Files: []dtoexport.ExportFile{}}
	for _, id := range ids {
		meta, ok := e.src.SessionMeta(ctx, id)
		if !ok {
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		events, truncated := e.src.SessionEvents(ctx, id, MaxExportEvents)
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
			Title:     meta.Title,
			RuntimeID: meta.RuntimeID,
			Status:    meta.Status,
			Filename:  exportFilename(id, meta.Title, format),
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

// exportEvents 取会话事件（升序；截断上限明确返回 truncated）。
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
func renderExport(meta SessionMeta, events []eventbus.Event, format string) string {
	if format == "json" {
		doc := map[string]any{
			"session": map[string]any{
				"id": meta.ID, "title": meta.Title, "runtime_id": meta.RuntimeID, "status": meta.Status,
				"created_at": meta.Created.UTC().Format(time.RFC3339),
				"updated_at": meta.Updated.UTC().Format(time.RFC3339),
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
	title := meta.Title
	if title == "" {
		title = meta.ID
	}
	if md {
		fmt.Fprintf(&b, "# %s\n\n", title)
		fmt.Fprintf(&b, "- 会话：`%s`\n- 底座：`%s`\n- 状态：`%s`\n- 创建：%s\n- 更新：%s\n- 事件数：%d\n\n---\n\n",
			meta.ID, meta.RuntimeID, meta.Status,
			meta.Created.UTC().Format(time.RFC3339), meta.Updated.UTC().Format(time.RFC3339), len(events))
	} else {
		fmt.Fprintf(&b, "%s\n会话: %s\n底座: %s\n状态: %s\n创建: %s\n更新: %s\n事件数: %d\n%s\n\n",
			title, meta.ID, meta.RuntimeID, meta.Status,
			meta.Created.UTC().Format(time.RFC3339), meta.Updated.UTC().Format(time.RFC3339), len(events),
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
