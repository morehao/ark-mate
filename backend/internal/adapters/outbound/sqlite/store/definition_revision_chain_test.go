package store

// ——— v32 验收：定义变更审计的哈希链（T4） ———
//
// 判据是**行为**而不是"字段被赋值了"：写三条 → 链连续；改中间一条的正文摘要 → 校验指出是哪一条；
// 删/清空中间一行 → 同样指出；v32 之前的历史行是链的前缀、不参与校验也不假装被保护过。
//
// 反证（实跑过，见提交说明）：把 `appendDefinitionRevision` 里的前驱写死成创世哨兵 →
// `TestDefinitionRevisionChainIsContinuous` 与并发那条当场红。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/db"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
)

// TestDefinitionRevisionChainSchemaVersionMatchesMigration 导出的"链从哪一步开始"必须等于那一步的
// 版本号：它是给 `mate doctor` 用的，写错一个字就会把"还没升级"误报成"链断了"（或反过来静默跳过）。
func TestDefinitionRevisionChainSchemaVersionMatchesMigration(t *testing.T) {
	if migrationV32.version != DefinitionRevisionChainSchemaVersion {
		t.Fatalf("DefinitionRevisionChainSchemaVersion=%d 与 migrationV32.version=%d 不一致",
			DefinitionRevisionChainSchemaVersion, migrationV32.version)
	}
}

// seedThreeRevisions 连写三条真实定义（每条各自一次提交，走的都是产品的写路径）。
func seedThreeRevisions(t *testing.T, s *Store) model.DefinitionRevisionEntityList {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		ok, err := s.UpsertMcpServerDefinition(ctx, id, `{"name":"`+id+`"}`, 0, "rest")
		if err != nil || !ok {
			t.Fatalf("写定义 %s: ok=%v err=%v", id, ok, err)
		}
	}
	// 三条的 definition_id 各不相同，`ListDefinitionRevisions` 是按定义维度的读面，
	// 这里要看的是**整条链**，所以直接按 seq 升序取全表。
	var revs model.DefinitionRevisionEntityList
	if err := s.db.Model(&model.DefinitionRevisionEntity{}).Order("seq ASC").Find(&revs).Error; err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("应 3 条审计，got %d", len(revs))
	}
	return revs
}

// TestDefinitionRevisionChainIsContinuous 三条链首尾相接，且第一条挂在创世哨兵上。
func TestDefinitionRevisionChainIsContinuous(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	revs := seedThreeRevisions(t, s)

	if revs[0].PrevHash != DefinitionRevisionGenesisHash {
		t.Fatalf("创世条的前驱应是哨兵 %q，got %q", DefinitionRevisionGenesisHash, revs[0].PrevHash)
	}
	for i := 1; i < len(revs); i++ {
		if revs[i].PrevHash != revs[i-1].Hash {
			t.Fatalf("第 %d 行的前驱应等于上一行的哈希：prev=%q want=%q", i, revs[i].PrevHash, revs[i-1].Hash)
		}
	}
	for _, r := range revs {
		if len(r.Hash) != 64 {
			t.Fatalf("链哈希应是 sha256 hex（64 字符），got %q", r.Hash)
		}
	}

	checked, brk, err := s.VerifyDefinitionRevisionChain(ctx)
	if err != nil {
		t.Fatalf("校验: %v", err)
	}
	if brk != nil {
		t.Fatalf("完好的链不该报断点：%+v", brk)
	}
	if checked != len(revs) {
		t.Fatalf("应校验 %d 行，got %d", len(revs), checked)
	}
}

// TestDefinitionRevisionChainDetectsContentTamper 改中间一条的内容 → 定位到**那一条**（content）。
//
// 这条是整件事存在的理由：链只查前后相接的话，改一个字段是发现不了的（下一行的 prev 依旧指着
// 旧哈希，而旧哈希自己没变），所以每行都要按内容重算一遍。
func TestDefinitionRevisionChainDetectsContentTamper(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	revs := seedThreeRevisions(t, s)

	// 直接改库（模拟"有人绕过应用改了历史"）：把中间那条的摘要换掉。
	if err := s.db.Exec(`UPDATE definition_revisions SET digest = 'tampered' WHERE seq = ?`, revs[1].Seq).Error; err != nil {
		t.Fatalf("篡改: %v", err)
	}

	checked, brk, err := s.VerifyDefinitionRevisionChain(ctx)
	if err != nil {
		t.Fatalf("校验: %v", err)
	}
	if brk == nil {
		t.Fatal("改过内容的行必须被指出来")
	}
	if brk.Seq != revs[1].Seq || brk.Reason != ChainBreakContent {
		t.Fatalf("应定位到 seq=%d 且 reason=%s，got seq=%d reason=%s", revs[1].Seq, ChainBreakContent, brk.Seq, brk.Reason)
	}
	if brk.Kind != model.DefinitionKindMcp || brk.DefinitionID != revs[1].DefinitionID {
		t.Fatalf("断点要能指回哪条定义：%+v", brk)
	}
	if checked != 1 {
		t.Fatalf("断点之前的行都验过了，应 checked=1，got %d", checked)
	}
}

// TestDefinitionRevisionChainDetectsDeletedRow 删掉中间一行 → 下一行的前驱对不上（link）。
func TestDefinitionRevisionChainDetectsDeletedRow(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	revs := seedThreeRevisions(t, s)

	if err := s.db.Exec(`DELETE FROM definition_revisions WHERE seq = ?`, revs[1].Seq).Error; err != nil {
		t.Fatalf("删行: %v", err)
	}
	_, brk, err := s.VerifyDefinitionRevisionChain(ctx)
	if err != nil {
		t.Fatalf("校验: %v", err)
	}
	if brk == nil || brk.Seq != revs[2].Seq || brk.Reason != ChainBreakLink {
		t.Fatalf("应指向被删行的**后一条**（seq=%d, reason=%s），got %+v", revs[2].Seq, ChainBreakLink, brk)
	}
	// Want = 链说"这里该接谁"（前一条**现存**行的哈希），Got = 库里写的那个（消失那条的哈希）。
	if brk.Want != revs[0].Hash || brk.Got != revs[1].Hash {
		t.Fatalf("断点应指出「期望 %s、实际 %s」：%+v", revs[0].Hash, revs[1].Hash, brk)
	}
}

// TestDefinitionRevisionChainDetectsBlankedHash 把某行的 hash 清空想让它"消失" → gap。
func TestDefinitionRevisionChainDetectsBlankedHash(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()
	revs := seedThreeRevisions(t, s)

	err := s.db.Exec(`UPDATE definition_revisions SET hash = '' WHERE seq = ?`, revs[1].Seq).Error
	if err != nil {
		t.Fatalf("清空: %v", err)
	}
	_, brk, verr := s.VerifyDefinitionRevisionChain(ctx)
	if verr != nil {
		t.Fatalf("校验: %v", verr)
	}
	if brk == nil || brk.Seq != revs[1].Seq || brk.Reason != ChainBreakGap {
		t.Fatalf("链开始之后出现的空哈希要报 gap（seq=%d），got %+v", revs[1].Seq, brk)
	}
}

// TestDefinitionRevisionChainSkipsLegacyRows v32 之前的行（hash 为空）是链的**前缀**：
// 链从第一条带哈希的行重新起算，创世哨兵仍成立。
func TestDefinitionRevisionChainSkipsLegacyRows(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	// 直接插两条"v32 之前"的行（那时没有 prev_hash/hash 两列，落库就是空串）。
	for _, id := range []string{"legacy-1", "legacy-2"} {
		if err := s.db.Exec(`INSERT INTO definition_revisions (kind, definition_id, action, actor, at, digest)
			VALUES (?, ?, 'create', 'rest', '2026-01-01T00:00:00Z', 'old')`,
			model.DefinitionKindMcp, id).Error; err != nil {
			t.Fatalf("种历史行 %s: %v", id, err)
		}
	}
	// 再走一次真实提交（v32 之后的行）。
	if ok, err := s.UpsertMcpServerDefinition(ctx, "new", `{"name":"new"}`, 0, "rest"); err != nil || !ok {
		t.Fatalf("写定义: ok=%v err=%v", ok, err)
	}

	checked, brk, err := s.VerifyDefinitionRevisionChain(ctx)
	if err != nil {
		t.Fatalf("校验: %v", err)
	}
	if brk != nil {
		t.Fatalf("历史行不许当断点（它们是链的前缀）：%+v", brk)
	}
	if checked != 1 {
		t.Fatalf("只该校验 v32 之后那 1 行，got %d", checked)
	}
	// 而它必须是新的创世条——前驱不能接在历史的空哈希上。
	revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindMcp, "new")
	if err != nil || len(revs) != 1 {
		t.Fatalf("读新行: n=%d err=%v", len(revs), err)
	}
	if revs[0].PrevHash != DefinitionRevisionGenesisHash {
		t.Fatalf("历史行之后的第一条新行应从创世哨兵起链，got %q", revs[0].PrevHash)
	}
}

// TestMigrationV32AddsChainColumns v31 → v32：两列在、既有行拿到空串默认值（不被补算），幂等。
func TestMigrationV32AddsChainColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v31.db")
	database, err := dbclient.Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	s := New(database)
	s.path = path
	t.Cleanup(func() { _ = s.Close() })
	migrateUpTo(t, s, 31)

	// v31 里的一条真实审计行（那时还没有链）。
	if err := s.db.Exec(`INSERT INTO definition_revisions (kind, definition_id, action, actor, at, digest)
		VALUES ('skill', 'old', 'create', 'rest', '2026-01-01T00:00:00Z', 'digest-old')`).Error; err != nil {
		t.Fatalf("种 v31 审计行: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate(v32): %v", err)
	}
	// 既有行：两列都是空串（**刻意不补算**——补算会把"当时没人校验"写成"当时被校验过"）。
	var prevHash, hash, digest string
	row := s.db.Raw(`SELECT prev_hash, hash, digest FROM definition_revisions WHERE definition_id = 'old'`).Row()
	if err := row.Scan(&prevHash, &hash, &digest); err != nil {
		t.Fatalf("读既有行: %v", err)
	}
	if prevHash != "" || hash != "" {
		t.Fatalf("历史行的链列必须留空，got prev=%q hash=%q", prevHash, hash)
	}
	if digest != "digest-old" {
		t.Fatalf("迁移不得改写既有内容，digest=%q", digest)
	}

	// 幂等。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("重复 Migrate: %v", err)
	}
	if n := countRows(t, s, model.TableNameDefinitionRevision); n != 1 {
		t.Fatalf("重复迁移后行数应仍为 1，got %d", n)
	}
}
