package store

import (
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/model"
)

// TestProviderDefinitionCAS 是 provider 面与 `TestCapabilityDefinitionCAS` **同形**的写面契约：
// 一次提交 = 一行 + 一条审计，CAS 未中则两表都不动，删除留痕且 digest 落空串。
//
// 与能力面重复一遍是刻意的：本面是**第五次**同形落地，而"两边共用 commitDefinitionChange"
// 这件事之所以可信，正是因为它被逐面钉过一次（任何一面偷偷绕开提交路径，这里就会缺审计）。
//
// v13 起本面的"正文"是**列**，所以审计 digest 的输入换成列行的规范 JSON（ADR-10）：
// 断言仍逐字比对一个**规范 JSON 字面量**（测试侧独立算出 sha256），从而把"键排序 + 固定
// 字段集 + 不含 revision/时间列"这三条一起钉住。
func TestProviderDefinitionCAS(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	// —— 新建（expectedRev=0）——
	ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai"}, 0, "rest")
	if err != nil || !ok {
		t.Fatalf("新建应命中: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetProviderDefinition(ctx, "p1")
	if err != nil || !found {
		t.Fatalf("新建后应读得到: found=%v err=%v", found, err)
	}
	if got.Revision != 1 || got.Protocol != "openai" || got.Name != "" || got.BaseURL != "" ||
		got.UpdatedAt.IsZero() || got.CreatedAt.IsZero() {
		t.Fatalf("新建后形态不符: rev=%d protocol=%q name=%q base_url=%q updated=%s created=%s",
			got.Revision, got.Protocol, got.Name, got.BaseURL, got.UpdatedAt, got.CreatedAt)
	}

	// —— 再建同 id → 不命中，且**不追审计** ——
	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai"}, 0, "rest"); err != nil || ok {
		t.Fatalf("重复新建应不命中: ok=%v err=%v", ok, err)
	}
	revs, err := s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if err != nil || len(revs) != 1 {
		t.Fatalf("重复新建不应产生审计: n=%d err=%v", len(revs), err)
	}
	if revs[0].Action != model.CapabilityActionCreate || revs[0].Actor != "rest" {
		t.Fatalf("新建审计的 action/actor 不符: %+v", revs[0])
	}

	// —— CAS 未中：行与审计都必须原样不动 ——
	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai", BaseURL: "https://x"}, 7, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetProviderDefinition(ctx, "p1")
	if got.Revision != 1 || got.BaseURL != "" {
		t.Fatalf("CAS 未中却改到了行: rev=%d base_url=%q", got.Revision, got.BaseURL)
	}
	if revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1"); len(revs) != 1 {
		t.Fatalf("CAS 未中却追了审计: n=%d", len(revs))
	}

	// —— CAS 命中：revision +1，审计里那条是新内容列的规范 JSON 摘要 ——
	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai", Note: "主账号"}, 1, "rest"); err != nil || !ok {
		t.Fatalf("正确 revision 应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetProviderDefinition(ctx, "p1")
	if got.Revision != 2 || got.Note != "主账号" {
		t.Fatalf("命中后形态不符: rev=%d note=%q", got.Revision, got.Note)
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if len(revs) != 2 || revs[1].Action != model.CapabilityActionUpdate {
		t.Fatalf("命中后审计应为 create+update: %+v", revs)
	}
	if want := digestOf(`{"base_url":"","id":"p1","name":"","note":"主账号","protocol":"openai"}`); revs[1].Digest != want {
		t.Fatalf("审计 digest 应为列行的规范 JSON 摘要: got=%q want=%q", revs[1].Digest, want)
	}
	// 同一内容再提交一次 → revision 前进但 **digest 相同**（"revision 不是内容版本"，ADR-13）。
	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai", Note: "主账号"}, 2, "rest"); err != nil || !ok {
		t.Fatalf("同内容再提交应命中: ok=%v err=%v", ok, err)
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if len(revs) != 3 || revs[2].Digest != revs[1].Digest {
		t.Fatalf("同内容两次提交的 digest 应相同（revision 只是记账）: %+v", revs)
	}

	// —— 导入路径：action=import 且只在行不存在时插入（幂等）——
	if ok, err := s.ImportProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai"}); err != nil || ok {
		t.Fatalf("已存在的行不该被导入覆盖: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ImportProviderDefinition(ctx, ProviderColumns{ID: "p2", Protocol: "openai"}); err != nil || !ok {
		t.Fatalf("导入应新建: ok=%v err=%v", ok, err)
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p2")
	if len(revs) != 1 || revs[0].Action != model.CapabilityActionImport || revs[0].Actor != model.CapabilityActorImport {
		t.Fatalf("导入审计应为单条 action=import: %+v", revs)
	}

	// —— 删除：过期 revision 挡住，正确 revision 删掉后审计仍在 ——
	if deleted, refs, err := s.DeleteProviderDefinitionIfUnreferenced(ctx, "p1", 1, "rest"); err != nil || deleted || len(refs) != 0 {
		t.Fatalf("过期 revision 不应删掉: deleted=%v refs=%v err=%v", deleted, refs, err)
	}
	if deleted, refs, err := s.DeleteProviderDefinitionIfUnreferenced(ctx, "p1", 3, "rest"); err != nil || !deleted || len(refs) != 0 {
		t.Fatalf("正确 revision 应删掉: deleted=%v refs=%v err=%v", deleted, refs, err)
	}
	if _, found, _ := s.GetProviderDefinition(ctx, "p1"); found {
		t.Fatal("删除后仍读得到行")
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if len(revs) != 4 || revs[3].Digest != "" {
		t.Fatalf("删除审计应留 4 条且 digest 为空串: %+v", revs)
	}
	if n, err := s.CountProviderDefinitions(ctx); err != nil || n != 1 {
		t.Fatalf("删掉 p1 后应只剩 p2: n=%d err=%v", n, err)
	}
}

// TestDeleteProviderDefinitionIfUnreferenced A5：引用检查与删除在**同一事务**里。
//
// 判据是三条，缺一不可：
//  1. 有模型挂靠 → **什么都不删**（行在、审计不追）并回带引用名单；
//  2. 引用消失后同一 Revision 能删掉（不是被上一次失败"卡死"）；
//  3. 行不存在 → deleted=false 且不报错（幂等）。
func TestDeleteProviderDefinitionIfUnreferenced(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai"}, 0, "rest"); err != nil || !ok {
		t.Fatalf("建供应商: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "m1", ProviderID: "p1", ModelID: "gpt-4o", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("建模型: ok=%v err=%v", ok, err)
	}

	// ① 有引用 → 不删、不写审计、回带名单。
	deleted, refs, err := s.DeleteProviderDefinitionIfUnreferenced(ctx, "p1", 1, "rest")
	if err != nil || deleted {
		t.Fatalf("被引用时不该删掉: deleted=%v err=%v", deleted, err)
	}
	if len(refs) != 1 || refs[0] != "m1" {
		t.Fatalf("应回带引用名单 [m1]: %v", refs)
	}
	if _, found, _ := s.GetProviderDefinition(ctx, "p1"); !found {
		t.Fatal("被引用时行不该消失")
	}
	if revs, _ := s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1"); len(revs) != 1 {
		t.Fatalf("被引用时不该追审计: %+v", revs)
	}

	// ② 引用解开 → 同一 revision 删得掉。
	row, _, _ := s.GetModelDefinition(ctx, "m1")
	if _, _, err := s.DeleteModelDefinitionAndClearDefault(ctx, "m1", row.Revision, "rest"); err != nil {
		t.Fatalf("删模型: %v", err)
	}
	deleted, refs, err = s.DeleteProviderDefinitionIfUnreferenced(ctx, "p1", 1, "rest")
	if err != nil || !deleted || len(refs) != 0 {
		t.Fatalf("引用解开后应删掉: deleted=%v refs=%v err=%v", deleted, refs, err)
	}
	if revs, _ := s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1"); len(revs) != 2 || revs[1].Action != model.CapabilityActionDelete {
		t.Fatalf("删除应留一条 action=delete 审计: %+v", revs)
	}

	// ③ 行不存在 → 幂等。
	if deleted, refs, err := s.DeleteProviderDefinitionIfUnreferenced(ctx, "nope", 1, "rest"); err != nil || deleted || len(refs) != 0 {
		t.Fatalf("不存在的行应幂等 (false, nil): deleted=%v refs=%v err=%v", deleted, refs, err)
	}
}

// TestDeleteModelDefinitionAndClearDefault B6：删行与清默认在**同一事务**。
//
// 两条判据：① 删的正是默认 → 一并清空并如实回带；② 删的不是默认 → 默认**原样不动**
// （删除是并发动作，"删 A"不该把用户刚设的默认 B 一起清掉）。
func TestDeleteModelDefinitionAndClearDefault(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	for _, id := range []string{"a", "b"} {
		if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
			ID: id, ProviderID: "p1", ModelID: "m-" + id, InputModalities: "text",
		}, "rest"); err != nil || !ok {
			t.Fatalf("建 %s: ok=%v err=%v", id, ok, err)
		}
	}
	if err := s.SetDefaultModelID(ctx, "a"); err != nil {
		t.Fatalf("设默认: %v", err)
	}

	// ① 删非默认 → 默认不动。
	row, _, _ := s.GetModelDefinition(ctx, "b")
	deleted, cleared, err := s.DeleteModelDefinitionAndClearDefault(ctx, "b", row.Revision, "rest")
	if err != nil || !deleted || cleared {
		t.Fatalf("删非默认应 (true,false,nil): deleted=%v cleared=%v err=%v", deleted, cleared, err)
	}
	if id, found, _ := s.DefaultModelID(ctx); !found || id != "a" {
		t.Fatalf("删非默认不该动默认: id=%q found=%v", id, found)
	}

	// ② 删默认 → 一并清空。
	row, _, _ = s.GetModelDefinition(ctx, "a")
	deleted, cleared, err = s.DeleteModelDefinitionAndClearDefault(ctx, "a", row.Revision, "rest")
	if err != nil || !deleted || !cleared {
		t.Fatalf("删默认应 (true,true,nil): deleted=%v cleared=%v err=%v", deleted, cleared, err)
	}
	if id, found, _ := s.DefaultModelID(ctx); found || id != "" {
		t.Fatalf("删默认后应回到未设: id=%q found=%v", id, found)
	}

	// ③ CAS 未中 → 不删、不清、不写审计。
	if deleted, cleared, err := s.DeleteModelDefinitionAndClearDefault(ctx, "a", 1, "rest"); err != nil || deleted || cleared {
		t.Fatalf("CAS 未中应 (false,false,nil): deleted=%v cleared=%v err=%v", deleted, cleared, err)
	}
}

// TestResourceDigest A6 / ADR-10：列行的审计摘要。
//
// 三条判据：同内容同摘要、不同内容不同摘要、删除路径 digest 为空串
// （最后一条由 `TestProviderDefinitionCAS` 与 `TestModelDefinitionPairUnique` 一起钉住）。
func TestResourceDigest(t *testing.T) {
	base := map[string]string{"id": "p1", "name": "主账号", "protocol": "openai", "base_url": "u", "note": "n"}
	if rowDigest(base) != rowDigest(map[string]string{
		"note": "n", "base_url": "u", "protocol": "openai", "name": "主账号", "id": "p1",
	}) {
		t.Fatal("同一内容（不同构造顺序）的 digest 必须相同：规范 JSON 要按键排序")
	}
	other := map[string]string{"id": "p1", "name": "主账号", "protocol": "openai", "base_url": "u2", "note": "n"}
	if rowDigest(base) == rowDigest(other) {
		t.Fatal("不同内容的 digest 必须不同")
	}
	if rowDigest(nil) != "" {
		t.Fatalf("空字段集的 digest 应为空串（删除路径口径）: %q", rowDigest(nil))
	}
	// 记账列不进摘要：revision/时间列不在字段集里，所以"同内容两次提交"digest 相同
	// （上面那条已由 TestProviderDefinitionCAS 在真实提交路径上钉过）。
	if rowDigest(base) == "" {
		t.Fatal("非空字段集不该得到空摘要")
	}
}

// TestModelDefinitionPairUnique `(provider_id, model_id)` 是**库级唯一索引**。
//
// v11 的唯一约束是三元组 `(runtime, provider_id, model_id)`，而 `model_id` 当时在 body 里、
// 索引表达不了——判据只能是"提交事务内先查后写"。v12 把 `model_id` 提升为列，于是这条不变量
// **由库自己保证**：重复插入被 SQLite 拒，且**拒绝时一行一审计都不落**
// （"检查失败"与"写了一半"必须可分辨）。
func TestModelDefinitionPairUnique(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "m1", ProviderID: "p1", ModelID: "v4-flash", Name: "Flash", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("首建应命中: ok=%v err=%v", ok, err)
	}
	// 同 `(provider_id, model_id)`（不同 id、不同显示名）→ 拒。
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "m2", ProviderID: "p1", ModelID: "v4-flash", Name: "别名", InputModalities: "text",
	}, "rest"); err != nil || ok {
		t.Fatalf("重复 (provider_id, model_id) 应被拒: ok=%v err=%v", ok, err)
	}
	// 换 provider → 是另一条。
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "m4", ProviderID: "p2", ModelID: "v4-flash", Name: "Flash", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("换 provider 应可建: ok=%v err=%v", ok, err)
	}

	rows, err := s.ListModelDefinitions(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("全量清单应为 2: n=%d err=%v", len(rows), err)
	}
	// 列表顺序有语义（ADR-6）：`(provider_id, model_id)` 升序 —— 这里的两行分别属于 p1/p2，
	// 所以升序结果恰好是 p1 那条在前。
	if rows[0].ID != "m1" || rows[1].ID != "m4" {
		t.Fatalf("清单顺序应为 (provider_id, model_id) 升序: %s, %s", rows[0].ID, rows[1].ID)
	}
	// 被拒的那次不能留下痕迹。
	if revs, _ := s.ListCapabilityRevisions(ctx, model.CapabilityKindModel, "m2"); len(revs) != 0 {
		t.Fatalf("被拒的重复创建不该留审计: %+v", revs)
	}
	if n, err := s.ListModelDefinitionsByProvider(ctx, "p1"); err != nil || len(n) != 1 {
		t.Fatalf("按 provider 查引用应为 1: n=%d err=%v", len(n), err)
	}
	// 按 `(provider_id, model_id)` 取一条（写面的 model_exists 判定走它）。
	if row, found, err := s.FindModelDefinition(ctx, "p1", "v4-flash"); err != nil || !found || row.ID != "m1" {
		t.Fatalf("FindModelDefinition 失真: found=%v err=%v row=%+v", found, err, row)
	}
	if _, found, _ := s.FindModelDefinition(ctx, "p1", "nope"); found {
		t.Fatal("不存在的 (provider_id, model_id) 不该命中")
	}
	// 列是唯一权威：`name` 与 `input_modalities` 逐字落在列上。
	if rows[0].Name != "Flash" || rows[0].InputModalities != "text" || rows[0].Note != "" {
		t.Fatalf("模型行的列值不符: %+v", rows[0])
	}
}

// TestDefaultModelIsSingleValue 默认模型是 `meta` 里的**单一值**（ADR-21）。
//
// 三个断言对应三件不同的事：
//  1. 设默认是覆盖式的（KV 天然单一值），没有"每 runtime 一条"这回事；
//  2. **清默认带条件**：只有当前值正是它时才清——"删 A"不该把用户刚设的默认 B 一起清掉；
//  3. 模型行的**内容列**改动**不碰**默认（两者在不同载体里，v11 那种"改别名顺手把默认清掉"的
//     双写漂移在 v12 结构上不可能）。
func TestDefaultModelIsSingleValue(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "a", ProviderID: "p1", ModelID: "m-a", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("建 a: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, ModelColumns{
		ID: "b", ProviderID: "p1", ModelID: "m-b", InputModalities: "text",
	}, "rest"); err != nil || !ok {
		t.Fatalf("建 b: ok=%v err=%v", ok, err)
	}

	// 未设 → ("", false, nil)。
	if id, found, err := s.DefaultModelID(ctx); err != nil || found || id != "" {
		t.Fatalf("未设默认应 (\"\", false, nil): id=%q found=%v err=%v", id, found, err)
	}
	if err := s.SetDefaultModelID(ctx, "a"); err != nil {
		t.Fatalf("设默认: %v", err)
	}
	if id, found, _ := s.DefaultModelID(ctx); !found || id != "a" {
		t.Fatalf("默认应为 a: id=%q found=%v", id, found)
	}
	// 覆盖式：设 b 之后只有一个值，a 不再是"某个 runtime 的默认"。
	if err := s.SetDefaultModelID(ctx, "b"); err != nil {
		t.Fatalf("覆盖默认: %v", err)
	}
	if id, _, _ := s.DefaultModelID(ctx); id != "b" {
		t.Fatalf("默认应变成 b，实际 %q", id)
	}

	// 清默认带条件：清 a 不动 b。
	if cleared, err := s.ClearDefaultModelID(ctx, "a"); err != nil || cleared {
		t.Fatalf("清一个不是默认的 id 应 (false, nil): cleared=%v err=%v", cleared, err)
	}
	if id, _, _ := s.DefaultModelID(ctx); id != "b" {
		t.Fatalf("清 a 不该动 b，实际 %q", id)
	}
	if cleared, err := s.ClearDefaultModelID(ctx, "b"); err != nil || !cleared {
		t.Fatalf("清当前默认应 (true, nil): cleared=%v err=%v", cleared, err)
	}
	if id, found, _ := s.DefaultModelID(ctx); found || id != "" {
		t.Fatalf("清掉之后应回到未设: id=%q found=%v", id, found)
	}

	// 模型行的内容列改动不碰默认（两者在不同载体）。
	if err := s.SetDefaultModelID(ctx, "a"); err != nil {
		t.Fatalf("重设默认 a: %v", err)
	}
	row, _, _ := s.GetModelDefinition(ctx, "a")
	if ok, err := s.UpdateModelDefinition(ctx, "a", ModelContent{
		Name: "别名", Note: row.Note, InputModalities: row.InputModalities,
	}, row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("覆盖内容列: ok=%v err=%v", ok, err)
	}
	if id, _, _ := s.DefaultModelID(ctx); id != "a" {
		t.Fatalf("覆盖内容把默认清掉了（默认被双写进模型行的典型症状），实际 %q", id)
	}
	got, _, _ := s.GetModelDefinition(ctx, "a")
	if got.Revision != row.Revision+1 || got.Name != "别名" || got.ProviderID != row.ProviderID || got.ModelID != row.ModelID {
		t.Fatalf("覆盖内容应前进一版并落新值、且不动身份列: rev=%d name=%q provider=%q model=%q",
			got.Revision, got.Name, got.ProviderID, got.ModelID)
	}
}

// TestRowDaoUpdateColumnsCASCannotTouchIdentity 身份列与记账列**结构上**改不动。
//
// `RowDao.UpdateColumnsCAS` 收的是"内容列"，但它的入参是一个 map——把 `id`/`revision`/
// `created_at` 传进去如果生效，就是一条绕过 CAS 与身份的旁路。这里逐列钉住它们被防御性剔除。
func TestRowDaoUpdateColumnsCASCannotTouchIdentity(t *testing.T) {
	s := openTestDB(t)
	ctx := ctxT()

	if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai", Name: "原名"}, 0, "rest"); err != nil || !ok {
		t.Fatalf("建行: ok=%v err=%v", ok, err)
	}
	before, _, _ := s.GetProviderDefinition(ctx, "p1")

	ok, err := s.providers.UpdateColumnsCAS(ctx, "p1", map[string]any{
		"id": "hijacked", "revision": int64(99), "created_at": "1970-01-01T00:00:00Z",
		"updated_at": "1970-01-01T00:00:00Z", "name": "新名",
	}, before.Revision, before.UpdatedAt)
	if err != nil || !ok {
		t.Fatalf("CAS 应命中: ok=%v err=%v", ok, err)
	}
	after, found, _ := s.GetProviderDefinition(ctx, "p1")
	if !found {
		t.Fatal("主键被改掉了（行 p1 消失）")
	}
	if after.Name != "新名" || after.Revision != before.Revision+1 || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("内容列该改、身份/记账列不该改: %+v", after)
	}
	if _, found, _ := s.GetProviderDefinition(ctx, "hijacked"); found {
		t.Fatal("id 被更新进了别的行")
	}
}

// TestRowDaoCASIsTheOnlyArbiter 并发下的唯一仲裁者：同一 Revision 的两次 CAS → **恰好一次**成功。
//
// 这条对应设计「风险评估」第 2 行（`RowDao` 与 `DefinitionDao` 的 CAS 语义不许分叉）：
// 判据只有 `rowsAffected == 0` 一处实现，所以"同一版被两个人同时改"必然只放行一个。
// 覆盖 `UpdateColumnsCAS` 与 `DeleteCAS` 两条路——"删掉别人刚改过的那一版"与"覆盖别人刚改过的那一版"
// 必须是同一档拦截。
func TestRowDaoCASIsTheOnlyArbiter(t *testing.T) {
	ctx := ctxT()

	for _, tc := range []struct {
		name string
		run  func(s *Store, id string, rev int64) (bool, error)
		want string
	}{
		{
			name: "UpdateColumnsCAS",
			run: func(s *Store, id string, rev int64) (bool, error) {
				return s.providers.UpdateColumnsCAS(ctx, id, map[string]any{"note": "改"}, rev, time.Now().UTC())
			},
		},
		{
			name: "DeleteCAS",
			run: func(s *Store, id string, rev int64) (bool, error) {
				return s.providers.DeleteCAS(ctx, id, rev)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestDB(t)
			if ok, err := s.UpsertProviderDefinition(ctx, ProviderColumns{ID: "p1", Protocol: "openai"}, 0, "rest"); err != nil || !ok {
				t.Fatalf("建行: ok=%v err=%v", ok, err)
			}
			row, _, _ := s.GetProviderDefinition(ctx, "p1")

			const n = 8
			var wg sync.WaitGroup
			results := make([]bool, n)
			errs := make([]error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					results[i], errs[i] = tc.run(s, "p1", row.Revision)
				}(i)
			}
			wg.Wait()

			wins := 0
			for i := 0; i < n; i++ {
				if errs[i] != nil {
					t.Fatalf("第 %d 次 CAS 报错: %v", i, errs[i])
				}
				if results[i] {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("同一 Revision 的 %d 次 CAS 应恰好成功 1 次，实际 %d 次", n, wins)
			}
		})
	}
}
