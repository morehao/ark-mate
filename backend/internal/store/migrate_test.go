package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/model"
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
	// 与 Migrate 一样先保底建版本表（空库上直接读 AppliedVersions 会 "no such table"）。
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

// insertLegacyDefinition 按**停在 v16 之前**那一版的表名，手写"定义行 + create 审计"。
//
// 为什么不能用 `UpsertXxxDefinition`：它们把审计写进 v16 的 `definition_revisions`，而
// v8–v14 的库里那张表还叫 `capability_revisions`（v16 才 `RENAME TO`）——DAO 认的永远是
// **最终**表名，所以"停在历史版本的用例"必须自己说那一版的话。理由与
// `legacyConversationRow`/`readLegacyConversation` 逐字相同：**实体改名不得跟随进历史迁移步**
// （守卫⑤/⑥），这一对助手是它在测试侧的两半。
//
// `table` 是那一版的定义表名（`skills` / `agents` / `automations`——三者在 v16 都没改名，
// 本次只改了审计表与 `sessions`/`inputs` 的列）。四张定义表同形：id / body / revision / updated_at。
func insertLegacyDefinition(t *testing.T, s *Store, table, kind, id, body string) {
	t.Helper()
	at := model.NowTS()
	if err := s.db.Exec(`INSERT INTO `+table+` (id, body, revision, updated_at) VALUES (?, ?, 1, ?)`,
		id, body, at).Error; err != nil {
		t.Fatalf("写历史定义行 %s(%s): %v", table, id, err)
	}
	// digest 用**当时那条**写路径的算法（`bodyDigest`），这样"审计能回答那一版到底是什么"
	// 这条性质在用例里也是真的，而不是随便填一个占位串。
	if err := s.db.Exec(`INSERT INTO capability_revisions (kind, capability_id, action, actor, at, digest)
		VALUES (?, ?, 'create', 'test', ?, ?)`, kind, id, at, bodyDigest(body)).Error; err != nil {
		t.Fatalf("写历史审计行 %s(%s): %v", kind, id, err)
	}
}

// legacyRevisionSeq 读历史审计表的最大 seq（表名是那一版的 `capability_revisions`）。
func legacyRevisionSeq(t *testing.T, s *Store) int64 {
	t.Helper()
	var seq int64
	if err := s.db.Raw(`SELECT COALESCE(MAX(seq), 0) FROM capability_revisions`).Scan(&seq).Error; err != nil {
		t.Fatalf("读历史审计表水位: %v", err)
	}
	return seq
}

// legacySkillRevision 读历史审计表里某条 skill 的（审计条数，首条 seq）。
func legacySkillRevision(t *testing.T, s *Store, id string) (int, int64) {
	t.Helper()
	var n int
	var first int64
	if err := s.db.Raw(`SELECT COUNT(*), COALESCE(MIN(seq), 0) FROM capability_revisions
		WHERE kind = 'skill' AND capability_id = ?`, id).Row().Scan(&n, &first); err != nil {
		t.Fatalf("读历史审计行 skill(%s): %v", id, err)
	}
	return n, first
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
		"c-old", "a1", "/tmp/w", 1, "p1", "m1", "rtx", "ses_1", "旧会话", 1, 0, "completed", 7, at, at,
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
		"c-v7", "a1", "/tmp/w", 1, "p1", "m1", "rtx", "ses_1", "旧会话", 1, 0, "completed", 7,
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
	if seq := legacyRevisionSeq(t, s); seq != 0 {
		t.Fatalf("升级后 capability_revisions 应为空: seq=%d", seq)
	}

	// ③ 新表可写（v8 的写路径真的落到了这三张表上）。
	//
	// 审计那一半走裸 SQL + **历史表名**：`definition_revisions` 是 v16 才有的名字。
	insertLegacyDefinition(t, s, "skills", "skill", "s1", `{"name":"升级后新建"}`)
	if n, first := legacySkillRevision(t, s, "s1"); n != 1 || first != 1 {
		t.Fatalf("升级后的审计应从 seq=1 开始: n=%d first=%d", n, first)
	}

	// ④ 索引要真的建出来（"这条能力的变更历史"查询靠它；建不出来时查询照样能跑，
	// 只是退化成全表扫——静默的性能回归，测试必须钉住）。索引名同样是**那一版**的。
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

// TestMigrateV8ToLatestKeepsDataAndDropsAgents v8 → 最新：v8 的存量行与审计原样保留，而 v19 把
// agent 面从库里彻底撤掉（表没了、`kind='agent'` 的审计行删了、meta 的两个键清了）。
//
// v19 之前这条用例叫 TestMigrateV8ToV9AddsAgents，判据是"agents 表建出来了、agent 写路径可用"。
// v9 那一步今天照旧成立，但它已被 v19 整体推翻，而 `Migrate` 一路推到最新——所以本用例分两段：
// 先重放到 v9 钉住**历史步骤**（agents 表真被建出来，且当时那套表与键能写进去），再推到最新钉
// **撤除**的结果。两段合起来才是今天的事实。
//
// 历史夹具一律走**当时那一版**的表名与裸 SQL（typed DAO 已随 v19 删除）：这正是"实体改名不得
// 跟随进历史迁移步"的另一面——夹具也不许依赖今天的 API，否则历史步骤一被重写就没人发现。
func TestMigrateV8ToLatestKeepsDataAndDropsAgents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	defer func() { _ = s.Close() }()

	// 造一个**真实的 v9 库**（`applySchemaUpTo` 只重放 v1..v9 的 DDL 并记版本，v10 起的
	// statements + Go 数据迁移由下面的 `Migrate` 真实跑完）。写一行 v9 的 skill 定义 + 审计，
	// 再写一行 agent 定义 + 审计与两个 meta 键——这些就是 v19 要撤掉的东西，夹具必须带真数据。
	//
	// 顺带钉住"v9 那一步的 DDL 是冻结的历史"：真删掉 v9 的建表语句，下面这行写 agent 定义
	// 会直接报 `no such table: agents`。
	applySchemaUpTo(t, s, 9)
	insertLegacyDefinition(t, s, "skills", "skill", "sk-old", `{"name":"v9 存量"}`)
	insertLegacyDefinition(t, s, "agents", "agent", "ag-legacy", `{"id":"ag-legacy","name":"v9 存量"}`)
	if err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('default_agent', 'ag-legacy'), ('imported_agents', 'stale')`).Error; err != nil {
		t.Fatalf("写历史 meta 键: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v10 → 最新 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v8 的行与审计原样还在（v19 只动 agent 那一类，别的定义一行不碰）。
	sk, found, err := s.GetSkillDefinition(ctx, "sk-old")
	if err != nil || !found || sk.Body != `{"name":"v9 存量"}` || sk.Revision != 1 {
		t.Fatalf("升级后 v9 的 skill 行失真: found=%v err=%v row=%+v", found, err, sk)
	}
	if revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "sk-old"); err != nil || len(revs) != 1 {
		t.Fatalf("升级后 v9 的审计应保留: n=%d err=%v", len(revs), err)
	}

	// ② v19 的撤除：表没了、agent 那类审计行没了、meta 的两个键清了。
	count := func(sql, what string) int64 {
		t.Helper()
		var got int64
		if err := s.db.Raw(sql).Row().Scan(&got); err != nil {
			t.Fatalf("查 %s: %v", what, err)
		}
		return got
	}
	if n := count(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='agents'`, "agents 表"); n != 0 {
		t.Fatalf("最终形态不该还有 agents 表: n=%d", n)
	}
	if n := count(`SELECT count(*) FROM definition_revisions WHERE kind = 'agent'`, "agent 审计行"); n != 0 {
		t.Fatalf("agent 审计行必须随 v19 删除（否则库里留着已撤概念的审计）: n=%d", n)
	}
	if n := count(`SELECT count(*) FROM meta WHERE key IN ('default_agent','imported_agents')`, "agent meta 键"); n != 0 {
		t.Fatalf("两个 agent meta 键必须随 v19 清掉: n=%d", n)
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
	insertLegacyDefinition(t, s, "agents", "agent", "ag-old", `{"id":"ag-old","name":"v9 存量"}`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v9 → v10 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v9 的存量行**不得让升级中断**（历史夹具就是要带一行真数据走完整条链），但它本身在
	// 最终形态里必须不存在：agents 表被 v19 整表删掉。历史步骤与 v19 的撤除分别由
	// TestMigrateV8ToLatestKeepsDataAndDropsAgents 钉住。
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
	revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindAutomation, "nightly")
	if err != nil || len(revs) != 1 || revs[0].Action != model.DefinitionActionCreate {
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
	insertLegacyDefinition(t, s, "agents", "agent", "ag-old", `{"id":"ag-old","name":"v10 存量"}`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v10 → v11 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① v10 的存量行同上：只需不中断升级，最终形态里 agents 表已不存在（见 v8 那条用例）。
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
	if ok, err := s.CreateModelDefinition(ctx, model.ModelColumns{
		ID: "md-1", ProviderID: "deepseek-main", Model: "deepseek-v4-flash",
		Name: "Flash", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("升级后写模型条目: ok=%v err=%v", ok, err)
	}
	if revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindProvider, "deepseek-main"); err != nil || len(revs) != 1 {
		t.Fatalf("provider 审计应为 1 条: n=%d err=%v", len(revs), err)
	}
	if revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindModel, "md-1"); err != nil || len(revs) != 1 {
		t.Fatalf("model 审计应为 1 条: n=%d err=%v", len(revs), err)
	}

	// ④ v12 起模型面**没有布尔列**了（默认模型是 meta 的单一值）：
	// 身份列 `model_id` 是**列**，且 `(provider_id, model_id)` 由库级唯一索引保证。
	if md, found, err := s.GetModelDefinition(ctx, "md-1"); err != nil || !found || md.Model != "deepseek-v4-flash" {
		t.Fatalf("读回模型条目失真: found=%v err=%v row=%+v", found, err, md)
	}
	if ok, err := s.CreateModelDefinition(ctx, model.ModelColumns{
		ID: "md-2", ProviderID: "deepseek-main", Model: "deepseek-v4-flash",
	}, "rest"); err != nil || ok {
		t.Fatalf("同 (provider_id, model_id) 应被唯一索引拦下: ok=%v err=%v", ok, err)
	}
	var idx int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_models_provider_model'`).
		Row().Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("v12 应建唯一索引 idx_models_provider_model: n=%d err=%v", idx, err)
	}
}

// TestMigrationBackupLayoutV19 钉 v19 的备份布局：新备份落 `<db 目录>/backups/`，而**老布局的
// 备份照旧被当成有效退路列出来**（新的在前）。
//
// 两侧都要钉，因为两种失效方式都是静默的：
//
//	只钉新位置  —— 有人把老备份从扫描里删掉（"根下不该有东西"），于是本机明明有回退窗口，
//	               `mate doctor` 报"回退窗口已关闭"（假红，且会把用户推向不必要的重设）
//	只钉老位置  —— 新备份又落回根下，7 个文件重新堆起来（本次要消灭的形态）
func TestMigrationBackupLayoutV19(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "mate.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	defer func() { _ = s.Close() }()

	// 先落一次写：库文件是**惰性创建**的（`dbclient.Open` 只建句柄），而备份要的是"能回退的那个
	// 库文件"——空库没有可回退的东西，`backupBeforeMigration` 会如实跳过（见它的注释）。
	if err := s.db.Exec(`CREATE TABLE IF NOT EXISTS backup_probe (k TEXT)`).Error; err != nil {
		t.Fatalf("造出库文件: %v", err)
	}

	// 老布局遗留：与库文件同级（v19 之前的形态），它**必须**仍被发现。
	legacy := path + ".bak-v12-20260101T000000Z"
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	// ① 新备份落 backups/ 子目录（文件名保持 `<db 文件名>.bak-<tag>-<ts>`，恢复说明不用换形状）。
	got, err := s.backupBeforeMigration(migration{version: 99, name: "布局用例", backup: "v99"})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	wantDir := filepath.Join(home, backupDirName)
	if filepath.Dir(got) != wantDir {
		t.Fatalf("新备份应落在 %s，实际 %s", wantDir, got)
	}
	if filepath.Base(got) == filepath.Base(path) {
		t.Fatalf("备份文件名不该与库文件同名: %s", got)
	}
	// 判据是"能打开的库快照 + 完整性通过"，**不是**与库文件逐字节相同：快照由 SQLite 自己产出
	// （`VACUUM INTO`），会并入 `-wal` 里已提交的内容并整理页——逐字节相同这条判据恰好会奖励
	// 那个错误的实现（拷文件），见 TestMigrationBackupIncludesUncheckpointedWAL。
	assertOpenableSnapshot(t, got)

	// ② 两个布局一起列，且**新的在前**（按文件名倒序：tag 与时间戳都在文件名里）。
	all := s.MigrationBackupPaths()
	if len(all) != 2 {
		t.Fatalf("应同时列出新老布局各一份: %v", all)
	}
	if all[0] != got || all[1] != legacy {
		t.Fatalf("最新的应排最前（新布局 v99 > 老布局 v12）: %v", all)
	}

	// ③ 遗留清单只含老布局那一份（doctor 用它报"根下还有 N 个"）。
	le := s.MigrationBackupPathsLegacy()
	if len(le) != 1 || le[0] != legacy {
		t.Fatalf("遗留清单应只含老布局备份: %v", le)
	}

	// ④ 扫描**只读**：遗留文件仍原样在盘上（医生不搬不移）。
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("遗留备份不得被搬移或删除: %v", err)
	}
}

// readAllOrFail 读一个测试夹具文件（失败即 fatal）。
func readAllOrFail(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s: %v", p, err)
	}
	return b
}

// assertOpenableSnapshot 断言某个备份**确实是一个可用的库文件**（能打开、完整性通过）。
func assertOpenableSnapshot(t *testing.T, path string) {
	t.Helper()
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("备份必须是一个能打开的库文件: %v", err)
	}
	sqlDB, derr := db.DB()
	if derr != nil {
		t.Fatalf("取底层句柄: %v", derr)
	}
	defer func() { _ = sqlDB.Close() }()
	var check string
	if err := db.Raw(`PRAGMA integrity_check`).Row().Scan(&check); err != nil {
		t.Fatalf("备份完整性检查查询失败: %v", err)
	}
	if check != "ok" {
		t.Fatalf("备份完整性检查未通过: %q", check)
	}
}

// 迁移备份必须含 `-wal` 里**已提交但未 checkpoint** 的行。
//
// 这条用例钉的是"为什么备份不能拷文件"：`journal_mode` 是库的持久属性，本机用户库头部就是 WAL
// （平台自己开的新库是 `delete`，见 pkg/dbclient），WAL 下已提交的事务先落 `-wal`。纯拷主文件
// 会静默漏掉它们（反向断言就在本用例里：同一时刻的主文件副本查不到那一行），而文件名写着
// `bak-v19-<ts>`——回退窗口在真正需要它的那天才发现是缺的。修复 = 用 `VACUUM INTO` 取快照。
func TestMigrationBackupIncludesUncheckpointedWAL(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "mate.db")

	// 连接 A：先在建库默认模式下落一行（它进主文件），再切 WAL 提交第二行（它只进 `-wal`）。
	// **保持打开**：一关就会 checkpoint，`-wal` 被清掉，这条用例就测不到东西了。
	writer, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开写连接: %v", err)
	}
	wsql, werr := writer.DB()
	if werr != nil {
		t.Fatalf("取写连接句柄: %v", werr)
	}
	defer func() { _ = wsql.Close() }()
	if err := writer.Exec(`CREATE TABLE IF NOT EXISTS probe (k TEXT)`).Error; err != nil {
		t.Fatalf("建表: %v", err)
	}
	if err := writer.Exec(`INSERT INTO probe (k) VALUES ('before-wal')`).Error; err != nil {
		t.Fatalf("写入切换前的行: %v", err)
	}
	if err := writer.Exec(`PRAGMA journal_mode=WAL`).Error; err != nil {
		t.Fatalf("切 WAL: %v", err)
	}
	if err := writer.Exec(`INSERT INTO probe (k) VALUES ('in-wal')`).Error; err != nil {
		t.Fatalf("写入 WAL 里的行: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("前置条件不成立（应当有 -wal 文件）: %v", err)
	}

	// 连接 B（store 自己那条）产快照。
	reader, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开读连接: %v", err)
	}
	s := New(reader)
	s.path = path
	defer func() { _ = s.Close() }()
	got, err := s.backupBeforeMigration(migration{version: 99, name: "WAL 快照用例", backup: "v99"})
	if err != nil {
		t.Fatalf("产快照失败: %v", err)
	}
	assertOpenableSnapshot(t, got)

	db, err := dbclient.Open(got)
	if err != nil {
		t.Fatalf("开快照: %v", err)
	}
	sqlDB, _ := db.DB()
	defer func() { _ = sqlDB.Close() }()
	var inWAL int64
	if err := db.Raw(`SELECT count(*) FROM probe WHERE k = 'in-wal'`).Scan(&inWAL).Error; err != nil {
		t.Fatalf("查快照: %v", err)
	}
	if inWAL != 1 {
		t.Fatalf("快照必须含 -wal 里已提交的那一行，实际 count=%d（旧实现拷主文件时这里就是 0）", inWAL)
	}

	// 反向断言：同一时刻的**主文件副本**确实缺那一行——这正是被替换掉的实现，写在这里当判据的理由。
	plain := filepath.Join(home, "plain-copy.db")
	if err := os.WriteFile(plain, readAllOrFail(t, path), 0o600); err != nil {
		t.Fatalf("造主文件副本: %v", err)
	}
	pdb, err := dbclient.Open(plain)
	if err != nil {
		t.Fatalf("开主文件副本: %v", err)
	}
	psql, _ := pdb.DB()
	defer func() { _ = psql.Close() }()
	var plainRows int64
	if err := pdb.Raw(`SELECT count(*) FROM probe WHERE k = 'in-wal'`).Scan(&plainRows).Error; err != nil {
		t.Fatalf("查主文件副本: %v", err)
	}
	if plainRows != 0 {
		t.Fatalf("反向断言不成立：主文件副本本不该含 WAL 里的行，实际 count=%d（前提变了，请重审这条用例）", plainRows)
	}
}
