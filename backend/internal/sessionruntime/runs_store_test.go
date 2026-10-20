package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/run"
	"regexp"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"strings"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/testutil"
)

// runIDShape 轮次身份的形状：`run_` + 26 位 Crockford Base32（v22 起重铸，不再是由
// (session_id, first_seq) 拼出来的派生串）。
var runIDShape = regexp.MustCompile(`^run_[0-9A-HJKMNP-TV-Z]{26}$`)

// TestRunMaterializationAtRunEnd 验证 v22 的 runs 物化不变量：
//   - 开轮（`run.started`）即落行：status=running、first_seq = 该事件位次、last_seq=-1、ended_at 空；
//   - 终态事件只**收口同一行**（id 不变）：status/stop_reason/error_code 对齐，last_seq = 终态位次；
//   - 窗口起点是本轮 `run.started`（不是"上一轮末尾"），多轮窗口不重叠、起点时刻是事件时刻；
//   - 无 store 时 ListRuns 返回空（读模型仅 sqlite 模式存在）。
func TestRunMaterializationAtRunEnd(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-run"})

	// 第一轮：成功。
	r1, s1, u1 := beginRun(t, svc, "s-run", "第一问")
	if s1.Seq >= u1.Seq {
		t.Fatalf("用户消息必须落在 run.started 之后: started=%d user=%d", s1.Seq, u1.Seq)
	}
	// 开轮即落行：此刻这一轮**在库里**（v22 之前要等终态才有行）。
	if rows, err := st.ListRuns(ctx, "s-run"); err != nil || len(rows) != 1 {
		t.Fatalf("开轮后应 1 条 run: n=%d err=%v", len(rows), err)
	} else if rows[0].Status != session.SessionStatusInProgress || rows[0].LastSeq != -1 || !rows[0].EndedAt.IsZero() {
		t.Fatalf("进行中的行应 status=running / last_seq=-1 / ended_at 空: %+v", rows[0])
	}
	svc.sessionPlane.Publish(session.BusMessage, "s-run", map[string]any{"text": "回答一"})
	end1 := endRun(svc, "s-run", r1, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	// 第二轮：失败。
	r2, s2, _ := beginRun(t, svc, "s-run", "第二问（会失败）")
	errEnd := endRun(svc, "s-run", r2, session.BusRunError, map[string]any{"code": "agent_error", "class": "agent"})

	runs, err := st.ListRuns(ctx, "s-run")
	if err != nil || len(runs) != 2 {
		t.Fatalf("应 2 条 run: n=%d err=%v %+v", len(runs), err, runs)
	}
	r0, r1row := runs[0], runs[1]
	if r0.ID != r1 {
		t.Fatalf("终态应收口开轮那一行（id 不变）: got %q want %q", r0.ID, r1)
	}
	if !runIDShape.MatchString(r0.ID) {
		t.Fatalf("run id 应为 %s 形状: %q", runIDShape, r0.ID)
	}
	if r0.Status != session.SessionStatusCompleted || r0.UserText != "第一问" || r0.StopReason != "end_turn" {
		t.Fatalf("run0 不符: %+v", r0)
	}
	if r0.FirstSeq != s1.Seq || r0.LastSeq != end1.Seq {
		t.Fatalf("run0 窗口应为「run.started → 终态」: %+v (s1=%d end1=%d)", r0, s1.Seq, end1.Seq)
	}
	if r1row.ID != r2 || r1row.Status != session.SessionStatusFailed || r1row.UserText != "第二问（会失败）" || r1row.ErrorCode != "agent_error" {
		t.Fatalf("run1 不符: %+v", r1row)
	}
	if r1row.FirstSeq != s2.Seq || r1row.LastSeq != errEnd.Seq {
		t.Fatalf("run1 窗口不符: %+v (s2=%d err=%d)", r1row, s2.Seq, errEnd.Seq)
	}
	if r1row.FirstTokenMS != 0 {
		t.Fatalf("run1 无 assistant 输出 first_token_ms 应为 0: %+v", r1row)
	}
	// 两轮窗口不重叠（起点由各自的 run.started 给出，不靠"上一轮末尾"推导）。
	if r1row.FirstSeq <= r0.LastSeq {
		t.Fatalf("两轮窗口重叠: run0=[%d,%d] run1=[%d,%d]", r0.FirstSeq, r0.LastSeq, r1row.FirstSeq, r1row.LastSeq)
	}

	// Service 读面。
	items, err := sessionPlaneOf(svc).ListRuns(ctx, "s-run")
	if err != nil || len(items) != 2 {
		t.Fatalf("ListRuns: n=%d err=%v", len(items), err)
	}
	if items[1].ErrorCode != "agent_error" || items[0].Status != session.SessionStatusCompleted {
		t.Fatalf("ListRuns 内容不符: %+v", items)
	}

	// 内存模式（无 store）→ 空读模型。
	memSvc := newServiceForTest(reg, nil)
	if empty, err := sessionPlaneOf(memSvc).ListRuns(ctx, "s-run"); err != nil || len(empty) != 0 {
		t.Fatalf("内存模式 ListRuns 应为空: %+v err=%v", empty, err)
	}
}

// TestRunWindowStartsAtRunStarted 轮次窗口从**本轮 `run.started`** 起算。
//
// v22 之前这条规则是"裁到首条用户消息"（2026-10-14 用户报「只问一个问题，messages 里却有三条」的
// 止血），而裁剪规则与身份构造耦合（`r-<session>-<first_seq>`）：规则一改，历史轮次 id 就跟着变。
// 现在起点由**事件自己**给出（`run.started` 先于用户消息落库），窗口不需要任何裁剪，会话级事件
// （session.created / session.resumed）天然落在窗口之外。
func TestRunWindowStartsAtRunStarted(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-win"})

	// 会话前置事件（真实路径由 session_start / ensureSession 发出，先于本轮 run.started）。
	svc.sessionPlane.Publish(session.BusSessionCreated, "s-win", map[string]any{"session_id": "s-win", "runtime": "stub"})
	svc.sessionPlane.Publish(session.BusSessionResumed, "s-win", map[string]any{"session_id": "s-win", "reason": "restart"})
	runID, started, u := beginRun(t, svc, "s-win", "你好")
	svc.sessionPlane.Publish(session.BusMessage, "s-win", map[string]any{"msg_id": "m1", "text": "你好呀"})
	end := endRun(svc, "s-win", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	runs, err := st.ListRuns(ctx, "s-win")
	if err != nil || len(runs) != 1 {
		t.Fatalf("应 1 条 run: n=%d err=%v", len(runs), err)
	}
	r := runs[0]
	if r.FirstSeq != started.Seq || r.LastSeq != end.Seq {
		t.Fatalf("run 窗口应为「run.started → 终态」: %+v (started=%d end=%d)", r, started.Seq, end.Seq)
	}
	// 身份与事件载荷同源（同一支 `run_<ULID>`），且与窗口起点**无关**——这正是重铸要买到的东西。
	if r.ID != runID {
		t.Fatalf("runs.id 应与 run.started 载荷同源: got %q want %q", r.ID, runID)
	}
	if u.Seq <= started.Seq {
		t.Fatalf("用户消息应在 run.started 之后: started=%d user=%d", started.Seq, u.Seq)
	}
	// 轮起点 = `run.started` 的**落库时刻**（不是 time.Now()，也不是用户消息时刻）。
	evs, _, err := st.EventsAfter(ctx, "s-win", -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	var startedEv model.EventEntity
	found := false
	for _, e := range evs {
		if e.Seq == started.Seq {
			startedEv, found = e, true
		}
	}
	// 用 found 而不是 `startedEv.Seq == 0` 判"找到了"：v21 起 0 是合法位次，拿零值当"没找到"
	// 会在起点恰好位于位次 0 时静默通过（那时 startedEv 是零值，断言却看起来成立）。
	if !found || !r.StartedAt.Equal(startedEv.CreatedAt) {
		t.Fatalf("run 起点应为 run.started 事件时刻 %s，got %s", startedEv.CreatedAt, r.StartedAt)
	}
	if r.Status != session.SessionStatusCompleted || r.UserText != "你好" {
		t.Fatalf("run 内容不符: %+v", r)
	}
	rows, err := st.ListMessages(ctx, "s-win")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Role != "user" || rows[1].Role != "assistant" {
		t.Fatalf("messages 应只有 user + assistant 两行（run.started 不是消息内容，不产空行）: %+v", rows)
	}
	if rows[1].Text != "你好呀" {
		t.Fatalf("assistant 行正文不符: %q", rows[1].Text)
	}
}

// TestOpenRunIsIdempotent 开轮落行对**同一轮**幂等：`(session_id, first_seq)` 是 DB 唯一键，
// 重放一次 `run.started`（同起点、可能已重铸过 id）不会插出第二行。
//
// 这是"每会话每起点一条"从代码约定升格为 DB 不变式的判据——v22 之前幂等靠"用窗口算出来的 id
// 当主键"，而那个 id 会随窗口规则变化（那时同一个终态事件能插出两条行）。
func TestOpenRunIsIdempotent(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-idem"})

	// 两条真实的 `run.started`（开轮行的起点**必须**是库里真有的那条事件：位次或 type 对不上
	// 时 `openRun` 会跳过——给不存在的起点插行等于让终态去收口别的事件）。
	startedA := svc.sessionPlane.Publish(session.BusRunStarted, "s-idem", map[string]any{"session_id": "s-idem", "text": "a"})
	startedB := svc.sessionPlane.Publish(session.BusRunStarted, "s-idem", map[string]any{"session_id": "s-idem", "text": "b"})

	// 同一轮的起点重放两次，用的是**不同 id**（模拟"重铸后再重放"）。
	svc.sessionPlane.OpenRun("s-idem", objid.New(objid.Run), "重复投递的输入", startedA.Seq)
	svc.sessionPlane.OpenRun("s-idem", objid.New(objid.Run), "重复投递的输入", startedA.Seq)
	// 同一支 id 换个起点再来一次（模拟同一事件重放）。
	dup := objid.New(objid.Run)
	svc.sessionPlane.OpenRun("s-idem", dup, "重复投递的输入", startedB.Seq)
	svc.sessionPlane.OpenRun("s-idem", dup, "重复投递的输入", startedB.Seq)

	rows, err := st.ListRuns(ctx, "s-idem")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("同 (session_id, first_seq) 与同 id 的重放都不该插新行: %+v", rows)
	}
	for _, r := range rows {
		if r.Status != session.SessionStatusInProgress || r.LastSeq != -1 || r.FirstSeq < 0 {
			t.Fatalf("开轮行形状不符: %+v", r)
		}
	}
	// 起点不存在的开轮请求被拒（WARN + 不落行）——这是"开轮行必须对应真事件"的判据。
	svc.sessionPlane.OpenRun("s-idem", objid.New(objid.Run), "起点不存在", 4242)
	if rows, err := st.ListRuns(ctx, "s-idem"); err != nil || len(rows) != 2 {
		t.Fatalf("起点不存在时不该落行: %+v err=%v", rows, err)
	}
}

// TestRunningRunSurvivesRestart 进程消失时**进行中的轮留在库里**：重启后它仍是 running、
// last_seq 仍是 -1、ended_at 仍为空。
//
// 这不是缺陷而是事实：那一轮既没成功也没失败，伪造一个终态会污染失败轮列表与「重试该轮」入口
// （会话侧的重启残留另有 `interrupted` 状态表达）。同时它验证 running 行**能**活过重启：
// 重建通道只读 events，running 行不参与重建，所以它必须自己活下来。
func TestRunningRunSurvivesRestart(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()

	st1 := testutil.OpenStore(t, dbPath)
	svc1 := newServiceForTest(reg, eventbus.New())
	enableStore(svc1, st1)
	svc1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-crash"})
	runID, started, _ := beginRun(t, svc1, "s-crash", "这一轮会中断")
	svc1.sessionPlane.Publish(session.BusMessage, "s-crash", map[string]any{"text": "答到一半"})
	svc1.sessionPlane.FlushChunks() // 增量先落盘（否则丢失的不是本用例要验的东西）
	_ = st1.Close()                 // 进程没了：没有终态事件

	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	rows, err := st2.ListRuns(ctx, "s-crash")
	if err != nil || len(rows) != 1 {
		t.Fatalf("重启后应仍有 1 条 run: n=%d err=%v", len(rows), err)
	}
	r := rows[0]
	if r.ID != runID || r.Status != session.SessionStatusInProgress || r.LastSeq != -1 || !r.EndedAt.IsZero() {
		t.Fatalf("中断的轮应原样留在库里: %+v", r)
	}
	if r.FirstSeq != started.Seq || r.UserText != "这一轮会中断" {
		t.Fatalf("开轮行内容不符: %+v", r)
	}
	// 读模型可以被后续收口（接管它的那一轮重新开一行，起点不同 → 不冲突）。
	svc2 := newServiceForTest(reg, eventbus.New())
	enableStore(svc2, st2)
	if _, _, err := svc2.SessionPlane().Restore(ctx); err != nil {
		t.Fatalf("RestoreFromStore: %v", err)
	}
	if rows, err := st2.ListRuns(ctx, "s-crash"); err != nil || len(rows) != 1 || rows[0].Status != session.SessionStatusInProgress {
		t.Fatalf("恢复不应改写 running 行: %+v err=%v", rows, err)
	}
}

// TestRunUsageExposedInListRuns runs.usage_json 在 ListRuns 读面被解析为 RunResp.Usage：
// 有上报 → 结构体；空串/无上报 → Usage 为 nil（omitempty）。
//
// 这里直接种**终态**行（不经开轮/收口）：本用例验的是读面解析，行怎么来的与它无关；
// 唯一要守住的是 `(session_id, first_seq)` 唯一键——三条种子行的起点必须各不相同。
func TestRunUsageExposedInListRuns(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-usage"})

	now := time.Now().UTC()
	seed := func(status, usageJSON string, firstTokenMS int64, firstSeq int64) {
		t.Helper()
		if err := st.CreateRun(ctx, run.Item{
			ID: objid.New(objid.Run), SessionID: "s-usage", Status: status, UsageJSON: usageJSON,
			FirstTokenMS: firstTokenMS, FirstSeq: firstSeq, LastSeq: firstSeq + 2,
			StartedAt: now.Add(-time.Minute), EndedAt: now,
		}); err != nil {
			t.Fatalf("CreateRun(%s): %v", status, err)
		}
	}
	seed("completed", `{"used":135,"size":128000}`, 714, 0)
	seed("failed", `{"used":150,"size":128000,"cost_amount":0.5,"cost_currency":"USD"}`, 0, 10)
	seed("cancelled", ``, 0, 20)

	items, err := sessionPlaneOf(svc).ListRuns(ctx, "s-usage")
	if err != nil || len(items) != 3 {
		t.Fatalf("ListRuns: n=%d err=%v", len(items), err)
	}
	if items[0].Usage == nil || items[0].Usage.Used != 135 || items[0].Usage.Size != 128000 {
		t.Fatalf("run1 usage 不符: %+v", items[0].Usage)
	}
	if items[0].FirstTokenMS != 714 {
		t.Fatalf("run1 first_token_ms=%d want 714", items[0].FirstTokenMS)
	}
	if items[1].Usage == nil || items[1].Usage.CostAmount != 0.5 || items[1].Usage.CostCurrency != "USD" {
		t.Fatalf("run2 usage 不符: %+v", items[1].Usage)
	}
	if items[2].Usage != nil {
		t.Fatalf("run3 无上报应为 nil Usage: %+v", items[2].Usage)
	}
}

// TestRunUsageLandsInRunRowThroughTheSessionPlane 用量快照必须**经会话域写进 runs 行**。
//
// 为什么单开一条：`MaterializeRun` 收口时写的 `usage_json` 来自**执行面内存**（编排器的
// `LatestUsage`），而 P3b-1a 把这条取值改成了经端口交下去（`Plane.SetUsageSource`，在
// `attachOrchestrator` 里接线）。**接线漏了的表现是静默的**：用量那一列永远是空串，界面上
// 只是"没有用量"，没有任何东西会红——所以这条判据钉的就是那根线。
func TestRunUsageLandsInRunRowThroughTheSessionPlane(t *testing.T) {
	ctx := context.Background()
	reg := testutil.StubRegistry()
	svc := newServiceForTest(reg, eventbus.New())
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	defer st.Close()
	enableStore(svc, st)
	// 造编排器 = 走真实接线（`NewOrchestrator` 末尾回挂，端口在那里接上）。
	orch := newOrchestratorForTest(svc, reg)
	defer orch.Close()
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-usage-wire"})

	runID, _, _ := beginRun(t, svc, "s-usage-wire", "问一句")
	// 执行面记一次用量上报（真实路径由 event_sink 在收到 usage 事件时调用）。
	orch.runner.RecordUsage("s-usage-wire", objusage.UsageInfo{Used: 321, Size: 128000})
	endRun(svc, "s-usage-wire", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})

	rows, err := st.ListRuns(ctx, "s-usage-wire")
	if err != nil || len(rows) != 1 {
		t.Fatalf("应有 1 条 run: n=%d err=%v", len(rows), err)
	}
	if rows[0].UsageJSON == "" {
		t.Fatal("用量快照没写进 runs.usage_json——会话域的用量端口没接上？" +
			"（`attachOrchestrator` 里的 `SetUsageSource` 是唯一接线点）")
	}
	if !strings.Contains(rows[0].UsageJSON, "321") {
		t.Fatalf("usage_json 应带上报的数值: %q", rows[0].UsageJSON)
	}
}

// TestRebuildRepairsMissingRunRow 重建通道必须能把**开轮落行失败**留下的洞补上（J7）。
//
// 为什么这条必须有判据：「runs 是纯投影」这句话有两种失效方式，都不会让别的测试变红——
//   - 洞补不上：那一轮从轮次明细里**永久消失**（终态找不到开轮行就跳过），而 events 里它还在；
//   - 补成"永远在跑"的幽灵行：补行时只看 `run.started`、不看日志里已有的终态，于是已经结束的轮
//     在界面上显示成进行中，用量统计也把它算进 running（比消失更糟：它主动说了一件假事）。
//
// 夹具做法：正常跑完一轮，然后把 runs 行删掉（等价于 `openRun` 那次写库失败），重建后要求
// 行**完整回来**：身份、起点、user_text 来自 `run.started`；status/stop_reason/last_seq/ended_at
// 来自日志里的终态事件；窗口事件重铺回 messages。
func TestRebuildRepairsMissingRunRow(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()
	st := testutil.OpenStore(t, dbPath)
	defer st.Close()
	svc := newServiceForTest(reg, eventbus.New())
	enableStore(svc, st)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s-repair"})

	runID, started, _ := beginRun(t, svc, "s-repair", "重建这一轮")
	svc.sessionPlane.Publish(session.BusMessage, "s-repair", map[string]any{"text": "答一"})
	end := endRun(svc, "s-repair", runID, session.BusRunEnded, map[string]any{"stop_reason": "end_turn"})
	if rows, err := st.ListRuns(ctx, "s-repair"); err != nil || len(rows) != 1 {
		t.Fatalf("夹具应已物化 1 条 run: %+v err=%v", rows, err)
	}

	// 抹掉读模型：run 行没了、messages 也没了（adapter 全丢，只有 events 还在）。
	if err := st.DB().Exec(`DELETE FROM runs WHERE id = ?`, runID).Error; err != nil {
		t.Fatalf("伪造缺失的 run 行: %v", err)
	}
	if err := st.DB().Exec(`DELETE FROM messages WHERE session_id = ?`, "s-repair").Error; err != nil {
		t.Fatalf("清 messages: %v", err)
	}

	if err := svc.sessionPlane.RebuildSessionReadModels(ctx, "s-repair"); err != nil {
		t.Fatalf("RebuildSessionReadModels: %v", err)
	}

	rows, err := st.ListRuns(ctx, "s-repair")
	if err != nil || len(rows) != 1 {
		t.Fatalf("重建应把那一轮补回来: n=%d err=%v", len(rows), err)
	}
	r := rows[0]
	if r.ID != runID || r.FirstSeq != started.Seq || r.UserText != "重建这一轮" {
		t.Fatalf("补出来的行身份/起点/输入不符（应全部取自 run.started）: %+v", r)
	}
	if r.Status != session.SessionStatusCompleted || r.StopReason != "end_turn" || r.LastSeq != end.Seq || r.EndedAt.IsZero() {
		t.Fatalf("补出来的行必须用日志里的终态收口（不是留在 running 的幽灵行）: %+v", r)
	}
	msgs, err := st.ListMessages(ctx, "s-repair")
	if err != nil || len(msgs) == 0 || msgs[0].Role != "user" || msgs[0].Text != "重建这一轮" {
		t.Fatalf("消息也该重铺回来: %+v err=%v", msgs, err)
	}
}
