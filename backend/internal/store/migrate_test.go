package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/dbclient"
)

// applySchemaUpTo 把库迁到指定版本（只跑 v1..v 的 DDL 并记录版本），用于重放**历史版本的真实库形态**。
//
// 为什么不放一个二进制旧库文件当夹具：DDL 是冻结的历史（migrate.go 里"绝不回改历史"是硬约定），
// 按版本重放能造出**任意**历史形态且可读可审；而二进制夹具既看不出它到底是哪一版，也没法在
// 需要时造出"v5 升级到 v7"这类别的组合。
func applySchemaUpTo(t *testing.T, s *Store, version int) {
	t.Helper()
	ctx := context.Background()
	if err := s.db.Exec(`CREATE TABLE IF NOT EXISTS ` + model.TableNameSchemaMigration + ` (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("建 schema_migrations: %v", err)
	}
	for _, m := range schemaMigrations {
		if m.version > version {
			break
		}
		for _, stmt := range m.statements {
			if err := s.db.Exec(stmt).Error; err != nil {
				t.Fatalf("重放 v%d 失败: %v（%s）", m.version, err, stmt)
			}
		}
		if err := s.migrations.WithTx(s.db).Record(ctx, m.version, m.name); err != nil {
			t.Fatalf("记录 v%d 失败: %v", m.version, err)
		}
	}
}

// migrateUpTo 是 `Migrate` 的"只跑到某个版本"版本（测试专用，走**同一条** statements+apply 路径）。
//
// 为什么需要它：v15 是**一次性破坏性重建**（清空 7 张表），于是"vN 升级不丢既有数据"这类
// 历史步骤的断言只有跑到 v15 **之前**才成立。把断言改成"数据没了"会让那些用例失去意义
// ——它们要守的正是"历史上的增列/建表步骤不丢数据"这条性质。
//
// 刻意复用 `applyMigration`（而不是只跑 DDL）：否则测试造出的是"线上不会出现的中间态"。
// 备份步骤与 `Migrate` 逐字一致（v12/v13 的 notes 里带它，用例也断言它）。
func migrateUpTo(t *testing.T, s *Store, version int) {
	t.Helper()
	ctx := context.Background()
	for _, m := range schemaMigrations {
		if m.version > version {
			break
		}
		applied, _, err := s.migrations.AppliedVersions(ctx)
		if err != nil {
			t.Fatalf("读已应用版本: %v", err)
		}
		if applied[m.version] {
			continue
		}
		if m.backup != "" {
			path, berr := s.backupBeforeMigration(m)
			if berr != nil {
				t.Fatalf("迁移 v%d 前备份失败: %v", m.version, berr)
			}
			s.migrationBackupPath = path
		}
		if err := s.applyMigration(ctx, m); err != nil {
			t.Fatalf("迁移到 v%d（%s）失败: %v", m.version, m.name, err)
		}
	}
}

// legacyConversationRow 是 v14 及更早的 `conversations` 表的一行（**历史形状**）。
//
// 只有"停在 v15 之前"的迁移用例用它：DAO 认的是 v15 的 `sessions`，读不到历史表——
// 这正是"实体改名不得跟随进历史迁移步"（守卫⑤）的另一面。
type legacyConversationRow struct {
	ID                    string
	Title                 string
	Provider              string
	Model                 string
	ProviderSessionID     string
	LastSeq               int64
	Pinned                int64
	WorkspaceTemporary    int64
	Origin                string
	CapabilityJSON        string
	CapabilityFingerprint string
}

// readLegacyConversation 用裸 SQL 读一行历史 `conversations`；读不到即失败。
func readLegacyConversation(t *testing.T, s *Store, id string) legacyConversationRow {
	t.Helper()
	var row legacyConversationRow
	if err := s.db.Raw(`SELECT id, title, provider, model, provider_session_id, last_seq,
		pinned, workspace_temporary, origin, capability_json, capability_fingerprint
		FROM conversations WHERE id = ?`, id).Scan(&row).Error; err != nil {
		t.Fatalf("读历史会话行 %s: %v", id, err)
	}
	if row.ID == "" {
		t.Fatalf("历史会话行 %s 不见了", id)
	}
	return row
}

// TestMigrateV6ToV7KeepsData v6 → v7 升级：三个新列纯增列（默认 `”`），既有数据一行不丢。
//
// 这一条是 M3-1 的验收核心：代际绑定把"会话钉住的能力"落进库，而**用户手上的库是 v6 的**。
// 升级一旦丢数据或把旧行读成异常，损失不可逆（会话是平台的事实源）。
func TestMigrateV6ToV7KeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	// 造一个**真实的 v6 库**：跑到 v6，写一行带数据的会话（此时还没有 origin 等列）。
	applySchemaUpTo(t, s, 6)
	at := model.NowTS()
	if err := s.db.Exec(`INSERT INTO conversations
		(id, agent_id, cwd, workspace_temporary, provider, model, runtime_hint, provider_session_id,
		 title, pinned, archived, status, last_seq, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"c-old", "a1", "/tmp/w", 1, "p1", "m1", "opencode", "ses_1", "旧会话", 1, 0, "completed", 7, at, at,
	).Error; err != nil {
		t.Fatalf("写 v6 会话行: %v", err)
	}

	// 升级（幂等：跑两次都应成功且不重复报错）。
	//
	// 只跑到 v7：本用例守的是"v7 的三个增列不丢既有数据"，而 v15 是**刻意的**破坏性重建
	// （见 TestMigrateV15*），跑到最新会把这条断言变成"数据没了"这种没有信息量的结论。
	migrateUpTo(t, s, 7)
	migrateUpTo(t, s, 7)

	// 旧行必须原样还在，新列为默认值（裸 SQL：这条用例停在 v7，表名还是历史名）。
	got := readLegacyConversation(t, s, "c-old")
	if got.Title != "旧会话" || got.Provider != "p1" || got.Model != "m1" || got.LastSeq != 7 ||
		got.ProviderSessionID != "ses_1" || got.Pinned != 1 || got.WorkspaceTemporary != 1 {
		t.Fatalf("旧列在升级中失真: %+v", got)
	}
	if got.Origin != "" || got.CapabilityJSON != "" || got.CapabilityFingerprint != "" {
		t.Fatalf("新列应是空串默认值（空 = 未声明能力选择，与升级前行为一致）: origin=%q json=%q fp=%q",
			got.Origin, got.CapabilityJSON, got.CapabilityFingerprint)
	}

	// 新列可写可读（v7 的三个增列真的落到了这张表上）。
	if err := s.db.Exec(`INSERT INTO conversations
		(id, agent_id, cwd, status, origin, capability_json, capability_fingerprint, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		"c-new", "a1", "/tmp/w", "in_progress", "automation:nightly", `{"skills":["s1"]}`, "fp123", at, at,
	).Error; err != nil {
		t.Fatalf("写 v7 会话行: %v", err)
	}
	got = readLegacyConversation(t, s, "c-new")
	if got.Origin != "automation:nightly" || got.CapabilityJSON != `{"skills":["s1"]}` || got.CapabilityFingerprint != "fp123" {
		t.Fatalf("新列往返失真: %+v", got)
	}

	// 两个索引要真的建出来（代际回收与"自动化会话"查询分别依赖它们；
	// 建不出来时查询照样能跑，只是退化成全表扫——静默的性能回归，测试必须钉住）。
	for _, idx := range []string{"idx_conversations_origin", "idx_conversations_fingerprint"} {
		var name string
		row := s.db.Raw(`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`, idx).Row()
		if err := row.Scan(&name); err != nil || name != idx {
			t.Fatalf("索引 %s 未建出: name=%q err=%v", idx, name, err)
		}
	}

	// 版本与"库高于二进制则拒绝启动"的既有语义不受影响。
	var maxV int
	if err := s.db.Raw(`SELECT MAX(version) FROM ` + model.TableNameSchemaMigration).Row().Scan(&maxV); err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if maxV != 7 {
		t.Fatalf("本用例只跑到 v7，实际版本 %d", maxV)
	}
}

// TestMigrateRefusesFutureSchema 库版本高于二进制 → **拒绝启动**（不是"尽力而为地打开"）。
//
// M6-1 的 Gate 明确要求这一条在 v8 上仍成立。它的价值只在**降级**时体现：用户用新二进制
// 建了 v9 的库，再用旧二进制（latest=v8）去开——若这里放行，旧代码会按自己认识的列去读写
// 一个它不认识的 schema，损坏是静默且不可逆的（v8 的 `capability_revisions` 正是"新列/新表"
// 频出的那一类改动）。
func TestMigrateRefusesFutureSchema(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	if err := s.db.Exec(`INSERT INTO `+model.TableNameSchemaMigration+` (version, name, applied_at) VALUES (?,?,?)`,
		latestSchemaVersion()+1, "future", model.NowTS()).Error; err != nil {
		t.Fatalf("造未来版本行: %v", err)
	}
	if err := s.Migrate(ctx); err == nil {
		t.Fatalf("库版本高于二进制（v%d > v%d）时必须拒绝启动", latestSchemaVersion()+1, latestSchemaVersion())
	}
}

// TestMigrateV7ToV14KeepsData v7 → v14 升级：v8 起都是**纯增表/纯增列**，既有数据一行不丢，
// 新表从空开始。
//
// 上界钉在 v14（而不是"最新"）：v15 是**一次性破坏性重建**（用户 2026-10-11 拍板、仅此一次），
// 跑到它上面会让本用例的"不丢数据"断言失去意义。
//
// 两个容易搞错的点，各自钉一条：
//  1. 新表**必须是空的**。用户的存量能力眼下还在 `<CapabilityDir>` 的文件里，要由 M6-4 的
//     导入器灌进来；升级时"顺手把什么默认值填进去"会让导入无法判断"库里到底有没有东西"。
//  2. v8 的库能被二进制打开、能升到最新、且**重复迁移幂等**（用户每次启动都会跑一遍）。
func TestMigrateV7ToV14KeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	// 造一个**真实的 v7 库**（含 v7 的三列）并写一行带数据的会话。
	applySchemaUpTo(t, s, 7)
	at := model.NowTS()
	if err := s.db.Exec(`INSERT INTO conversations
		(id, agent_id, cwd, workspace_temporary, provider, model, runtime_hint, provider_session_id,
		 title, pinned, archived, status, last_seq, origin, capability_json, capability_fingerprint,
		 created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"c-v7", "a1", "/tmp/w", 1, "p1", "m1", "opencode", "ses_1", "旧会话", 1, 0, "completed", 7,
		"automation:nightly", `{"skills":["s1"]}`, "fp-v7", at, at,
	).Error; err != nil {
		t.Fatalf("写 v7 会话行: %v", err)
	}

	migrateUpTo(t, s, 14)
	migrateUpTo(t, s, 14)

	// ① 旧行原样还在（含 v7 那三列）；裸 SQL 读历史表名（见 legacyConversationRow）。
	got := readLegacyConversation(t, s, "c-v7")
	if got.Title != "旧会话" || got.LastSeq != 7 || got.Origin != "automation:nightly" ||
		got.CapabilityJSON != `{"skills":["s1"]}` || got.CapabilityFingerprint != "fp-v7" {
		t.Fatalf("旧行在升级中失真: %+v", got)
	}

	// ② 三张新表都建出来了，且**从空开始**（存量定义还在文件里，等 M6-4 导入）。
	if n, err := s.CountSkillDefinitions(ctx); err != nil || n != 0 {
		t.Fatalf("升级后 skills 应为空: n=%d err=%v", n, err)
	}
	if n, err := s.CountMcpServerDefinitions(ctx); err != nil || n != 0 {
		t.Fatalf("升级后 mcp_servers 应为空: n=%d err=%v", n, err)
	}
	if seq, err := s.MaxCapabilityRevisionSeq(ctx); err != nil || seq != 0 {
		t.Fatalf("升级后 capability_revisions 应为空: seq=%d err=%v", seq, err)
	}

	// ③ 新表可写（v8 的写路径真的落到了这三张表上）。
	if ok, err := s.UpsertSkillDefinition(ctx, "s1", `{"name":"升级后新建"}`, 0, "cli"); err != nil || !ok {
		t.Fatalf("升级后写 skill 定义: ok=%v err=%v", ok, err)
	}
	revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindSkill, "s1")
	if err != nil || len(revs) != 1 || revs[0].Seq != 1 {
		t.Fatalf("升级后的审计应从 seq=1 开始: %+v err=%v", revs, err)
	}

	// ④ 索引要真的建出来（"这条能力的变更历史"查询靠它；建不出来时查询照样能跑，
	// 只是退化成全表扫——静默的性能回归，测试必须钉住）。
	var name string
	row := s.db.Raw(`SELECT name FROM sqlite_master WHERE type='index' AND name = ?`,
		"idx_capability_revisions_capability").Row()
	if err := row.Scan(&name); err != nil || name != "idx_capability_revisions_capability" {
		t.Fatalf("索引 idx_capability_revisions_capability 未建出: name=%q err=%v", name, err)
	}

	// ⑤ 版本到位。
	var maxV int
	if err := s.db.Raw(`SELECT MAX(version) FROM ` + model.TableNameSchemaMigration).Row().Scan(&maxV); err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if maxV != 14 {
		t.Fatalf("本用例只跑到 v14（v15 是刻意的破坏性重建），got %d", maxV)
	}
}

// TestMigrateV8ToV9AddsAgents v8 → v9 升级：v9 同样是**纯增表**（agents），既有定义行一行不丢，
// 新表从空开始（存量 agent 还在 `<AgentDir>/*.json` 里，等 M6-6 的导入器灌进来）。
func TestMigrateV8ToV9AddsAgents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	// 造一个**真实的 v8 库**并写一行 skill 定义 + 一条审计。
	applySchemaUpTo(t, s, 8)
	if ok, err := s.UpsertSkillDefinition(ctx, "sk-old", `{"name":"v8 存量"}`, 0, "test"); err != nil || !ok {
		t.Fatalf("写 v8 skill 行: ok=%v err=%v", ok, err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v8 → v9 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v8 的行与审计原样还在。
	sk, found, err := s.GetSkillDefinition(ctx, "sk-old")
	if err != nil || !found || sk.Body != `{"name":"v8 存量"}` || sk.Revision != 1 {
		t.Fatalf("升级后 v8 skill 行失真: found=%v err=%v row=%+v", found, err, sk)
	}
	if revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindSkill, "sk-old"); err != nil || len(revs) != 1 {
		t.Fatalf("升级后 v8 审计应保留: n=%d err=%v", len(revs), err)
	}

	// ② agents 表建出来了且**从空开始**（种子不落行；存量定义等导入器）。
	if n, err := s.CountAgentDefinitions(ctx); err != nil || n != 0 {
		t.Fatalf("升级后 agents 应为空: n=%d err=%v", n, err)
	}
	if _, ok, err := s.GetDefaultAgentID(ctx); err != nil || ok {
		t.Fatalf("升级后 default_agent 应未设置: ok=%v err=%v", ok, err)
	}

	// ③ agent 写路径真的落到新表上（行 + 审计，且审计 seq 从 v8 的那条之后继续）。
	if ok, err := s.UpsertAgentDefinition(ctx, "my-agent", `{"id":"my-agent","name":"升级后新建"}`, 0, "rest"); err != nil || !ok {
		t.Fatalf("升级后写 agent 定义: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UpsertAgentDefinition(ctx, "my-agent", `{"id":"my-agent","name":"改名"}`, 1, "rest"); err != nil || !ok {
		t.Fatalf("升级后覆盖写 agent 定义: ok=%v err=%v", ok, err)
	}
	// CAS 没中 → 行与审计都不变（与能力面同一条不变量）。
	if ok, err := s.UpsertAgentDefinition(ctx, "my-agent", `{"id":"my-agent"}`, 1, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 应被拒: ok=%v err=%v", ok, err)
	}
	ag, found, err := s.GetAgentDefinition(ctx, "my-agent")
	if err != nil || !found || ag.Revision != 2 || ag.Body != `{"id":"my-agent","name":"改名"}` {
		t.Fatalf("agent 行不符: found=%v err=%v row=%+v", found, err, ag)
	}
	revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindAgent, "my-agent")
	if err != nil || len(revs) != 2 || revs[0].Action != "create" || revs[1].Action != "update" {
		t.Fatalf("agent 审计不符: %+v err=%v", revs, err)
	}

	// ④ 「当前 Agent」落 meta，且能被清除（被删 agent 的连带清理）。
	if err := s.SetDefaultAgentID(ctx, "my-agent"); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := s.GetDefaultAgentID(ctx); err != nil || !ok || id != "my-agent" {
		t.Fatalf("读 default_agent: id=%q ok=%v err=%v", id, ok, err)
	}
	if err := s.ClearDefaultAgentID(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetDefaultAgentID(ctx); err != nil || ok {
		t.Fatalf("清除后应报未设置: ok=%v err=%v", ok, err)
	}

	// ⑤ 删除也留痕（审计在行消失后仍可查）。
	if ok, err := s.DeleteAgentDefinition(ctx, "my-agent", 2, "rest"); err != nil || !ok {
		t.Fatalf("删 agent 行: ok=%v err=%v", ok, err)
	}
	if _, found, _ := s.GetAgentDefinition(ctx, "my-agent"); found {
		t.Fatal("删除后不该还读到行")
	}
	revs, err = s.ListCapabilityRevisions(ctx, model.CapabilityKindAgent, "my-agent")
	if err != nil || len(revs) != 3 || revs[2].Action != "delete" || revs[2].Digest != "" {
		t.Fatalf("删除审计不符（删定义也留痕且 digest 为空）: %+v err=%v", revs, err)
	}
}

// TestMigrateV9ToV10AddsAutomations v9 → v10 升级：v10 同样是**纯增表**（automations），
// 既有 agent/能力定义一行不丢，新表从空开始（存量定时任务还在 `<AutomationDir>/*.json` 里）。
//
// 运行态列的默认值也要钉住：未触发过的形态是**空串 / 0 / '[]'** 而不是 NULL——多一种表示就多
// 一条"面板上明明是空的，读出来却是个 nil 解引用"的路径。
func TestMigrateV9ToV10AddsAutomations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v9.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	applySchemaUpTo(t, s, 9)
	if ok, err := s.UpsertAgentDefinition(ctx, "ag-old", `{"id":"ag-old","name":"v9 存量"}`, 0, "test"); err != nil || !ok {
		t.Fatalf("写 v9 agent 行: ok=%v err=%v", ok, err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v9 → v10 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v9 的行原样还在。
	if ag, found, err := s.GetAgentDefinition(ctx, "ag-old"); err != nil || !found || ag.Revision != 1 {
		t.Fatalf("升级后 v9 agent 行失真: found=%v err=%v row=%+v", found, err, ag)
	}
	// ② automations 从空开始。
	if n, err := s.CountAutomationDefinitions(ctx); err != nil || n != 0 {
		t.Fatalf("升级后 automations 应为空: n=%d err=%v", n, err)
	}

	// ③ 新建行的运行态列有确定的默认形态（不是 NULL）。
	if ok, err := s.UpsertAutomationDefinition(ctx, "nightly", `{"id":"nightly","name":"夜跑"}`, 0, "rest"); err != nil || !ok {
		t.Fatalf("升级后写 automation 定义: ok=%v err=%v", ok, err)
	}
	row, found, err := s.GetAutomationDefinition(ctx, "nightly")
	if err != nil || !found {
		t.Fatalf("读回 automation: found=%v err=%v", found, err)
	}
	if row.LastFiredAt != "" || row.LastStatus != "" || row.LastReason != "" ||
		row.LastSessionID != "" || row.ConsecutiveFailures != 0 || row.RecentRuns != "[]" {
		t.Fatalf("未触发过的运行态默认值不符: %+v", row)
	}

	// ④ 认领是一道 CAS：只有还等于读到的那个值才推得动（"不重复触发"的仲裁者）。
	if ok, err := s.ClaimAutomationOccurrence(ctx, "nightly", "", "2026-10-09T01:00:00Z"); err != nil || !ok {
		t.Fatalf("首次认领应成功: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ClaimAutomationOccurrence(ctx, "nightly", "", "2026-10-09T01:00:00Z"); err != nil || ok {
		t.Fatalf("同一 occurrence 的二次认领必须失败（否则会重复触发）: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ClaimAutomationOccurrence(ctx, "nightly", "2026-10-09T01:00:00Z", "2026-10-10T01:00:00Z"); err != nil || !ok {
		t.Fatalf("下一个 occurrence 应能认领: ok=%v err=%v", ok, err)
	}

	// ⑤ 运行态写回同事务 + 认领 CAS；定义 revision 不被运行态写回抬高（与文件载体的有意差异）。
	before, _, _ := s.GetAutomationDefinition(ctx, "nightly")
	if ok, err := s.SaveAutomationRuntime(ctx, "nightly", "2026-10-09T01:00:00Z",
		AutomationRuntimeState{LastFiredAt: "2026-10-10T01:00:00Z", LastStatus: "completed", ConsecutiveFailures: 0}); err != nil || ok {
		t.Fatalf("拿过期的认领写回运行态必须被拒: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SaveAutomationRuntime(ctx, "nightly", "2026-10-10T01:00:00Z",
		AutomationRuntimeState{LastFiredAt: "2026-10-10T01:00:00Z", LastStatus: "completed", RecentRuns: `[{"status":"completed"}]`}); err != nil || !ok {
		t.Fatalf("运行态写回: ok=%v err=%v", ok, err)
	}
	after, _, _ := s.GetAutomationDefinition(ctx, "nightly")
	if after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("运行态写回不该抬高定义 revision: before=%+v after=%+v", before, after)
	}
	if after.LastStatus != "completed" || after.RecentRuns != `[{"status":"completed"}]` {
		t.Fatalf("运行态列未落库: %+v", after)
	}
	// 审计只记定义变更（运行态推进不产生审计行）。
	revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindAutomation, "nightly")
	if err != nil || len(revs) != 1 || revs[0].Action != model.CapabilityActionCreate {
		t.Fatalf("automation 审计应只有建定义那一条: %+v err=%v", revs, err)
	}
}

// TestMigrateV10ToV11AddsProviderAndModel v10 → v11 升级：v11 同样是**纯增表**
// （providers + models），既有 agent/能力/automation 定义一行不丢，两张新表从空开始。
//
// 两张表**同版**是刻意的（设计「数据迁移与校验」第 2 条）：不出现"升级后供应商有了、
// 模型还不能用"的中间态。`models` 在文件时代**没有载体**（模型面整个由运行时派生），
// 所以它只建空表、没有任何东西要搬——升级后的第一屏是「清单为空」而不是「清单等于底座现有模型」。
func TestMigrateV10ToV11AddsProviderAndModel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v10.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	applySchemaUpTo(t, s, 10)
	if ok, err := s.UpsertAgentDefinition(ctx, "ag-old", `{"id":"ag-old","name":"v10 存量"}`, 0, "test"); err != nil || !ok {
		t.Fatalf("写 v10 agent 行: ok=%v err=%v", ok, err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v10 → v11 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v10 的行原样还在。
	if ag, found, err := s.GetAgentDefinition(ctx, "ag-old"); err != nil || !found || ag.Revision != 1 {
		t.Fatalf("升级后 v10 agent 行失真: found=%v err=%v row=%+v", found, err, ag)
	}
	// ② 两张新表从空开始。
	if n, err := s.CountProviderDefinitions(ctx); err != nil || n != 0 {
		t.Fatalf("升级后 providers 应为空: n=%d err=%v", n, err)
	}
	if rows, err := s.ListModelDefinitions(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("升级后 models 应为空: n=%d err=%v", len(rows), err)
	}

	// ③ 两张表都可写、可读，且审计按 kind 隔离（provider / model 各自一条）。
	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{
		ID: "deepseek-main", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
	}, 0, "rest"); err != nil || !ok {
		t.Fatalf("升级后写供应商: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "md-1", ProviderID: "deepseek-main", ModelID: "deepseek-v4-flash",
		Name: "Flash", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("升级后写模型条目: ok=%v err=%v", ok, err)
	}
	if revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "deepseek-main"); err != nil || len(revs) != 1 {
		t.Fatalf("provider 审计应为 1 条: n=%d err=%v", len(revs), err)
	}
	if revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindModel, "md-1"); err != nil || len(revs) != 1 {
		t.Fatalf("model 审计应为 1 条: n=%d err=%v", len(revs), err)
	}

	// ④ v12 起模型面**没有布尔列**了（默认模型是 meta 的单一值）：
	// 身份列 `model_id` 是**列**，且 `(provider_id, model_id)` 由库级唯一索引保证。
	if md, found, err := s.GetModelDefinition(ctx, "md-1"); err != nil || !found || md.ModelID != "deepseek-v4-flash" {
		t.Fatalf("读回模型条目失真: found=%v err=%v row=%+v", found, err, md)
	}
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "md-2", ProviderID: "deepseek-main", ModelID: "deepseek-v4-flash",
	}, "rest"); err != nil || ok {
		t.Fatalf("同 (provider_id, model_id) 应被唯一索引拦下: ok=%v err=%v", ok, err)
	}
	var idx int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_models_provider_model'`).
		Row().Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("v12 应建唯一索引 idx_models_provider_model: n=%d err=%v", idx, err)
	}
}
