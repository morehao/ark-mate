package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
)

// openV22ForV23Test 造一个**真实的 v22 库**（完整跑 v1..v22，含 v22 的 Go 数据步骤）。
//
// 为什么不用 `openTestDB`（它直接落到最新版）：本步要读的正是**旧形态**——`messages` 还没有
// `ordinal`/`kind` 两列，`idx_messages_session` 还指向 `run_id`。对着已经改过的库测等于什么都没测。
func openV22ForV23Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v22.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 22)
	return s
}

// v23SessionA / v23SessionB 夹具会话 id（形状与生产一致：`ses_` + 26 位 Crockford 主体）。
const (
	v23SessionA = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QC"
	v23SessionB = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QD"
)

// TestMigrateV23MessagesGetOrdinal v23（消息白名单投影）：`messages` 补 `ordinal`/`kind`，
// 存量行的序号按 `(seq_lo, id)` **逐会话**从 1 起连续回填，索引按"整会话读"重排。
//
// 每一段判据都对应一种**静默**失败：
//
//	① 行数与正文/窗口逐行不变——本步只该加序号，不碰任何既有列；
//	② 序号每会话从 1 起、稠密、按 `(seq_lo, id)` 定序（同 `seq_lo` 由 id 决定先后——
//	   ULID 字典序 = 铸造序，所以这个定序是确定的，不是"库的返回顺序"）；
//	③ `UNIQUE(session_id, ordinal)` 真的建出来了（否则"序号唯一"只是回填时的巧合）；
//	④ 索引重排：`idx_messages_session` 现在指 `(session_id, seq_lo)`（**不再是 run_id**），
//	   `idx_messages_run` 接手按轮查；
//	⑤ 重建标记抬到 23（整会话重铺之后序号才真正稠密）；
//	⑥ 幂等：再跑一次 `Migrate` 不改任何序号。
func TestMigrateV23MessagesGetOrdinal(t *testing.T) {
	ctx := context.Background()
	s := openV22ForV23Test(t)

	at := "2026-05-06T10:00:00Z"
	for _, sid := range []string{v23SessionA, v23SessionB} {
		if err := s.db.Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
			VALUES (?, 'completed', ?, ?)`, sid, at, at).Error; err != nil {
			t.Fatalf("写会话夹具 %s: %v", sid, err)
		}
	}
	// A 四条：**有意乱序插入**（id 与 seq_lo 的顺序相反），且 `m-a2b`/`m-a2a` 同 seq_lo
	// 用来钉住"同序时按 id 定先后"。B 一条：序号**从 1 起**，不是接着 A 的号。
	type msgFix struct {
		id, session string
		lo, hi      int64
		text        string
	}
	msgs := []msgFix{
		{"m-a9", v23SessionA, 9, 11, "第四"},
		{"m-a2b", v23SessionA, 2, 2, "第二（id 大）"},
		{"m-a2a", v23SessionA, 2, 2, "第二（id 小）"},
		{"m-a0", v23SessionA, 0, 0, "第一"},
		{"m-b0", v23SessionB, 5, 5, "B 的第一条"},
	}
	for _, m := range msgs {
		if err := s.db.Exec(`INSERT INTO messages (id, session_id, run_id, role, text, seq_lo, seq_hi, created_at)
			VALUES (?,?, 'run_legacy_unrelated', 'assistant', ?, ?,?,?)`,
			m.id, m.session, m.text, m.lo, m.hi, at).Error; err != nil {
			t.Fatalf("写 message 夹具 %s: %v", m.id, err)
		}
	}
	// 钉住"我们确实从旧形态出发"：v22 库没有 ordinal/kind 列，`idx_messages_session` 指 run_id。
	for _, col := range []string{"ordinal", "kind"} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name=?`, col).
			Row().Scan(&n); err != nil || n != 0 {
			t.Fatalf("v22 的 messages 不该有 %s 列（夹具造错版本了？）: n=%d err=%v", col, n, err)
		}
	}
	var oldIdxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_messages_session'`).
		Row().Scan(&oldIdxSQL); err != nil {
		t.Fatalf("读 v22 的 idx_messages_session: %v", err)
	}
	if !strings.Contains(oldIdxSQL, "run_id") {
		t.Fatalf("v22 的 idx_messages_session 应指 (session_id, run_id)，实际 %q", oldIdxSQL)
	}

	// —— 跑 v23 ——
	migrateUpTo(t, s, 23)
	if v, err := s.SchemaVersion(ctx); err != nil || v != 23 {
		t.Fatalf("迁移后版本应为 23：v=%d err=%v", v, err)
	}

	// ①② 行数不变、内容不变、序号按 (seq_lo, id) 定序且每会话从 1 起。
	type msgRow struct {
		ID      string
		Text    string
		SeqLo   int64
		SeqHi   int64
		Ordinal int64
		Kind    string
	}
	var after []msgRow
	if err := s.db.Raw(`SELECT id, text, seq_lo, seq_hi, ordinal, kind FROM messages ORDER BY session_id, ordinal`).
		Scan(&after).Error; err != nil {
		t.Fatalf("读迁移后的 messages: %v", err)
	}
	if len(after) != len(msgs) {
		t.Fatalf("message 行数变了：%d → %d（本步只该加序号）", len(msgs), len(after))
	}
	// 期望：会话内按 (seq_lo, id) 排出的 id 序列 —— 序号必须是它的下标 + 1。
	byID := map[string]msgRow{}
	for _, m := range after {
		byID[m.ID] = m
	}
	wantOrder := []string{"m-a0", "m-a2a", "m-a2b", "m-a9", "m-b0"}
	wantOrdinal := []int64{1, 2, 3, 4, 1}
	for i, id := range wantOrder {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("迁移后少了 %s", id)
		}
		if got.Ordinal != wantOrdinal[i] {
			t.Fatalf("%s 的 ordinal 应为 %d（会话内按 (seq_lo, id) 从 1 数），得到 %d",
				id, wantOrdinal[i], got.Ordinal)
		}
		if got.Kind != "" {
			t.Fatalf("kind 迁移后应恒为空串（扩展位），%s 得到 %q", id, got.Kind)
		}
	}
	// 正文与窗口逐行不变。
	want := map[string]msgFix{}
	for _, m := range msgs {
		want[m.id] = m
	}
	for _, got := range after {
		w := want[got.ID]
		if got.Text != w.text || got.SeqLo != w.lo || got.SeqHi != w.hi {
			t.Fatalf("message %s 的正文/窗口被改了: %+v want %+v", got.ID, got, w)
		}
	}
	// 序号在会话内连续（1..N 无洞）：与"行数"和"最大序号"两边对齐。
	for _, sid := range []string{v23SessionA, v23SessionB} {
		var n, maxOrd int64
		if err := s.db.Raw(`SELECT COUNT(*), COALESCE(MAX(ordinal), 0) FROM messages WHERE session_id = ?`, sid).
			Row().Scan(&n, &maxOrd); err != nil {
			t.Fatalf("读 %s 的序号统计: %v", sid, err)
		}
		if maxOrd != n {
			t.Fatalf("%s 的序号不连续：%d 行而最大序号 %d（有洞）", sid, n, maxOrd)
		}
	}

	// ③④ 三条索引的形状与归属。
	var ordSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_messages_session_ordinal'`).
		Row().Scan(&ordSQL); err != nil {
		t.Fatalf("索引 idx_messages_session_ordinal 不存在: %v", err)
	}
	if !strings.HasPrefix(ordSQL, "CREATE UNIQUE INDEX") || !strings.Contains(ordSQL, "session_id, ordinal") {
		t.Fatalf("idx_messages_session_ordinal 应是 (session_id, ordinal) 上的 UNIQUE 索引，实际 %q", ordSQL)
	}
	var runIdxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_messages_run'`).
		Row().Scan(&runIdxSQL); err != nil {
		t.Fatalf("索引 idx_messages_run 不存在: %v", err)
	}
	if !strings.Contains(runIdxSQL, "(run_id)") {
		t.Fatalf("idx_messages_run 应指 (run_id)，实际 %q", runIdxSQL)
	}
	var newIdxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_messages_session'`).
		Row().Scan(&newIdxSQL); err != nil {
		t.Fatalf("读 idx_messages_session: %v", err)
	}
	if strings.Contains(newIdxSQL, "run_id") || !strings.Contains(newIdxSQL, "seq_lo") {
		t.Fatalf("idx_messages_session 应重指 (session_id, seq_lo)（不再是 run_id），实际 %q", newIdxSQL)
	}

	// ⑤ 重建标记（整会话重铺之后序号才真正稠密，所以必须抬到 23）。
	if v, ok, err := s.GetMeta(ctx, "read_models_rebuild"); err != nil || !ok || v != "23" {
		t.Fatalf("meta.read_models_rebuild 应写为 \"23\"：ok=%v value=%q err=%v", ok, v, err)
	}

	// ⑥ 幂等：再跑一次，序号一个都不动。
	type ordOnly struct {
		ID      string
		Ordinal int64
	}
	var before []ordOnly
	if err := s.db.Raw(`SELECT id, ordinal FROM messages ORDER BY id`).Scan(&before).Error; err != nil {
		t.Fatalf("读幂等前的序号: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	var afterIdem []ordOnly
	if err := s.db.Raw(`SELECT id, ordinal FROM messages ORDER BY id`).Scan(&afterIdem).Error; err != nil {
		t.Fatalf("读幂等后的序号: %v", err)
	}
	if len(before) != len(afterIdem) {
		t.Fatalf("重复迁移改了行数：%d → %d", len(before), len(afterIdem))
	}
	for i := range before {
		if before[i].ID != afterIdem[i].ID || before[i].Ordinal != afterIdem[i].Ordinal {
			t.Fatalf("重复迁移又改了一次序号：%+v → %+v", before[i], afterIdem[i])
		}
	}
}

// TestMigrateV23OnEmptyMessages v23 在**空 messages** 上必须照常完成：语句里任何
// "至少有一行"的假设（例如用首行定序）都会在这里红，两条新索引也必须照样建出来。
func TestMigrateV23OnEmptyMessages(t *testing.T) {
	ctx := context.Background()
	s := openV22ForV23Test(t)

	migrateUpTo(t, s, 23)
	seedSession(t, s, v23SessionA)

	for _, idx := range []string{"idx_messages_session_ordinal", "idx_messages_run", "idx_messages_session"} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("空库上也要建出 %s：n=%d err=%v", idx, n, err)
		}
	}
	// 空表上的直插照常可用（唯一索引不该把第一条挡住）。
	if err := s.db.Exec(`INSERT INTO messages (id, session_id, run_id, role, text, ordinal, seq_lo, seq_hi, created_at)
		VALUES ('m-first', ?, '', 'user', 'hi', 1, 0, 0, '2026-05-06T10:00:00Z')`, v23SessionA).Error; err != nil {
		t.Fatalf("空库上直插第一条: %v", err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != 23 {
		t.Fatalf("版本应为 23：v=%d err=%v", v, err)
	}
}

// TestMigrateV23UniqueIndexBlocksDuplicateOrdinal 反证：挡住"同一会话重号"的**只有那条唯一索引**。
//
// 做法与判据：迁移完成后往同一会话插两条 `ordinal = 1`——有索引时第二条报错；DROP 掉索引后
// 同一个动作成功、库里出现两条重号行。于是"序号唯一靠 DB 不变式"这句话有了可区分力：
// 删掉索引时别的断言仍然全绿，只有这一条会红。
func TestMigrateV23UniqueIndexBlocksDuplicateOrdinal(t *testing.T) {
	s := openV22ForV23Test(t)
	migrateUpTo(t, s, 23)
	seedSession(t, s, v23SessionA)

	ins := func(id string) error {
		return s.db.Exec(`INSERT INTO messages (id, session_id, run_id, role, text, ordinal, seq_lo, seq_hi, created_at)
			VALUES (?, ?, '', 'user', 'hi', 1, 0, 0, '2026-05-06T10:00:00Z')`, id, v23SessionA).Error
	}
	if err := ins("m-dup-1"); err != nil {
		t.Fatalf("首次插入: %v", err)
	}
	if err := ins("m-dup-2"); err == nil {
		t.Fatal("有唯一索引时第二条重号插入必须失败（否则「序号唯一」只是回填时的巧合）")
	}
	// 反证：没有索引时同一个动作会插出重号——所以上面那条断言拦的是**索引**，不是"恰好没撞"。
	if err := s.db.Exec(`DROP INDEX idx_messages_session_ordinal`).Error; err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}
	if err := ins("m-dup-3"); err != nil {
		t.Fatalf("无索引时的重号插入: %v", err)
	}
	var dup int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM messages WHERE session_id = ? AND ordinal = 1`, v23SessionA).
		Row().Scan(&dup); err != nil || dup != 2 {
		t.Fatalf("删掉唯一索引后应有两行重号（证明判据就是那条索引），得到 %d err=%v", dup, err)
	}
}
