package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/dto/dtoexport"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/object/objid"
	"github.com/morehao/golib/gerror"
)

// seedSession 落一个会话 + 若干事件（含 messages 物化行）。
func seedSearchSession(t *testing.T, svc *Service, st *store.Store, id, title string, texts ...[2]string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := st.UpsertSession(ctx, model.SessionEntity{
		ID: id, Title: title, Status: "completed", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: id, Title: title, Status: "completed",
		CreatedAt: now, UpdatedAt: now})
	var msgs []model.MessageEntity
	for i, pair := range texts {
		role, text := pair[0], pair[1]
		seq, err := st.AppendEvent(ctx, objid.New(objid.Event), id, session.BusUserMessage, map[string]any{"text": text})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		// 直插的读模型行：`ordinal` 是**会话内**序号（`UNIQUE(session_id, ordinal)` 会咬重号），
		// run_id 只是这一行的来源标记（这里没建 runs 行，本用例验的是搜索/导出而非轮次明细）。
		msgs = append(msgs, model.MessageEntity{ID: objid.New(objid.Message), SessionID: id,
			RunID: fmt.Sprintf("run-%s-%d", id, i), Role: role,
			Text: text, Ordinal: int64(i) + 1, SeqLo: seq, SeqHi: seq,
			CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	if err := st.CreateMessages(ctx, msgs); err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}
}

func TestSearchMessagesSQLite(t *testing.T) {
	ctx := context.Background()
	svc, st := newSearchRig(t)
	seedSearchSession(t, svc, st, "c-1", "部署排查",
		[2]string{"user", "网关 502 怎么排查"},
		[2]string{"assistant", "先看 upstream 健康检查与超时配置"})
	seedSearchSession(t, svc, st, "c-2", "无关会话",
		[2]string{"user", "今天天气不错"})

	resp, err := sessionPlaneOf(svc).SearchMessages(ctx, "502", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if resp.Source != "sqlite" || resp.Total != 1 || len(resp.Hits) != 1 {
		t.Fatalf("命中不符: %+v", resp)
	}
	h := resp.Hits[0]
	// 锚点判据是 `< 0` 而不是 `<= 0`：v21 起会话第一条消息的 seq 就是 0，"没有锚点"的
	// 哨兵是 -1（`<= 0` 会把一条正常命中当成缺锚点）。
	if h.SessionID != "c-1" || h.Title != "部署排查" || h.Role != "user" || h.Seq < 0 {
		t.Fatalf("命中字段不符: %+v", h)
	}
	if !strings.Contains(h.Snippet, "502") {
		t.Fatalf("片段应含关键词: %q", h.Snippet)
	}

	// 助手消息也命中（角色不限）。
	resp2, err := sessionPlaneOf(svc).SearchMessages(ctx, "upstream", 10)
	if err != nil {
		t.Fatalf("SearchMessages(assistant): %v", err)
	}
	if resp2.Total != 1 || resp2.Hits[0].Role != "assistant" {
		t.Fatalf("助手消息应命中: %+v", resp2.Hits)
	}

	// 大小写不敏感 + limit 截断语义。
	seedSearchSession(t, svc, st, "c-3", "大写",
		[2]string{"user", "UPSTREAM 再问一次"})
	resp3, err := sessionPlaneOf(svc).SearchMessages(ctx, "upstream", 1)
	if err != nil {
		t.Fatalf("SearchMessages(limit): %v", err)
	}
	if resp3.Total != 1 || !resp3.HasMore {
		t.Fatalf("limit=1 应截断且 has_more: %+v", resp3)
	}

	// 空关键词 → 明确 40001。
	if _, err := sessionPlaneOf(svc).SearchMessages(ctx, "   ", 10); err == nil {
		t.Fatal("空关键词应报错")
	} else if ge, ok := err.(gerror.Error); !ok || ge.Code != 40001 || !strings.Contains(ge.Msg, "invalid_search_query") {
		t.Fatalf("错误码不符: %v", err)
	}
}

func TestSearchMessagesTranscriptFallback(t *testing.T) {
	// memory 模式（无 store）：回扫 transcript，source 明确标注 transcript。
	//
	// P3b-2b：检索实现搬进了 `domain/session`，域内已有一条同判据的用例
	// （`TestSearchMessagesTranscriptFallback`）；这一条留在 manager 是因为它走的是
	// **真 Service 装配链**（`SetTranscriptDir` → 域、`publish` → 域）——验的是"接上了没有"。
	dir := t.TempDir()
	svc := NewService(testutil.StubRegistry(), eventbus.New())
	svc.SessionPlane().SetTranscriptDir(dir)
	t.Cleanup(svc.SessionPlane().CloseTranscripts)

	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "t-1", Title: "内存会话"})
	svc.sessionPlane.BeginTranscript("t-1", "stub", "/tmp", false)
	svc.sessionPlane.Publish(session.BusUserMessage, "t-1", map[string]any{"text": "内存里的关键词 needle"})
	svc.sessionPlane.Publish(session.BusMessage, "t-1", map[string]any{"text": "助手也提到 needle"})
	// ③ 文本增量攒批：断言前手动落盘（生产由 25ms 窗口 / 终态事件 / 阈值触发）。
	svc.sessionPlane.FlushChunks()

	resp, err := sessionPlaneOf(svc).SearchMessages(context.Background(), "needle", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if resp.Source != "transcript" || resp.Total != 2 {
		t.Fatalf("transcript 命中不符: %+v", resp)
	}
	for _, h := range resp.Hits {
		if h.SessionID != "t-1" || h.Seq < 0 {
			t.Fatalf("命中锚点缺失: %+v", h)
		}
	}
}

// exportSystemSvc 按**装配点的形状**造导出用例：三份依赖事实 → `NewSource` → `New`。
//
// 与 `app.go` 的 `SessionPlane: a.svc.SessionPlane(), Bus: a.bus` 逐条对应（用例实例在
// `ctrexport` 里造；测试里由这里造——`internal/service/svcsystem` 不能 import `manager`）。
func exportSystemSvc(svc *Service) svcsystem.SystemSvc {
	return svcsystem.New(svcsystem.NewSource(storeAsSourceFacts(svc.planes.Resources.Store()), svc.sessionPlane, svc.bus))
}

func TestExportSessions(t *testing.T) {
	ctx := context.Background()
	svc, st := newSearchRig(t)
	seedSearchSession(t, svc, st, "e-1", "导出用例",
		[2]string{"user", "第一问"},
		[2]string{"assistant", "第一答"})
	// 追加思考/工具/终态事件（导出渲染覆盖）。
	svc.sessionPlane.Publish(session.BusThought, "e-1", map[string]any{"text": "先想一下"})
	svc.sessionPlane.Publish(session.BusToolCall, "e-1", map[string]any{"title": "运行 ls", "kind": "execute", "status": "in_progress"})
	svc.sessionPlane.Publish(session.BusRunEnded, "e-1", map[string]any{"stop_reason": "end_turn"})
	svc.sessionPlane.Publish(session.BusUserMessage, "e-2", map[string]any{"text": "另一会话"})

	for _, format := range []string{"markdown", "text", "json"} {
		resp, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: []string{"e-1"}, Format: format})
		if err != nil {
			t.Fatalf("ExportSessions(%s): %v", format, err)
		}
		if resp.Exported != 1 || len(resp.Files) != 1 {
			t.Fatalf("%s 导出数不符: %+v", format, resp)
		}
		f := resp.Files[0]
		if f.SessionID != "e-1" || f.Format != format || f.Bytes != len(f.Content) || f.Events == 0 {
			t.Fatalf("%s 文件元信息不符: %+v", format, f)
		}
		if !strings.HasPrefix(f.Filename, "导出用例-") || !strings.HasSuffix(f.Filename, map[string]string{"markdown": ".md", "text": ".txt", "json": ".json"}[format]) {
			t.Fatalf("%s 文件名不符: %s", format, f.Filename)
		}
		switch format {
		case "markdown":
			if !strings.Contains(f.Content, "# 导出用例") || !strings.Contains(f.Content, "## 用户") ||
				!strings.Contains(f.Content, "> 思考：先想一下") || !strings.Contains(f.Content, "- 工具 运行 ls（execute） [in_progress]") {
				t.Fatalf("markdown 正文不符:\n%s", f.Content)
			}
		case "text":
			if !strings.Contains(f.Content, "[用户] 第一问") || !strings.Contains(f.Content, "(思考) 先想一下") {
				t.Fatalf("text 正文不符:\n%s", f.Content)
			}
		case "json":
			var doc struct {
				Session map[string]any `json:"session"`
				Events  []any          `json:"events"`
			}
			if err := json.Unmarshal([]byte(f.Content), &doc); err != nil {
				t.Fatalf("json 正文不可解析: %v", err)
			}
			if doc.Session["id"] != "e-1" || len(doc.Events) == 0 {
				t.Fatalf("json 正文不符: %+v", doc)
			}
		}
	}

	// 批量：未知会话进 skipped（不静默丢弃），格式校验与上限校验明确报错。
	resp, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: []string{"e-1", "no-such"}, Format: "text"})
	if err != nil {
		t.Fatalf("批量导出: %v", err)
	}
	if resp.Exported != 1 || len(resp.Skipped) != 1 || resp.Skipped[0] != "no-such" {
		t.Fatalf("skipped 语义不符: %+v", resp)
	}
	if _, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: []string{"e-1"}, Format: "pdf"}); err == nil {
		t.Fatal("非法格式应报错")
	} else if ge, ok := err.(gerror.Error); !ok || ge.Code != 40001 || !strings.Contains(ge.Msg, "invalid_export_format") {
		t.Fatalf("格式错误码不符: %v", err)
	}
	if _, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: []string{}}); err == nil {
		t.Fatal("空 ids 应报错")
	}
	ids := make([]string, svcsystem.MaxExportIDs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("x-%d", i) // 互不相同：去重后仍超上限
	}
	if _, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: ids}); err == nil {
		t.Fatal("超上限应报错")
	} else if ge, ok := err.(gerror.Error); !ok || ge.Code != 40001 || !strings.Contains(ge.Msg, "export_too_many_ids") {
		t.Fatalf("上限错误码不符: %v", err)
	}
}

// TestExportMarkdownFileWritable 产物可直接落盘（文件名安全 + 内容非空）。
func TestExportMarkdownFileWritable(t *testing.T) {
	ctx := context.Background()
	svc, st := newSearchRig(t)
	// 标题含路径分隔符与控制字符 → 文件名应被净化。
	seedSearchSession(t, svc, st, "e-9", "a/b:c\n标题",
		[2]string{"user", "内容"})
	resp, err := exportSystemSvc(svc).ExportSessions(ctx, dtoexport.ExportReq{IDs: []string{"e-9"}, Format: "markdown"})
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Files[0]
	if strings.ContainsAny(f.Filename, "/\\:\n") {
		t.Fatalf("文件名未净化: %q", f.Filename)
	}
	path := filepath.Join(t.TempDir(), f.Filename)
	if err := os.WriteFile(path, []byte(f.Content), 0o600); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "内容") {
		t.Fatalf("落盘内容不符: %v %s", err, raw)
	}
}
