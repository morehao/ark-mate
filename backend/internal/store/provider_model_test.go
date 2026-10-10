package store

import (
	"testing"

	"github.com/morehao/ark-mate/internal/model"
)

// TestProviderDefinitionCAS 是 provider 面与 `TestCapabilityDefinitionCAS` **同形**的写面契约：
// 一次提交 = 一行 + 一条审计，CAS 未中则两表都不动，删除留痕且 digest 落空串。
//
// 与能力面重复一遍是刻意的：本面是**第五次**同形落地，而"两边共用 commitDefinitionChange"
// 这件事之所以可信，正是因为它被逐面钉过一次（任何一面偷偷绕开提交路径，这里就会缺审计）。
func TestProviderDefinitionCAS(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()

	// —— 新建（expectedRev=0）——
	ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"protocol":"openai"}`, 0, "rest")
	if err != nil || !ok {
		t.Fatalf("新建应命中: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetProviderDefinition(ctx, "p1")
	if err != nil || !found {
		t.Fatalf("新建后应读得到: found=%v err=%v", found, err)
	}
	if got.Revision != 1 || got.Body != `{"protocol":"openai"}` || got.UpdatedAt.IsZero() {
		t.Fatalf("新建后形态不符: rev=%d body=%q at=%s", got.Revision, got.Body, got.UpdatedAt)
	}

	// —— 再建同 id → 不命中，且**不追审计** ——
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"protocol":"dup"}`, 0, "rest"); err != nil || ok {
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
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"protocol":"stale"}`, 7, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetProviderDefinition(ctx, "p1")
	if got.Revision != 1 || got.Body != `{"protocol":"openai"}` {
		t.Fatalf("CAS 未中却改到了行: rev=%d body=%q", got.Revision, got.Body)
	}
	if revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1"); len(revs) != 1 {
		t.Fatalf("CAS 未中却追了审计: n=%d", len(revs))
	}

	// —— CAS 命中：revision +1，审计里那条是新 body 的摘要 ——
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"protocol":"openai","note":"主账号"}`, 1, "rest"); err != nil || !ok {
		t.Fatalf("正确 revision 应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetProviderDefinition(ctx, "p1")
	if got.Revision != 2 {
		t.Fatalf("命中后 revision 应为 2，实际 %d", got.Revision)
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if len(revs) != 2 || revs[1].Action != model.CapabilityActionUpdate {
		t.Fatalf("命中后审计应为 create+update: %+v", revs)
	}
	if want := digestOf(`{"protocol":"openai","note":"主账号"}`); revs[1].Digest != want {
		t.Fatalf("审计 digest 应为新 body 摘要: got=%q want=%q", revs[1].Digest, want)
	}

	// —— 导入路径：action=import 且只在行不存在时插入（幂等）——
	if ok, err := s.ImportProviderDefinition(ctx, "p1", `{"protocol":"imported"}`); err != nil || ok {
		t.Fatalf("已存在的行不该被导入覆盖: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ImportProviderDefinition(ctx, "p2", `{"protocol":"imported"}`); err != nil || !ok {
		t.Fatalf("导入应新建: ok=%v err=%v", ok, err)
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p2")
	if len(revs) != 1 || revs[0].Action != model.CapabilityActionImport || revs[0].Actor != model.CapabilityActorImport {
		t.Fatalf("导入审计应为单条 action=import: %+v", revs)
	}

	// —— 删除：过期 revision 挡住，正确 revision 删掉后审计仍在 ——
	if ok, err := s.DeleteProviderDefinition(ctx, "p1", 1, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应删掉: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteProviderDefinition(ctx, "p1", 2, "rest"); err != nil || !ok {
		t.Fatalf("正确 revision 应删掉: ok=%v err=%v", ok, err)
	}
	if _, found, _ := s.GetProviderDefinition(ctx, "p1"); found {
		t.Fatal("删除后仍读得到行")
	}
	revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1")
	if len(revs) != 3 || revs[2].Digest != "" {
		t.Fatalf("删除审计应留 3 条且 digest 为空串: %+v", revs)
	}
	if n, err := s.CountProviderDefinitions(ctx); err != nil || n != 1 {
		t.Fatalf("删掉 p1 后应只剩 p2: n=%d err=%v", n, err)
	}
}

// TestModelDefinitionPairUnique `(provider_id, model_id)` 是**库级唯一索引**。
//
// v11 的唯一约束是三元组 `(runtime, provider_id, model_id)`，而 `model_id` 当时在 body 里、
// 索引表达不了——判据只能是"提交事务内先查后写"。v12 把 `model_id` 提升为列，于是这条不变量
// **由库自己保证**：重复插入被 SQLite 拒，且**拒绝时一行一审计都不落**
// （"检查失败"与"写了一半"必须可分辨）。
func TestModelDefinitionPairUnique(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()

	if ok, err := s.CreateModelDefinition(ctx, "m1", "p1", "v4-flash", `{"name":"Flash"}`, "rest"); err != nil || !ok {
		t.Fatalf("首建应命中: ok=%v err=%v", ok, err)
	}
	// 同 `(provider_id, model_id)`（不同 id、不同显示名）→ 拒。
	if ok, err := s.CreateModelDefinition(ctx, "m2", "p1", "v4-flash", `{"name":"别名"}`, "rest"); err != nil || ok {
		t.Fatalf("重复 (provider_id, model_id) 应被拒: ok=%v err=%v", ok, err)
	}
	// 换 provider → 是另一条。
	if ok, err := s.CreateModelDefinition(ctx, "m4", "p2", "v4-flash", `{"name":"Flash"}`, "rest"); err != nil || !ok {
		t.Fatalf("换 provider 应可建: ok=%v err=%v", ok, err)
	}

	rows, err := s.ListModelDefinitions(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("全量清单应为 2: n=%d err=%v", len(rows), err)
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
}

// TestDefaultModelIsSingleValue 默认模型是 `meta` 里的**单一值**（ADR-21）。
//
// 三个断言对应三件不同的事：
//  1. 设默认是覆盖式的（KV 天然单一值），没有"每 runtime 一条"这回事；
//  2. **清默认带条件**：只有当前值正是它时才清——"删 A"不该把用户刚设的默认 B 一起清掉；
//  3. 模型行的正文改动**不碰**默认（两者在不同载体里，v11 那种"改别名顺手把默认清掉"的
//     双写漂移在 v12 结构上不可能）。
func TestDefaultModelIsSingleValue(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()

	if ok, err := s.CreateModelDefinition(ctx, "a", "p1", "m-a", `{}`, "rest"); err != nil || !ok {
		t.Fatalf("建 a: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, "b", "p1", "m-b", `{}`, "rest"); err != nil || !ok {
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

	// 模型行的正文改动不碰默认（两者在不同载体）。
	if err := s.SetDefaultModelID(ctx, "a"); err != nil {
		t.Fatalf("重设默认 a: %v", err)
	}
	row, _, _ := s.GetModelDefinition(ctx, "a")
	if ok, err := s.UpsertModelDefinition(ctx, "a", `{"name":"别名"}`, row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("覆盖正文: ok=%v err=%v", ok, err)
	}
	if id, _, _ := s.DefaultModelID(ctx); id != "a" {
		t.Fatalf("覆盖正文把默认清掉了（默认被双写进模型行的典型症状），实际 %q", id)
	}
	got, _, _ := s.GetModelDefinition(ctx, "a")
	if got.Revision != row.Revision+1 || got.Body != `{"name":"别名"}` {
		t.Fatalf("覆盖正文应前进一版并落新正文: rev=%d body=%q", got.Revision, got.Body)
	}
}
