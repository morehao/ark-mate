package store

// ——— v31 验收：MCP 网关调用审计表（`mcp_call_audits`） ———
//
// 三条判据（对应 T2 第一刀的验收）：
//   - 写入与读面：一次调用一行、字段原样往返、读序是**写入序倒序**；
//   - 裁剪：保留期与行数上限两条都真的删得动，且**不误删边界那一秒**（`created_at` 是变长的
//     RFC3339Nano 文本，字符串比较在同一秒内不等于时间序——本文件最后一组用例就是钉这个的）；
//   - 迁移：v30 → v31 只多一张表，既有行一个都不动。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
	"github.com/morehao/ark-mate/internal/domain/session/run"
)

// TestMigrationV31AddsAuditTableWithoutTouchingExistingData v30 库升级到 v31：只有新表与新索引，
// 既有行原样不动；再跑一次幂等。
func TestMigrationV31AddsAuditTableWithoutTouchingExistingData(t *testing.T) {
	ctx := context.Background()
	s := openV30ForV31Test(t)
	seedSession(t, s, "s1")

	// 迁移**之前**造几条真实数据（会话 / 轮 / 事件）：判据是它们一个都不被改写。
	if err := s.CreateRun(ctx, run.Item{ID: "r1", SessionID: "s1", Status: "in_progress", LastSeq: -1}); err != nil {
		t.Fatalf("种 run: %v", err)
	}
	if _, err := s.AppendEvent(ctx, "evt_v31_probe", "s1", "session.user_message", map[string]any{"text": "迁移前"}); err != nil {
		t.Fatalf("种事件: %v", err)
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM mcp_call_audits`); n != -1 {
		t.Fatalf("v30 库里不该有 mcp_call_audits（计数应为读不到），got %d", n)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate(v31): %v", err)
	}
	// 版本号钉成"≥ 31 且等于本二进制支持的最新版"：以后每加一步迁移都不用回来改这条
	// （它要验的是"审计表在这一步之后存在"，不是"schema 永远停在 31"）。
	if v, err := s.SchemaVersion(ctx); err != nil || v != latestSchemaVersion() {
		t.Fatalf("schema 版本应 = %d，got %d (err=%v)", latestSchemaVersion(), v, err)
	}
	// 表与两张索引都在（索引名带表名段是守卫 A2 的要求，这里顺带验它真的建出来了）。
	for _, name := range []string{
		"mcp_call_audits",
		"idx_mcp_call_audits_session",
		"idx_mcp_call_audits_created",
	} {
		if n := scalarCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n == 0 {
			t.Fatalf("迁移后应有 %s", name)
		}
	}

	// 既有数据不动：会话还在、轮还是进行中、事件正文没被改写。
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM sessions WHERE id = 's1'`); n != 1 {
		t.Fatalf("迁移不得动既有会话行，got %d", n)
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM runs WHERE id = 'r1' AND status = 'in_progress'`); n != 1 {
		t.Fatalf("迁移不得动既有轮行，got %d", n)
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM events WHERE id = 'evt_v31_probe'`); n != 1 {
		t.Fatalf("迁移不得动既有事件行，got %d", n)
	}

	// 幂等：再跑一次，新表仍然是空的（不重复建表、不重放）。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
	if n := scalarCount(t, s, `SELECT COUNT(*) FROM mcp_call_audits`); n != 0 {
		t.Fatalf("重复迁移后审计表应仍为空，got %d", n)
	}
}

// openV30ForV31Test 造一个**真实的 v30 库**（完整跑 v1..v30）。
//
// 与 `openTestDB` 的分工：那个直接落到最新版，测的是"新库长什么样"；本步要验的是
// **从上一版升上来**这件事，对着已经是 v31 的库测等于什么都没测。
func openV30ForV31Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v30.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 30)
	return s
}

// scalarCount 跑一条计数查询；查询本身失败（比如表还不存在）→ -1
// （与 `migrate_v13_test.go` 的 `countRows` 分工不同：那个按表名数行数、读不到即致命）。
func scalarCount(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.Raw(query, args...).Row().Scan(&n); err != nil {
		return -1
	}
	return n
}

// TestMcpCallAuditAppendAndRead 写入与读面：字段原样往返、读序是写入序倒序。
func TestMcpCallAuditAppendAndRead(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	at := time.Date(2026, 10, 21, 9, 0, 0, 123456789, time.UTC)

	recs := []McpCallRecord{
		{SessionID: "ses_1", RunID: "run_1", ServerID: "gh", Tool: "search", Outcome: McpCallOutcomeOK, LatencyMS: 42, At: at},
		{SessionID: "ses_1", RunID: "run_1", ServerID: "gh", Tool: "search", Outcome: McpCallOutcomeError, ErrorKind: "upstream", LatencyMS: 8000, At: at.Add(time.Second)},
		{SessionID: "ses_1", Tool: "ghost__echo", Outcome: McpCallOutcomeDeny, At: at.Add(2 * time.Second)},
		{SessionID: "ses_2", ServerID: "pg", Tool: "query", Outcome: McpCallOutcomeOK, At: at.Add(3 * time.Second)},
	}
	for _, r := range recs {
		if err := s.AppendMcpCallAudit(ctx, r); err != nil {
			t.Fatalf("写审计 %+v: %v", r, err)
		}
	}

	got, err := s.McpCallAuditsBySession(ctx, "ses_1", 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ses_1 应恰好 3 行（另一个会话的行不许串进来），got %d：%+v", len(got), got)
	}
	// 倒序：最后写的排第一（复盘要看"最近的"）。
	if got[0].Outcome != McpCallOutcomeDeny || got[2].Outcome != McpCallOutcomeOK {
		t.Fatalf("读序应是写入序倒序，got %+v", got)
	}
	// 字段往返（含两张窄列：RunID / ErrorKind / LatencyMS）。
	if got[1].RunID != "run_1" || got[1].ServerID != "gh" || got[1].Tool != "search" ||
		got[1].ErrorKind != "upstream" || got[1].LatencyMS != 8000 {
		t.Fatalf("字段没有原样往返：%+v", got[1])
	}
	if !got[1].At.Equal(at.Add(time.Second)) {
		t.Fatalf("时刻应按 RFC3339Nano(UTC) 原样往返：got %v（want %v）", got[1].At, at.Add(time.Second))
	}
	// deny 行没有 server（选址之前就被拒）也不能因此丢字段。
	if got[0].ServerID != "" || got[0].Tool != "ghost__echo" {
		t.Fatalf("deny 行应保留请求名、server 留空：%+v", got[0])
	}

	// 读面有界：limit 生效（复盘一次只看最近若干行）。
	if got, err := s.McpCallAuditsBySession(ctx, "ses_1", 1); err != nil || len(got) != 1 || got[0].Outcome != McpCallOutcomeDeny {
		t.Fatalf("limit=1 应只回最近一行，got %+v (err=%v)", got, err)
	}
}

// TestPruneMcpCallAuditsByRetention 保留期：过期的删、没过期的一行不动。
//
// 时间边界刻意**跨秒**取样之外还取同一秒内的两行（见下一条用例）：这里先验基本行为。
func TestPruneMcpCallAuditsByRetention(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	for i, at := range []time.Time{
		now.Add(-31 * 24 * time.Hour), // 过期
		now.Add(-29 * 24 * time.Hour), // 未过期
		now.Add(-1 * time.Hour),       // 未过期
	} {
		if err := s.AppendMcpCallAudit(ctx, McpCallRecord{
			SessionID: "ses_1", Tool: "t", Outcome: McpCallOutcomeOK, At: at, LatencyMS: int64(i),
		}); err != nil {
			t.Fatalf("写审计: %v", err)
		}
	}

	removed, err := s.PruneMcpCallAudits(ctx, now)
	if err != nil {
		t.Fatalf("裁剪: %v", err)
	}
	if removed != 1 {
		t.Fatalf("应只删掉那条 31 天前的行，got removed=%d", removed)
	}
	got, err := s.McpCallAuditsBySession(ctx, "ses_1", 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("保留期内应剩 2 行，got %d：%+v", len(got), got)
	}
	// 幂等：再裁一次什么都不删。
	if removed, err := s.PruneMcpCallAudits(ctx, now); err != nil || removed != 0 {
		t.Fatalf("重复裁剪应为 no-op，got removed=%d err=%v", removed, err)
	}
}

// TestPruneMcpCallAuditsKeepsNewestWithinCap 行数上限：超上限时截掉**最旧**的行，留下的就是最新的。
func TestPruneMcpCallAuditsKeepsNewestWithinCap(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if err := s.AppendMcpCallAudit(ctx, McpCallRecord{
			SessionID: "ses_1", Tool: "t", Outcome: McpCallOutcomeOK,
			At: now.Add(-time.Duration(5-i) * time.Minute), LatencyMS: int64(i),
		}); err != nil {
			t.Fatalf("写审计: %v", err)
		}
	}
	// 用**带参形态**跑小上限：上限的取值是产品口径（两万），与"超限就截最旧的"是两个问题。
	removed, err := s.pruneMcpCallAudits(ctx, now, 24*time.Hour, 2)
	if err != nil {
		t.Fatalf("裁剪: %v", err)
	}
	if removed != 3 {
		t.Fatalf("应删掉 3 行（5 行收到上限 2），got %d", removed)
	}
	got, err := s.McpCallAuditsBySession(ctx, "ses_1", 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(got) != 2 || got[0].LatencyMS != 4 || got[1].LatencyMS != 3 {
		t.Fatalf("留下应是**最新**的两行（写入序），got %+v", got)
	}
}

// TestPruneMcpCallAuditsComparesTimesNotStrings 裁剪按**时间**比，不按字符串比。
//
// 这是 `created_at` 为变长 RFC3339Nano 文本（末尾的 0 被裁掉）带来的真实陷阱：
// `"…:00Z"` 与 `"…:00.5Z"` 在同一秒里，字符串比较会说前者**更晚**（'Z' > '.'），
// 于是"按字符串删过期行"会把**更旧**的那行留下来、把**更新**的那行删掉。判据直接钉结果。
func TestPruneMcpCallAuditsComparesTimesNotStrings(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	old := base                                // 整秒：落库文本以 `Z` 收尾、没有小数部分
	fresh := base.Add(500 * time.Millisecond)  // 同一秒内、更晚：落库文本带 `.5Z`
	cutoff := base.Add(200 * time.Millisecond) // 保留期切在两者之间
	now := base.Add(time.Hour)

	for _, at := range []time.Time{old, fresh} {
		if err := s.AppendMcpCallAudit(ctx, McpCallRecord{
			SessionID: "ses_1", Tool: "t", Outcome: McpCallOutcomeOK, At: at,
		}); err != nil {
			t.Fatalf("写审计: %v", err)
		}
	}
	// now - retention = cutoff ⇒ retention = now - cutoff
	if _, err := s.pruneMcpCallAudits(ctx, now, now.Sub(cutoff), 0); err != nil {
		t.Fatalf("裁剪: %v", err)
	}
	got, err := s.McpCallAuditsBySession(ctx, "ses_1", 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(got) != 1 || !got[0].At.Equal(fresh) {
		t.Fatalf("应只留更晚的那行（%v），got %+v——按字符串比会删错人", fresh, got)
	}
}
