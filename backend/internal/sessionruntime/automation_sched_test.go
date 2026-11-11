package sessionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/core/automation"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	automationcore "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/runtime"
)

// silentACPSpec 一个**真子进程**的最小 ACP responder：握手与 session/new 正常应答，
// `session/prompt` **故意不回**（挂住）。
//
// 为什么需要它：automation 的 overlap 判据是"上一次触发的会话仍在 in_progress"，
// 而 `testutil.StubRegistry()` 的 spec 没有 Launch（拨号即 spawn_failed），
// `stubACPSpec` 又会把 prompt 回成 method not found（轮次立刻失败收尾）。
// 只有"prompt 永不回应"的桩才能把会话稳定地留在 in_progress 上。
func silentACPSpec(t *testing.T) runtime.Spec {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "acp_silent.py")
	code := `import json, sys
n = 0
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    method, mid = msg.get("method"), msg.get("id")
    if mid is None:
        continue
    if method == "initialize":
        res = {"protocolVersion": 1, "agentCapabilities": {}}
    elif method == "session/new":
        n += 1
        res = {"sessionId": "silent-%d" % n}
    elif method == "session/prompt":
        continue  # 故意挂住：不回应 → 会话停在 in_progress
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid,
            "error": {"code": -32601, "message": "method not found: %s" % method}}) + "\n")
        sys.stdout.flush()
        continue
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": res}) + "\n")
    sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(code), 0o600); err != nil {
		t.Fatalf("写 ACP 桩脚本失败: %v", err)
	}
	return runtime.Spec{ID: "silent", Name: "Silent",
		Launch:          runtime.LaunchSpec{Command: "python3", Args: []string{script}},
		CapabilityScope: runtime.CapabilityScopeProcess}
}

// schedRig automation 调度夹具：真 runtime 桩 + 注入 agent a1 + 独立 automation 目录。
type schedRig struct {
	svc *Service
	bus *eventbus.Bus
	dir string
	cwd string
}

func newSchedRig(t *testing.T) *schedRig {
	t.Helper()
	spec := silentACPSpec(t)
	reg := runtime.NewRegistry(spec)
	bus := eventbus.New()
	svc := NewService(reg, bus)
	setBuildRuntime(t, "silent") // 夹具注册表只有 silent
	orch := NewOrchestrator(svc, reg)
	orch.SetSettleTimeout(50 * time.Millisecond) // 取消用例不按生产 10s 等
	t.Cleanup(orch.Close)
	dir := t.TempDir()
	cwd := t.TempDir()
	if n := svc.Planes().Automation.LoadAutomationDefinitions(dir); n != 0 {
		t.Fatalf("空目录应装载 0 条，got %d", n)
	}
	return &schedRig{svc: svc, bus: bus, dir: dir, cwd: cwd}
}

// writeDaily 写一条 daily 定义（cwd 固定为本夹具的临时目录）。
func (r *schedRig) writeDaily(t *testing.T, id, lastFired string) {
	t.Helper()
	body := map[string]any{
		"id": id, "name": "每日巡检", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0},
		"target":   map[string]any{"cwd": r.cwd, "prompt": "跑一遍依赖巡检", "delivery": "new_session"},
	}
	if lastFired != "" {
		body["last_fired_at"] = lastFired
	}
	raw, _ := json.Marshal(body)
	writeAutomationFile(t, filepath.Join(r.dir, id+".json"), string(raw))
	if n := r.svc.Planes().Automation.LoadAutomationDefinitions(r.dir); n != 1 {
		t.Fatalf("期望装载 1 条，got %d", n)
	}
}

func (r *schedRig) file(t *testing.T, id string) map[string]any {
	t.Helper()
	return readDef(t, filepath.Join(r.dir, id+".json"))
}

// sessionsOf 按 origin 收集会话（"某 automation 历次生效运行"的快速路径）。
func (r *schedRig) sessionsOf(t *testing.T, id string) []*session.SessionRec {
	t.Helper()
	// **不持 `s.mu`**：`Snapshot` 自己取会话域的锁（与 `manager_lock_order_test.go` 的纪律同源）。
	snap := r.svc.sessionPlane.Snapshot()
	var out []*session.SessionRec
	for i := range snap {
		if snap[i].Origin == automationcore.Origin(id) {
			out = append(out, &snap[i])
		}
	}
	return out
}

// TestAutomationTickFiresAndAdvances 到期触发：建会话 + 推进游标 + 重算 next_due + 事件载荷。
//
// 这是 Gate M4 的第一条：**一次触发恰好产生一个会话**，且第二次 tick 不会再来一次。
func TestAutomationTickFiresAndAdvances(t *testing.T) {
	r := newSchedRig(t)
	// 上一次触发是 2020 年 → 现在是"停机错过的补跑"（recover）
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	rec, _ := r.svc.Planes().Automation.RecByID("daily")
	if rec.Startup != automation.StartupRecover {
		t.Fatalf("期望 recover，got %s", rec.Startup)
	}
	sub := r.bus.SubscribeAfter(nil) // 回放 + live：断言的是本轮触发新增了哪些事件
	defer r.bus.Unsubscribe(sub)

	now := time.Now()
	r.svc.Planes().Automation.Tick(context.Background(), now)

	convs := r.sessionsOf(t, "daily")
	if len(convs) != 1 {
		t.Fatalf("一次触发应恰好产生 1 个会话，got %d", len(convs))
	}
	// origin 是 automation:<id>（会话列表凭它标注"这是自动跑出来的"）
	if convs[0].Origin != "automation:daily" {
		t.Fatalf("origin 不符: %q", convs[0].Origin)
	}
	// 端口形态也要带上它：面板读的是 `GET /sessions` 的信封，不是内存记录。
	// 这里断言"标记真的走完了到客户端的那一段路"，否则面板只能靠时间去猜哪条是自动跑的。
	resp := r.svc.sessionPlane.ToSessionRespOf(convs[0])
	if resp.Origin != "automation:daily" {
		t.Fatalf("SessionResp 应回带 origin: %q", resp.Origin)
	}
	listed, err := sessionPlaneOf(r.svc).ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	marked := 0
	for _, c := range listed {
		if c.Origin == "automation:daily" {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("会话列表里应恰好 1 条带 automation 标，got %d（%+v）", marked, listed)
	}
	got := r.file(t, "daily")
	if got["last_status"] != automation.RunClaimed {
		t.Fatalf("last_status 应为 claimed: %v", got["last_status"])
	}
	// 认领游标 = 本次 occurrence（上海 09:00 = 01:00Z），不是"现在"
	fired, err := time.Parse(time.RFC3339Nano, got["last_fired_at"].(string))
	if err != nil {
		t.Fatalf("last_fired_at 解析: %v", err)
	}
	if h := fired.UTC().Hour(); h != 1 {
		t.Fatalf("last_fired_at 应为该 occurrence（01:00Z），got %s", fired.UTC())
	}
	if got["last_session_id"] != convs[0].ID {
		t.Fatalf("last_session_id 不符: %v vs %s", got["last_session_id"], convs[0].ID)
	}
	if n, _ := got["consecutive_failures"].(float64); n != 0 {
		t.Fatalf("成功触发应清零连续失败，got %v", got["consecutive_failures"])
	}
	runs, _ := got["recent_runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("应有 1 条运行留痕，got %d", len(runs))
	}
	first := runs[0].(map[string]any)
	if first["status"] != automation.RunClaimed || first["trigger"] != automation.TriggerRecovery ||
		first["session_id"] != convs[0].ID {
		t.Fatalf("留痕不符: %+v", first)
	}
	// next_due 推到下一个 occurrence（次日的 09:00 上海 = 次日 01:00Z）
	view, _ := r.svc.Planes().Automation.AutomationByID("daily")
	next := time.UnixMilli(view.NextDueAt)
	if d := next.Sub(fired); d != 24*time.Hour {
		t.Fatalf("next_due 应恰好推进一天，got %s（%s → %s）", d, fired, next)
	}
	// 第二次 tick：同一 occurrence 不再触发（**一次触发一个会话**）
	r.svc.Planes().Automation.Tick(context.Background(), now)
	if n := len(r.sessionsOf(t, "daily")); n != 1 {
		t.Fatalf("第二次 tick 不该再建会话，got %d", n)
	}
	if n := len(mustRuns(t, r.file(t, "daily"))); n != 1 {
		t.Fatalf("第二次 tick 不该追加留痕，got %d", n)
	}
	// 事件：automation.run 带 automation_id/status/trigger/session_id
	var ev eventbus.Event
	deadline := time.After(2 * time.Second)
	for ev.Type == "" {
		select {
		case e := <-sub.C:
			if e.Type == session.BusAutomationRun {
				ev = e
			}
		case <-deadline:
			t.Fatal("未收到 automation.run 事件")
		}
	}
	if ev.AggregID != automationcore.Origin("daily") {
		t.Fatalf("事件 agg 应为 automation:daily，got %q", ev.AggregID)
	}
	p := ev.Payload.(map[string]any)
	if p["automation_id"] != "daily" || p["status"] != automation.RunClaimed ||
		p["trigger"] != automation.TriggerRecovery || p["session_id"] != convs[0].ID {
		t.Fatalf("事件载荷不符: %+v", p)
	}
}

func mustRuns(t *testing.T, m map[string]any) []any {
	t.Helper()
	runs, _ := m["recent_runs"].([]any)
	return runs
}

// TestAutomationTickOverlapSkips 上一次会话还在跑 → 记 overlap 跳过、**不排队**（不建第二个会话）。
func TestAutomationTickOverlapSkips(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	// 固定时钟 + 钉死第一次触发的 occurrence（上海 09:00 = 01:00Z）。
	//
	// 为什么不能用 time.Now()：装载期的 next_due 由**真实墙钟**推出（LoadAutomationDefinitions →
	// applySchedule → Schedule.Startup），而下面要拨回的"下一次到期点"是 now-1h。若 now 距当日
	// occurrence 不足一小时（本地 09:00–10:00 这一小时），拨回值会落在 last_fired_at **之前**，
	// Decide 便判 duplicate（claim.go:29-34：duplicate 优先于 overlap），而 duplicate 不写盘
	// （noRecord，automation_sched.go:184-193）——文件里的 last_status 会停在第一次的 claimed，
	// 这条用例就假红。钉死游标同时让用例不再依赖"跑测试时真实日期是哪天"。
	now := time.Date(2026, 10, 9, 2, 30, 0, 0, time.UTC)
	r.svc.planes.Automation.Mutate("daily", func(rec *automationcore.Rec) {
		rec.NextDue = time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	})
	r.svc.Planes().Automation.Tick(context.Background(), now)
	if n := len(r.sessionsOf(t, "daily")); n != 1 {
		t.Fatalf("首次触发应建 1 个会话，got %d", n)
	}
	// 制造"下一次 occurrence 也到期了，但上一次会话仍在跑"：把内存游标拨回过去
	// （等价于调度器在会话结束前又扫到一个新的到期点）。
	// 该值**必须严格晚于 last_fired_at**（上一次认领 = 01:00Z）：这是真实调度必然满足的不变式
	// ——NextDue 只在 latest > lastFiredAt 时才给到期点（schedule.go:91-93）。
	past := now.Add(-time.Hour)
	r.svc.planes.Automation.Mutate("daily", func(rec *automationcore.Rec) { rec.NextDue = past })

	r.svc.Planes().Automation.Tick(context.Background(), now)

	if n := len(r.sessionsOf(t, "daily")); n != 1 {
		t.Fatalf("overlap 不该建第二个会话（不排队），got %d", n)
	}
	got := r.file(t, "daily")
	if got["last_status"] != automation.RunOverlap {
		t.Fatalf("last_status 应为 overlap，got %v", got["last_status"])
	}
	if got["state"] != automation.StateActive {
		t.Fatalf("overlap 不是失败，状态应留在 active，got %v", got["state"])
	}
	if !strings.Contains(got["last_reason"].(string), "不排队") {
		t.Fatalf("overlap 理由应说明「不排队」：%v", got["last_reason"])
	}
	runs := mustRuns(t, got)
	if len(runs) != 2 || runs[0].(map[string]any)["status"] != automation.RunOverlap {
		t.Fatalf("应追加一条 overlap 留痕: %+v", runs)
	}
	// 游标也推进了（否则每次 tick 都会再来一次 overlap）
	if len(runs) != 2 {
		t.Fatalf("overlap 也要留痕: %+v", runs)
	}
}

// TestAutomationTickFailureAdvancesAndNeedsAttention 触发失败（底座未装配）：
// **推进游标**（ADR-10）+ 落 needs_attention + 不重试 + 下一次 occurrence 照常。
func TestAutomationTickFailureAdvancesAndNeedsAttention(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	// 让构建期固定的那个底座在本进程里查不到（夹具注册表只有 silent）：模拟"定义还在、
	// 它依赖的东西没了"。v19 之前这里是把 `target.agent_id` 指到一个不存在的 agent——
	// agent 面撤销后，同一类"环境里的目标不见了"只能来自底座未装配。
	setBuildRuntime(t, "ghost")

	r.svc.Planes().Automation.Tick(context.Background(), time.Now())

	if n := len(r.sessionsOf(t, "daily")); n != 0 {
		t.Fatalf("触发失败不该建会话，got %d", n)
	}
	after := r.file(t, "daily")
	if after["last_fired_at"] == nil {
		t.Fatal("ADR-10：失败也必须推进认领游标（否则每 tick 重报一次）")
	}
	if after["state"] != automation.StateNeedsAttention {
		t.Fatalf("失败应落 needs_attention，got %v", after["state"])
	}
	if after["last_status"] != automation.RunSkipped {
		t.Fatalf("last_status 应为 skipped，got %v", after["last_status"])
	}
	if !strings.Contains(after["last_reason"].(string), "runtime") {
		t.Fatalf("理由应指出是底座（runtime）的问题：%v", after["last_reason"])
	}
	if n, _ := after["consecutive_failures"].(float64); n != 1 {
		t.Fatalf("连续失败应记 1，got %v", after["consecutive_failures"])
	}
	// 同一个 now 再 tick：没有新到期点（游标已推进到明天），不重试
	r.svc.Planes().Automation.Tick(context.Background(), time.Now())
	again := r.file(t, "daily")
	if n := len(mustRuns(t, again)); n != 1 {
		t.Fatalf("失败后不该在同一 occurrence 上重试，留痕 got %d", n)
	}
	if n, _ := again["consecutive_failures"].(float64); n != 1 {
		t.Fatalf("连续失败不该重复累加，got %v", again["consecutive_failures"])
	}
}

// TestAutomationStartupPassExpireOnceOnce 过期的 once：置 inactive、**不补跑**，且重启不会重复留痕。
func TestAutomationStartupPassExpireOnceOnce(t *testing.T) {
	r := newSchedRig(t)
	body := map[string]any{
		"id": "mig", "enabled": true,
		"schedule": map[string]any{"kind": "once", "timezone": "UTC", "at": "2020-01-01T00:00:00Z"},
		"target":   map[string]any{"cwd": r.cwd, "prompt": "跑一次迁移", "delivery": "new_session"},
	}
	raw, _ := json.Marshal(body)
	writeAutomationFile(t, filepath.Join(r.dir, "mig.json"), string(raw))
	if n := r.svc.Planes().Automation.LoadAutomationDefinitions(r.dir); n != 1 {
		t.Fatalf("装载应为 1，got %d", n)
	}
	r.svc.Planes().Automation.StartupPass(context.Background())
	got := r.file(t, "mig")
	if got["state"] != automation.StateInactive {
		t.Fatalf("过期的 once 应置 inactive，got %v", got["state"])
	}
	runs := mustRuns(t, got)
	if len(runs) != 1 || runs[0].(map[string]any)["status"] != automation.RunSkipped {
		t.Fatalf("应留一条 skipped 痕迹（不静默消失）: %+v", runs)
	}
	if !strings.Contains(got["last_reason"].(string), "once") {
		t.Fatalf("理由应说明 once 不补跑：%v", got["last_reason"])
	}
	// 不补跑：tick 也不该建会话
	r.svc.Planes().Automation.Tick(context.Background(), time.Now())
	if n := len(r.sessionsOf(t, "mig")); n != 0 {
		t.Fatalf("过期的 once 不该被 tick 补跑，got %d 个会话", n)
	}
	// 幂等：状态已是 inactive → 再跑启动 pass 不追加留痕（否则每次重启都留一条）
	r.svc.Planes().Automation.StartupPass(context.Background())
	r.svc.Planes().Automation.StartupPass(context.Background())
	if n := len(mustRuns(t, r.file(t, "mig"))); n != 1 {
		t.Fatalf("启动 pass 应幂等，留痕 got %d", n)
	}
	// 反过来：用户手工把状态改回 active（"我想再试一次"）→ 平台会**再告诉他一次**
	// "那一刻已经过去"，而不是默默不动（once 的语义是闹钟，过了就是过了）。
	r.svc.Planes().Automation.WriteAutomation(context.Background(), "mig", map[string]any{"state": automation.StateActive}, false)
	r.svc.Planes().Automation.StartupPass(context.Background())
	if got := r.file(t, "mig"); got["state"] != automation.StateInactive || len(mustRuns(t, got)) != 2 {
		t.Fatalf("改回 active 后应再次收敛到 inactive 并留痕: %v", got)
	}
}

// TestRunAutomationNowManualDoesNotConsumeSchedule 手动触发：建会话、留手动痕迹、**不吃掉排期**。
func TestRunAutomationNowManualDoesNotConsumeSchedule(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "") // 从未跑过 → next_due 在未来（或今天 09:00）
	before, _ := r.svc.Planes().Automation.AutomationByID("daily")

	out, err := r.svc.Planes().Automation.RunNow(context.Background(), "daily")
	if err != nil {
		t.Fatalf("手动触发: %v", err)
	}
	if out.Status != automation.RunClaimed || out.Trigger != automation.TriggerManual {
		t.Fatalf("手动触发结果不符: %+v", out)
	}
	got := r.file(t, "daily")
	if got["last_fired_at"] != nil {
		t.Fatalf("手动触发**不得**推进认领游标（否则会静默跳过当晚的排期），got %v", got["last_fired_at"])
	}
	after, _ := r.svc.Planes().Automation.AutomationByID("daily")
	if after.NextDueAt != before.NextDueAt {
		t.Fatalf("手动触发不得改变 next_due：%d → %d", before.NextDueAt, after.NextDueAt)
	}
	runs := mustRuns(t, got)
	if len(runs) != 1 || runs[0].(map[string]any)["trigger"] != automation.TriggerManual {
		t.Fatalf("应有 1 条 manual 留痕: %+v", runs)
	}
	// 会话还在跑 → 再来一次手动触发必须 overlap（手动不等于可以并发）
	out2, err := r.svc.Planes().Automation.RunNow(context.Background(), "daily")
	if err != nil {
		t.Fatalf("第二次手动触发: %v", err)
	}
	if out2.Status != automation.RunOverlap {
		t.Fatalf("上一次会话还在跑时手动触发应 overlap，got %s", out2.Status)
	}
	// 未注册 → 40401；被停用 → 40001
	if _, err := r.svc.Planes().Automation.RunNow(context.Background(), "nope"); errCode(err) != "automation_not_found" {
		t.Fatalf("未知 id 应 automation_not_found，got %v", err)
	}
	if _, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"enabled": false}, false); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := r.svc.Planes().Automation.RunNow(context.Background(), "daily"); errCode(err) != "automation_disabled" {
		t.Fatalf("停用后手动触发应 automation_disabled，got %v", err)
	}
}

// TestAutomationSchedulerStartStop 循环真能启停：启动后按 tick 跑，停止后不再跑。
func TestAutomationSchedulerStartStop(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	r.svc.Planes().Automation.StartAutomationScheduler(20 * time.Millisecond)
	// 等第一跳（宽限 2s 防慢机器）
	deadline := time.Now().Add(2 * time.Second)
	for len(r.sessionsOf(t, "daily")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(r.sessionsOf(t, "daily")); n != 1 {
		t.Fatalf("调度循环应触发一次，got %d", n)
	}
	r.svc.Planes().Automation.StopAutomationScheduler()
	// 停止只保证"不再有新触发起步"：已在途的那一次会照常收尾，故基线取在收尾之后。
	time.Sleep(100 * time.Millisecond)
	n := len(r.sessionsOf(t, "daily"))
	runs := len(mustRuns(t, r.file(t, "daily")))
	time.Sleep(150 * time.Millisecond) // 7 个 tick 的时间
	if got := len(r.sessionsOf(t, "daily")); got != n {
		t.Fatalf("停止后不该再触发：%d → %d", n, got)
	}
	if got := len(mustRuns(t, r.file(t, "daily"))); got != runs {
		t.Fatalf("停止后不该再留痕：%d → %d", runs, got)
	}
	// 幂等
	r.svc.Planes().Automation.StopAutomationScheduler()
	// interval=0 = 关闭（不启 goroutine）
	r2 := newSchedRig(t)
	r2.svc.Planes().Automation.StartAutomationScheduler(0)
	if !r2.svc.planes.Automation.SchedulerStopped() {
		t.Fatal("interval=0 不该启动调度 goroutine")
	}
}

// TestAutomationConcurrentTriggersOnlyRunOnce 并发触发只能有一个会话。
//
// 认领判定是纯函数，纯函数不提供互斥：tick 与 `POST /run` 同时看到"这个 occurrence 还没认领"
// 时，两边都会去建会话。automation 的 prompt 常常有副作用（改代码、发通知），
// 所以这条必须由 `automationRunMu` 兜住，而不是靠"窗口很小"。
func TestAutomationConcurrentTriggersOnlyRunOnce(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	now := time.Now()
	var wg sync.WaitGroup
	// 三路并发：两个 tick + 一个手动，抢同一个"停机错过的 occurrence"。
	wg.Add(3)
	go func() { defer wg.Done(); r.svc.Planes().Automation.Tick(context.Background(), now) }()
	go func() { defer wg.Done(); r.svc.Planes().Automation.Tick(context.Background(), now) }()
	go func() { defer wg.Done(); _, _ = r.svc.Planes().Automation.RunNow(context.Background(), "daily") }()
	wg.Wait()

	if n := len(r.sessionsOf(t, "daily")); n != 1 {
		t.Fatalf("并发触发应恰好产生 1 个会话，got %d", n)
	}
	runs := mustRuns(t, r.file(t, "daily"))
	claimed := 0
	for _, x := range runs {
		if x.(map[string]any)["status"] == automation.RunClaimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("运行史里 claimed 应恰好 1 条，got %d（%+v）", claimed, runs)
	}
	// 并发下其余几路要么 duplicate 要么 overlap，都必须有痕（不能静默丢弃）
	if len(runs) < 2 {
		t.Fatalf("被挡下的触发也应留痕: %+v", runs)
	}
}

// TestAutomationReservedKeysRejected 平台独占键不可由用户写面写（能写 last_fired_at 就能重放）。
func TestAutomationReservedKeysRejected(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "")
	for _, k := range []string{"last_fired_at", "last_session_id", "last_status", "last_reason", "consecutive_failures", "recent_runs"} {
		if _, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{k: "x"}, false); errCode(err) != "invalid_param" {
			t.Fatalf("写 %s 应被拒（40000），got %v", k, err)
		}
	}
	// 用户可写的（含 state）必须照旧可写
	if _, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"state": automation.StateArchived}, false); err != nil {
		t.Fatalf("state 应可写: %v", err)
	}
	if got := r.file(t, "daily"); got["state"] != automation.StateArchived {
		t.Fatalf("state 未写入: %v", got["state"])
	}
}

// TestAutomationRejectsRuntimeHint `target.runtime_hint` 整体下线：写面带它就显式拒绝
// （40001 `runtime_selection_removed`），存量定义里的残留则"读面宽容 + 下次落盘自愈"。
//
// 这取代了原来的 TestAutomationRuntimeHint（它测的是"hint 不在 agent 候选里 → 40401"）：
// 候选面已不存在，那个键不再是"选错了"而是"这个面没有了"。两条路必须分开，
// 因为正确出路不同——见 `rejectRuntimeHintInPatch` / `dropLegacyRuntimeHint` 的注释：
// 请求里带了就报错（否则它变成"写了、存了、什么都不发生"的骗人字段），老文件里的残留就删掉
// （否则存量 automation 永远存不回去，而界面已经没有这个字段可让用户删）。
func TestAutomationRejectsRuntimeHint(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "")

	// ① 请求体里带它 → 显式拒绝。
	_, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{
		"target": map[string]any{"cwd": r.cwd, "prompt": "x", "delivery": "new_session", "runtime_hint": "silent"},
	}, false)
	if err == nil {
		t.Fatal("写面带 target.runtime_hint 应被拒")
	}
	if got := errCode(err); got != "runtime_selection_removed" {
		t.Fatalf("应是 runtime_selection_removed，got %q（%v）", got, err)
	}
	if !strings.Contains(err.Error(), "构建期") {
		t.Fatalf("文案应指路「底座由构建期固定」: %v", err)
	}

	// ② 老定义里的残留（本次请求没带）→ 照常装载，且下一次落盘把它删掉（自愈）。
	got := r.file(t, "daily")
	got["target"].(map[string]any)["runtime_hint"] = "silent"
	raw, _ := json.Marshal(got)
	writeAutomationFile(t, filepath.Join(r.dir, "daily.json"), string(raw))
	if n := r.svc.Planes().Automation.LoadAutomationDefinitions(r.dir); n != 1 {
		t.Fatalf("带残留 runtime_hint 的老定义应照常装载，got %d", n)
	}
	if _, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"name": "改名"}, false); err != nil {
		t.Fatalf("只改别的键不该被残留键拦住: %v", err)
	}
	if _, has := r.file(t, "daily")["target"].(map[string]any)["runtime_hint"]; has {
		t.Fatalf("落盘时应把残留的 runtime_hint 自愈删掉: %v", r.file(t, "daily"))
	}
}

// TestAutomationReloadAfterRestart 重启语义：装载即可恢复调度态（next_due 由文件里的认领游标推出）。
func TestAutomationReloadAfterRestart(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "2020-01-01T01:00:00Z")
	r.svc.Planes().Automation.Tick(context.Background(), time.Now())
	after := r.file(t, "daily")

	// 新进程：新 Service + 同目录
	r2 := newSchedRig(t)
	raw, _ := json.Marshal(after)
	writeAutomationFile(t, filepath.Join(r2.dir, "daily.json"), string(raw))
	if n := r2.svc.Planes().Automation.LoadAutomationDefinitions(r2.dir); n != 1 {
		t.Fatalf("装载应为 1，got %d", n)
	}
	view, _ := r2.svc.Planes().Automation.AutomationByID("daily")
	first, _ := r.svc.Planes().Automation.AutomationByID("daily")
	if view.NextDueAt != first.NextDueAt {
		t.Fatalf("重启后 next_due 应与停机前一致：%d vs %d", view.NextDueAt, first.NextDueAt)
	}
	// 重启后不重复触发同一 occurrence
	r2.svc.Planes().Automation.Tick(context.Background(), time.Now())
	if n := len(r2.sessionsOf(t, "daily")); n != 0 {
		t.Fatalf("重启后不该重跑已认领的 occurrence，got %d", n)
	}
}

// TestAutomationMaxRunDurationWatchdog 外层超时收紧：到点取消在跑的会话。
func TestAutomationMaxRunDurationWatchdog(t *testing.T) {
	r := newSchedRig(t)
	r.writeDaily(t, "daily", "")
	if _, err := r.svc.Planes().Automation.WriteAutomation(context.Background(), "daily", map[string]any{"max_run_duration": "80ms"}, false); err != nil {
		t.Fatalf("设置 max_run_duration: %v", err)
	}
	out, err := r.svc.Planes().Automation.RunNow(context.Background(), "daily")
	if err != nil {
		t.Fatalf("手动触发: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.svc.HostSessionRunning(out.SessionID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.svc.HostSessionRunning(out.SessionID) {
		t.Fatalf("超过 max_run_duration 后会话应被取消（仍在 in_progress）")
	}
	// 桩进程回收由夹具的 t.Cleanup(orch.Close) 负责（先停编排器再关库）。
}

// TestAutomationDeliverySessionEndToEnd v35 P5：delivery=session 的真实端到端——
// 建一个真会话 → 定义一条每天 9 点、投递回该会话的任务 → 强制到期触发 →
// **消息投进原会话**（不是新建会话）：目标会话收到 session.user_message，且没有新建 automation 会话。
func TestAutomationDeliverySessionEndToEnd(t *testing.T) {
	r := newSchedRig(t)
	// 1) 建一个真实的"原会话"（idle）。
	target, err := r.svc.CreateSession(context.Background(), dtosession.SessionCreateReq{PermissionMode: "workspace-write", Cwd: r.cwd})
	if err != nil {
		t.Fatalf("建目标会话: %v", err)
	}
	// 2) 定义一条 delivery=session 的 daily（经文件，可带 session_id）。
	body := map[string]any{
		"id": "remind", "name": "喝水提醒", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "Asia/Shanghai", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "该喝水了", "delivery": "session", "session_id": target.ID},
	}
	raw, _ := json.Marshal(body)
	writeAutomationFile(t, filepath.Join(r.dir, "remind.json"), string(raw))
	if n := r.svc.Planes().Automation.LoadAutomationDefinitions(r.dir); n != 1 {
		t.Fatalf("装载应为 1，got %d", n)
	}
	// 3) 强制到期：把内存游标拨到过去，触发一次。
	now := time.Now()
	r.svc.planes.Automation.Mutate("remind", func(rec *automationcore.Rec) {
		rec.NextDue = now.Add(-time.Minute)
	})
	sub := r.bus.SubscribeAfter(nil)
	defer r.bus.Unsubscribe(sub)
	r.svc.Planes().Automation.Tick(context.Background(), now)

	// 4) 目标会话收到了 user_message（投递成功），且**没有**新建 automation 会话。
	var delivered bool
	for _, ev := range busEvents(r.svc) {
		if ev.Type == session.BusUserMessage && ev.AggregID == target.ID {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("delivery=session 应把消息投进原会话 %s（未收到 user_message）", target.ID)
	}
	if newly := r.sessionsOf(t, "remind"); len(newly) != 0 {
		t.Fatalf("delivery=session 不该新建 automation 会话，got %d", len(newly))
	}
	// 运行史记 claimed，last_session_id = 目标会话。
	got := r.file(t, "remind")
	if got["last_status"] != automation.RunClaimed {
		t.Fatalf("last_status 应为 claimed，got %v", got["last_status"])
	}
	if got["last_session_id"] != target.ID {
		t.Fatalf("last_session_id 应记目标会话，got %v", got["last_session_id"])
	}
}

// TestAutomationRRuleCreateAndRun 端到端（WP-B rrule）：
// 用 DTO 写面建一条 rrule 自动化（时区由 DTSTART 自带）→ 定义落盘 → 手动触发
// （走既有 RunAutomationNow → 认领 → 建会话 → automation.run 事件）。验证 rrule 全程走既有路径，
// 没有新开并发/状态/事件。
func TestAutomationRRuleCreateAndRun(t *testing.T) {
	r := newSchedRig(t)
	sub := r.bus.SubscribeAfter(nil)
	defer r.bus.Unsubscribe(sub)

	// DTSTART 用 UTC（Z），每月第 2 个周三 —— 覆盖 BYDAY 序数这条最典型的 rrule 表达力。
	expr := "DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYDAY=2WE"
	resp, err := autoSvc(r.svc).CreateAutomation(context.Background(), rawReq(t, map[string]any{
		"id": "rr", "name": "rrule 巡检", "enabled": true,
		"schedule": map[string]any{"kind": "rrule", "rrule": expr},
		"target":   map[string]any{"cwd": r.cwd, "prompt": "跑一遍", "delivery": "new_session"},
	}))
	if err != nil {
		t.Fatalf("建 rrule 自动化应成功: %v", err)
	}
	if resp.Schedule.Kind != "rrule" || resp.Schedule.RRule != expr {
		t.Fatalf("回执应原样回带 rrule（权威文本不规范化）: %+v", resp.Schedule)
	}
	// 下次到期应落在"第 2 个周三"，即某个 UTC 周三 09:00。
	if resp.NextDueAt == 0 {
		t.Fatal("rrule 应算出 next_due")
	}
	if wd := time.UnixMilli(resp.NextDueAt).UTC().Weekday(); wd != time.Wednesday {
		t.Fatalf("2WE 的 next_due 应是周三，got %s", wd)
	}

	// 手动触发一次：claimed + 建会话。
	out, err := autoSvc(r.svc).RunAutomationNow(context.Background(), "rr")
	if err != nil || out.Status != "claimed" || out.SessionID == "" || out.Trigger != "manual" {
		t.Fatalf("rrule 手动触发应 claimed 且带会话: %+v %v", out, err)
	}
	if convs := r.sessionsOf(t, "rr"); len(convs) != 1 {
		t.Fatalf("应恰好建 1 个会话，got %d", len(convs))
	}
	// automation.run 事件发出（不新增事件类型，仍是既有 BusAutomationRun）。
	sawEvent := false
	deadline := time.After(2 * time.Second)
	for !sawEvent {
		select {
		case e := <-sub.C:
			if e.Type == session.BusAutomationRun && e.AggregID == automationcore.Origin("rr") {
				sawEvent = true
			}
		case <-deadline:
			t.Fatal("未收到 automation.run 事件")
		}
	}
	// 非法 rrule 在写面被拒（invalid_schedule / definition_invalid），零字节落盘。
	if _, err := autoSvc(r.svc).CreateAutomation(context.Background(), rawReq(t, map[string]any{
		"id": "bad", "name": "坏 rrule", "enabled": true,
		"schedule": map[string]any{"kind": "rrule", "rrule": "DTSTART:20260101T090000Z\nRRULE:FREQ=SECONDLY"},
		"target":   map[string]any{"cwd": r.cwd, "prompt": "x", "delivery": "new_session"},
	})); err == nil {
		t.Fatal("SECONDLY 应被写面拒绝")
	}
	if _, ok := r.svc.Planes().Automation.AutomationByID("bad"); ok {
		t.Fatal("被拒的坏定义不该进注册表")
	}
}
