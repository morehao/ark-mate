package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/object/objid"
)

// openV20ForV21Test 造一个**真实的 v20 库**（完整跑 v1..v20，含 v12–v20 的全部 Go 数据步骤），
// 事件表还是"seq 是**全局**位次、`idx_events_seq` 唯一"的那一版。
//
// 为什么不用 `openTestDB` + 手改版本号：v21 要读的是**旧位次**（`runs.first_seq` /
// `messages.seq_lo` 里存的都是全局 seq），把已经重编号过的库当成 v20 用，测的就不是迁移——
// 那正是本步最容易写错的地方（映射只对旧位次成立）。
func openV20ForV21Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v20.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 20)
	return s
}

// v21SessionA / v21SessionB 夹具会话 id（形状与生产一致：`ses_` + 26 位 Crockford 主体）。
const (
	v21SessionA = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1QA"
	v21SessionB = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1QB"
)

// TestMigrateV21RenumbersSeqPerSession v21（位次回归每会话）：`events.seq` 从全局号序改成
// **会话内**位次（0 起、稠密），并同步把读模型窗口列从旧位次映射到新位次。
//
// 为什么这条必须用"真实 v20 库 + **两个会话交错**的真行"来测：单会话时"全局位次"与
// "会话内位次"是同一串数（1,2,3… vs 0,1,2… 只差一个常数），任何实现都看起来对；两个会话交错
// 时才分得清（A 拿到 0,1,2 而 B 也拿到 0,1,2）。**错位的窗口列不报错、不违反约束**——它只是
// 让每条 run 指向别的轮次事件，所以每一段判据都对应一种静默失败：
//
//	① 行数与身份（id）集合不变——重编号不是重建，一行都不该丢；
//	② 每个会话的位次稠密、从 0 起、且**相对顺序不变**（按旧 seq 的顺序 = 按新 seq 的顺序）；
//	③ 窗口列落在**新**位次区间里（`runs` / `messages`）；这条是顺序陷阱的判据：把 ② 的
//	   UPDATE 排在重建之后，窗口列会整体错位而本条必红；
//	④ `event_sequence` 回填 == 每会话最大位次（新事件的起点）；
//	⑤ `sessions.last_seq` 按会话内位次对齐；
//	⑥ 全局计数键 `events_last_seq` 被删、`read_models_rebuild` 被写上；
//	⑦ 迁移后**继续追加**：新事件的位次接着每会话的最大值走（取号器回填没做对 → 重号 → UNIQUE 咬人）；
//	⑧ 幂等：再跑一次 `Migrate` 不报错、不动数据。
func TestMigrateV21RenumbersSeqPerSession(t *testing.T) {
	ctx := context.Background()
	s := openV20ForV21Test(t)

	at := "2026-04-05T10:00:00Z"
	// 会话行用**裸 SQL** 的少数列（其余有默认值）：`agent_id` 已在 v19 出列，走 DAO 会按今天的
	// 实体拼出多余的列名。
	for _, sid := range []string{v21SessionA, v21SessionB} {
		if err := s.db.Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
			VALUES (?, 'completed', ?, ?)`, sid, at, at).Error; err != nil {
			t.Fatalf("写会话夹具 %s: %v", sid, err)
		}
	}
	// **交错**的全局位次：A 拿 1/3/5，B 拿 2/4/6（`idx_events_seq` 在 v20 是全局唯一，交错合法）。
	// 迁移后两边都应是 0/1/2——"每会话从 0 起"这句话只有这样才被真的测到。
	type ev struct {
		globalSeq int64
		session   string
		typ       string
		text      string
		id        string
	}
	events := []ev{
		{1, v21SessionA, "session.created", "", objid.New(objid.Event)},
		{2, v21SessionB, "session.created", "", objid.New(objid.Event)},
		{3, v21SessionA, "session.user_message", "a-1", objid.New(objid.Event)},
		{4, v21SessionB, "session.user_message", "b-1", objid.New(objid.Event)},
		{5, v21SessionA, "run.ended", "", objid.New(objid.Event)},
		{6, v21SessionB, "run.ended", "", objid.New(objid.Event)},
	}
	for _, e := range events {
		body := `{}`
		if e.text != "" {
			body = `{"text":"` + e.text + `"}`
		}
		if err := s.db.Exec(`INSERT INTO events (id, seq, session_id, type, payload_json, created_at)
			VALUES (?,?,?,?,?,?)`, e.id, e.globalSeq, e.session, e.typ, body, at).Error; err != nil {
			t.Fatalf("写事件夹具 seq=%d: %v", e.globalSeq, err)
		}
	}
	// 读模型窗口列：**存的是旧全局位次**。
	//   A 的 run：全局 3..5 → 新 1..2；B 的 run：全局 2..4 → 新 0..1。
	//   另加一条"没有窗口"的畸形行（first_seq=last_seq=0）：映射后必须仍是 0（`COUNT(seq<0)=0`）。
	//   它同时是"加了 `WHERE last_seq > 0` 守卫就会静默跳过"的那个反例。
	runs := []struct {
		id, session         string
		firstSeq, lastSeq   int64
		wantFirst, wantLast int64
	}{
		{"r-a", v21SessionA, 3, 5, 1, 2},
		{"r-b", v21SessionB, 2, 4, 0, 1},
		{"r-a-degenerate", v21SessionA, 0, 0, 0, 0},
	}
	for _, r := range runs {
		if err := s.db.Exec(`INSERT INTO runs (id, session_id, first_seq, last_seq, started_at)
			VALUES (?,?,?,?,?)`, r.id, r.session, r.firstSeq, r.lastSeq, at).Error; err != nil {
			t.Fatalf("写 run 夹具 %s: %v", r.id, err)
		}
	}
	// messages 同理：A 的窗口 1..5 → 0..2；B 的窗口 2..4 → 0..1。
	msgs := []struct {
		id, session    string
		lo, hi         int64
		wantLo, wantHi int64
	}{
		{"m-a", v21SessionA, 1, 5, 0, 2},
		{"m-b", v21SessionB, 2, 4, 0, 1},
	}
	for _, m := range msgs {
		if err := s.db.Exec(`INSERT INTO messages (id, session_id, run_id, role, seq_lo, seq_hi, created_at)
			VALUES (?,?,?, 'assistant', ?,?,?)`, m.id, m.session, "r-x", m.lo, m.hi, at).Error; err != nil {
			t.Fatalf("写 message 夹具 %s: %v", m.id, err)
		}
	}
	// 字面量（v21 起仓里没有这个常量）：这一行正是 v21 要删掉的东西。
	if err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('events_last_seq', '6')`).Error; err != nil {
		t.Fatalf("写事件水位: %v", err)
	}
	// 迁移前：位次还是**全局**唯一（钉住"我们确实从旧形态出发"）。
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_seq'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("v20 库应有 idx_events_seq（夹具造错了版本？）: n=%d err=%v", n, err)
	}

	// —— 跑 v21（与 `Migrate` 同一条 statements 路径）。
	migrateUpTo(t, s, 21)
	if v, err := s.SchemaVersion(ctx); err != nil || v != 21 {
		t.Fatalf("迁移后版本应为 21：v=%d err=%v", v, err)
	}

	// ① 行数与身份集合不变（重编号不是重建）。
	type row struct {
		ID, SessionID, Type, PayloadJSON, CreatedAt string
		Seq                                         int64
	}
	var after []row
	if err := s.db.Raw(`SELECT id, session_id, seq, type, payload_json, created_at
		FROM events ORDER BY session_id, seq`).Scan(&after).Error; err != nil {
		t.Fatalf("读迁移后的 events: %v", err)
	}
	if len(after) != len(events) {
		t.Fatalf("事件行数变了：%d → %d（重编号不该丢行）", len(events), len(after))
	}
	ids := map[string]bool{}
	for _, r := range after {
		ids[r.ID] = true
		if r.CreatedAt != at {
			t.Fatalf("事件 %s 的 created_at 变了：%q", r.ID, r.CreatedAt)
		}
	}
	for _, e := range events {
		if !ids[e.id] {
			t.Fatalf("事件身份 %s 在迁移后消失了（重编号必须逐行保留 id）", e.id)
		}
	}

	// ② 每会话位次稠密、从 0 起，且相对顺序不变。
	want := map[string][]int64{v21SessionA: {0, 1, 2}, v21SessionB: {0, 1, 2}}
	got := map[string][]int64{}
	for _, r := range after {
		got[r.SessionID] = append(got[r.SessionID], r.Seq)
	}
	for sid, w := range want {
		if len(got[sid]) != len(w) {
			t.Fatalf("会话 %s 的事件数变了：%v", sid, got[sid])
		}
		for i := range w {
			if got[sid][i] != w[i] {
				t.Fatalf("会话 %s 的位次应为 %v（0 起稠密），实际 %v", sid, w, got[sid])
			}
		}
	}
	// 顺序不变：A 的旧 3（user_message）现在是 1，旧 5（run.ended）现在是 2。
	var seen []string
	if err := s.db.Raw(`SELECT type FROM events WHERE session_id = ? ORDER BY seq`, v21SessionA).
		Scan(&seen).Error; err != nil {
		t.Fatalf("读 A 的类型序: %v", err)
	}
	if strings.Join(seen, ",") != "session.created,session.user_message,run.ended" {
		t.Fatalf("分会话重编号必须**保持相对顺序**，A 的实际类型序 %v", seen)
	}

	// ③ 窗口列落在**新**位次区间里。
	for _, r := range runs {
		var f, l int64
		if err := s.db.Raw(`SELECT first_seq, last_seq FROM runs WHERE id = ?`, r.id).Row().Scan(&f, &l); err != nil {
			t.Fatalf("读 run %s: %v", r.id, err)
		}
		if f != r.wantFirst || l != r.wantLast {
			t.Fatalf("run %s 的窗口应映射到 (%d,%d)（旧 (%d,%d)），实际 (%d,%d)\n"+
				"（映射用旧位次算——排在重建之后会让它整体错位，且不报任何错）",
				r.id, r.wantFirst, r.wantLast, r.firstSeq, r.lastSeq, f, l)
		}
	}
	for _, m := range msgs {
		var lo, hi int64
		if err := s.db.Raw(`SELECT seq_lo, seq_hi FROM messages WHERE id = ?`, m.id).Row().Scan(&lo, &hi); err != nil {
			t.Fatalf("读 message %s: %v", m.id, err)
		}
		if lo != m.wantLo || hi != m.wantHi {
			t.Fatalf("message %s 的窗口应映射到 (%d,%d)（旧 (%d,%d)），实际 (%d,%d)",
				m.id, m.wantLo, m.wantHi, m.lo, m.hi, lo, hi)
		}
	}

	// ④ `event_sequence` 回填 = 每会话最大位次。
	type seqRow struct {
		SessionID string
		Seq       int64
	}
	var seqs []seqRow
	if err := s.db.Raw(`SELECT session_id, seq FROM event_sequence ORDER BY session_id`).Scan(&seqs).Error; err != nil {
		t.Fatalf("读 event_sequence: %v", err)
	}
	if len(seqs) != 2 {
		t.Fatalf("event_sequence 应有 2 行（每会话一行），实际 %+v", seqs)
	}
	for _, r := range seqs {
		if r.Seq != 2 {
			t.Fatalf("会话 %s 的计数器应为 2（该会话最大位次），实际 %d", r.SessionID, r.Seq)
		}
	}

	// ⑤ `sessions.last_seq` 按会话内位次对齐。
	var lastSeq int64
	if err := s.db.Raw(`SELECT last_seq FROM sessions WHERE id = ?`, v21SessionA).Row().Scan(&lastSeq); err != nil {
		t.Fatalf("读 sessions.last_seq: %v", err)
	}
	if lastSeq != 2 {
		t.Fatalf("sessions.last_seq 应为 2（会话内最大位次），实际 %d", lastSeq)
	}

	// ⑥ 全局计数键退场 + 读模型重建标记。
	if _, ok, err := s.GetMeta(ctx, "events_last_seq"); err != nil || ok {
		t.Fatalf("meta.events_last_seq 必须被删除：ok=%v err=%v", ok, err)
	}
	if v, ok, err := s.GetMeta(ctx, "read_models_rebuild"); err != nil || !ok || v != "21" {
		t.Fatalf("meta.read_models_rebuild 应写为 \"21\"：ok=%v value=%q err=%v", ok, v, err)
	}

	// ⑦ 迁移后继续追加：位次接着每会话的最大值走（B 的下一条是 3，而不是接着 A 的号）。
	gotSeq, err := s.AppendEvent(ctx, objid.New(objid.Event), v21SessionB, "session.message",
		map[string]any{"text": "after-migrate"})
	if err != nil {
		t.Fatalf("迁移后追加事件: %v", err)
	}
	if gotSeq != 3 {
		t.Fatalf("迁移后 B 的下一条位次应为 3（接着该会话最大值），实际 %d——"+
			"计数器回填漏了这个会话，或者取号又退回全局 MAX", gotSeq)
	}

	// ⑧ 幂等。
	before, _ := s.EventCount(ctx, "")
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	if again, _ := s.EventCount(ctx, ""); again != before {
		t.Fatalf("重复迁移改了数据：before=%d after=%d", before, again)
	}

	// 反证（人工执行过一次，记在这里当作判据的"有区分力"证据）：把 ③ 的
	// `ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq) - 1` 换成恒等 `seq`，
	// ② 立刻红（A 的位次会是 1/3/5）；把 ② 的两条 UPDATE 挪到重建之后，③ 立刻红
	// （窗口列会按新位次再映射一次：A 的 run 变成 1..2 → 1..1 之类）。
}

// TestMigrateV21OnEmptyEvents v21 在**空 events** 上必须照常完成，且计数器为空（不是一行 0）。
//
// 为什么单独一条：全新库正是空表，而 `INSERT … SELECT session_id, MAX(seq) … GROUP BY session_id`
// 在空表上不插任何行——`event_sequence` 于是**没有行**，"第一条事件 seq = 0"靠的是 upsert 的
// `ON CONFLICT DO UPDATE`（首次插入 0）而不是"预建一行 0"。把这件事写死成断言，才挡得住
// "顺手给每个会话插一行 0" 那种实现（它会让第一条事件的位次变成 1）。
func TestMigrateV21OnEmptyEvents(t *testing.T) {
	ctx := context.Background()
	s := openV20ForV21Test(t)

	migrateUpTo(t, s, 21)

	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM event_sequence`).Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("空表的 event_sequence 不该有行（计数器是「用过才存在」的）：n=%d err=%v", n, err)
	}
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_seq'`).
		Row().Scan(&n); err != nil || n != 0 {
		t.Fatalf("v21 后不应再有 idx_events_seq：n=%d err=%v", n, err)
	}
	var idxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_events_session'`).
		Row().Scan(&idxSQL); err != nil {
		t.Fatalf("读 idx_events_session: %v", err)
	}
	if !strings.HasPrefix(idxSQL, "CREATE UNIQUE INDEX") {
		t.Fatalf("v21 后 idx_events_session 必须是 UNIQUE，实际 %q", idxSQL)
	}

	// 空库上的**第一条**事件位次是 0（不是 1）。
	at := "2026-04-05T10:00:00Z"
	if err := s.db.Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
		VALUES (?, 'completed', ?, ?)`, v21SessionA, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}
	seq, err := s.AppendEvent(ctx, objid.New(objid.Event), v21SessionA, "session.created", map[string]any{})
	if err != nil {
		t.Fatalf("追加首条事件: %v", err)
	}
	if seq != 0 {
		t.Fatalf("会话的第一条事件位次必须是 0（`session.created` 就是第 0 条），实际 %d", seq)
	}
	// 且 `sessions.last_seq` 也跟着走到 0（"0 是没有水位"与"第 0 条"必须能分开）。
	var lastSeq int64
	if err := s.db.Raw(`SELECT last_seq FROM sessions WHERE id = ?`, v21SessionA).Row().Scan(&lastSeq); err != nil {
		t.Fatalf("读 last_seq: %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("首条事件后 sessions.last_seq 应为 0，实际 %d", lastSeq)
	}
}
