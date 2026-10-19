package sessionruntime

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/internal/testutil"
)

// TestListSessionsArchivedFilter 归档是软删除：默认列表不展示，`archived=true` 才展示。
//
// 为什么单开一条：这个过滤位随读面搬进 `domain/session` 时**没有判据**——实测把
// `r.Archived != archived` 整个抽掉，全量 `go test ./...` **一条都不会红**。它的失效方式是静默的：
// 归档过的会话重新出现在默认列表里，用户看到的是"我删掉的又回来了"。
func TestListSessionsArchivedFilter(t *testing.T) {
	reg := testutil.StubRegistry()
	svc := NewService(reg, eventbus.New())
	svc.EnableStore(testutil.OpenStore(t, testutil.TempDBPath(t)))
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-live", Title: "在用的", RuntimeID: "stub"})
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-archived", Title: "归档的", RuntimeID: "stub", Archived: true})

	live, err := sessionPlaneOf(svc).ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions(false): %v", err)
	}
	if len(live) != 1 || live[0].ID != "s-live" {
		t.Fatalf("默认列表只该有未归档的那条: %+v", live)
	}
	archived, err := sessionPlaneOf(svc).ListSessions(true, "", "")
	if err != nil {
		t.Fatalf("ListSessions(true): %v", err)
	}
	if len(archived) != 1 || archived[0].ID != "s-archived" {
		t.Fatalf("`archived=true` 只该有归档的那条: %+v", archived)
	}
}

// sessionMaxSeq 读某会话当前的最高位次。
//
// v21 起"号序到哪了"**只能按会话问**（全局水位那行 meta 已删）：所以这里走会话自己的
// events（`before=-1` = 取末尾一页）而不是某个全局计数。
func sessionMaxSeq(t *testing.T, st *store.Store, sessionID string) int64 {
	t.Helper()
	evs, _, err := st.EventsBefore(context.Background(), sessionID, -1, 1)
	if err != nil {
		t.Fatalf("读会话最高位次: %v", err)
	}
	if len(evs) == 0 {
		t.Fatalf("会话 %s 没有任何事件", sessionID)
	}
	return evs[0].Seq
}

// TestSQLiteLivePersistenceAcrossRestart 验证 P1 live 接线核心不变量：
//  1. publish 落库（events + session.last_seq + 元数据），bus 与 DB 共用同一套**会话内**位次；
//  2. markSession/UpdateSession/标题自动推导同步写库；
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

	svc1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-live"})
	// 首个用户消息 → 标题自动推导（应同步写库）。
	eUser := svc1.sessionPlane.Publish(session.BusUserMessage, "s-live", map[string]any{"session_id": "s-live", "text": "计算 1+1"})
	// ③ 文本增量合并：publish 进缓冲并返回零值事件，号序在 flush 落库时才分配。
	svc1.sessionPlane.Publish(session.BusMessage, "s-live", map[string]any{"text": "答案是 2"})
	svc1.sessionPlane.FlushChunks()
	eMsgSeq := eUser.Seq + 1
	svc1.sessionPlane.MarkSession("s-live", session.SessionStatusCompleted)
	pinned := true
	title := "用户改的标题"
	if _, err := metaSvc(svc1).UpdateSession(ctx, "s-live", dtosession.SessionPatch{Pinned: &pinned, Title: &title}); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if eMsgSeq != eUser.Seq+1 {
		t.Fatalf("live seq 应连续: %d %d", eUser.Seq, eMsgSeq)
	}
	if got := sessionMaxSeq(t, st1, "s-live"); got != eMsgSeq {
		t.Fatalf("DB 会话位次=%d want %d", got, eMsgSeq)
	}
	sess1, ok, _ := st1.GetSession(ctx, "s-live")
	if !ok || sess1.Status != session.SessionStatusCompleted || sess1.LastSeq != eMsgSeq || !sess1.Pinned {
		t.Fatalf("DB 会话元数据未同步: %+v ok=%v", sess1, ok)
	}
	_ = st1.Close() // 释放文件句柄，模拟进程退出

	// —— 进程二：重启恢复 ——
	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	bus2 := eventbus.New()
	svc2 := NewService(reg, bus2)
	svc2.EnableStore(st2)

	n, injected, err := svc2.SessionPlane().Restore(ctx)
	if err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	if n != 1 || injected != 2 {
		t.Fatalf("恢复统计: sessions=%d events=%d", n, injected)
	}
	list, _ := sessionPlaneOf(svc2).ListSessions(false, "", "")
	if len(list) != 1 || list[0].Status != session.SessionStatusCompleted || list[0].Title != "用户改的标题" || !list[0].Pinned {
		t.Fatalf("恢复后会话元数据不符: %+v", list)
	}
	// bus 按 DB 位次回放（旧游标可续传）：after=eUser.Seq → 只剩 eMsg。
	replay := bus2.EventsAfter("s-live", eUser.Seq)
	if len(replay) != 1 || replay[0].Seq != eMsgSeq {
		t.Fatalf("重启后回放应带 DB 号序: %+v", replay)
	}
	// 新事件 seq 跨重启连续（③ 合并：publish 进缓冲，flush 后才分配号序）。
	svc2.sessionPlane.Publish(session.BusMessage, "s-live", map[string]any{"text": "继续"})
	svc2.sessionPlane.FlushChunks()
	eNextSeq := eMsgSeq + 1
	if got := sessionMaxSeq(t, st2, "s-live"); got != eNextSeq {
		t.Fatalf("跨重启 seq 应连续: got %d want %d", got, eNextSeq)
	}
	evs, more, err := st2.EventsAfter(ctx, "s-live", -1, 10)
	if err != nil || more || len(evs) != 3 {
		t.Fatalf("DB 事件应 3 条: n=%d more=%v err=%v", len(evs), more, err)
	}
	if evs[2].Seq != eNextSeq {
		t.Fatalf("DB 第三条 seq=%d want %d", evs[2].Seq, eNextSeq)
	}
}

// TestSQLiteRestoreAcrossSessions 恢复注入必须**每个会话各自有序**：
// v21 起位次只在会话内可比，所以恢复按 `(session_id, seq)` 取（组内升序），
// 两个会话各自从 0 起。跨会话的相对顺序不承载语义（旧版本要求"全局升序"是因为号是全局的）。
func TestSQLiteRestoreAcrossSessions(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	st := testutil.OpenStore(t, dbPath)
	svc := NewService(runtime.NewRegistry(), nil)
	svc.EnableStore(st)
	// 手动制造交错号序：s-a 先事件、s-b 后事件、s-a 再事件（ListSessions 按 updated_at 降序可能先 s-a 后 s-b）。
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-a"})
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-b"})
	_ = svc.sessionPlane.Publish(session.BusMessage, "s-a", map[string]any{"i": 1})
	_ = svc.sessionPlane.Publish(session.BusMessage, "s-b", map[string]any{"i": 1})
	_ = svc.sessionPlane.Publish(session.BusMessage, "s-a", map[string]any{"i": 2})
	svc.sessionPlane.FlushChunks() // ③ 落盘后再关库（否则缓冲里的增量随进程消失）
	_ = st.Close()

	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	bus2 := eventbus.New()
	svc2 := NewService(runtime.NewRegistry(), bus2)
	svc2.EnableStore(st2)
	if _, _, err := svc2.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	replay := bus2.EventsAll()
	if len(replay) != 3 {
		t.Fatalf("注入应 3 条: %d", len(replay))
	}
	// 组内升序 + 每会话独立从 0 起（s-a 两条 0/1，s-b 一条 0）。
	byAgg := map[string][]int64{}
	for _, e := range replay {
		byAgg[e.AggregID] = append(byAgg[e.AggregID], e.Seq)
	}
	for agg, seqs := range byAgg {
		for i := 1; i < len(seqs); i++ {
			if seqs[i] != seqs[i-1]+1 {
				t.Fatalf("聚合 %s 的注入位次不连续: %v", agg, seqs)
			}
		}
		if seqs[0] != 0 {
			t.Fatalf("聚合 %s 的注入位次应从 0 起: %v", agg, seqs)
		}
	}
	if len(byAgg["s-a"]) != 2 || len(byAgg["s-b"]) != 1 {
		t.Fatalf("每会话条数不符: %v", byAgg)
	}
	// 新事件接续**本会话**最大号（③ 合并：先 flush 再查库）。
	svc2.sessionPlane.Publish(session.BusMessage, "s-a", map[string]any{"i": 3})
	svc2.sessionPlane.FlushChunks()
	if got, want := sessionMaxSeq(t, st2, "s-a"), int64(2); got != want {
		t.Fatalf("接续号错误: got %d want %d", got, want)
	}
}

// TestRestoreRebuildsStaleReadModels 重启恢复必须**消费**迁移留下的读模型重建标记：
// 位次口径换了之后，库里那份 `messages`（窗口列按旧全局位次算的）已经不可信。
//
// 为什么这条必须有判据：整条链（迁移写标记 → 启动读标记 → 重建 → 清标记）里**任何一段断掉
// 都是静默的**——库照样能读、界面照样显示，只是每条消息的窗口指向别的轮次事件（正文/时间线
// 张冠李戴），而没有任何一条别的测试会红。标记若只写不读，甚至不会有任何运行时信号。
func TestRestoreRebuildsStaleReadModels(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()

	// —— 进程一：跑一轮（用户消息 + 终态），让 runs/messages 由事件物化出来 ——
	st1 := testutil.OpenStore(t, dbPath)
	svc1 := NewService(testutil.StubRegistry(), eventbus.New())
	svc1.EnableStore(st1)
	svc1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-rebuild"})
	runID, _, _ := beginRun(t, svc1, "s-rebuild", "重建我")
	endRun(svc1, "s-rebuild", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})
	svc1.sessionPlane.FlushChunks()
	msgs, err := st1.ListMessages(ctx, "s-rebuild")
	if err != nil || len(msgs) == 0 {
		t.Fatalf("夹具应先物化出 messages：n=%d err=%v", len(msgs), err)
	}
	// 模拟"旧口径留下的读模型"：把窗口列改成一个新口径下不可能出现的值、正文改成哨兵串。
	// 只改窗口列也够判据，但正文哨兵让失败信息一眼看得出"这份是被改过的旧行"。
	if err := st1.DB().Exec(`UPDATE messages SET text = 'STALE', seq_lo = 4242, seq_hi = 4242`).Error; err != nil {
		t.Fatalf("伪造旧读模型: %v", err)
	}
	// 迁移会把标记写成刚跑过的版本号（这里手工写，等价于 v21 那一步）。
	if err := st1.SetMeta(ctx, "read_models_rebuild", "21"); err != nil {
		t.Fatalf("写重建标记: %v", err)
	}
	_ = st1.Close()

	// —— 进程二：重启恢复 ——
	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	svc2 := NewService(testutil.StubRegistry(), eventbus.New())
	svc2.EnableStore(st2)
	if _, _, err := svc2.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}

	rebuilt, err := st2.ListMessages(ctx, "s-rebuild")
	if err != nil || len(rebuilt) == 0 {
		t.Fatalf("重建后 messages 不该为空：n=%d err=%v", len(rebuilt), err)
	}
	for _, m := range rebuilt {
		if m.Text == "STALE" {
			t.Fatalf("旧读模型的行还在（重建没跑）：%+v", m)
		}
		if m.SeqLo == 4242 || m.SeqHi == 4242 {
			t.Fatalf("旧窗口列还在（重建没跑）：%+v", m)
		}
		// 新口径的窗口必须落在**会话内位次**区间里（本会话 3 条事件：0=run.started、
		// 1=user_message、2=run.ended；窗口从 0 起算——它由开轮行给出，不再"裁到用户消息"）。
		if m.SeqLo < 0 || m.SeqHi > 2 || m.SeqLo > m.SeqHi {
			t.Fatalf("重建后的窗口不在会话内位次区间内：%+v", m)
		}
	}
	// 标记必须被消费掉（留着它 = 每次启动都白重建一遍，且掩盖"重建其实失败了"这件事）。
	if v, ok, err := st2.GetMeta(ctx, "read_models_rebuild"); err != nil || ok {
		t.Fatalf("重建成功后标记应被清掉：ok=%v value=%q err=%v", ok, v, err)
	}
}

// TestFirstUserMessageDerivesTitleInMemory 首条用户消息推导出的标题必须**同时**落内存与库。
//
// 为什么单开一条：这条路径曾经**没有任何判据**（`derive_title_test.go` 只测纯函数）。于是
// P3b-1b-ii 把 `s.sessions[id]` 换成返回**副本**的 `Lookup` 之后，`r.Title = derived` 改的是
// 副本——库里写上了、内存里永远空着（列表读内存 ⇒ 界面看不到标题，重启后才回来），
// 而当时全量测试**一条都不红**。判据就钉这两侧（内存镜像 + 库），漏一侧都要红。
func TestFirstUserMessageDerivesTitleInMemory(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.StubRegistry(), eventbus.New())
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	svc.EnableStore(st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-title", RuntimeID: "stub"})

	svc.sessionPlane.Publish(session.BusUserMessage, "s-title", map[string]any{"session_id": "s-title", "text": "第一条用户消息"})

	list, err := sessionPlaneOf(svc).ListSessions(false, "", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessions: n=%d err=%v", len(list), err)
	}
	if list[0].Title != "第一条用户消息" {
		t.Fatalf("内存镜像里应已有推导出的标题（列表读的是它）: %q", list[0].Title)
	}
	rows, err := st.ListSessions(ctx, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("库里应有 1 条: n=%d err=%v", len(rows), err)
	}
	if rows[0].Title != "第一条用户消息" {
		t.Fatalf("库里也应落上标题: %q", rows[0].Title)
	}
}
