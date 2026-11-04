package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	domainautomation "github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/runtime"
)

// recordHost 记录触发分派走的是哪条路（CreateSession vs DeliverSession）。
type recordHost struct {
	delivered []domainautomation.HostDeliverReq
	created   []domainautomation.HostSessionReq
	running   bool
}

func (h *recordHost) CreateSession(_ context.Context, req domainautomation.HostSessionReq) (string, error) {
	h.created = append(h.created, req)
	return "ses_new", nil
}
func (h *recordHost) DeliverSession(_ context.Context, req domainautomation.HostDeliverReq) error {
	h.delivered = append(h.delivered, req)
	return nil
}
func (h *recordHost) CancelRun(context.Context, string) error                { return nil }
func (h *recordHost) SessionRunning(string) bool                             { return false }
func (h *recordHost) SessionExists(string) bool                              { return true }
func (h *recordHost) EmitRun(context.Context, string, int64, map[string]any) {}

// TestAutomationDispatchByDelivery v35 §3.2：触发按 target.delivery 分派——
// delivery=session → DeliverSession（不新建会话）；缺省（空）= session；
// delivery=new_session → CreateSession。
func TestAutomationDispatchByDelivery(t *testing.T) {
	svc, _ := automationDBRig(t, "")
	h := &recordHost{}
	svc.Planes().Automation.SetHost(h) // 覆盖 Service 的适配器（只为记录分派）
	dir := t.TempDir()
	cwd := t.TempDir()

	seed := func(id string, target map[string]any) {
		p := map[string]any{
			"id": id, "enabled": true,
			"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
			"target":   target,
		}
		if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), id, p, true); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	_ = dir
	_ = cwd

	// delivery=session（显式）
	seed("deliver", map[string]any{"prompt": "提醒我喝水", "delivery": "session", "session_id": "sess_target"})
	if _, err := svc.Planes().Automation.RunNow(context.Background(), "deliver"); err != nil {
		t.Fatalf("RunNow deliver: %v", err)
	}
	if len(h.delivered) != 1 || h.delivered[0].SessionID != "sess_target" || h.delivered[0].Prompt != "提醒我喝水" {
		t.Fatalf("delivery=session 应 DeliverSession 到 sess_target，got %+v", h.delivered)
	}
	if len(h.created) != 0 {
		t.Fatalf("delivery=session 不该 CreateSession，got %+v", h.created)
	}

	// new_session
	seed("newsess", map[string]any{"prompt": "批处理", "delivery": "new_session", "cwd": cwd})
	if _, err := svc.Planes().Automation.RunNow(context.Background(), "newsess"); err != nil {
		t.Fatalf("RunNow new_session: %v", err)
	}
	if len(h.created) != 1 || h.created[0].Prompt != "批处理" {
		t.Fatalf("new_session 应 CreateSession，got %+v", h.created)
	}
	if len(h.delivered) != 1 {
		t.Fatalf("new_session 不该 DeliverSession，got %+v", h.delivered)
	}
}

// TestAutomationSessionDeliveryIgnoresBusyTarget v35 §3.3：delivery=session 时，目标会话在跑
// **不**按 overlap 跳过——投递走 after_run（忙则入队），skip 反而丢提醒；重复投递由 duplicate 挡。
func TestAutomationSessionDeliveryIgnoresBusyTarget(t *testing.T) {
	svc, _ := automationDBRig(t, "")
	h := &recordHost{running: true} // 目标会话一直 in_progress
	svc.Planes().Automation.SetHost(h)
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "d", map[string]any{
		"id": "d", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "提醒", "delivery": "session", "session_id": "sess_busy"},
	}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := svc.Planes().Automation.RunNow(context.Background(), "d")
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if out.Status != "claimed" {
		t.Fatalf("目标会话忙时 delivery=session 仍应投递（claimed，after_run 入队），got %s", out.Status)
	}
	if len(h.delivered) != 1 {
		t.Fatalf("应 DeliverSession 一次，got %+v", h.delivered)
	}
}

// TestSchedulerReloadsDefinitionsAcrossProcess v35 P3：daemon 只启动装载一次，MCP 子进程
// （另一个平面/进程）写进 store 的新定义，必须靠调度循环每 tick 的 ReloadDefinitionsForTick
// 被看到——否则"我在会话里建了它却从来不跑"。
func TestSchedulerReloadsDefinitionsAcrossProcess(t *testing.T) {
	// 两个 Service 共用同一个 store，模拟 daemon 与 MCP 子进程。
	daemonSvc, st := automationDBRig(t, "")
	subSvc := NewService(runtime.NewRegistry(silentACPSpec(t)), eventbus.New())
	setBuildRuntime(t, "silent")
	subSvc.EnableStore(st)

	// 子进程（subSvc）写一条定义进库。daemon 此刻看不到。
	if _, err := subSvc.Planes().Automation.WriteAutomation(context.Background(), "x", map[string]any{
		"id": "x", "enabled": true,
		"schedule": map[string]any{"kind": "daily", "timezone": "UTC", "hour": 9, "minute": 0},
		"target":   map[string]any{"prompt": "p", "delivery": "new_session", "cwd": t.TempDir()},
	}, true); err != nil {
		t.Fatalf("子进程写入: %v", err)
	}
	if _, ok := daemonSvc.Planes().Automation.AutomationByID("x"); ok {
		t.Fatal("daemon 未重载前不该看到子进程写的定义")
	}
	// daemon 调度循环每 tick 前调 ReloadDefinitionsForTick → 看到新定义。
	daemonSvc.Planes().Automation.ReloadDefinitionsForTick(context.Background())
	if _, ok := daemonSvc.Planes().Automation.AutomationByID("x"); !ok {
		t.Fatal("daemon 重载后应看到子进程写的新定义（跨进程生效）")
	}
}

// TestTickReloadDoesNotDriftIntervalDue 回归：每 tick 的增量重载**不得**把 interval 的 next_due
// 往后推（P3 引入 reload 时的真实缺陷：整体重建按 now 重算，interval"从未触发"分支恒给 now+every
// → 永远等不到 due、永不触发；once 过期分支则会判 expire 直接不跑）。
//
// 用 **sqlite 载体**（reloadMergeFromStore 是缺陷所在）。断言：
//  1. 连续重载多次，NextDue 保持不变；
//  2. 到点后 DueSnapshot 能看到它（说明它真的会成为 due，而不是被推走）。
func TestTickReloadDoesNotDriftIntervalDue(t *testing.T) {
	svc, _ := automationDBRig(t, "")
	if _, err := svc.Planes().Automation.WriteAutomation(context.Background(), "iv", map[string]any{
		"id": "iv", "enabled": true,
		"schedule": map[string]any{"kind": "interval", "timezone": "UTC", "every_seconds": 60},
		"target":   map[string]any{"prompt": "p", "delivery": "new_session", "cwd": t.TempDir()},
	}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec0, ok := svc.Planes().Automation.RecByID("iv")
	if !ok {
		t.Fatal("seed 后应能取到记录")
	}
	due := rec0.NextDue
	if due.IsZero() {
		t.Fatal("interval 从未触发应有一个未来 due（now+every）")
	}
	for i := 0; i < 5; i++ {
		time.Sleep(5 * time.Millisecond) // 让 now 前进，模拟多次 tick
		svc.Planes().Automation.ReloadDefinitionsForTick(context.Background())
		rec, _ := svc.Planes().Automation.RecByID("iv")
		if !rec.NextDue.Equal(due) {
			t.Fatalf("第 %d 次重载后 next_due 漂移了：%s → %s（interval 会被永久推后、永不触发）",
				i+1, due.UTC(), rec.NextDue.UTC())
		}
	}
	// 到点后应出现在 due 快照里（证明它真的会成为 due）。
	svc.planes.Automation.ReloadDefinitionsForTick(context.Background())
	dueList := svc.Planes().Automation.DueSnapshot(due.Add(time.Second))
	var found bool
	for _, r := range dueList {
		if r.Def.ID == "iv" {
			found = true
		}
	}
	if !found {
		t.Fatalf("到点后 DueSnapshot 应包含 iv（next_due=%s）", due.UTC())
	}
}
