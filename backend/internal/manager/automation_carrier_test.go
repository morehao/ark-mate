package manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/eventbus"
	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/internal/store"
	"github.com/morehao/ark-forge/internal/testutil"
	"github.com/morehao/ark-forge/pkg/automation"
	"github.com/morehao/ark-forge/pkg/runtime"
)

// ——— M6-7：automation 定义的 DB 载体（定义列 + 运行态列） ———
//
// 这一组只针对 **sqlite 载体**：memory 载体的既有用例（automation_test.go / automation_sched_test.go /
// automation_rest_test.go）原样保留且绿，两者共同的对外语义（拒写名单、坏定义留列表、认领游标）
// 在两边各有一份断言——它们的实现确实不同（文件是整份重写 + mtime 锁，行是两段 CAS）。

// automationDBRig 一个"装配期已从 store 装载"的 Service + 其库（可直连断言行与审计）。
//
// 装 silent 桩 runtime 是因为调度用例要真的建会话（`CreateConversation` 需要 agent 与 runtime）；
// 纯写面/装载用例不用它，但共用一个夹具省得两套配置漂移。
func automationDBRig(t *testing.T, dir string) (*Service, *store.Store) {
	t.Helper()
	spec := silentACPSpec(t)
	reg := runtime.NewRegistry(spec)
	svc := NewService(reg, eventbus.New())
	svc.agents["a1"] = &AgentRec{ID: "a1", Name: "A1", Runtimes: []string{"silent"}, State: AgentStateStopped}
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	svc.EnableStore(st)
	if dir != "" {
		svc.LoadAutomationDefinitionsFromStore(context.Background(), dir)
	}
	return svc, st
}

// dbDailyPatch 一条 daily 定义（cwd 必须是绝对路径，取临时目录）。
func dbDailyPatch(t *testing.T, id, hour string) map[string]any {
	t.Helper()
	p := dailyPatch(id, hour)
	p["target"].(map[string]any)["cwd"] = t.TempDir()
	return p
}

// mustSeedAutomationRow 直接往库里写一行（模拟"存量文件已导入"的落点；可带运行态列）。
func mustSeedAutomationRow(t *testing.T, st *store.Store, row *store.AutomationDefinition) {
	t.Helper()
	if row.RecentRuns == "" {
		row.RecentRuns = "[]"
	}
	if ok, err := st.UpsertAutomationDefinition(context.Background(), row.ID, row.Body, 0, "import"); err != nil || !ok {
		t.Fatalf("写 automation 行 %s: ok=%v err=%v", row.ID, ok, err)
	}
	if rt := (store.AutomationRuntimeState{
		LastFiredAt: row.LastFiredAt, LastStatus: row.LastStatus, LastReason: row.LastReason,
		LastConversationID: row.LastConversationID, ConsecutiveFailures: row.ConsecutiveFailures,
		RecentRuns: row.RecentRuns,
	}); rt != (store.AutomationRuntimeState{RecentRuns: "[]"}) {
		if ok, err := st.SaveAutomationRuntime(context.Background(), row.ID, "", rt); err != nil || !ok {
			t.Fatalf("写 automation 运行态 %s: ok=%v err=%v", row.ID, ok, err)
		}
	}
}

// automationConversations 按 origin 收集某条 automation 的会话（与 schedRig 的同名方法同源，
// 这里不收进夹具是因为 DB 用例不需要整个 schedRig）。
func automationConversations(t *testing.T, svc *Service, id string) []*ConversationRec {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	var out []*ConversationRec
	for _, c := range svc.conversations {
		if c.Origin == automationOrigin(id) {
			out = append(out, c)
		}
	}
	return out
}

// TestAutomationDefinitionsStoreRoundTrip 新建 → 覆盖 → 删除，全程落在 automations 表 + 审计里。
func TestAutomationDefinitionsStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, t.TempDir())

	first, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true)
	if err != nil {
		t.Fatalf("新建: %v", err)
	}
	if first.ID != "daily" || first.Schedule.Kind != automation.KindDaily || first.Error != "" {
		t.Fatalf("新建响应不符: %+v", first)
	}
	row, found, err := st.GetAutomationDefinition(ctx, "daily")
	if err != nil || !found {
		t.Fatalf("库里应有定义行: found=%v err=%v", found, err)
	}
	if row.Revision != 1 || row.RecentRuns != "[]" {
		t.Fatalf("新建行的初值不符: rev=%d recent_runs=%q", row.Revision, row.RecentRuns)
	}
	// 运行态键**不在**正文里（结构上不可写），`state` 在（它是用户操作）。
	if strings.Contains(row.Body, "last_fired_at") {
		t.Fatalf("运行态键不得进定义正文: %s", row.Body)
	}
	// 落库的目录根本不存在（写面在 sqlite 模式下不需要 AutomationDir）——这正是"载体换了"的判据。

	// 覆盖写：只改 name，其余键保留，revision 前进。
	second, err := svc.writeAutomation(ctx, "daily", map[string]any{"name": "改名了"}, false)
	if err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if second.Name != "改名了" || second.Target.Prompt != "跑一遍依赖巡检" {
		t.Fatalf("覆盖写应只改出现的键: %+v", second)
	}
	row, _, _ = st.GetAutomationDefinition(ctx, "daily")
	if row.Revision != 2 {
		t.Fatalf("覆盖写应把 revision 推到 2，got %d", row.Revision)
	}

	// 拒写名单（既有对外契约）：保留键一律 40001，且**一个字节都没改**。
	for _, k := range automation.ReservedKeys {
		if _, err := svc.writeAutomation(ctx, "daily", map[string]any{k: "x"}, false); errCode(err) != "invalid_param" {
			t.Fatalf("保留键 %s 应被拒（40001），got %v", k, err)
		}
	}
	after, _, _ := st.GetAutomationDefinition(ctx, "daily")
	if after.Revision != 2 || after.Body != row.Body {
		t.Fatalf("被拒的写不该碰任何字节: rev=%d", after.Revision)
	}
	// `state` 刻意不在名单里（用户要能把 needs_attention 改回 active）。
	if _, err := svc.writeAutomation(ctx, "daily", map[string]any{"state": automation.StateActive}, false); err != nil {
		t.Fatalf("state 应可由用户写: %v", err)
	}
	// 新建已存在 / 覆盖不存在：两条错路各自的码。
	if _, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true); errCode(err) != "automation_exists" {
		t.Fatalf("重复新建应报 automation_exists，got %v", err)
	}
	if _, err := svc.writeAutomation(ctx, "nope", map[string]any{"name": "x"}, false); errCode(err) != "automation_not_found" {
		t.Fatalf("覆盖不存在的定义应报 automation_not_found，got %v", err)
	}

	// 删除 + 幂等 + 内存同步摘除。
	if removed, err := svc.DeleteAutomationByID(ctx, "daily"); err != nil || !removed {
		t.Fatalf("删除: removed=%v err=%v", removed, err)
	}
	if _, found, _ := st.GetAutomationDefinition(ctx, "daily"); found {
		t.Fatal("行应已删除")
	}
	if _, ok := svc.AutomationByID("daily"); ok {
		t.Fatal("内存条目应一并摘除")
	}
	if removed, err := svc.DeleteAutomationByID(ctx, "daily"); err != nil || removed {
		t.Fatalf("重复删除应幂等（removed=false）: removed=%v err=%v", removed, err)
	}

	// 审计：create → update（改 name）→ update（改 state）→ delete，逐条带 id。
	revs, err := st.ListCapabilityRevisions(ctx, model.CapabilityKindAutomation, "daily")
	if err != nil {
		t.Fatal(err)
	}
	actions := make([]string, 0, len(revs))
	for _, r := range revs {
		actions = append(actions, r.Action)
	}
	if strings.Join(actions, ",") != "create,update,update,delete" {
		t.Fatalf("审计动作序列不符: %v", actions)
	}
	// 被拒的写与重复新建/覆盖不存在的尝试都不得留下审计。
	for _, r := range revs {
		if r.Actor != definitionWriteActor {
			t.Fatalf("审计 actor 不符: %+v", r)
		}
	}
}

// TestAutomationStoreLoaderToleranceAndRuntime 装载：行 → 内存（含运行态列、坏定义留列表带 loadErr）。
func TestAutomationStoreLoaderToleranceAndRuntime(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, "")
	good := dbDailyPatch(t, "good", "9")
	body, _ := json.Marshal(good)
	fired := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	mustSeedAutomationRow(t, st, &store.AutomationDefinition{
		ID: "good", Body: string(body),
		LastFiredAt: fired.Format(time.RFC3339Nano), LastStatus: automation.RunCompleted,
		RecentRuns: `[{"scheduled_for":"2026-10-09T01:00:00Z","fired_at":"2026-10-09T01:00:01Z","status":"completed","trigger":"scheduled"}]`,
	})
	// 坏定义：schedule 缺时区（Validate 会拒）——留列表带 loadErr，而不是从列表里消失。
	mustSeedAutomationRow(t, st, &store.AutomationDefinition{
		ID: "broken", Body: `{"id":"broken","name":"坏","schedule":{"kind":"daily","hour":9,"minute":0},"target":{"agent_id":"a1","cwd":"/tmp","prompt":"x"}}`,
	})
	// 行正文不是 JSON 对象（手工塞进去的）→ 同样如实留列表。
	mustSeedAutomationRow(t, st, &store.AutomationDefinition{ID: "junk", Body: `[1,2,3]`})

	// 返回值是**装载成功**的条数（与文件装载同一口径）；坏的两行仍进注册表，靠 loadErr 标识。
	if n := svc.LoadAutomationDefinitionsFromStore(ctx, t.TempDir()); n != 1 {
		t.Fatalf("只有 good 装载成功，got %d", n)
	}
	items := svc.mustListAutomations(t)
	if len(items) != 3 {
		t.Fatalf("列表应有三条（含坏定义）: %+v", items)
	}
	byID := map[string]bool{}
	for _, it := range items {
		byID[it.ID] = it.Error != ""
	}
	if byID["good"] != false || byID["broken"] != true || byID["junk"] != true {
		t.Fatalf("坏定义应留列表且带 error: %+v", items)
	}
	// 运行态列 → 视图；`state` 从正文读（默认 active）。
	goodResp, ok := svc.AutomationByID("good")
	if !ok {
		t.Fatal("good 应在内存里")
	}
	if goodResp.LastStatus != automation.RunCompleted || len(goodResp.RecentRuns) != 1 {
		t.Fatalf("运行态列未进视图: %+v", goodResp)
	}
	if goodResp.LastFiredAt == "" || goodResp.State != automation.StateActive {
		t.Fatalf("认领游标/state 不符: %+v", goodResp)
	}
	// 坏行的运行史读不出来是**定义不可执行**（40001，而不是"没有运行史"）。
	if _, err := svc.ListAutomationRuns("broken"); errCode(err) != "definition_invalid" {
		t.Fatalf("坏定义的运行史应报 definition_invalid，got %v", err)
	}
}

// TestAutomationRuntimeWriteKeepsDefinitionRevision 运行态写回的 revision 语义：
//
//   - 只改运行态列 → **不抬高** revision（与文件载体的 mtime 语义有意不同：调度器跑一轮不该让
//     用户保存时撞 40901）；
//   - `state`（定义键，刻意不在拒写名单里）走正文 patch → 值真的变了才前进一版。
func TestAutomationRuntimeWriteKeepsDefinitionRevision(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, t.TempDir())
	if _, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true); err != nil {
		t.Fatal(err)
	}
	before, _, _ := st.GetAutomationDefinition(ctx, "daily")
	rec, _ := svc.automationRecByID("daily")
	fired := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	// 第一次：同时推进游标 + 补一个 `state`（定义键）→ 正文真的变了，前进一版。
	if err := svc.saveAutomationOverlay(ctx, "daily", map[string]any{
		"last_fired_at": fired.Format(time.RFC3339Nano),
		"last_status":   automation.RunCompleted,
		"state":         automation.StateInactive,
	}, automationLastFiredText(rec.def.LastFiredAt)); err != nil {
		t.Fatalf("运行态写回: %v", err)
	}
	after, _, _ := st.GetAutomationDefinition(ctx, "daily")
	if after.Revision != before.Revision+1 {
		t.Fatalf("state 是定义键：值变了就该前进一版（%d → %d）", before.Revision, after.Revision)
	}
	if after.LastStatus != automation.RunCompleted || after.LastFiredAt != fired.Format(time.RFC3339Nano) {
		t.Fatalf("运行态列未落库: %+v", after)
	}
	if !strings.Contains(after.Body, automation.StateInactive) {
		t.Fatalf("state 应写进定义正文: %s", after.Body)
	}
	// 第二次：**只**改运行态列（`state` 已是那个值，正文逐字节不变）→ revision 不动。
	before = after
	rec, _ = svc.automationRecByID("daily")
	if err := svc.saveAutomationOverlay(ctx, "daily", map[string]any{
		"last_status": automation.RunFailed,
		"state":       automation.StateInactive,
	}, automationLastFiredText(rec.def.LastFiredAt)); err != nil {
		t.Fatalf("运行态写回（第二次）: %v", err)
	}
	after, _, _ = st.GetAutomationDefinition(ctx, "daily")
	if after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("纯运行态写回不该抬高定义 revision: before=%+v after=%+v", before, after)
	}
	if after.LastStatus != automation.RunFailed {
		t.Fatalf("运行态列未更新: %+v", after)
	}
	// 认领 CAS：拿**过期**的认领值再写一次必须被拒（0 行），且一个字节都不改。
	if err := svc.saveAutomationOverlay(ctx, "daily", map[string]any{"last_status": automation.RunCompleted},
		"2020-01-01T00:00:00Z"); errCode(err) != "definition_changed" {
		t.Fatalf("过期的运行态写回应被拒（40901），got %v", err)
	}
	still, _, _ := st.GetAutomationDefinition(ctx, "daily")
	if still.LastStatus != automation.RunFailed {
		t.Fatalf("被拒的写不该落任何列: %+v", still)
	}
	// 内存与载体一致（写回后重载）。
	resp, _ := svc.AutomationByID("daily")
	if resp.State != automation.StateInactive || resp.LastStatus != automation.RunFailed {
		t.Fatalf("内存态未跟上载体: %+v", resp)
	}
}

// TestAutomationClaimIsAtMostOnce 预认领是"同一 occurrence 至多触发一次"的仲裁者：
// 拿**过期快照**再走一次同一条路径（模拟另一个 tick / 另一个进程并发），必须判 duplicate 且不建会话。
func TestAutomationClaimIsAtMostOnce(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, t.TempDir())
	if _, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true); err != nil {
		t.Fatal(err)
	}
	rec, _ := svc.automationRecByID("daily")
	occurrence := rec.nextDue
	stale := rec // 第二个触发者手里的旧快照

	first := svc.automationRunOnce(ctx, rec, occurrence, automation.TriggerScheduled)
	if first.status != automation.RunClaimed {
		t.Fatalf("第一次应认领成功: %+v", first)
	}
	second := svc.automationRunOnce(ctx, stale, occurrence, automation.TriggerScheduled)
	if second.status != automation.RunDuplicate || !second.noRecord {
		t.Fatalf("同一 occurrence 的第二次触发必须判 duplicate: %+v", second)
	}
	if convs := automationConversations(t, svc, "daily"); len(convs) != 1 {
		t.Fatalf("应恰好 1 个会话（认领挡住了第二次），got %d", len(convs))
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "daily")
	if row.LastFiredAt != occurrence.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("认领游标应落在本次 occurrence: %q vs %q", row.LastFiredAt, occurrence.UTC())
	}
	// 首次推进往正文补了一个 `state`（定义键）→ 前进一版；此后不再变。
	if row.Revision != 2 {
		t.Fatalf("首次推进只该前进一版（补 state），got %d", row.Revision)
	}
	if strings.Contains(row.Body, "last_fired_at") {
		t.Fatalf("认领游标不得进定义正文: %s", row.Body)
	}
}

// TestAutomationClaimFailureRetriesNextTick（P-12 专项）认领失败 → **本轮不推进内存游标**、
// 不建会话；修好之后下一次触发仍能补上这一次 occurrence（不是"跳过就永远跳过"）。
//
// 注入方式是**已取消的 ctx**：认领那一条 UPDATE 会直接失败——这比"关库"更贴近平常的失败形态
// （超时/取消），而且不影响随后用活 ctx 重试。
func TestAutomationClaimFailureRetriesNextTick(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, t.TempDir())
	if _, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true); err != nil {
		t.Fatal(err)
	}
	rec, _ := svc.automationRecByID("daily")
	occurrence := rec.nextDue

	dead, cancel := context.WithCancel(ctx)
	cancel()
	out := svc.automationRunOnce(dead, rec, occurrence, automation.TriggerScheduled)
	if !out.failure || out.status != automation.RunSkipped || !strings.Contains(out.reason, "认领失败") {
		t.Fatalf("认领失败的结果不符: %+v", out)
	}
	if convs := automationConversations(t, svc, "daily"); len(convs) != 0 {
		t.Fatalf("认领失败不得建会话，got %d", len(convs))
	}
	row, _, _ := st.GetAutomationDefinition(ctx, "daily")
	if row.LastFiredAt != "" {
		t.Fatalf("认领失败不得推进库里的游标: %q", row.LastFiredAt)
	}
	mem, _ := svc.automationRecByID("daily")
	if mem.def.LastFiredAt != nil {
		t.Fatalf("认领失败不得推进内存游标: %+v", mem.def.LastFiredAt)
	}
	// 内存 nextDue 保持原样 → 下一次触发仍然认定"这个 occurrence 到期"。
	if !mem.nextDue.Equal(occurrence) {
		t.Fatalf("认领失败后 nextDue 不应变化: %s vs %s", mem.nextDue, occurrence)
	}

	retry := svc.automationRunOnce(ctx, mem, occurrence, automation.TriggerScheduled)
	if retry.status != automation.RunClaimed {
		t.Fatalf("修好后的重试应认领成功: %+v", retry)
	}
	if convs := automationConversations(t, svc, "daily"); len(convs) != 1 {
		t.Fatalf("重试应恰好建 1 个会话，got %d", len(convs))
	}
}

// TestAutomationRecordFailureKeepsCursor（P-12 专项）**运行态事务失败 → 内存游标不推进**。
//
// 与文件载体相反（那里写失败照样推进内存态）：DB 载体的推进就是那一个事务，事务没提交，
// 内存就不该出现"已经跑过了"的结论——否则下一次触发会误判 duplicate，把这一次永久吞掉。
func TestAutomationRecordFailureKeepsCursor(t *testing.T) {
	ctx := context.Background()
	svc, st := automationDBRig(t, t.TempDir())
	if _, err := svc.writeAutomation(ctx, "daily", dbDailyPatch(t, "daily", "9"), true); err != nil {
		t.Fatal(err)
	}
	rec, _ := svc.automationRecByID("daily")
	occurrence := rec.nextDue
	nextDueBefore := rec.nextDue

	// 关掉库 = 事务一定失败（比"取消 ctx"更彻底：连重载也做不了）。
	if err := st.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}
	out := svc.finishAutomationRun(ctx, rec, automationRunOutcome{
		scheduledFor: occurrence, status: automation.RunOverlap, trigger: automation.TriggerScheduled,
		advance: true, reason: "上一次还在跑；下一次 occurrence 照常",
	})
	if out.state != automation.StateActive {
		t.Fatalf("结果状态应由调用方推导: %+v", out)
	}
	mem, _ := svc.automationRecByID("daily")
	if mem.def.LastFiredAt != nil {
		t.Fatalf("事务失败不得推进内存游标: %+v", mem.def.LastFiredAt)
	}
	if !mem.nextDue.Equal(nextDueBefore) {
		t.Fatalf("事务失败不得重算 nextDue: %s vs %s", mem.nextDue, nextDueBefore)
	}
}
