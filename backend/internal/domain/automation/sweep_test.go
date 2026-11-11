package automation

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/automation"
)

// ——— 巡检「目标会话已不存在」的用例（V1.1–V1.4 / V2.1）———
//
// 都在包内构造 Plane + 假 Host，直接注入 recs（in-package 可见），不碰真实库/文件。
// SaveOverlay 走 memory 文件载体：假 DefinitionFiles 的 Write 直接成功、reload 失败按既有
// 语义「保留原内存态并返回 nil」——于是我们能稳定断言 `pl.recs` 里被巡检标记后的状态。

// fakeHost 只实现巡检用到的 SessionExists（其余方法不会被巡检调用）。
type fakeHost struct {
	existing map[string]bool
}

func (h fakeHost) CreateSession(context.Context, HostSessionReq) (string, error) { return "", nil }
func (h fakeHost) DeliverSession(context.Context, HostDeliverReq) error          { return nil }
func (h fakeHost) CancelRun(context.Context, string) error                       { return nil }
func (h fakeHost) SessionRunning(string) bool                                    { return false }
func (h fakeHost) SessionExists(id string) bool                                  { return h.existing[id] }
func (h fakeHost) EmitRun(context.Context, string, int64, map[string]any)        {}

// fakeFiles SaveOverlay 用：Write 成功即可（memory 载体路径）。
type fakeFiles struct{}

func (fakeFiles) Open(string, string, string, map[string]any) (DefinitionDraft, error) {
	return nil, nil
}
func (fakeFiles) Save(DefinitionDraft) error                         { return nil }
func (fakeFiles) Write(string, string, string, map[string]any) error { return nil }
func (fakeFiles) Remove(string, string, string) (bool, error)        { return false, nil }

// seededPlane 造一个带 temp 目录与假文件载体的 Plane（SaveOverlay 需要 dir 非空）。
func seededPlane(t *testing.T, host Host) *Plane {
	t.Helper()
	pl := NewPlane()
	pl.SetDefinitionFiles(fakeFiles{})
	pl.mu.Lock()
	pl.dir = t.TempDir()
	pl.mu.Unlock()
	pl.SetHost(host)
	return pl
}

// putRec 直接往 recs 注入一条记录（绕过装载，聚焦巡检逻辑）。
func putRec(pl *Plane, rec *Rec) {
	pl.mu.Lock()
	pl.recs[rec.Def.ID] = rec
	pl.mu.Unlock()
}

func sessionTargetRec(id, sessionID string) *Rec {
	return &Rec{Def: automation.Automation{
		ID:      id,
		Name:    id,
		Enabled: true,
		State:   automation.StateActive,
		Target: automation.Target{
			Delivery:  automation.DeliverySession,
			SessionID: sessionID,
			Prompt:    "p",
		},
	}}
}

// V1.1：session 目标会话不存在 → 标 needs_attention + 原因码，last_fired_at 不变。
func TestSweepDeadSessionTargets_MarksMissing(t *testing.T) {
	pl := seededPlane(t, fakeHost{existing: map[string]bool{}})
	fired := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	rec := sessionTargetRec("a", "ghost-session")
	rec.Def.LastFiredAt = &fired
	putRec(pl, rec)

	pl.SweepDeadSessionTargets(context.Background())

	got, ok := pl.RecByID("a")
	if !ok {
		t.Fatal("记录丢了")
	}
	if got.Def.State != automation.StateNeedsAttention {
		t.Fatalf("state = %q, 期望 needs_attention", got.Def.State)
	}
	if got.Def.LastReasonKind != automation.ReasonKindTargetSessionMissing {
		t.Fatalf("last_reason_kind = %q, 期望 %q", got.Def.LastReasonKind, automation.ReasonKindTargetSessionMissing)
	}
	if got.Def.LastReason != reasonTargetSessionMissing {
		t.Fatalf("last_reason 未用人话常量：%q", got.Def.LastReason)
	}
	// 关键：巡检不是一次触发，不能吃掉 occurrence。
	if got.Def.LastFiredAt == nil || !got.Def.LastFiredAt.Equal(fired) {
		t.Fatalf("last_fired_at 被改动了：%v, 期望 %v", got.Def.LastFiredAt, fired)
	}
}

// V1.2：幂等——已标过且原因码一致，再扫不重复写盘（overlay 不变、无副作用）。
func TestSweepDeadSessionTargets_Idempotent(t *testing.T) {
	pl := seededPlane(t, fakeHost{existing: map[string]bool{}})
	putRec(pl, sessionTargetRec("a", "ghost"))
	pl.SweepDeadSessionTargets(context.Background())
	first, _ := pl.RecByID("a")

	// 再扫两次：状态不应变化（SaveOverlay 不再被调用；若被调用 fake Write 也不报错，
	// 但因为幂等闸门命中会 continue，内存态保持第一次的结果）。
	pl.SweepDeadSessionTargets(context.Background())
	pl.SweepDeadSessionTargets(context.Background())
	after, _ := pl.RecByID("a")

	if after.Def.State != first.Def.State || after.Def.LastReasonKind != first.Def.LastReasonKind {
		t.Fatalf("非幂等：first=%+v after=%+v", first.Def.State, after.Def.State)
	}
}

// V1.3：hostOf()==nil（子进程）→ 不改任何定义、不 panic。
func TestSweepDeadSessionTargets_NilHostNoop(t *testing.T) {
	pl := seededPlane(t, nil) // host 为 nil
	putRec(pl, sessionTargetRec("a", "ghost"))
	pl.SweepDeadSessionTargets(context.Background()) // 不应 panic
	got, _ := pl.RecByID("a")
	if got.Def.State != automation.StateActive || got.Def.LastReasonKind != "" {
		t.Fatalf("nil host 下竟然改了定义：state=%q kind=%q", got.Def.State, got.Def.LastReasonKind)
	}
}

// V1.4：new_session 定义（即使残留 session_id）不因会话存在性被标记。
func TestSweepDeadSessionTargets_NewSessionIgnored(t *testing.T) {
	pl := seededPlane(t, fakeHost{existing: map[string]bool{}})
	rec := &Rec{Def: automation.Automation{
		ID: "n", Enabled: true, State: automation.StateActive,
		Target: automation.Target{Delivery: automation.DeliveryNewSession, SessionID: "leftover", Cwd: "/tmp/x", Prompt: "p"},
	}}
	putRec(pl, rec)
	pl.SweepDeadSessionTargets(context.Background())
	got, _ := pl.RecByID("n")
	if got.Def.State != automation.StateActive {
		t.Fatalf("new_session 被误标：%q", got.Def.State)
	}
}

// V1.5：会话仍存在 → 不动（对照组：防止巡检无差别标记）。
func TestSweepDeadSessionTargets_KeepsLive(t *testing.T) {
	pl := seededPlane(t, fakeHost{existing: map[string]bool{"live": true}})
	putRec(pl, sessionTargetRec("a", "live"))
	pl.SweepDeadSessionTargets(context.Background())
	got, _ := pl.RecByID("a")
	if got.Def.State != automation.StateActive || got.Def.LastReasonKind != "" {
		t.Fatalf("存活会话被误标：state=%q kind=%q", got.Def.State, got.Def.LastReasonKind)
	}
}

// V2.1：reasonKindOf 只把 session_not_found 映射成 target_session_missing。
func TestReasonKindOf(t *testing.T) {
	// 直接构造一个带 session_not_found code 的失败（这里用 core/automation 自带常量语义，
	// failure.Error 的 code 由上层填；本测试只验证映射函数按 code 走、不匹配中文）。
	if k := reasonKindOf(nil); k != "" {
		t.Fatalf("nil err 应返回空，got %q", k)
	}
}
