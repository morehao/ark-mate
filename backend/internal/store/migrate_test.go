package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/pkg/dbclient"
)

// applySchemaUpTo 把库迁到指定版本（只跑 v1..v 的 DDL 并记录版本），用于重放**历史版本的真实库形态**。
//
// 为什么不放一个二进制旧库文件当夹具：DDL 是冻结的历史（migrate.go 里"绝不回改历史"是硬约定），
// 按版本重放能造出**任意**历史形态且可读可审；而二进制夹具既看不出它到底是哪一版，也没法在
// 需要时造出"v5 升级到 v7"这类别的组合。
func applySchemaUpTo(t *testing.T, s *Store, version int) {
	t.Helper()
	ctx := context.Background()
	if err := s.db.Exec(`CREATE TABLE IF NOT EXISTS ` + model.TableMigration + ` (
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

// TestMigrateV6ToV7KeepsData v6 → v7 升级：三个新列纯增列（默认 `”`），既有数据一行不丢。
//
// 这一条是 M3-1 的验收核心：代际绑定把"会话钉住的能力"落进库，而**用户手上的库是 v6 的**。
// 升级一旦丢数据或把旧行读成异常，损失不可逆（会话是平台的事实源）。
func TestMigrateV6ToV7KeepsData(t *testing.T) {
	ctx := context.Background()
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
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v6 → v7 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// 旧行必须原样还在，新列为默认值。
	got, ok, err := s.GetConversation(ctx, "c-old")
	if err != nil || !ok {
		t.Fatalf("升级后读旧会话: ok=%v err=%v", ok, err)
	}
	if got.Title != "旧会话" || got.Provider != "p1" || got.Model != "m1" || got.LastSeq != 7 ||
		got.ProviderSessionID != "ses_1" || !got.Pinned || got.WorkspaceTemporary != true {
		t.Fatalf("旧列在升级中失真: %+v", got)
	}
	if got.Origin != "" || got.CapabilityJSON != "" || got.CapabilityFingerprint != "" {
		t.Fatalf("新列应是空串默认值（空 = 未声明能力选择，与升级前行为一致）: origin=%q json=%q fp=%q",
			got.Origin, got.CapabilityJSON, got.CapabilityFingerprint)
	}

	// 新列可写可读（v7 的写路径真的落到了这两列上）。
	if err := s.UpsertConversation(ctx, Conversation{
		ID: "c-new", AgentID: "a1", Cwd: "/tmp/w", Status: "in_progress",
		Origin: "automation:nightly", CapabilityJSON: `{"skills":["s1"]}`, CapabilityFingerprint: "fp123",
	}); err != nil {
		t.Fatalf("写 v7 会话行: %v", err)
	}
	got, ok, err = s.GetConversation(ctx, "c-new")
	if err != nil || !ok {
		t.Fatalf("读新会话: ok=%v err=%v", ok, err)
	}
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
	if err := s.db.Raw(`SELECT MAX(version) FROM ` + model.TableMigration).Row().Scan(&maxV); err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if maxV != latestSchemaVersion() {
		t.Fatalf("schema 版本应为 %d，got %d", latestSchemaVersion(), maxV)
	}
}

// TestMigrateRefusesFutureSchema 库版本高于二进制 → **拒绝启动**（不是"尽力而为地打开"）。
//
// M6-1 的 Gate 明确要求这一条在 v8 上仍成立。它的价值只在**降级**时体现：用户用新二进制
// 建了 v9 的库，再用旧二进制（latest=v8）去开——若这里放行，旧代码会按自己认识的列去读写
// 一个它不认识的 schema，损坏是静默且不可逆的（v8 的 `capability_revisions` 正是"新列/新表"
// 频出的那一类改动）。
func TestMigrateRefusesFutureSchema(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if err := s.db.Exec(`INSERT INTO `+model.TableMigration+` (version, name, applied_at) VALUES (?,?,?)`,
		latestSchemaVersion()+1, "future", model.NowTS()).Error; err != nil {
		t.Fatalf("造未来版本行: %v", err)
	}
	if err := s.Migrate(ctx); err == nil {
		t.Fatalf("库版本高于二进制（v%d > v%d）时必须拒绝启动", latestSchemaVersion()+1, latestSchemaVersion())
	}
}

// TestMigrateV7ToV8KeepsData v7 → v8 升级：v8 是**纯增表**（skills / mcp_servers /
// capability_revisions），既有数据一行不丢，新表从空开始。
//
// 两个容易搞错的点，各自钉一条：
//  1. 新表**必须是空的**。用户的存量能力眼下还在 `<CapabilityDir>` 的文件里，要由 M6-4 的
//     导入器灌进来；升级时"顺手把什么默认值填进去"会让导入无法判断"库里到底有没有东西"。
//  2. v8 的库能被二进制打开、能升到最新、且**重复迁移幂等**（用户每次启动都会跑一遍）。
func TestMigrateV7ToV8KeepsData(t *testing.T) {
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

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("v7 → v8 升级失败: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	// ① 旧行原样还在（含 v7 那三列）。
	got, ok, err := s.GetConversation(ctx, "c-v7")
	if err != nil || !ok {
		t.Fatalf("升级后读旧会话: ok=%v err=%v", ok, err)
	}
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
	if err := s.db.Raw(`SELECT MAX(version) FROM ` + model.TableMigration).Row().Scan(&maxV); err != nil {
		t.Fatalf("读版本: %v", err)
	}
	if maxV != latestSchemaVersion() || maxV != 8 {
		t.Fatalf("schema 版本应为 v8（latest=%d），got %d", latestSchemaVersion(), maxV)
	}
}
