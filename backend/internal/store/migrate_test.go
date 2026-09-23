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
