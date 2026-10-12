package store

import (
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/pkg/dbclient"
)

// openV15ForV16Test 造一个**真实的 v15 库**（跑完整的 v1..v15，含 v12/v13/v15 的 Go 数据步骤）。
//
// 与 v15 用例同一个理由：`applySchemaUpTo` 只跑 DDL，会留下 `models_v13` 这类中间形态，
// 与线上 v15 库不是一回事。
func openV15ForV16Test(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v15.db")
	db, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(db)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 15)
	return s
}

// TestMigrateV16RenamesPreserveRows v16 是"只改名字"的一次迁移，判据就是**行没被改坏**：
//
//   - `capability_revisions` → `definition_revisions`：行数、`kind`、`seq` 全保留，
//     只有列名从 `capability_id` 变成 `definition_id`；
//   - `inputs` 是唯一被**重建**的表（`delivery` 的 DEFAULT 变了，SQLite 没有 `ALTER COLUMN`）：
//     行必须一条不少，`after_turn` 要落成 `after_run`，`target_turn_id` 的值要落进
//     `target_run_id`，而**别的投递值不许被顺手归一**（`interrupt_then_prompt` 原样）；
//   - `sessions.provider/model` → `provider_id/model_id`：值原样；
//   - 索引：两个旧名真的没了、新名真的在（`idx_inputs_queue` 在重建表之后必须建回来）。
//
// 为什么这几条必须单独钉：改名类迁移的失败方式**全都不响**——漏搬 `inputs` 的列会让队列项
// 静默丢字段（投递判据是 generation，但审计与前端展示会空）、漏建 `idx_inputs_queue` 只是
// 将来变慢、旧索引没 DROP 只是"优化器可能还在用旧的"。只有这里逐条问库，才会当场红。
func TestMigrateV16RenamesPreserveRows(t *testing.T) {
	s := openV15ForV16Test(t)
	at := "2026-03-04T00:00:00Z"

	// —— 夹具：v15 的历史表名与列名（DAO 已改认 v16 的名字，故一律裸 SQL）——
	if err := s.db.Exec(`INSERT INTO capability_revisions (kind, capability_id, action, actor, at, digest)
		VALUES ('skill', 'sk-1', 'create', 'test', ?, 'sha256:aaa'),
		       ('provider', 'pv-1', 'update', 'test', ?, 'sha256:bbb')`, at, at).Error; err != nil {
		t.Fatalf("写审计夹具: %v", err)
	}
	if err := s.db.Exec(`INSERT INTO sessions (id, agent_id, status, provider, model, created_at, updated_at)
		VALUES ('ses_v16', 'ag-1', 'completed', 'pv-1', 'deepseek-chat', ?, ?)`, at, at).Error; err != nil {
		t.Fatalf("写会话夹具: %v", err)
	}
	if err := s.db.Exec(`INSERT INTO inputs (id, session_id, text, delivery, target_turn_id, generation, status, created_at, claimed_at)
		VALUES ('inp_a', 'ses_v16', '排队那条', 'after_turn',            'run_old_a', 7, 'queued', ?, ''),
		       ('inp_b', 'ses_v16', '打断那条', 'interrupt_then_prompt',  'run_old_b', 8, 'queued', ?, '')`,
		at, at).Error; err != nil {
		t.Fatalf("写队项夹具: %v", err)
	}
	// v15 的旧索引名，用来证明 v16 真的 DROP 了它（不是只加了新名）。
	var memIdxBefore int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_memories_session'`).Scan(&memIdxBefore).Error; err != nil {
		t.Fatal(err)
	}
	if memIdxBefore != 1 {
		t.Fatalf("v15 库应存在旧索引 idx_memories_session，实际 %d", memIdxBefore)
	}

	migrateUpTo(t, s, 16)
	migrateUpTo(t, s, 16) // 幂等：第二次不该报错、也不该再动数据

	// ① 审计行：内容逐列保留，只有列名换了。
	var revKind, revDefID, revDigest string
	var revSeq int64
	if err := s.db.Raw(`SELECT kind, definition_id, digest, seq FROM definition_revisions ORDER BY seq LIMIT 1`).
		Row().Scan(&revKind, &revDefID, &revDigest, &revSeq); err != nil {
		t.Fatalf("读 definition_revisions: %v", err)
	}
	if revKind != "skill" || revDefID != "sk-1" || revDigest != "sha256:aaa" || revSeq == 0 {
		t.Fatalf("审计行被改坏: kind=%q definition_id=%q digest=%q seq=%d", revKind, revDefID, revDigest, revSeq)
	}
	var revCount int64
	if err := s.db.Raw(`SELECT count(*) FROM definition_revisions`).Scan(&revCount).Error; err != nil || revCount != 2 {
		t.Fatalf("审计行数应为 2，实际 %d（err=%v）", revCount, err)
	}

	// ② 会话：值原样搬进新列名。
	var providerID, modelID string
	if err := s.db.Raw(`SELECT provider_id, model_id FROM sessions WHERE id='ses_v16'`).Row().Scan(&providerID, &modelID); err != nil {
		t.Fatalf("读 sessions: %v", err)
	}
	if providerID != "pv-1" || modelID != "deepseek-chat" {
		t.Fatalf("会话引用列被改坏: provider_id=%q model_id=%q", providerID, modelID)
	}

	// ③ 队项：一条不少、语义列各就各位、**非 after_turn 的值不许被归一**。
	var delivery, targetRunID, text string
	var generation int64
	if err := s.db.Raw(`SELECT delivery, target_run_id, text, generation FROM inputs WHERE id='inp_a'`).
		Row().Scan(&delivery, &targetRunID, &text, &generation); err != nil {
		t.Fatalf("读 inputs: %v", err)
	}
	if delivery != "after_run" || targetRunID != "run_old_a" || generation != 7 || text != "排队那条" {
		t.Fatalf("队项 inp_a 被改坏: delivery=%q target_run_id=%q generation=%d text=%q",
			delivery, targetRunID, generation, text)
	}
	var passthrough string
	if err := s.db.Raw(`SELECT delivery FROM inputs WHERE id='inp_b'`).Row().Scan(&passthrough); err != nil {
		t.Fatal(err)
	}
	if passthrough != "interrupt_then_prompt" {
		t.Fatalf("非 after_turn 的投递值不该被改写，实际 %q", passthrough)
	}
	// 新列的 DEFAULT 也要是新的（否则新入队的行会写回旧词）。
	var dflt string
	if err := s.db.Raw(`SELECT dflt_value FROM pragma_table_info('inputs') WHERE name='delivery'`).Row().Scan(&dflt); err != nil {
		t.Fatalf("读 inputs.delivery 默认值: %v", err)
	}
	if dflt != "'after_run'" {
		t.Fatalf("inputs.delivery 的 DEFAULT 应是 'after_run'，实际 %s", dflt)
	}

	// ④ 索引：旧名没了、新名在，且重建表没把 idx_inputs_queue 弄丢。
	for name, want := range map[string]int64{
		"idx_capability_revisions_capability": 0,
		"idx_definition_revisions_history":    1,
		"idx_memories_session":                0,
		"idx_permission_memories_session":     1,
		"idx_inputs_queue":                    1,
	} {
		var got int64
		if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("索引 %s：期望 %d，实际 %d", name, want, got)
		}
	}

	// ⑤ 旧表真的没了（不是"两张表并存"）。
	var oldTable int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='capability_revisions'`).Scan(&oldTable).Error; err != nil {
		t.Fatal(err)
	}
	if oldTable != 0 {
		t.Fatal("capability_revisions 应已被 RENAME 掉，不能与新表并存")
	}
}
