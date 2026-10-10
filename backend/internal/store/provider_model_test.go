package store

import (
	"sync"
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
	ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"type":"deepseek"}`, 0, "rest")
	if err != nil || !ok {
		t.Fatalf("新建应命中: ok=%v err=%v", ok, err)
	}
	got, found, err := s.GetProviderDefinition(ctx, "p1")
	if err != nil || !found {
		t.Fatalf("新建后应读得到: found=%v err=%v", found, err)
	}
	if got.Revision != 1 || got.Body != `{"type":"deepseek"}` || got.UpdatedAt.IsZero() {
		t.Fatalf("新建后形态不符: rev=%d body=%q at=%s", got.Revision, got.Body, got.UpdatedAt)
	}

	// —— 再建同 id → 不命中，且**不追审计** ——
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"type":"dup"}`, 0, "rest"); err != nil || ok {
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
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"type":"stale"}`, 7, "rest"); err != nil || ok {
		t.Fatalf("过期 revision 不应命中: ok=%v err=%v", ok, err)
	}
	got, _, _ = s.GetProviderDefinition(ctx, "p1")
	if got.Revision != 1 || got.Body != `{"type":"deepseek"}` {
		t.Fatalf("CAS 未中却改到了行: rev=%d body=%q", got.Revision, got.Body)
	}
	if revs, _ = s.ListCapabilityRevisions(ctx, model.CapabilityKindProvider, "p1"); len(revs) != 1 {
		t.Fatalf("CAS 未中却追了审计: n=%d", len(revs))
	}

	// —— CAS 命中：revision +1，审计里那条是新 body 的摘要 ——
	if ok, err := s.UpsertProviderDefinition(ctx, "p1", `{"type":"deepseek","note":"主账号"}`, 1, "rest"); err != nil || !ok {
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
	if want := digestOf(`{"type":"deepseek","note":"主账号"}`); revs[1].Digest != want {
		t.Fatalf("审计 digest 应为新 body 摘要: got=%q want=%q", revs[1].Digest, want)
	}

	// —— 导入路径：action=import 且只在行不存在时插入（幂等）——
	if ok, err := s.ImportProviderDefinition(ctx, "p1", `{"type":"imported"}`); err != nil || ok {
		t.Fatalf("已存在的行不该被导入覆盖: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ImportProviderDefinition(ctx, "p2", `{"type":"imported"}`); err != nil || !ok {
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

// TestModelDefinitionTripleUnique `(runtime, provider_id, model_id)` 是唯一约束。
//
// 它不是库级唯一索引（`model_id` 在 body 里，索引表达不了），而是**提交事务内**的一次
// 存在性查询——所以这条测试同时钉住两件事：重复三元组被拒、且**拒绝时一行一审计都不落**
// （"检查失败"与"写了一半"必须可分辨）。
func TestModelDefinitionTripleUnique(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()

	body := func(modelID, name string) string {
		return `{"model_id":"` + modelID + `","name":"` + name + `"}`
	}
	if ok, err := s.CreateModelDefinition(ctx, "m1", "opencode", "p1", "v4-flash", body("v4-flash", "Flash"), "rest"); err != nil || !ok {
		t.Fatalf("首建应命中: ok=%v err=%v", ok, err)
	}
	// 同三元组（不同 id、不同显示名）→ 拒。
	if ok, err := s.CreateModelDefinition(ctx, "m2", "opencode", "p1", "v4-flash", body("v4-flash", "别名"), "rest"); err != nil || ok {
		t.Fatalf("重复三元组应被拒: ok=%v err=%v", ok, err)
	}
	// 换 runtime → 是另一条（同一模型要供两个底座用就得建两行）。
	if ok, err := s.CreateModelDefinition(ctx, "m3", "dsh", "p1", "v4-flash", body("v4-flash", "Flash"), "rest"); err != nil || !ok {
		t.Fatalf("换 runtime 应可建: ok=%v err=%v", ok, err)
	}
	// 换 provider → 也是另一条。
	if ok, err := s.CreateModelDefinition(ctx, "m4", "opencode", "p2", "v4-flash", body("v4-flash", "Flash"), "rest"); err != nil || !ok {
		t.Fatalf("换 provider 应可建: ok=%v err=%v", ok, err)
	}

	rows, err := s.ListModelDefinitions(ctx, "opencode")
	if err != nil || len(rows) != 2 || rows[0].ID != "m1" || rows[1].ID != "m4" {
		t.Fatalf("按 runtime 过滤的清单不符: %+v err=%v", rows, err)
	}
	if all, err := s.ListModelDefinitions(ctx, ""); err != nil || len(all) != 3 {
		t.Fatalf("全量清单应为 3: n=%d err=%v", len(all), err)
	}
	// 被拒的那次不能留下痕迹。
	if revs, _ := s.ListCapabilityRevisions(ctx, model.CapabilityKindModel, "m2"); len(revs) != 0 {
		t.Fatalf("被拒的重复创建不该留审计: %+v", revs)
	}
	if n, err := s.ListModelDefinitionsByProvider(ctx, "p1"); err != nil || len(n) != 2 {
		t.Fatalf("按 provider 查引用应为 2: n=%d err=%v", len(n), err)
	}
}

// TestSetDefaultModelPerRuntimeUnique 每 runtime **恰好一条**默认；置默认走 CAS 并前进一版。
//
// 三个断言对应三件不同的事：
//  1. 置默认会清掉同 runtime 的旧默认（同一个事务里的两条语句）；
//  2. 不同 runtime 各自独立（默认模型的归属是 per-runtime 的，不是全局一个）；
//  3. 并发置两条同 runtime 的默认之后仍然恰好一条（不变量由写面维持，不由调用方维持）。
func TestSetDefaultModelPerRuntimeUnique(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()

	body := func(modelID string) string { return `{"model_id":"` + modelID + `"}` }
	if ok, err := s.CreateModelDefinition(ctx, "a", "opencode", "p1", "m-a", body("m-a"), "rest"); err != nil || !ok {
		t.Fatalf("建 a: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, "b", "opencode", "p1", "m-b", body("m-b"), "rest"); err != nil || !ok {
		t.Fatalf("建 b: ok=%v err=%v", ok, err)
	}
	if ok, err := s.CreateModelDefinition(ctx, "c", "dsh", "p1", "m-c", body("m-c"), "rest"); err != nil || !ok {
		t.Fatalf("建 c: ok=%v err=%v", ok, err)
	}

	// 默认位初始为 false（列上的默认值 0，不是 NULL）。
	if row, _, _ := s.GetModelDefinition(ctx, "a"); row.IsDefault {
		t.Fatal("新行的默认位应为 false")
	}

	if ok, err := s.SetDefaultModel(ctx, "a", "rest"); err != nil || !ok {
		t.Fatalf("置 a 为默认: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SetDefaultModel(ctx, "c", "rest"); err != nil || !ok {
		t.Fatalf("置 c 为默认: ok=%v err=%v", ok, err)
	}
	// 幂等：已经是默认时不再写一版（revision 不动）。
	before, _, _ := s.GetModelDefinition(ctx, "a")
	if ok, err := s.SetDefaultModel(ctx, "a", "rest"); err != nil || !ok {
		t.Fatalf("重复置同一默认应幂等成功: ok=%v err=%v", ok, err)
	}
	after, _, _ := s.GetModelDefinition(ctx, "a")
	if after.Revision != before.Revision {
		t.Fatalf("重复置默认不该前进一版: before=%d after=%d", before.Revision, after.Revision)
	}

	// 换成 b：a 必须被清掉，且 b 前进一版、a 的 revision 不动（清默认位不是"改这一行"）。
	if ok, err := s.SetDefaultModel(ctx, "b", "rest"); err != nil || !ok {
		t.Fatalf("置 b 为默认: ok=%v err=%v", ok, err)
	}
	rowA, _, _ := s.GetModelDefinition(ctx, "a")
	rowB, _, _ := s.GetModelDefinition(ctx, "b")
	if rowA.IsDefault || !rowB.IsDefault {
		t.Fatalf("切换默认失败: a=%v b=%v", rowA.IsDefault, rowB.IsDefault)
	}
	if rowA.Revision != before.Revision {
		t.Fatalf("被清掉默认的那行 revision 不该变: %d", rowA.Revision)
	}
	if rowB.Revision != 2 {
		t.Fatalf("置默认应前进一版: rev=%d", rowB.Revision)
	}
	// 每 runtime 恰好一条。
	defaults, err := s.ListDefaultModelDefinitions(ctx)
	if err != nil || len(defaults) != 2 {
		t.Fatalf("默认应为 2 条（opencode/dsh 各一）: %+v err=%v", defaults, err)
	}
	if d, found, _ := s.DefaultModelDefinition(ctx, "opencode"); !found || d.ID != "b" {
		t.Fatalf("opencode 的默认应为 b: found=%v row=%+v", found, d)
	}
	if d, found, _ := s.DefaultModelDefinition(ctx, "dsh"); !found || d.ID != "c" {
		t.Fatalf("dsh 的默认应为 c: found=%v row=%+v", found, d)
	}

	// —— 并发置两条同 runtime 的默认：仍然恰好一条 ——
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, errs[i] = s.SetDefaultModel(ctx, id, "rest")
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发置默认 %d 失败: %v", i, err)
		}
	}
	rows, err := s.ListModelDefinitions(ctx, "opencode")
	if err != nil {
		t.Fatalf("ListModelDefinitions: %v", err)
	}
	n := 0
	for i := range rows {
		if rows[i].IsDefault {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("并发置默认后每 runtime 应恰好一条，实际 %d: %+v", n, rows)
	}

	// 不存在的行 → (false, nil)，调用方翻成 model_not_found(40401)。
	if ok, err := s.SetDefaultModel(ctx, "nope", "rest"); err != nil || ok {
		t.Fatalf("对不存在的行置默认应 (false, nil): ok=%v err=%v", ok, err)
	}
}

// TestModelDefinitionBodyEditsKeepIsDefault 覆盖正文（改别名/隐藏/置顶）不得动默认位。
//
// 这条挡的是"把 is_default 也塞进 body 再双写"的退化：一旦双写，改别名就会顺手把默认清掉，
// 而那种漂移不会让别的测试变红——只有这里会。
func TestModelDefinitionBodyEditsKeepIsDefault(t *testing.T) {
	s := openMem(t)
	ctx := ctxT()
	if ok, err := s.CreateModelDefinition(ctx, "m1", "opencode", "p1", "v4-flash", `{"model_id":"v4-flash"}`, "rest"); err != nil || !ok {
		t.Fatalf("建: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SetDefaultModel(ctx, "m1", "rest"); err != nil || !ok {
		t.Fatalf("置默认: ok=%v err=%v", ok, err)
	}
	row, _, _ := s.GetModelDefinition(ctx, "m1")
	if ok, err := s.UpsertModelDefinition(ctx, "m1", `{"model_id":"v4-flash","name":"别名","hidden":true}`, row.Revision, "rest"); err != nil || !ok {
		t.Fatalf("覆盖正文: ok=%v err=%v", ok, err)
	}
	got, _, _ := s.GetModelDefinition(ctx, "m1")
	if !got.IsDefault {
		t.Fatal("覆盖正文把默认位清掉了（is_default 被双写进 body 的典型症状）")
	}
	if got.Revision != row.Revision+1 {
		t.Fatalf("覆盖正文应前进一版: %d → %d", row.Revision, got.Revision)
	}
}
