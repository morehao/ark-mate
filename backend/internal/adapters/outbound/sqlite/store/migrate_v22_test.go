package store

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/run"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
)

// openV21ForV22Test 造一个**真实的 v21 库**（完整跑 v1..v21，含 v21 的 Go 数据步骤）。
//
// 为什么不用 `openTestDB`（它直接落到 v22）：本步要读的正是**旧形态**——`runs.id` 还是派生串
// `r-<session>-<first_seq>`，`inputs` 还没有 `seq` 列。对着已经重铸过的库测，等于什么都没测。
func openV21ForV22Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v21.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 21)
	return s
}

// v22SessionA / v22SessionB 夹具会话 id（形状与生产一致：`ses_` + 26 位 Crockford 主体）。
const (
	v22SessionA = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QA"
	v22SessionB = "ses_01J9Z6Q0M7X2V8K4B3N5TRW2QB"
)

// TestMigrateV22RunsBecomeEntities v22（轮次实体化）：`runs.id` 从派生串重铸成 `run_<ULID>`，
// 指向它的 `messages.run_id` **同事务**改指；`inputs` 补会话内位次 `seq`；两组唯一约束落库。
//
// 每一段判据都对应一种**静默**失败：
//
//	① 行数与身份集合（session_id + 窗口）不变——重铸不是重建，一行都不该丢；
//	② 每支新 id 形状合法且互不相同（`run_<session>-<first_seq>` 那种"加个前缀"会当场红）；
//	③ `messages.run_id` 逐行改指（漏改 → 消息挂在一个不存在的轮上，读出来是孤儿）；
//	④ 空 `run_id` 按 `seq_lo` 落在哪个 run 窗口里归属；**窗口不覆盖任何一行时保持空**（不猜）；
//	⑤ `inputs.seq` 每会话从 0 起、稠密、且**与入队顺序同序**（跨会话互不干扰）；
//	⑥ 两条唯一索引真的建出来了（`sqlite_master`），重建标记抬到 22；
//	⑦ 幂等：再跑一次 `Migrate` 不改任何 id/位次。
func TestMigrateV22RunsBecomeEntities(t *testing.T) {
	ctx := context.Background()
	s := openV21ForV22Test(t)

	at := "2026-05-06T10:00:00Z"
	for _, sid := range []string{v22SessionA, v22SessionB} {
		if err := s.db.Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
			VALUES (?, 'completed', ?, ?)`, sid, at, at).Error; err != nil {
			t.Fatalf("写会话夹具 %s: %v", sid, err)
		}
	}
	// runs：A 两条（旧 id 由 (session, first_seq) 派生）、B 一条。
	type runFix struct {
		oldID      string
		session    string
		first, end int64
	}
	runs := []runFix{
		{"r-" + v22SessionA + "-0", v22SessionA, 0, 4},
		{"r-" + v22SessionA + "-5", v22SessionA, 5, 9},
		{"r-" + v22SessionB + "-0", v22SessionB, 0, 2},
	}
	for _, r := range runs {
		if err := s.db.Exec(`INSERT INTO runs (id, session_id, user_text, status, first_seq, last_seq, started_at, ended_at)
			VALUES (?,?,?, 'completed', ?,?,?, ?)`,
			r.oldID, r.session, "问："+r.oldID, r.first, r.end, at, at).Error; err != nil {
			t.Fatalf("写 run 夹具 %s: %v", r.oldID, err)
		}
	}
	// messages：① 指向 A 的第一条 run（旧 id）→ 必须改指；② run_id 空、窗口落在 A 第二条 run 内
	// → 必须归属到它；③ run_id 空、窗口**不在任何** run 里 → 必须保持空（不猜）。
	type msgFix struct {
		id, session, runID string
		lo, hi             int64
	}
	msgs := []msgFix{
		{"m-a1", v22SessionA, "r-" + v22SessionA + "-0", 1, 2},
		{"m-a2", v22SessionA, "", 6, 8},
		{"m-orphan", v22SessionA, "", 40, 41},
		{"m-b1", v22SessionB, "r-" + v22SessionB + "-0", 1, 1},
	}
	for _, m := range msgs {
		if err := s.db.Exec(`INSERT INTO messages (id, session_id, run_id, role, text, seq_lo, seq_hi, created_at)
			VALUES (?,?,?, 'assistant', 'x', ?,?,?)`, m.id, m.session, m.runID, m.lo, m.hi, at).Error; err != nil {
			t.Fatalf("写 message 夹具 %s: %v", m.id, err)
		}
	}
	// inputs：**有意乱序插入**（id 与 created_at 的顺序相反），seq 必须按 created_at 回填。
	// A 三条（seq 0/1/2）、B 一条（**从 0 起**，不是接着 A 的号）。
	type inFix struct {
		id, session, createdAt string
	}
	inputs := []inFix{
		{"in-a2", v22SessionA, "2026-05-06T10:00:02Z"},
		{"in-a1", v22SessionA, "2026-05-06T10:00:01Z"},
		{"in-b1", v22SessionB, "2026-05-06T10:00:01Z"},
		{"in-a3", v22SessionA, "2026-05-06T10:00:03Z"},
	}
	for _, in := range inputs {
		if err := s.db.Exec(`INSERT INTO inputs (id, session_id, text, delivery, target_run_id, generation, status, created_at)
			VALUES (?,?, 'q', 'after_run', 'run_legacy_unrelated', 1, 'queued', ?)`,
			in.id, in.session, in.createdAt).Error; err != nil {
			t.Fatalf("写 input 夹具 %s: %v", in.id, err)
		}
	}
	// 钉住"我们确实从旧形态出发"：v21 库没有 seq 列，也没有那两条唯一索引。
	for _, idx := range []string{"idx_runs_session_first_seq", "idx_inputs_session_seq"} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 0 {
			t.Fatalf("v21 库不该有 %s（夹具造错版本了？）: n=%d err=%v", idx, n, err)
		}
	}
	var hasSeq int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('inputs') WHERE name='seq'`).
		Row().Scan(&hasSeq); err != nil || hasSeq != 0 {
		t.Fatalf("v21 的 inputs 不该有 seq 列: n=%d err=%v", hasSeq, err)
	}

	// —— 跑 v22 ——
	migrateUpTo(t, s, 22)
	if v, err := s.SchemaVersion(ctx); err != nil || v != 22 {
		t.Fatalf("迁移后版本应为 22：v=%d err=%v", v, err)
	}

	// ① + ② runs：行数不变、窗口逐行保留、id 全部重铸且互不相同。
	type runRow struct {
		ID        string
		SessionID string
		UserText  string
		FirstSeq  int64
		LastSeq   int64
	}
	var afterRuns []runRow
	if err := s.db.Raw(`SELECT id, session_id, user_text, first_seq, last_seq FROM runs ORDER BY session_id, first_seq`).
		Scan(&afterRuns).Error; err != nil {
		t.Fatalf("读迁移后的 runs: %v", err)
	}
	if len(afterRuns) != len(runs) {
		t.Fatalf("run 行数变了：%d → %d（重铸不是重建）", len(runs), len(afterRuns))
	}
	oldToNew := map[string]string{}
	seenNew := map[string]bool{}
	for i, r := range afterRuns {
		want := runs[i]
		if r.SessionID != want.session || r.FirstSeq != want.first || r.LastSeq != want.end || r.UserText != "问："+want.oldID {
			t.Fatalf("第 %d 行内容变了（重铸只该改 id）：got %+v want %+v", i, r, want)
		}
		// 形状判据用**平台自己的**校验器（`objid.IsPlatformID`）而不是实现里那个正则：
		// 拿实现的正则来判实现，"正则写错"这一类错会两边一起错、判据空转。
		if !objid.IsPlatformID(r.ID) || !strings.HasPrefix(r.ID, "run_") {
			t.Fatalf("run id 应重铸成 `run_<26 位 ULID>`（`run_<session>-<seq>` 那种「加个前缀」会当场红）: %q", r.ID)
		}
		if seenNew[r.ID] {
			t.Fatalf("重铸后的 id 撞了: %q", r.ID)
		}
		seenNew[r.ID] = true
		oldToNew[want.oldID] = r.ID
	}
	// 旧 id 一个都不该留在库里。
	for _, r := range runs {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM runs WHERE id = ?`, r.oldID).Row().Scan(&n); err != nil || n != 0 {
			t.Fatalf("旧 id %q 还在库里: n=%d err=%v", r.oldID, n, err)
		}
	}

	// ③ + ④ messages 的改指与归属。
	type msgRow struct {
		ID    string
		RunID string
		SeqLo int64
		SeqHi int64
		Role  string
		Text  string
	}
	var afterMsgs []msgRow
	if err := s.db.Raw(`SELECT id, run_id, seq_lo, seq_hi, role, text FROM messages ORDER BY id`).Scan(&afterMsgs).Error; err != nil {
		t.Fatalf("读迁移后的 messages: %v", err)
	}
	if len(afterMsgs) != len(msgs) {
		t.Fatalf("message 行数变了：%d → %d", len(msgs), len(afterMsgs))
	}
	byID := map[string]msgRow{}
	for _, m := range afterMsgs {
		byID[m.ID] = m
	}
	if got := byID["m-a1"].RunID; got != oldToNew["r-"+v22SessionA+"-0"] {
		t.Fatalf("m-a1 应改指 A 的第一条 run %q，得到 %q", oldToNew["r-"+v22SessionA+"-0"], got)
	}
	if got := byID["m-b1"].RunID; got != oldToNew["r-"+v22SessionB+"-0"] {
		t.Fatalf("m-b1 应改指 B 的 run %q，得到 %q", oldToNew["r-"+v22SessionB+"-0"], got)
	}
	if got := byID["m-a2"].RunID; got != oldToNew["r-"+v22SessionA+"-5"] {
		t.Fatalf("m-a2（空 run_id）应按 seq_lo=6 归属到 A 的第二条 run %q，得到 %q",
			oldToNew["r-"+v22SessionA+"-5"], got)
	}
	if got := byID["m-orphan"].RunID; got != "" {
		t.Fatalf("窗口不覆盖任何 run 的行必须**保持空**（不猜一个轮次给它），得到 %q", got)
	}
	// 窗口列与正文逐行不变（改指不该顺手改别的）。
	for _, m := range msgs {
		got := byID[m.id]
		if got.SeqLo != m.lo || got.SeqHi != m.hi || got.Role != "assistant" || got.Text != "x" {
			t.Fatalf("message %s 的窗口/正文被改了: %+v", m.id, got)
		}
	}
	// 每条非空 run_id 都必须查得到（孤儿引用是静默的：读模型照样能读，只是永远对不上）。
	var dangling int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM messages m
		WHERE m.run_id <> '' AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.id = m.run_id)`).
		Row().Scan(&dangling); err != nil || dangling != 0 {
		t.Fatalf("有 %d 条 message 指向不存在的 run（改指漏了）：err=%v", dangling, err)
	}

	// ⑤ inputs.seq：每会话从 0 起、稠密、按 created_at 序。
	type inputRow struct {
		ID        string
		SessionID string
		Seq       int64
	}
	var afterInputs []inputRow
	if err := s.db.Raw(`SELECT id, session_id, seq FROM inputs ORDER BY session_id, seq`).Scan(&afterInputs).Error; err != nil {
		t.Fatalf("读迁移后的 inputs: %v", err)
	}
	if len(afterInputs) != len(inputs) {
		t.Fatalf("input 行数变了：%d → %d", len(inputs), len(afterInputs))
	}
	wantSeq := map[string]int64{"in-a1": 0, "in-a2": 1, "in-a3": 2, "in-b1": 0}
	for _, in := range afterInputs {
		if w, ok := wantSeq[in.ID]; !ok || in.Seq != w {
			t.Fatalf("input %s 的 seq 应为 %d，得到 %d（回填必须按入队顺序、每会话从 0 起）",
				in.ID, wantSeq[in.ID], in.Seq)
		}
	}
	// 唯一性由库保证（不是"回填时恰好没重复"）。
	var dup int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM (SELECT session_id, seq FROM inputs GROUP BY session_id, seq HAVING COUNT(*) > 1)`).
		Row().Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("inputs 里有重复的 (session_id, seq)：%d 组 err=%v", dup, err)
	}

	// ⑥ 两条唯一索引 + 重建标记。
	for _, idx := range []string{"idx_runs_session_first_seq", "idx_inputs_session_seq"} {
		var sql string
		if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&sql); err != nil {
			t.Fatalf("索引 %s 不存在: %v", idx, err)
		}
		if !strings.HasPrefix(sql, "CREATE UNIQUE INDEX") {
			t.Fatalf("%s 必须是 UNIQUE，实际 %q", idx, sql)
		}
	}
	if v, ok, err := s.GetMeta(ctx, "read_models_rebuild"); err != nil || !ok || v != "22" {
		t.Fatalf("meta.read_models_rebuild 应写为 \"22\"：ok=%v value=%q err=%v", ok, v, err)
	}

	// ⑦ 幂等：再跑一次，重铸过的 id 不再变（第二次迁移跑的是"已经重铸过的库"，一行都不该动）。
	type idOnly struct{ ID string }
	var before []idOnly
	if err := s.db.Raw(`SELECT id FROM runs ORDER BY id`).Scan(&before).Error; err != nil {
		t.Fatalf("读幂等前的 id: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	var after []idOnly
	if err := s.db.Raw(`SELECT id FROM runs ORDER BY id`).Scan(&after).Error; err != nil {
		t.Fatalf("读幂等后的 id: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("重复迁移改了行数：%d → %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("重复迁移又重铸了一次 id：%q → %q", before[i].ID, after[i].ID)
		}
	}
}

// TestMigrateV22OnEmptyRuns v22 在**空 runs / 空 inputs** 上必须照常完成：
// Go 步骤里任何"至少有一行"的假设（例如拿首行的 id 当锚点）都会在这里红。
func TestMigrateV22OnEmptyRuns(t *testing.T) {
	ctx := context.Background()
	s := openV21ForV22Test(t)

	migrateUpTo(t, s, 22)
	// `runs.session_id` 是**外键**（SQLite 这边真的开着），会话行不是可省的前置。
	seedSession(t, s, v22SessionA)

	for _, idx := range []string{"idx_runs_session_first_seq", "idx_inputs_session_seq"} {
		var n int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Row().Scan(&n); err != nil || n != 1 {
			t.Fatalf("空库上也要建出 %s：n=%d err=%v", idx, n, err)
		}
	}
	// 空表上的开轮插入照常可用（唯一索引不该把第一条挡住）。
	if err := s.CreateRun(ctx, runFixture(t, objid.New(objid.Run), v22SessionA, 0)); err != nil {
		t.Fatalf("空库上开轮插入: %v", err)
	}
}

// TestMigrateV22UniqueIndexBlocksDuplicateRun 反证：挡住"同一轮插出两行"的**只有那条唯一索引**。
//
// 做法与判据：迁移完成后 DROP 掉 `idx_runs_session_first_seq`，再用**同一 (session_id, first_seq)**
// 走一次开轮插入——插入会成功、库里出现两行。于是"幂等靠 DB 不变式"这句话有了可区分力：
// 删掉索引时别的断言仍然全绿，只有这一条会红。
func TestMigrateV22UniqueIndexBlocksDuplicateRun(t *testing.T) {
	ctx := context.Background()
	s := openV21ForV22Test(t)
	migrateUpTo(t, s, 22)
	seedSession(t, s, v22SessionA)

	first := int64(3)
	if err := s.CreateRun(ctx, runFixture(t, objid.New(objid.Run), v22SessionA, first)); err != nil {
		t.Fatalf("首次开轮: %v", err)
	}
	// 有索引时：同起点第二次插入被 `ON CONFLICT DO NOTHING` 静默跳过（幂等的外观）。
	if err := s.CreateRun(ctx, runFixture(t, objid.New(objid.Run), v22SessionA, first)); err != nil {
		t.Fatalf("同起点重复开轮应幂等跳过: %v", err)
	}
	if n := countRuns(t, s, v22SessionA); n != 1 {
		t.Fatalf("有唯一索引时应只有 1 行，得到 %d", n)
	}
	// 反证：没有索引时同一个动作会插出第二行——所以上面那条断言拦的是**索引**，不是"恰好没撞"。
	if err := s.db.Exec(`DROP INDEX idx_runs_session_first_seq`).Error; err != nil {
		t.Fatalf("DROP INDEX: %v", err)
	}
	if err := s.CreateRun(ctx, runFixture(t, objid.New(objid.Run), v22SessionA, first)); err != nil {
		t.Fatalf("无索引时的重复插入: %v", err)
	}
	if n := countRuns(t, s, v22SessionA); n != 2 {
		t.Fatalf("删掉唯一索引后应插出第 2 行（证明判据就是那条索引），得到 %d", n)
	}
}

// runFixture 拼一条最小可插入的开轮行；countRuns 数某会话的 run 行数（本文件专用）。
func runFixture(t *testing.T, id, sessionID string, firstSeq int64) run.Item {
	t.Helper()
	at, err := time.Parse(time.RFC3339, "2026-05-06T10:00:00Z")
	if err != nil {
		t.Fatalf("解析夹具时刻: %v", err)
	}
	return run.Item{
		ID: id, SessionID: sessionID, UserText: "q", Status: "running",
		FirstSeq: firstSeq, LastSeq: -1, StartedAt: at,
	}
}

func countRuns(t *testing.T, s *Store, sessionID string) int64 {
	t.Helper()
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM runs WHERE session_id = ?`, sessionID).Row().Scan(&n); err != nil {
		t.Fatalf("数 runs: %v", err)
	}
	return n
}

// TestMigrateV22DoesNotFabricateRunStarted v22 只搬**已有的**行，绝不往事件流里补一条
// `run.started`——即便"补上它"能让每条历史轮都满足「窗口起点指向 `run.started`」这条判据。
//
// 这条为什么值得单独钉（2026-10-14 端到端验收实测）：老库里 9/9 条历史轮**都没有** `run.started`
// （那条事件是 v22 引入的），于是升级后 `runs.first_seq` 指不到任何 `run.started` 事件。这是**事实**，
// 不是漏做：事件流是"当时发生了什么"的记录（判据 J4：身份随数据走），迁移伪造出来的事件会改变
// transcript、改变 `events` 的身份集合，还会让用户在界面里看见一轮凭空的「开始」。
// 这条判据把"不伪造"钉成机器可读的意图，挡的正是那种"为了让判据变绿而补事件"的改动；
// 新轮的对称性由 `domain/session` 的"开轮三连"保证（`Runner.Prompt`）。
func TestMigrateV22DoesNotFabricateRunStarted(t *testing.T) {
	s := openV21ForV22Test(t)
	at := "2026-05-06T10:00:00Z"
	if err := s.db.Exec(`INSERT INTO sessions (id, status, created_at, updated_at)
		VALUES (?, 'completed', ?, ?)`, v22SessionA, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}
	// 历史事件流：0 session.created / 1 session.user_message / 2 session.message（**没有** run.started）。
	for _, ev := range []struct {
		id, typ string
		seq     int64
	}{
		{"evt_01J9Z6Q0M7X2V8K4B3N5TRW2Q0", "session.created", 0},
		{"evt_01J9Z6Q0M7X2V8K4B3N5TRW2Q1", "session.user_message", 1},
		{"evt_01J9Z6Q0M7X2V8K4B3N5TRW2Q2", "session.message", 2},
	} {
		if err := s.db.Exec(`INSERT INTO events (id, session_id, seq, type, payload_json, created_at)
			VALUES (?,?,?,?, '{}', ?)`, ev.id, v22SessionA, ev.seq, ev.typ, at).Error; err != nil {
			t.Fatalf("写事件夹具 %s: %v", ev.id, err)
		}
	}
	if err := s.db.Exec(`INSERT INTO runs (id, session_id, user_text, status, first_seq, last_seq, started_at, ended_at)
		VALUES (?,?, '问', 'completed', 1, 2, ?, ?)`,
		"r-"+v22SessionA+"-1", v22SessionA, at, at).Error; err != nil {
		t.Fatalf("写 run 夹具: %v", err)
	}
	before := eventFingerprint(t, s, v22SessionA)

	migrateUpTo(t, s, 22)

	if after := eventFingerprint(t, s, v22SessionA); after != before {
		t.Fatalf("迁移动了事件流（v22 只搬已有的行，绝不补 run.started）：\n之前 %s\n之后 %s", before, after)
	}
	// 顺带把"历史轮的窗口起点指不到 run.started"写成判据：这是**刻意的不对称**，不是漏做。
	var dangling int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM runs r WHERE NOT EXISTS (
		SELECT 1 FROM events e WHERE e.session_id = r.session_id AND e.seq = r.first_seq AND e.type = 'run.started')`).
		Row().Scan(&dangling); err != nil {
		t.Fatalf("查窗口起点: %v", err)
	}
	if dangling != 1 {
		t.Fatalf("历史轮本就该指不到 run.started（期望 1，实际 %d）", dangling)
	}
}

// eventFingerprint 取一个会话事件流的指纹（位次 + 类型 + 身份，按位次排序）。
func eventFingerprint(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	var parts []string
	if err := s.db.Raw(`SELECT seq || ':' || type || ':' || id FROM events
		WHERE session_id = ? ORDER BY seq`, sessionID).Scan(&parts).Error; err != nil {
		t.Fatalf("取事件指纹: %v", err)
	}
	return strings.Join(parts, "|")
}
