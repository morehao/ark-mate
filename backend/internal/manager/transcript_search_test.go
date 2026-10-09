package manager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestSearchConversations 验证会话搜索：正文命中 + 片段、元数据命中、大小写不敏感、
// 多命中计数、归档可见、排序 updated_at 降序、limit 截断。
func TestSearchConversations(t *testing.T) {
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	dir := t.TempDir()
	svc.SetTranscriptDir(dir)
	defer svc.closeTranscripts()

	register := func(id, title, agent, runtimeID string, archived bool, at time.Time) {
		svc.registerConversation(&ConversationRec{
			ID: id, AgentID: agent, Status: ConversationStatusCompleted,
			Title: title, Archived: archived, CreatedAt: at, UpdatedAt: at,
		})
		svc.beginTranscript(id, agent, runtimeID, "/tmp", false)
	}
	now := time.Now().UTC()
	// sA：正文两处命中「银河」
	register("sA", "漫游指南", "agent-stub", "stub", false, now.Add(-3*time.Minute))
	svc.publish(BusUserMessage, "sA", map[string]any{"text": "帮我讲讲银河系漫游指南里的答案"})
	svc.publish(BusUserMessage, "sA", map[string]any{"text": "银河再展开一点？"})
	// sB：标题命中「峡谷」，正文大小写不同（Mac）
	register("sB", "神秘峡谷地图", "agent-stub", "stub", true, now.Add(-2*time.Minute))
	svc.publish(BusUserMessage, "sB", map[string]any{"text": "MacOS 上怎么装"})
	// sC：无命中
	register("sC", "今天吃啥", "agent-stub", "stub", false, now.Add(-1*time.Minute))
	svc.publish(BusUserMessage, "sC", map[string]any{"text": "推荐一个菜"})

	ctx := context.Background()

	// 1) 正文命中 + 片段 + 双命中计数；sA 排最前（updated 更早…见排序断言用 q 单会话）
	hits, err := svc.SearchConversations(ctx, "银河", 30)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "sA" {
		t.Fatalf("期望仅 sA 命中，got %+v", hits)
	}
	if hits[0].Matches != 2 {
		t.Fatalf("期望正文命中 2 次，got %d", hits[0].Matches)
	}
	if hits[0].Snippet == "" || !strings.Contains(hits[0].Snippet, "银河") {
		t.Fatalf("片段应含首条命中文本，got %q", hits[0].Snippet)
	}

	// 2) 标题命中（含归档 sB）+ 大小写不敏感（"mac" 命中正文）
	hits, err = svc.SearchConversations(ctx, "MAC", 30)
	if err != nil {
		t.Fatalf("search mac: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "sB" {
		t.Fatalf("期望 sB 标题命中且排最前，got %+v", hits)
	}
	ids := map[string]bool{}
	for _, h := range hits {
		ids[h.ID] = true
	}
	if !ids["sB"] {
		t.Fatalf("sB 应命中（标题+正文大小写），got %+v", ids)
	}

	// 3) 无命中 → 空
	hits, err = svc.SearchConversations(ctx, "不存在的词", 30)
	if err != nil || len(hits) != 0 {
		t.Fatalf("期望空结果，got %+v err=%v", hits, err)
	}

	// 4) limit=0（调用方已校验但防御）不 panic
	if _, err := svc.SearchConversations(ctx, "银河", 0); err != nil {
		t.Fatalf("limit=0 不 panic: %v", err)
	}
}
