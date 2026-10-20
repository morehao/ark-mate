package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/morehao/ark-mate/internal/persistence/model"
)

// TestCapabilityDefinitionCAS 能力定义写面的核心不变量（M6-1 验收）。
//
// 一次写面 = **一行 + 一条审计**，且 CAS 没中时**两个表都不变**。这条是 40901
// （`definition_changed`）在 DB 载体下的唯一判据来源：今天它由 mtime+size 承担，
// 换成 `rowsAffected == 0` 之后，对外表现必须逐字不变。
func TestCapabilityDefinitionCAS(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	// —— 新建（expectedRev=0）——
	ok, err := s.UpsertSkillDefinition(ctx, "s1", `{"name":"one"}`, 0, "rest")
	if err != nil || !ok {
		t.Fatalf("新建应命中: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetSkillDefinition(ctx, "s1")
	if err != nil || !found {
		t.Fatalf("新建后应读得到: found=%v err=%v", found, err)
	}
	if got.Revision != 1 || got.Body != `{"name":"one"}` {
		t.Fatalf("新建后形态不符: rev=%d body=%q", got.Revision, got.Body)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("新建后 updated_at 不应为零值")
	}

	// —— 再建同 id → 不命中（"别人先建了"与 40901 是同一个对外表现），且**不追审计** ——
	if ok, err := s.UpsertSkillDefinition(ctx, "s1", `{"name":"dup"}`, 0, "rest"); err != nil || ok {
		t.Fatalf("重复新建应不命中: ok=%v err=%v", ok, err)
	}
	revs, err := s.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "s1")
	if err != nil || len(revs) != 1 {
		t.Fatalf("重复新建不应产生审计: n=%d err=%v", len(revs), err)
	}
	if revs[0].Action != model.DefinitionActionCreate || revs[0].Actor != "rest" {
		t.Fatalf("新建审计的 action/actor 不符: %+v", revs[0])
	}
	if want := digestOf(`{"name":"one"}`); revs[0].Digest != want {
		t.Fatalf("新建审计 digest 应为 body 摘要: got=%q want=%q", revs[0].Digest, want)
	}

	// —— CAS 未中（过期的 revision）：行与审计都必须**原样不动** ——
	if ok, err := s.UpsertSkillDefinition(ctx, "s1", `{"name":"stale"}`, 7, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetSkillDefinition(ctx, "s1")
	if got.Revision != 1 || got.Body != `{"name":"one"}` {
		t.Fatalf("CAS 未中却改到了行: rev=%d body=%q", got.Revision, got.Body)
	}
	if revs, _ = s.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "s1"); len(revs) != 1 {
		t.Fatalf("CAS 未中却追了审计: n=%d", len(revs))
	}

	// —— CAS 命中：revision +1，且审计与提升列**同源**（同一个 now）——
	if ok, err := s.UpsertSkillDefinition(ctx, "s1", `{"name":"two"}`, 1, "rest"); err != nil || !ok {
		t.Fatalf("正确 revision 应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetSkillDefinition(ctx, "s1")
	if got.Revision != 2 || got.Body != `{"name":"two"}` {
		t.Fatalf("命中后形态不符: rev=%d body=%q", got.Revision, got.Body)
	}
	revs, _ = s.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "s1")
	if len(revs) != 2 || revs[1].Action != model.DefinitionActionUpdate {
		t.Fatalf("命中后审计应为 create+update: %+v", revs)
	}
	// 提升列守卫：revision 必须等于"非删除审计"的条数，updated_at 必须逐纳秒等于最后一条审计的 at。
	// 这条挡的是"后来有人往表里塞派生列"——那种列一旦双写就会漂移，而漂移不会让任何别的测试变红。
	if got.Revision != int64(len(revs)) {
		t.Fatalf("revision(%d) 与非删除审计条数(%d)不一致", got.Revision, len(revs))
	}
	if !got.UpdatedAt.Equal(revs[1].At) {
		t.Fatalf("updated_at(%s) 与最后一条审计 at(%s) 不同源", got.UpdatedAt, revs[1].At)
	}

	// —— 删除：过期 revision 挡住（"删掉别人刚改过的那一版"比覆盖更不可逆）——
	if ok, err := s.DeleteSkillDefinition(ctx, "s1", 1, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应删掉: ok=%v err=%v", ok, err)
	}
	if _, found, _ := s.GetSkillDefinition(ctx, "s1"); !found {
		t.Fatal("过期 revision 却删掉了行")
	}
	if ok, err := s.DeleteSkillDefinition(ctx, "s1", 2, "rest"); err != nil || !ok {
		t.Fatalf("正确 revision 应删掉: ok=%v err=%v", ok, err)
	}
	if _, found, _ := s.GetSkillDefinition(ctx, "s1"); found {
		t.Fatal("删除后仍读得到行")
	}
	// 审计必须在能力被删除后**仍然存在**（这正是不加外键的理由）。
	revs, _ = s.ListDefinitionRevisions(ctx, model.DefinitionKindSkill, "s1")
	if len(revs) != 3 || revs[2].Action != model.DefinitionActionDelete {
		t.Fatalf("删除后审计应留 3 条且末条为 delete: %+v", revs)
	}
	// 删除没有"提交后的 body"，digest 落空串（不是上一个版本的摘要——那会骗人）。
	if revs[2].Digest != "" {
		t.Fatalf("删除审计的 digest 应为空串，实际 %q", revs[2].Digest)
	}

	// —— 两张表互不串（同 id 的 mcp 是另一条独立的定义）——
	if ok, err := s.UpsertMcpServerDefinition(ctx, "s1", `{"transport":"stdio"}`, 0, "rest"); err != nil || !ok {
		t.Fatalf("同 id 的 mcp 应可独立新建: ok=%v err=%v", ok, err)
	}
	mcp, found, _ := s.GetMcpServerDefinition(ctx, "s1")
	if !found || mcp.Revision != 1 {
		t.Fatalf("mcp 定义形态不符: found=%v rev=%d", found, mcp.Revision)
	}
	if revs, _ := s.ListDefinitionRevisions(ctx, model.DefinitionKindMcp, "s1"); len(revs) != 1 {
		t.Fatalf("mcp 的审计应按 kind 隔离: n=%d", len(revs))
	}

	// —— seq 单调不复用：删掉定义后再建，序号必须**严格更大**（AUTOINCREMENT 语义）——
	seqBefore, err := s.MaxDefinitionRevisionSeq(ctx)
	if err != nil {
		t.Fatalf("MaxDefinitionRevisionSeq: %v", err)
	}
	if seqBefore != 4 {
		t.Fatalf("此时应为 4 条审计（create/update/delete/mcp-create），got %d", seqBefore)
	}
	if ok, _ := s.DeleteMcpServerDefinition(ctx, "s1", 1, "rest"); !ok {
		t.Fatal("mcp 删除应命中")
	}
	if ok, _ := s.UpsertSkillDefinition(ctx, "s2", `{"name":"three"}`, 0, "cli"); !ok {
		t.Fatal("删除后新建应命中")
	}
	seqAfter, _ := s.MaxDefinitionRevisionSeq(ctx)
	if seqAfter != 6 {
		t.Fatalf("序号不得复用：应为 6，got %d", seqAfter)
	}

	// —— 读面：全量列表与计数 ——
	skills, err := s.ListSkillDefinitions(ctx)
	if err != nil || len(skills) != 1 || skills[0].ID != "s2" {
		t.Fatalf("ListSkillDefinitions 不符: %+v err=%v", skills, err)
	}
	if n, _ := s.CountSkillDefinitions(ctx); n != 1 {
		t.Fatalf("CountSkillDefinitions 应为 1，got %d", n)
	}
	if n, _ := s.CountMcpServerDefinitions(ctx); n != 0 {
		t.Fatalf("mcp 已删空，CountMcpServerDefinitions 应为 0，got %d", n)
	}
	// 审计是**只追加**的：删掉定义后全量历史仍在。
	if recent, err := s.RecentDefinitionRevisions(ctx, 0); err != nil || len(recent) != 6 {
		t.Fatalf("RecentDefinitionRevisions 应 6 条: n=%d err=%v", len(recent), err)
	}
}

// digestOf 复算审计摘要（测试侧独立实现，不调被测代码）。
func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// TestCapabilityDefinitionColumnsFrozen 钉住定义面各表的列集合。
//
// 这一条对应计划 §1 P-2 的"加一条守卫测试：提升列与 body 派生值一致（防'有人后来往表里
// 塞派生列'）"——最直接的实现就是**把列集合冻住**：`body` 之外的任何列都是派生列，
// 而派生列与 body 的双写必然漂移（漂移本身不会让别的测试变红）。
//
// `models` 是唯一一处**刻意**多于 `{id, body, revision, updated_at}` 的表：v11 多的是
// `runtime` / `provider_id` / `is_default`（三个查询轴），**v12 换成 `provider_id` / `model_id`**
// ——`model_id` 从 body 提升为列之后，`(provider_id, model_id)` 的唯一性才能由**库级唯一索引**
// 表达（v11 那条"提交事务内先查后写"的约定随之退役）。`runtime` 与 `is_default` 被删除：
// 模型不再带底座维度、默认模型改住 `meta` 的单一值（ADR-21）。
//
// 所以这张表的列集合是**冻结形态**，不是"可以顺手再加列"的许可。
func TestCapabilityDefinitionColumnsFrozen(t *testing.T) {
	s := openTestDB(t)
	want := map[string][]string{
		model.TableNameSkill:              {"id", "body", "revision", "updated_at"},
		model.TableNameMcpServer:          {"id", "body", "revision", "updated_at"},
		model.TableNameDefinitionRevision: {"seq", "kind", "definition_id", "action", "actor", "at", "digest"},
		// v13：providers/models 不再是「定义载体」而是「资源表」——平台字段全部成列、
		// 无 body（ADR-1/ADR-2）。它们的列集合由 `archtest` 的
		// `TestProviderModelTablesAreColumnsNotCarriers` 按**白名单**钉住（这里只冻住
		// 四张定义载体表，两者判据分工不同：那张表说的是"哪些列可以有"，这里说的是"不许再加列"）。
		// v20 起多一列 `key`（明文密钥进库）；v24 把 `model_id` 改名 `model`；v25 补两个容量列
		// （可空、无 DEFAULT，`ALTER TABLE ADD COLUMN` **追加在末尾**——这条判据比的是**物理列序**，
		// 所以两个容量列排在时间列之后，不按"逻辑分组"提前）。
		model.TableNameProvider: {"id", "name", "protocol", "base_url", "note", "revision", "created_at", "updated_at", "key"},
		model.TableNameModel:    {"id", "provider_id", "model", "name", "note", "input_modalities", "revision", "created_at", "updated_at", "max_input_tokens", "max_output_tokens"},
	}
	for table, wantCols := range want {
		rows, err := s.db.Raw(`PRAGMA table_info(` + table + `)`).Rows()
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		var gotCols []string
		for rows.Next() {
			var (
				cid, notnull, pk int
				name, typ        string
				dflt             any
			)
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				_ = rows.Close()
				t.Fatalf("扫 %s 的列定义: %v", table, err)
			}
			gotCols = append(gotCols, name)
		}
		_ = rows.Close()
		if len(gotCols) != len(wantCols) {
			t.Fatalf("%s 的列集合变了：got %v，want %v（body 之外的列都是派生列，别加）", table, gotCols, wantCols)
		}
		for i := range wantCols {
			if gotCols[i] != wantCols[i] {
				t.Fatalf("%s 的列集合变了：got %v，want %v", table, gotCols, wantCols)
			}
		}
	}
}

// TestDefinitionRevisionConcurrentAppend 多 goroutine 同时提交：序号唯一、单调、无空洞。
//
// 本仓的库是 **MaxOpenConns=1**（单写者模型，见 `pkg/dbclient`），因此并发提交在连接池上
// 被串行化——这正是它的部署形态。要验的不是"SQLite 能并发写"，而是"计数器与插行是同一个
// 原子动作"：若有人把 seq 改成"先查 MAX 再插"（两步），这里就会看到重复或空洞。
func TestDefinitionRevisionConcurrentAppend(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	const n = 40
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "s-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			ok, err := s.UpsertSkillDefinition(ctx, id, `{"name":"x"}`, 0, "rest")
			if err == nil && !ok {
				err = fmt.Errorf("CAS 未命中（id=%s）", id)
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 次提交失败: %v", i, err)
		}
	}

	revs, err := s.RecentDefinitionRevisions(ctx, 0)
	if err != nil {
		t.Fatalf("RecentDefinitionRevisions: %v", err)
	}
	if len(revs) != n {
		t.Fatalf("应 %d 条审计，got %d", n, len(revs))
	}
	seen := make(map[int64]bool, n)
	for _, r := range revs {
		if seen[r.Seq] {
			t.Fatalf("序号重复: %d", r.Seq)
		}
		seen[r.Seq] = true
	}
	for want := int64(1); want <= n; want++ {
		if !seen[want] {
			t.Fatalf("序号 %d 缺失（应为 1..%d 连续，无空洞）", want, n)
		}
	}
	if max, _ := s.MaxDefinitionRevisionSeq(ctx); max != n {
		t.Fatalf("MaxDefinitionRevisionSeq 应为 %d，got %d", n, max)
	}
}
