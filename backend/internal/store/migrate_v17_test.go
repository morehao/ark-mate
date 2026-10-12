package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/dbclient"
	"github.com/morehao/ark-mate/pkg/object/objid"
)

// openV16ForV17Test 造一个**真实的 v16 库**（完整跑 v1..v16，含 v12/v13 的 Go 数据步骤与
// v15 的破坏性重建），事件表还是"seq 就是主键"的那一版。
//
// 为什么不用 `openTestDB` + 回退版本标记：那是把**已经重建过的** v17 表当成 v16 表用，
// 形状根本不同（v17 的 id 是主键且 NOT NULL）；而 v17 是本仓第一个 O(全部事件行) 的迁移
// 步骤——在错误的前提上测它，测的就不是迁移。
func openV16ForV17Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v16.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 16)
	return s
}

// v17SessionID 夹具会话 id（形状与生产一致：`ses_` + 26 位 Crockford 主体）。
const v17SessionID = "ses_01J9Z6Q0M7X2V8K4B3N5TRW1QA"

// TestMigrateV17KeepsEveryEvent v17（事件身份）：`events` 从「seq 就是身份」改成
// 「id 是身份、seq 是位次」，**一行不丢**。
//
// 为什么这条必须用"真实的 v16 库 + 真行"来测：v17 是本仓第一个 O(全部事件行) 的 Go 迁移
// 步骤（keyset 分页 + 逐行铸号 + 对账 + 换表）。空库上它什么都不会发生——"迁移正确"与
// "迁移根本没跑到"在空库上看起来完全一样。
//
// 判据七段（每一段都对应一种**静默**的失败）：
//
//	① 行数 / 内容列逐行相等（漏行 → 事件没了，而版本号照样前进）；
//	② 每行都有平台形状的 `evt_` 身份且互不相同（铸号撞了 → 主键冲突或更糟：两行同 id）；
//	③ **按 id 排序 == 按 seq 排序**（铸号顺序即 seq 升序，这条性质不该被悄悄打破）；
//	④ `meta.events_last_seq` **保留**（与 v15 相反！清掉它 → 下一条新事件从 1 起跳、
//	   撞上既有行的 UNIQUE(seq)：这是照抄 v15 的人一定会踩的那一脚）；
//	⑤ 迁移后仍能正常追加新事件（铸号 + 水位推进都还工作）；
//	⑥ 库级约束是"seq 全局唯一 + (session_id, seq) 索引"，且 UNIQUE 真的会咬人；
//	⑦ 幂等：再跑一次 `Migrate` 不报错、不动数据。
func TestMigrateV17KeepsEveryEvent(t *testing.T) {
	ctx := context.Background()
	s := openV16ForV17Test(t)

	at := "2026-03-04T10:00:00Z"
	// 会话行也用**裸 SQL**：v16 的 `sessions` 列名与今天的实体不同（v17 才把两列收口到
	// runtime 词），走 DAO 会按今天的列名拼 INSERT 而报 no such column。这里只写与列名无关的
	// 那几列（其余都有默认值），于是这条夹具不必写出任何历史列名。
	if err := s.db.Exec(`INSERT INTO sessions (id, agent_id, status, created_at, updated_at)
		VALUES (?, 'a1', 'completed', ?, ?)`, v17SessionID, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}
	// 关键夹具：`seq` 有洞（1,2,5,9）——全局位次本来就会因为回滚/失败出现空洞，
	// 迁移不得"顺手压实"它们（压实 = 改写历史位次，客户端书签全错）。
	rows := []struct {
		seq  int64
		typ  string
		body string
	}{
		{1, "session.user_message", `{"text":"你好"}`},
		{2, "session.message", `{"text":"hello"}`},
		{5, "run.ended", `{"stop_reason":"end_turn"}`},
		{9, "session.thought", ``},
	}
	for _, r := range rows {
		if err := s.db.Exec(`INSERT INTO events (seq, session_id, type, payload_json, created_at)
			VALUES (?,?,?,?,?)`, r.seq, v17SessionID, r.typ, r.body, at).Error; err != nil {
			t.Fatalf("写事件夹具 seq=%d: %v", r.seq, err)
		}
	}
	if err := s.SetMeta(ctx, model.MetaKeyEventsLastSeq, "9"); err != nil {
		t.Fatalf("写事件水位: %v", err)
	}
	// 迁移前：主键还是 seq（钉住"我们确实从旧形态出发"）。
	if pk := eventsPrimaryKeyColumn(t, s); pk != "seq" {
		t.Fatalf("v16 库的 events 主键应为 seq，实际 %q——夹具造错了版本？", pk)
	}

	// —— 跑 v17（与 `Migrate` 同一条 statements+apply 路径）。
	migrateUpTo(t, s, 17)
	if v, err := s.SchemaVersion(ctx); err != nil || v != 17 {
		t.Fatalf("迁移后版本应为 17：v=%d err=%v", v, err)
	}

	// ① 行数与内容列逐行相等（按 seq 比对）。
	type evRow struct {
		ID, SessionID, Type, PayloadJSON, CreatedAt string
		Seq                                         int64
	}
	var after []evRow
	if err := s.db.Raw(`SELECT id, seq, session_id, type, payload_json, created_at
		FROM events ORDER BY seq`).Scan(&after).Error; err != nil {
		t.Fatalf("读迁移后的 events: %v", err)
	}
	if len(after) != len(rows) {
		t.Fatalf("事件行数变了：%d → %d（漏行）", len(rows), len(after))
	}
	for i, r := range rows {
		a := after[i]
		if a.Seq != r.seq || a.Type != r.typ || a.PayloadJSON != r.body || a.CreatedAt != at ||
			a.SessionID != v17SessionID {
			t.Fatalf("第 %d 行在重建中失真：\n want seq=%d type=%s payload=%q created_at=%s\n got  %+v",
				i, r.seq, r.typ, r.body, at, a)
		}
		// ② 身份：形状合法、前缀正确、互不相同。
		if !objid.IsPlatformID(a.ID) || !strings.HasPrefix(a.ID, objid.Event) {
			t.Fatalf("第 %d 行的 id 不是平台形状：%q（期望 %s<26 位 ULID>）", i, a.ID, objid.Event)
		}
	}
	seen := map[string]bool{}
	for _, a := range after {
		if seen[a.ID] {
			t.Fatalf("同一支 id 出现在两行上：%q（铸号撞了，而 id 是主键）", a.ID)
		}
		seen[a.ID] = true
	}

	// ③ 按 id 排序 == 按 seq 排序（铸号顺序 = seq 升序）。
	var byID []evRow
	if err := s.db.Raw(`SELECT id, seq FROM events ORDER BY id`).Scan(&byID).Error; err != nil {
		t.Fatalf("按 id 读 events: %v", err)
	}
	for i := range byID {
		if byID[i].Seq != after[i].Seq {
			t.Fatalf("`ORDER BY id` 与 `ORDER BY seq` 不再同序：第 %d 位 id 序 seq=%d / seq 序 seq=%d\n"+
				"（铸号按 seq 升序是这条性质的来源；被打破意味着迁移的分页顺序错了）",
				i, byID[i].Seq, after[i].Seq)
		}
	}

	// ④ 水位保留。
	if v, ok, err := s.GetMeta(ctx, model.MetaKeyEventsLastSeq); err != nil || !ok || v != "9" {
		t.Fatalf("meta.%s 必须保留为 \"9\"（v17 一行不丢，与 v15 清空正好相反）：ok=%v value=%q err=%v",
			model.MetaKeyEventsLastSeq, ok, v, err)
	}

	// ⑤ 迁移后照常追加新事件。
	seq, err := s.AppendEvent(ctx, objid.New(objid.Event), v17SessionID, "session.message",
		map[string]any{"text": "迁移之后"})
	if err != nil {
		t.Fatalf("迁移后追加事件失败: %v", err)
	}
	if seq != 10 {
		t.Fatalf("新事件的 seq 应为 10（水位从保留的 9 继续），实际 %d", seq)
	}
	if n, err := s.EventCount(ctx, ""); err != nil || n != int64(len(rows)+1) {
		t.Fatalf("追加后事件数应 %d：n=%d err=%v", len(rows)+1, n, err)
	}

	// ⑥ 库级约束。
	if pk := eventsPrimaryKeyColumn(t, s); pk != "id" {
		t.Fatalf("v17 后 events 主键应为 id，实际 %q", pk)
	}
	var idxSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_events_seq'`).
		Row().Scan(&idxSQL); err != nil {
		t.Fatalf("读 idx_events_seq 定义: %v", err)
	}
	if !strings.HasPrefix(idxSQL, "CREATE UNIQUE INDEX") || !strings.Contains(idxSQL, "events(seq)") {
		t.Fatalf("idx_events_seq 应是 `CREATE UNIQUE INDEX … ON events(seq)`，实际 %q", idxSQL)
	}
	var n int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_events_session'`).
		Row().Scan(&n); err != nil || n != 1 {
		t.Fatalf("idx_events_session 应存在：n=%d err=%v", n, err)
	}
	if err := s.db.Exec(`INSERT INTO events (id, seq, session_id, type, payload_json, created_at)
		VALUES (?, 1, ?, 'session.message', '', ?)`, objid.New(objid.Event), v17SessionID, at).Error; err == nil {
		t.Fatal("重复的 seq 必须被 UNIQUE(seq) 拦住（seq 是全局位次，不是每会话独立序号）")
	}

	// ⑦ 幂等。
	before, _ := s.EventCount(ctx, "")
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	if again, _ := s.EventCount(ctx, ""); again != before {
		t.Fatalf("重复迁移改了数据：before=%d after=%d", before, again)
	}
}

// TestMigrateV17OnEmptyEvents v17 在空 events 上必须**照常完成**（对账不能因为 0 行而误判）。
//
// 为什么单独一条：对账用 `COUNT(*)` + `COALESCE(MAX(seq), 0)`，而"最大 seq = 0"与"搬了 0 行"
// 在空表上同时成立——单边比较（例如只写 `afterMax != beforeMax` 就报错）在空表上最容易误报红，
// 而全新库正是空表。
func TestMigrateV17OnEmptyEvents(t *testing.T) {
	ctx := context.Background()
	s := openV16ForV17Test(t)

	// events 表是空的，但**会话行必须在**：events.session_id 是 REFERENCES sessions(id)，
	// 而 v1.32.22 起外键真的会咬人（ncruces 的 wasm 构建带 SQLITE_DEFAULT_FOREIGN_KEYS=1），
	// 给不存在的会话追加事件会被库拒掉——那时候这条测试红的是夹具，不是迁移。
	// 裸 SQL 的理由见 TestMigrateV17KeepsEveryEvent：v16 的列名与今天的实体不同。
	at := "2026-03-04T10:00:00Z"
	if err := s.db.Exec(`INSERT INTO sessions (id, agent_id, status, created_at, updated_at)
		VALUES (?, 'a1', 'completed', ?, ?)`, v17SessionID, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}

	migrateUpTo(t, s, 17)
	if n, err := s.EventCount(ctx, ""); err != nil || n != 0 {
		t.Fatalf("空 events 迁移后仍应为 0 行：n=%d err=%v", n, err)
	}
	if seq, err := s.AppendEvent(ctx, objid.New(objid.Event), v17SessionID, "session.message", nil); err != nil || seq != 1 {
		t.Fatalf("空表迁移后第一条事件应从 1 起跳：seq=%d err=%v", seq, err)
	}
}

// eventsPrimaryKeyColumn 返回 events 的主键列名（fail-closed：没有主键/读不到即失败）。
func eventsPrimaryKeyColumn(t *testing.T, s *Store) string {
	t.Helper()
	type col struct {
		Name string
		PK   int64
	}
	var cols []col
	if err := s.db.Raw(`SELECT name, pk FROM pragma_table_info('events')`).Scan(&cols).Error; err != nil {
		t.Fatalf("读 events 列信息: %v", err)
	}
	if len(cols) == 0 {
		t.Fatal("pragma_table_info('events') 什么都没读到（fail-closed）")
	}
	for _, c := range cols {
		if c.PK > 0 {
			return c.Name
		}
	}
	t.Fatal("events 没有主键列（fail-closed）")
	return ""
}
