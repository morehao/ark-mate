package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	// 本包的测试要注册 glog 的 zap driver：日志出口归装配根（cmd/mate 的匿名引入），
	// 而 test 二进制不含 cmd/mate，Open → dbclient.Open → glog.NewLogger 需要它。
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/object/objid"
	_ "github.com/morehao/golib/glog/driver/zap"
)

func ctxT() context.Context { return context.Background() }

// openTestDB 给当前测试一个**独立**的 sqlite 库（t.TempDir() 下的文件库）。
//
// 为什么是文件库而不是 ":memory:"：① 生产就是文件库（迁移备份、只读句柄、重启恢复这些
// 路径只有文件库才走得到）；② 夹具要的是"每个测试一个空库"，而历史上 golib 的 dialect
// 把内存库统一改写成固定名 `file::memory:?cache=shared`，同进程两次 `Open(":memory:")`
// 会落到同一个库上（实测症状：usage 汇总把别的用例的行一起算进去，`CostCurrency: MIXED`、
// Runs 多出来）。v1.32.22 的纯 Go dialect 已改成每次 Open 分配新库名，`":memory:"` 重新
// 变得安全（见 pkg/dbclient 的 TestOpenMemoryIsIndependent），但第 ① 条理由与驱动无关，
// 所以夹具继续用文件库。
func openTestDB(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mate.db"))
	if err != nil {
		t.Fatalf("Open(test db): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedSession(t *testing.T, s *Store, id string) {
	t.Helper()
	// **走原始 SQL 且列固定**，不用 `UpsertSession`：后者按**当前实体**列出全部列，于是这套
	// "停在某个历史版本"的夹具用例（`migrateUpTo(t, s, 22)` 之类）每加一列就会集体红——
	// 而它们要钉的是**当时那一版**的行为，不是"最新实体能被写进旧 schema"。
	//
	// 列固定在这里 = 后来加的任何列都由 DDL 的 `DEFAULT` 兜住（这也正是那些迁移步的自我要求）。
	err := s.DB().Exec(`INSERT INTO sessions
		(id, cwd, workspace_id, workspace_temporary, model_id, runtime_id, runtime_session_id,
		 title, pinned, archived, status, origin, capability_json, capability_fingerprint,
		 last_seq, created_at, updated_at)
		VALUES (?, '', '', 0, '', '', '', '', 0, 0, 'in_progress', '', '', '', 0, ?, ?)
		ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at`,
		id, time.Now().UTC(), time.Now().UTC()).Error
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// TestSchemaMigrated 空库打开自动迁移到 latest；重复打开幂等；版本正确。
func TestSchemaMigrated(t *testing.T) {
	s := openTestDB(t)
	v, err := s.SchemaVersion(ctxT())
	if err != nil || v != latestSchemaVersion() {
		t.Fatalf("schema 版本不符: v=%d err=%v", v, err)
	}
	// 幂等：再跑一次 migrate 不报错。
	if err := s.Migrate(ctxT()); err != nil {
		t.Fatalf("re-migrate 应幂等: %v", err)
	}
}

// TestAppendEventSeqIsPerSessionDense AppendEvent 的位次是**会话内**的（v21）：
// 每个会话各自从 0 起、连续无洞、互不影响；session.last_seq 跟着本会话位次走。
//
// 反证：把 `appendEventTx` 换回全局取号（v21 之前 meta 计数行那一版）→ s2 的首条事件 seq 是 1
// 而不是 0，本用例红。
func TestAppendEventSeqIsPerSessionDense(t *testing.T) {
	s := openTestDB(t)
	seedSession(t, s, "s1")
	seedSession(t, s, "s2")

	s1, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s1", "session.created", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	s2a, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s2", "session.created", map[string]any{"text": "yo"})
	if err != nil {
		t.Fatalf("AppendEvent s2: %v", err)
	}
	s1b, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s1", "session.user_message", map[string]any{})
	if err != nil {
		t.Fatalf("AppendEvent s1b: %v", err)
	}
	// 每个会话自己的第一条是 0，第二条是 1；交错发布不改变这一点。
	if s1 != 0 || s2a != 0 || s1b != 1 {
		t.Fatalf("位次应为「每会话 0 起、稠密」: s1=%d s2=%d s1b=%d", s1, s2a, s1b)
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
	// event_sequence 计数行 = 该会话最后分配的位次（每会话一行）。
	for id, want := range map[string]int64{"s1": 1, "s2": 0} {
		var got int64
		if err := s.db.Raw(`SELECT seq FROM event_sequence WHERE session_id = ?`, id).Row().Scan(&got); err != nil {
			t.Fatalf("读 event_sequence(%s): %v", id, err)
		}
		if got != want {
			t.Fatalf("event_sequence(%s)=%d want %d", id, got, want)
		}
	}
}

// sessionWatermarks 返回每个会话当前的最高位次（v21 起没有"全局水位"这个东西，
// 测试要问"某个会话的号有没有前进"只能按会话问）。
func sessionWatermarks(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	type row struct {
		SessionID string
		Seq       int64
	}
	var rows []row
	if err := s.db.Raw(`SELECT session_id, MAX(seq) AS seq FROM events GROUP BY session_id`).Scan(&rows).Error; err != nil {
		t.Fatalf("读每会话水位: %v", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r.Seq
	}
	return out
}

// sameWatermarks 比较两份"会话 → 最高位次"快照（缺失的会话按 -1 比，与"没有事件"一致）。
func sameWatermarks(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

// TestAppendEventUnknownSession 未知会话的事件**被库拦住**——外键又回来了（v1.32.22 起）。
//
// 这条守卫的方向是历史翻转的，值得记一笔：自注册的纯 Go dialector 在 DSN 里注入
// `foreign_keys(1)`，events 的 session_id 外键由 SQLite 兜底，插未知会话必然报错；
// 换到 golib v1.32.21 的 mattn 方言后不再注入（SQLite 默认关闭），这次插入会**成功**
// ——"给不存在的会话追加事件"从"库拒绝"退化成"写进去一行孤儿数据"，当时的守卫因此
// 反过来断言"不该报错"，并留了话："哪天 foreign_keys 回来了，把它改回'应报错'"。
// v1.32.22 的 ncruces 方言带 `SQLITE_DEFAULT_FOREIGN_KEYS=1`（wasm 构建项），话应验了。
//
// 它守的仍然是同一件事：**调用方必须自己保证 session 存在**，只是现在库也帮着拦一道。
// 哪天它又变绿了（插入成功），说明外键又被关掉——那时先把本注释与
// pkg/dbclient 的 TestOpenConnectionPragmasFollowGolibAndDriver 对齐，别只改断言。
func TestAppendEventUnknownSession(t *testing.T) {
	s := openTestDB(t)
	if _, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "ghost", "session.message", map[string]any{}); err == nil {
		t.Fatalf("插入未知会话的事件竟然成功了：外键又被关掉了？（见本测试注释）")
	}
}

// TestEventsAfterReplay events 回放：按 session + after 分页、hasMore 正确。
func TestEventsAfterReplay(t *testing.T) {
	s := openTestDB(t)
	seedSession(t, s, "s1")
	seedSession(t, s, "s2")
	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s1", "session.message", map[string]any{"i": i})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		seqs = append(seqs, seq)
	}
	_, _ = s.AppendEvent(ctxT(), objid.New(objid.Event), "s2", "session.message", map[string]any{}) // 其它会话不混入

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
	s := openTestDB(t)
	base := model.SessionEntity{ID: "s1", Title: "t0",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.UpsertSession(ctxT(), base); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 覆盖 upsert 不能把 last_seq 写回去（MAX 触顶）。
	// 注意判据必须落在**非零**位次上：v21 起位次从 0 起，"归零"与"没写过"是同一个值，
	// 拿 `LastSeq == 0` 当"被清零了"会永远绿（这条断言曾经就是这么写的）。
	seedSession(t, s, "s1")
	for i := 0; i < 2; i++ {
		if _, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s1", "x", map[string]any{}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	low := base
	low.LastSeq = 0
	if err := s.UpsertSession(ctxT(), low); err != nil {
		t.Fatalf("upsert-low: %v", err)
	}
	sess, _, _ := s.GetSession(ctxT(), "s1")
	if sess.LastSeq != 1 {
		t.Fatalf("覆盖 upsert 不应把 last_seq 写回去: got %d want 1", sess.LastSeq)
	}

	pinned := true
	title := "新标题"
	if err := s.UpdateSessionMeta(ctxT(), "s1", model.SessionMetaPatch{Pinned: &pinned, Title: &title}); err != nil {
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
	s := openTestDB(t)
	seedSession(t, s, "s1")
	if err := s.AddMemory(ctxT(), model.PermissionMemoryEntity{ID: objid.New(objid.PermissionMemory), SessionID: "s1", Title: "跑脚本", Kind: "execute", Decision: "allow"}); err != nil {
		t.Fatalf("add allow: %v", err)
	}
	// 同键异侧覆盖为 reject，不新增行。
	if err := s.AddMemory(ctxT(), model.PermissionMemoryEntity{ID: objid.New(objid.PermissionMemory), SessionID: "s1", Title: "跑脚本", Kind: "execute", Decision: "reject"}); err != nil {
		t.Fatalf("add reject: %v", err)
	}
	if err := s.AddMemory(ctxT(), model.PermissionMemoryEntity{ID: objid.New(objid.PermissionMemory), SessionID: "s1", Title: "写文件", Kind: "write", Decision: "allow"}); err != nil {
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
	s := openTestDB(t)
	now := time.Now().UTC()
	// v17：导入的事件行必须带**身份**（`evt_…`），所以每次造夹具都现铸——
	// 固定字面量会在"同一支 id 落在两个会话上"那里撞成"分叉"（那正是另一个用例要测的）。
	newEvents := func() []model.EventEntity {
		return []model.EventEntity{
			{ID: objid.New(objid.Event), Type: "session.user_message", PayloadJSON: `{"text":"hi"}`},
			{ID: objid.New(objid.Event), Type: "session.message", PayloadJSON: `{"text":"yo"}`},
			{ID: objid.New(objid.Event), Type: "run.ended", PayloadJSON: `{"stop_reason":"end_turn"}`},
		}
	}
	mkItem := func(sessionID string, events []model.EventEntity) ImportItem {
		return ImportItem{
			Session: model.SessionEntity{ID: sessionID, Title: "迁移会话",
				Status: "completed", CreatedAt: now, UpdatedAt: now},
			Events: events,
		}
	}
	item := mkItem("s-x", newEvents())
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
	// 新 marker（不同目录）继续导入会追加（不同会话、**另一批身份**）。
	item2 := mkItem("s-y", newEvents())
	ev2, _, err := s.ImportBatch(ctxT(), []ImportItem{item2}, "fp:test-dir-2", 1, 3)
	if err != nil || ev2 != 3 {
		t.Fatalf("二次导入: ev=%d err=%v", ev2, err)
	}

	// 错配校验 → 整体回滚（事件数与**每会话位次**均不变）。
	before, _ := s.EventCount(ctxT(), "")
	lastBefore := sessionWatermarks(t, s)
	if _, _, err := s.ImportBatch(ctxT(), []ImportItem{mkItem("s-z", newEvents())}, "fp:test-dir-3", 1, 999); err == nil {
		t.Fatal("错配计数应报错")
	}
	after, _ := s.EventCount(ctxT(), "")
	if before != after {
		t.Fatalf("失败导入应整体回滚: before=%d after=%d", before, after)
	}
	if !sameWatermarks(sessionWatermarks(t, s), lastBefore) {
		t.Fatalf("回滚后位次不应前进: got=%v want=%v", sessionWatermarks(t, s), lastBefore)
	}
}

// TestImportBatchEventIdentity 导入的事件级身份判据（v17 / D2）：
//
//	① 同 id + 同内容 → **跳过**（幂等，不写第二份，也不报错）；
//	② 同 id + 不同内容 → **报错**（分叉：同一支身份不可能有两份内容）；
//	③ 缺 id / id 形状非法 → 报错（导入面不代铸身份）。
//
// 为什么这三条必须分开测：①与②的分界正是"幂等"与"静默篡改"的分界——合并成一条，实现里
// 把②也当①处理（直接跳过）不会让任何测试变红，而库里会留下两份同 id 不同内容的行。
func TestImportBatchEventIdentity(t *testing.T) {
	s := openTestDB(t)
	now := time.Now().UTC()
	mk := func(events []model.EventEntity) ImportItem {
		return ImportItem{
			Session: model.SessionEntity{ID: "s-idem", Status: "completed",
				CreatedAt: now, UpdatedAt: now},
			Events: events,
		}
	}
	first := model.EventEntity{ID: objid.New(objid.Event), Type: "session.user_message", PayloadJSON: `{"text":"hi"}`}
	if ev, _, err := s.ImportBatch(ctxT(), []ImportItem{mk([]model.EventEntity{first})}, "fp-a", 1, 1); err != nil || ev != 1 {
		t.Fatalf("首次导入: ev=%d err=%v", ev, err)
	}
	countBefore, _ := s.EventCount(ctxT(), "")
	seqBefore := sessionWatermarks(t, s)

	// ① 同 id 同内容（不同 marker，模拟"清掉 marker 再导一次"）→ 跳过，不新增行，也不报错。
	ev, _, err := s.ImportBatch(ctxT(), []ImportItem{mk([]model.EventEntity{first})}, "fp-b", 1, 1)
	if err != nil {
		t.Fatalf("同 id 同内容应幂等跳过，实际报错: %v", err)
	}
	if ev != 1 {
		t.Fatalf("在场事件数应为 1（跳过也算在场），实际 %d", ev)
	}
	if n, _ := s.EventCount(ctxT(), ""); n != countBefore {
		t.Fatalf("幂等导入不该新增行: before=%d after=%d", countBefore, n)
	}
	if !sameWatermarks(sessionWatermarks(t, s), seqBefore) {
		t.Fatalf("全部跳过时不该取号: got=%v want=%v", sessionWatermarks(t, s), seqBefore)
	}

	// ② 同 id 不同内容 → 报错 + 整批回滚。
	diverged := first
	diverged.PayloadJSON = `{"text":"改过了"}`
	if _, _, err := s.ImportBatch(ctxT(), []ImportItem{mk([]model.EventEntity{diverged})}, "fp-c", 1, 1); err == nil {
		t.Fatal("同 id 不同内容必须报错（分叉），不能静默跳过")
	}
	if n, _ := s.EventCount(ctxT(), ""); n != countBefore {
		t.Fatalf("分叉导入应整体回滚: before=%d after=%d", countBefore, n)
	}

	// ③ 缺 id / 形状非法 → 报错。
	for _, bad := range []string{"", "evt_short", "ses_01J9Z6Q0M7X2V8K4B3N5TRW1PD"} {
		if _, _, err := s.ImportBatch(ctxT(), []ImportItem{mk([]model.EventEntity{
			{ID: bad, Type: "session.message", PayloadJSON: `{"text":"x"}`},
		})}, "fp-d-"+bad, 1, 1); err == nil {
			t.Fatalf("id=%q 应被拒绝", bad)
		}
	}
}

// TestSearchMessages 跨会话消息检索（M3 ②）：角色不限、大小写不敏感、seq 锚点、
// LIKE 通配转义（%/_ 不应被当通配符）、limit 截断。
func TestSearchMessages(t *testing.T) {
	s := openTestDB(t)
	seedSession(t, s, "m1")
	seedSession(t, s, "m2")
	ctx := ctxT()
	now := time.Now().UTC()
	// `ordinal` 是**会话内**序号（v23 的 `UNIQUE(session_id, ordinal)`）：两个会话各自从 1 起。
	msgs := []model.MessageEntity{
		{ID: objid.New(objid.Message), SessionID: "m1", RunID: "r1", Role: "user", Text: "网关 502 怎么排查", Ordinal: 1, SeqLo: 1, SeqHi: 1, CreatedAt: now},
		{ID: objid.New(objid.Message), SessionID: "m1", RunID: "r2", Role: "assistant", Text: "先看 UPSTREAM 健康检查", Ordinal: 2, SeqLo: 2, SeqHi: 2, CreatedAt: now.Add(time.Second)},
		{ID: objid.New(objid.Message), SessionID: "m2", RunID: "r3", Role: "user", Text: "100% 覆盖 _under_ 场景", Ordinal: 1, SeqLo: 3, SeqHi: 3, CreatedAt: now.Add(2 * time.Second)},
	}
	if err := s.CreateMessages(ctx, msgs); err != nil {
		t.Fatalf("CreateMessages: %v", err)
	}

	hits, err := s.SearchMessages(ctx, "502", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 || hits[0].SessionID != "m1" || hits[0].Role != "user" || hits[0].Seq != 1 {
		t.Fatalf("命中不符: %+v", hits)
	}
	if hits[0].MessageID == "" || hits[0].RunID != "r1" || hits[0].CreatedAt.IsZero() {
		t.Fatalf("锚点字段缺失: %+v", hits[0])
	}

	// 大小写不敏感 + 助手消息也命中。
	hits, err = s.SearchMessages(ctx, "upstream", 10)
	if err != nil || len(hits) != 1 || hits[0].Role != "assistant" {
		t.Fatalf("大小写/角色检索不符: %v %+v", err, hits)
	}

	// LIKE 通配转义：% 与 _ 按字面匹配。
	if hits, _ := s.SearchMessages(ctx, "%", 10); len(hits) != 1 || hits[0].SessionID != "m2" {
		t.Fatalf("百分号应按字面匹配: %+v", hits)
	}
	if hits, _ := s.SearchMessages(ctx, "_under_", 10); len(hits) != 1 {
		t.Fatalf("下划线应按字面匹配: %+v", hits)
	}

	// limit 截断 + 排序（created_at 降序：m2 最新在前）。
	hits, err = s.SearchMessages(ctx, "排", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("limit 截断不符: %v %+v", err, hits)
	}
}

// TestEventsBefore 反向分页（C2「加载更早」）：seq < before 的最近 limit 条、升序返回、
// has_more 指向「更早还有」、`before=-1` 取末尾、跨会话不混入。
//
// v21 把哨兵从 0 换成了 -1：`0` 现在是该会话第一条事件的位次，于是 `before=0` 的语义从
// "取末尾一页"变成"第 0 条之前"（空页）——两种都断言，正是这一改的分界。
func TestEventsBefore(t *testing.T) {
	s := openTestDB(t)
	seedSession(t, s, "s1")
	seedSession(t, s, "s2")
	var seqs []int64
	for i := 0; i < 5; i++ {
		seq, err := s.AppendEvent(ctxT(), objid.New(objid.Event), "s1", "session.message", map[string]any{"i": i})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		seqs = append(seqs, seq)
	}
	_, _ = s.AppendEvent(ctxT(), objid.New(objid.Event), "s2", "session.message", map[string]any{"other": true})

	got, more, err := s.EventsBefore(ctxT(), "s1", seqs[3], 2)
	if err != nil {
		t.Fatalf("EventsBefore: %v", err)
	}
	if len(got) != 2 || !more {
		t.Fatalf("窗口不符: len=%d more=%v", len(got), more)
	}
	if got[0].Seq != seqs[1] || got[1].Seq != seqs[2] {
		t.Fatalf("应升序返回 [seq2,seq3]: %+v", got)
	}
	if got[0].SessionID != "s1" || got[1].SessionID != "s1" {
		t.Fatalf("跨会话混入: %+v", got)
	}

	oldest, more2, err := s.EventsBefore(ctxT(), "s1", seqs[1], 5)
	if err != nil {
		t.Fatalf("EventsBefore oldest: %v", err)
	}
	if len(oldest) != 1 || more2 || oldest[0].Seq != seqs[0] {
		t.Fatalf("最早一页不符: len=%d more=%v %+v", len(oldest), more2, oldest)
	}

	// before=-1 → 取末尾 limit 条（仍升序）。
	tail, more3, err := s.EventsBefore(ctxT(), "s1", -1, 2)
	if err != nil {
		t.Fatalf("EventsBefore tail: %v", err)
	}
	if len(tail) != 2 || !more3 || tail[0].Seq != seqs[3] || tail[1].Seq != seqs[4] {
		t.Fatalf("末尾页不符: len=%d more=%v %+v", len(tail), more3, tail)
	}

	// before=0 → 「第 0 条之前」：空页、没有更早的（哨兵换成 -1 之后 0 不再是"取末尾"）。
	empty, more4, err := s.EventsBefore(ctxT(), "s1", 0, 2)
	if err != nil {
		t.Fatalf("EventsBefore before=0: %v", err)
	}
	if len(empty) != 0 || more4 {
		t.Fatalf("before=0 应为空页: len=%d more=%v %+v", len(empty), more4, empty)
	}
}

// TestSessionWorkspaceRoundTrip 工作空间字段（cwd + workspace_temporary）落库往返。
// 权限标志必须是**权威断言**：前端/离线回放不得从路径形状推断"临时空间"。
func TestSessionWorkspaceRoundTrip(t *testing.T) {
	s := openTestDB(t)
	now := time.Now().UTC()
	in := model.SessionEntity{
		ID: "ws-1", Cwd: "/work/alpha", WorkspaceTemporary: true,
		Status: "in_progress", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertSession(ctxT(), in); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	got, ok, err := s.GetSession(ctxT(), "ws-1")
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if got.Cwd != "/work/alpha" || !got.WorkspaceTemporary {
		t.Fatalf("工作空间字段未往返: %+v", got)
	}

	// 覆盖写：显式指定（非临时）的新值必须能覆盖旧值（不是只写一次的"创建期标志"）。
	in.WorkspaceTemporary = false
	in.Cwd = "/work/beta"
	if err := s.UpsertSession(ctxT(), in); err != nil {
		t.Fatalf("UpsertSession(覆盖): %v", err)
	}
	got, _, _ = s.GetSession(ctxT(), "ws-1")
	if got.Cwd != "/work/beta" || got.WorkspaceTemporary {
		t.Fatalf("覆盖写未生效: %+v", got)
	}

	// ListSessions 列表路径也带出该列（v5 迁移后的全列查询）。
	list, err := s.ListSessions(ctxT(), nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, c := range list {
		if c.ID == "ws-1" && (c.Cwd != "/work/beta" || c.WorkspaceTemporary) {
			t.Fatalf("列表路径字段不符: %+v", c)
		}
	}
}
