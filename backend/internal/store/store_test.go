package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ctxT() context.Context { return context.Background() }

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedSession(t *testing.T, s *Store, id string) {
	t.Helper()
	sess := Session{
		ID: id, AgentID: "agent-opencode", RuntimeID: "opencode",
		Title: "", Status: "in_progress",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.UpsertSession(ctxT(), sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
}

// TestSchemaMigrated 空库打开自动迁移到 latest；重复打开幂等；版本正确。
func TestSchemaMigrated(t *testing.T) {
	s := openMem(t)
	v, err := s.SchemaVersion(ctxT())
	if err != nil || v != latestSchemaVersion() {
		t.Fatalf("schema 版本不符: v=%d err=%v", v, err)
	}
	// 幂等：再跑一次 migrate 不报错。
	if err := s.migrate(ctxT()); err != nil {
		t.Fatalf("re-migrate 应幂等: %v", err)
	}
}

// TestAppendEventSeqMonotonic AppendEvent 全局 seq 单调递增、LastSeq 联动、session.last_seq 触顶。
func TestAppendEventSeqMonotonic(t *testing.T) {
	s := openMem(t)
	seedSession(t, s, "s1")
	seedSession(t, s, "s2")

	s1, err := s.AppendEvent(ctxT(), "s1", "session.message", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	s2a, err := s.AppendEvent(ctxT(), "s2", "session.user_message", map[string]any{"text": "yo"})
	if err != nil {
		t.Fatalf("AppendEvent s2: %v", err)
	}
	s1b, err := s.AppendEvent(ctxT(), "s1", "turn.ended", map[string]any{})
	if err != nil {
		t.Fatalf("AppendEvent s1b: %v", err)
	}
	if !(s1 < s2a && s2a < s1b) {
		t.Fatalf("seq 应全局单调: %d %d %d", s1, s2a, s1b)
	}
	if last, _ := s.LastSeq(ctxT()); last != s1b {
		t.Fatalf("LastSeq 应为 %d, got %d", s1b, last)
	}
	// 会话水位：s1 应到 s1b；s2 到 s2a。
	for id, want := range map[string]int64{"s1": s1b, "s2": s2a} {
		sess, ok, err := s.GetSession(ctxT(), id)
		if err != nil || !ok {
			t.Fatalf("GetSession %s: ok=%v err=%v", id, ok, err)
		}
		if sess.LastSeq != want {
			t.Fatalf("session %s last_seq 应为 %d, got %d", id, want, sess.LastSeq)
		}
	}
}

// TestAppendEventUnknownSession FK：会话不存在时插入失败。
func TestAppendEventUnknownSession(t *testing.T) {
	s := openMem(t)
	if _, err := s.AppendEvent(ctxT(), "ghost", "session.message", map[string]any{}); err == nil {
		t.Fatal("未知会话应 FK 报错")
	}
}

// TestEventsAfterReplay events 回放：按 session + after 分页、hasMore 正确。
func TestEventsAfterReplay(t *testing.T) {
	s := openMem(t)
	seedSession(t, s, "s1")
	seedSession(t, s, "s2")
	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := s.AppendEvent(ctxT(), "s1", "session.message", map[string]any{"i": i})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		seqs = append(seqs, seq)
	}
	_, _ = s.AppendEvent(ctxT(), "s2", "session.message", map[string]any{}) // 其它会话不混入

	got, more, err := s.EventsAfter(ctxT(), "s1", seqs[0], 3)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(got) != 3 || !more {
		t.Fatalf("分页不符: len=%d more=%v", len(got), more)
	}
	if got[0].Seq != seqs[1] || got[2].Seq != seqs[3] {
		t.Fatalf("回放序不符: %+v", got)
	}
	tail, more2, err := s.EventsAfter(ctxT(), "s1", got[2].Seq, 3)
	if err != nil {
		t.Fatalf("EventsAfter tail: %v", err)
	}
	if len(tail) != 1 || more2 || tail[0].Seq != seqs[4] {
		t.Fatalf("末页不符: len=%d more=%v %+v", len(tail), more2, tail)
	}
}

// TestSessionCRUD upsert（last_seq 触顶不清零）、list 排序、meta 子集更新。
func TestSessionCRUD(t *testing.T) {
	s := openMem(t)
	base := Session{ID: "s1", AgentID: "a", RuntimeID: "r", Title: "t0",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.UpsertSession(ctxT(), base); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 覆盖 upsert 不能清零 last_seq。
	seedSession(t, s, "s1")
	if _, err := s.AppendEvent(ctxT(), "s1", "x", map[string]any{}); err != nil {
		t.Fatalf("append: %v", err)
	}
	low := base
	low.LastSeq = 0
	if err := s.UpsertSession(ctxT(), low); err != nil {
		t.Fatalf("upsert-low: %v", err)
	}
	sess, _, _ := s.GetSession(ctxT(), "s1")
	if sess.LastSeq == 0 {
		t.Fatal("覆盖 upsert 不应清零 last_seq")
	}

	pinned := true
	title := "新标题"
	if err := s.UpdateSessionMeta(ctxT(), "s1", SessionMetaPatch{Pinned: &pinned, Title: &title}); err != nil {
		t.Fatalf("update meta: %v", err)
	}
	sess, ok, _ := s.GetSession(ctxT(), "s1")
	if !ok || !sess.Pinned || sess.Title != "新标题" {
		t.Fatalf("meta 更新未生效: %+v ok=%v", sess, ok)
	}

	list, err := s.ListSessions(ctxT(), nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: n=%d err=%v", len(list), err)
	}
}

// TestMemoriesCRUD allow/reject 双侧、同键覆盖、逐条/整会话撤销。
func TestMemoriesCRUD(t *testing.T) {
	s := openMem(t)
	seedSession(t, s, "s1")
	if err := s.AddMemory(ctxT(), Memory{SessionID: "s1", Title: "跑脚本", Kind: "execute", Decision: "allow"}); err != nil {
		t.Fatalf("add allow: %v", err)
	}
	// 同键异侧覆盖为 reject，不新增行。
	if err := s.AddMemory(ctxT(), Memory{SessionID: "s1", Title: "跑脚本", Kind: "execute", Decision: "reject"}); err != nil {
		t.Fatalf("add reject: %v", err)
	}
	if err := s.AddMemory(ctxT(), Memory{SessionID: "s1", Title: "写文件", Kind: "write", Decision: "allow"}); err != nil {
		t.Fatalf("add allow2: %v", err)
	}
	list, err := s.ListMemories(ctxT(), "s1")
	if err != nil || len(list) != 2 {
		t.Fatalf("应 2 条记忆: n=%d err=%v %+v", len(list), err, list)
	}
	// 顺序按 created_at；同键行 decision 已是 reject。
	found := false
	for _, m := range list {
		if m.Title == "跑脚本" && m.Decision == "reject" {
			found = true
		}
	}
	if !found {
		t.Fatalf("同键覆盖未生效: %+v", list)
	}
	if ok, _ := s.DeleteMemory(ctxT(), "s1", "跑脚本", "execute"); !ok {
		t.Fatal("删除单条应命中")
	}
	if n, _ := s.ListMemories(ctxT(), "s1"); len(n) != 1 {
		t.Fatalf("删除后应剩 1: %+v", n)
	}
	if err := s.DeleteSessionMemories(ctxT(), "s1"); err != nil {
		t.Fatalf("一键撤销: %v", err)
	}
	if n, _ := s.ListMemories(ctxT(), "s1"); len(n) != 0 {
		t.Fatalf("一键撤销后应为空: %+v", n)
	}
}

// TestImportBatch 单事务导入：seq 全局分配、校验计数、marker 幂等、错配整体回滚。
func TestImportBatch(t *testing.T) {
	s := openMem(t)
	now := time.Now().UTC()
	item := ImportItem{
		Session: Session{ID: "s-x", AgentID: "agent-opencode", RuntimeID: "opencode", Title: "迁移会话",
			Status: "completed", CreatedAt: now, UpdatedAt: now},
		Events: []Event{
			{Type: "session.user_message", PayloadJSON: `{"text":"hi"}`},
			{Type: "session.message", PayloadJSON: `{"text":"yo"}`},
			{Type: "turn.ended", PayloadJSON: `{"stop_reason":"end_turn"}`},
		},
	}
	marker := "fp:test-dir-1"

	ev, files, err := s.ImportBatch(ctxT(), []ImportItem{item}, marker, 1, 3)
	if err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}
	if ev != 3 || files != 1 {
		t.Fatalf("导入统计不符: ev=%d files=%d", ev, files)
	}
	// 幂等：同 marker 再次导入 → ErrAlreadyImported。
	if _, _, err := s.ImportBatch(ctxT(), []ImportItem{item}, marker, 1, 3); !errors.Is(err, ErrAlreadyImported) {
		t.Fatalf("同 marker 应幂等跳过: %v", err)
	}
	// 新 marker（不同目录）继续导入会追加（不同会话）。
	item2 := item
	item2.Session.ID = "s-y"
	ev2, _, err := s.ImportBatch(ctxT(), []ImportItem{item2}, "fp:test-dir-2", 1, 3)
	if err != nil || ev2 != 3 {
		t.Fatalf("二次导入: ev=%d err=%v", ev2, err)
	}

	// 错配校验 → 整体回滚（事件数与全局 seq 均不变）。
	before, _ := s.EventCount(ctxT(), "")
	lastBefore, _ := s.LastSeq(ctxT())
	if _, _, err := s.ImportBatch(ctxT(), []ImportItem{item}, "fp:test-dir-3", 1, 999); err == nil {
		t.Fatal("错配计数应报错")
	}
	after, _ := s.EventCount(ctxT(), "")
	if before != after {
		t.Fatalf("失败导入应整体回滚: before=%d after=%d", before, after)
	}
	if got, _ := s.LastSeq(ctxT()); got != lastBefore {
		t.Fatalf("回滚后 seq 不应前进: got=%d want=%d", got, lastBefore)
	}
}
