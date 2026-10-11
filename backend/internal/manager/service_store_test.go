package manager

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoconversation"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestSQLiteLivePersistenceAcrossRestart 验证 P1 live 接线核心不变量：
//  1. publish 落库（events + conversation.last_seq + 元数据），bus 与 DB 共用同一全局号序；
//  2. markConversation/UpdateConversation/标题自动推导同步写库；
//  3. 重启恢复：新 Service + 新 bus 从 DB 载入会话并把事件原样注入（PublishAt），
//     旧游标可续传；后续新事件 seq 跨重启连续（不重叠、不倒退）。
func TestSQLiteLivePersistenceAcrossRestart(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()

	// —— 进程一：sqlite 模式活会话 ——
	st1 := testutil.OpenStore(t, dbPath)
	bus1 := eventbus.New()
	svc1 := NewService(reg, bus1)
	svc1.EnableStore(st1)

	svc1.registerConversation(&ConversationRec{ID: "s-live", AgentID: "agent-stub"})
	// 首个用户消息 → 标题自动推导（应同步写库）。
	eUser := svc1.publish(BusUserMessage, "s-live", map[string]any{"session_id": "s-live", "text": "计算 1+1"})
	// ③ 文本增量合并：publish 进缓冲并返回零值事件，号序在 flush 落库时才分配。
	svc1.publish(BusMessage, "s-live", map[string]any{"text": "答案是 2"})
	svc1.flushChunks()
	eMsgSeq := eUser.Seq + 1
	svc1.markConversation("s-live", ConversationStatusCompleted)
	pinned := true
	title := "用户改的标题"
	if _, err := svc1.UpdateConversation(ctx, "s-live", dtoconversation.ConversationPatch{Pinned: &pinned, Title: &title}); err != nil {
		t.Fatalf("UpdateConversation: %v", err)
	}
	if eMsgSeq != eUser.Seq+1 {
		t.Fatalf("live seq 应连续: %d %d", eUser.Seq, eMsgSeq)
	}
	if got, _ := st1.LastSeq(ctx); got != eMsgSeq {
		t.Fatalf("DB LastSeq=%d want %d", got, eMsgSeq)
	}
	sess1, ok, _ := st1.GetConversation(ctx, "s-live")
	if !ok || sess1.Status != ConversationStatusCompleted || sess1.LastSeq != eMsgSeq || !sess1.Pinned {
		t.Fatalf("DB 会话元数据未同步: %+v ok=%v", sess1, ok)
	}
	_ = st1.Close() // 释放文件句柄，模拟进程退出

	// —— 进程二：重启恢复 ——
	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	bus2 := eventbus.New()
	svc2 := NewService(reg, bus2)
	svc2.EnableStore(st2)

	n, injected, err := svc2.RestoreFromStore(ctx)
	if err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	if n != 1 || injected != 2 {
		t.Fatalf("恢复统计: conversations=%d events=%d", n, injected)
	}
	list, _ := svc2.ListConversations(false, "", "")
	if len(list) != 1 || list[0].Status != ConversationStatusCompleted || list[0].Title != "用户改的标题" || !list[0].Pinned {
		t.Fatalf("恢复后会话元数据不符: %+v", list)
	}
	// bus 按 DB 号序回放（旧游标可续传）：after=eUser.Seq → 只剩 eMsg。
	replay := bus2.EventsAfter(eUser.Seq)
	if len(replay) != 1 || replay[0].Seq != eMsgSeq {
		t.Fatalf("重启后回放应带 DB 号序: %+v", replay)
	}
	// 新事件 seq 跨重启连续（③ 合并：publish 进缓冲，flush 后才分配号序）。
	svc2.publish(BusMessage, "s-live", map[string]any{"text": "继续"})
	svc2.flushChunks()
	eNextSeq := eMsgSeq + 1
	if got, _ := st2.LastSeq(ctx); got != eNextSeq {
		t.Fatalf("跨重启 seq 应连续: got %d want %d", got, eNextSeq)
	}
	evs, more, err := st2.EventsAfter(ctx, "s-live", 0, 10)
	if err != nil || more || len(evs) != 3 {
		t.Fatalf("DB 事件应 3 条: n=%d more=%v err=%v", len(evs), more, err)
	}
	if evs[2].Seq != eNextSeq {
		t.Fatalf("DB 第三条 seq=%d want %d", evs[2].Seq, eNextSeq)
	}
}

// TestSQLiteRestoreAcrossConversations 恢复注入跨会话也必须全局 seq 升序
// （防止 PublishAt 遇小号回退取号导致乱序/丢事件）。
func TestSQLiteRestoreAcrossConversations(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	st := testutil.OpenStore(t, dbPath)
	svc := NewService(runtime.NewRegistry(), nil)
	svc.EnableStore(st)
	// 手动制造交错号序：s-a 先事件、s-b 后事件、s-a 再事件（ListConversations 按 updated_at 降序可能先 s-a 后 s-b）。
	svc.registerConversation(&ConversationRec{ID: "s-a", AgentID: "a"})
	svc.registerConversation(&ConversationRec{ID: "s-b", AgentID: "b"})
	_ = svc.publish(BusMessage, "s-a", map[string]any{"i": 1})
	_ = svc.publish(BusMessage, "s-b", map[string]any{"i": 1})
	_ = svc.publish(BusMessage, "s-a", map[string]any{"i": 2})
	svc.flushChunks() // ③ 落盘后再关库（否则缓冲里的增量随进程消失）
	_ = st.Close()

	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	bus2 := eventbus.New()
	svc2 := NewService(runtime.NewRegistry(), bus2)
	svc2.EnableStore(st2)
	if _, _, err := svc2.RestoreFromStore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	replay := bus2.EventsAfter(0)
	if len(replay) != 3 {
		t.Fatalf("注入应 3 条: %d", len(replay))
	}
	for i := 1; i < len(replay); i++ {
		if replay[i].Seq != replay[i-1].Seq+1 {
			t.Fatalf("注入非全局升序: %+v", replay)
		}
	}
	// 新事件接续最大号（③ 合并：先 flush 再查库）。
	svc2.publish(BusMessage, "s-a", map[string]any{"i": 3})
	svc2.flushChunks()
	wantSeq := replay[len(replay)-1].Seq + 1
	if got, _ := st2.LastSeq(ctx); got != wantSeq {
		t.Fatalf("接续号错误: got %d want %d", got, wantSeq)
	}
}
