package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/internal/object/objid"
)

// P3b-2b：本文件是 `manager/messages_search_test.go` 的域内那一半。
//
// manager 侧仍有一条同名的端到端用例（`TestSearchMessagesSQLite`，走 `Service` 装配链），
// 它验的是"装配接上了"；这里验的是**域自身**的两种数据面（sqlite 读模型 / transcript 回扫）
// 与错误出路——两边判据独立，不互相替代（这也是"编译通过 ≠ 装配接上"那条教训的另一面）。

// TestSearchMessagesTranscriptFallback memory 模式（无 store）：回扫 transcript，
// source 明确标注 transcript，锚点取事件位次。
func TestSearchMessagesTranscriptFallback(t *testing.T) {
	dir := t.TempDir()
	pl, _ := testPlane(t, dir)
	defer pl.CloseTranscripts()

	pl.RegisterSession(&SessionRec{ID: "t-1", Title: "内存会话"})
	pl.BeginTranscript("t-1", "stub", "/tmp", false)
	pl.Publish(BusUserMessage, "t-1", map[string]any{"text": "内存里的关键词 needle"})
	pl.Publish(BusMessage, "t-1", map[string]any{"text": "助手也提到 needle"})
	// ③ 文本增量攒批：断言前手动落盘（生产由 25ms 窗口 / 终态事件 / 阈值触发）。
	pl.FlushChunks()

	resp, err := pl.SearchMessages(context.Background(), "needle", 10)
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

// TestSearchMessagesSQLiteInDomain sqlite 模式：走 messages 读模型（角色不限）、
// 大小写不敏感、limit 截断与 has_more、空关键词明确 40001。
func TestSearchMessagesSQLiteInDomain(t *testing.T) {
	ctx := context.Background()
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	pl := NewPlane()
	pl.SetStore(st)
	pl.SetWorkspaceDisplay(func(workspaceID string) (string, string) { return workspaceID, "" })

	seed := func(id, title string, pairs ...[2]string) {
		t.Helper()
		now := time.Now().UTC()
		if err := st.UpsertSession(ctx, model.SessionEntity{
			ID: id, Title: title, Status: "completed", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertSession: %v", err)
		}
		pl.RegisterSession(&SessionRec{ID: id, Title: title, Status: SessionStatusCompleted, CreatedAt: now, UpdatedAt: now})
		msgs := make([]model.MessageEntity, 0, len(pairs))
		for i, pair := range pairs {
			seq, err := st.AppendEvent(ctx, objid.New(objid.Event), id, BusUserMessage, map[string]any{"text": pair[1]})
			if err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
			msgs = append(msgs, model.MessageEntity{
				ID: objid.New(objid.Message), SessionID: id,
				RunID: fmt.Sprintf("run-%s-%d", id, i), Role: pair[0],
				Text: pair[1], Ordinal: int64(i) + 1, SeqLo: seq, SeqHi: seq,
				CreatedAt: now.Add(time.Duration(i) * time.Second),
			})
		}
		if err := st.CreateMessages(ctx, msgs); err != nil {
			t.Fatalf("CreateMessages: %v", err)
		}
	}
	seed("c-1", "部署排查",
		[2]string{"user", "网关 502 怎么排查"},
		[2]string{"assistant", "先看 upstream 健康检查与超时配置"})
	seed("c-2", "无关会话", [2]string{"user", "今天天气不错"})

	resp, err := pl.SearchMessages(ctx, "502", 10)
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

	// 助手消息也命中（角色不限）+ 大小写不敏感。
	if resp2, err := pl.SearchMessages(ctx, "UPSTREAM", 10); err != nil {
		t.Fatalf("SearchMessages(assistant): %v", err)
	} else if resp2.Total != 1 || resp2.Hits[0].Role != "assistant" {
		t.Fatalf("助手消息应命中: %+v", resp2.Hits)
	}

	// limit 截断语义（多造一条命中，limit=1 时 has_more）。
	seed("c-3", "大写", [2]string{"user", "UPSTREAM 再问一次"})
	resp3, err := pl.SearchMessages(ctx, "upstream", 1)
	if err != nil {
		t.Fatalf("SearchMessages(limit): %v", err)
	}
	if resp3.Total != 1 || !resp3.HasMore {
		t.Fatalf("limit=1 应截断且 has_more: %+v", resp3)
	}

	// 空关键词 → 明确 40001。
	if _, err := pl.SearchMessages(ctx, "   ", 10); err == nil {
		t.Fatal("空关键词应报错")
	} else if !strings.Contains(err.Error(), "invalid_search_query") {
		t.Fatalf("错误码不符: %v", err)
	}
}
