package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/runtime"
)

// fakeExtractor 记录 ExtractToolWrite 的调用（并断言拿到的 info 已合并 rawInput）。
type fakeExtractor struct {
	mu    sync.Mutex
	calls []protocol.ToolCallInfo
}

func (f *fakeExtractor) ExtractToolWrite(_ context.Context, _, _ string, info protocol.ToolCallInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, info)
}

func (f *fakeExtractor) snapshot() []protocol.ToolCallInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.ToolCallInfo(nil), f.calls...)
}

// newExtractRunner 造一个挂了 fakeExtractor 的最小 Runner（无连接、无库）。
func newExtractRunner(t *testing.T, ex ArtifactExtractor) (*Runner, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	pl := NewPlane()
	pl.SetBus(bus)
	r := NewRunner(RunnerDeps{
		Plane:     pl,
		Handles:   NewHandles(),
		Acquire:   func(context.Context, string) (*ActiveSession, error) { return nil, nil },
		Busy:      NewInFlightTracker(),
		Extractor: ex,
	})
	return r, bus
}

// waitCalls 等 extractor 收到 n 条（抽取在 goroutine 里跑，需要等）。
func waitCalls(t *testing.T, ex *fakeExtractor, n int) []protocol.ToolCallInfo {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		got := ex.snapshot()
		if len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("等 extractor 调用超时: got %d want %d", len(got), n)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// TestEmitToolWrite_TriggersOnCompleted 验证：in_progress 帧的 rawInput 被缓存，completed 帧触发一次，
// 且交给 extractor 的 info 同时带 rawInput（来自 in_progress）与 status/content（来自终态）。
func TestEmitToolWrite_TriggersOnCompleted(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	// in_progress 帧（R1 实测：带 rawInput.file_path）
	r.rememberToolWrite("s1", "run1", "dsh", protocol.ToolCallInfo{
		ID: "c1", Title: "write", Status: protocol.ToolStatusInProgress,
		Input: map[string]any{"file_path": "a.txt", "content": "x"},
	})
	// 终态 completed 帧（不带 rawInput，带 content）
	term := protocol.ToolCallInfo{ID: "c1", Status: protocol.ToolStatusCompleted, ContentText: "Created file"}
	pw, ok := r.takeToolWrite("c1")
	if !ok {
		t.Fatalf("应取到在途线索")
	}
	r.emitToolWrite(pw, term)

	calls := waitCalls(t, ex, 1)
	if len(calls) != 1 {
		t.Fatalf("应触发 1 次，实际 %d", len(calls))
	}
	got := calls[0]
	if got.Status != protocol.ToolStatusCompleted {
		t.Errorf("合并后 status=%q 期望 completed", got.Status)
	}
	if got.ContentText != "Created file" {
		t.Errorf("合并后 ContentText=%q", got.ContentText)
	}
	m, ok := got.Input.(map[string]any)
	if !ok || m["file_path"] != "a.txt" {
		t.Errorf("合并后应保留 in_progress 帧的 rawInput.file_path，实际 %+v", got.Input)
	}
}

// TestTakeToolWrite_Clears 验证取出即清（幂等：不会被重复触发）。
func TestTakeToolWrite_Clears(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	r.rememberToolWrite("s1", "run1", "dsh", protocol.ToolCallInfo{ID: "c1", Title: "write"})
	if _, ok := r.takeToolWrite("c1"); !ok {
		t.Fatalf("第一次应取到")
	}
	if _, ok := r.takeToolWrite("c1"); ok {
		t.Fatalf("取过之后不应再取到")
	}
}

// TestEmitToolWrite_NilExtractorNoop 验证未装 extractor 时缓存/触发零开销、不 panic。
func TestEmitToolWrite_NilExtractorNoop(t *testing.T) {
	r, _ := newExtractRunner(t, nil)
	r.rememberToolWrite("s1", "run1", "dsh", protocol.ToolCallInfo{ID: "c1", Title: "write"})
	if _, ok := r.takeToolWrite("c1"); ok {
		t.Fatalf("nil extractor 时不应缓存任何线索")
	}
	r.emitToolWrite(pendingToolWrite{}, protocol.ToolCallInfo{Status: protocol.ToolStatusCompleted}) // 不应 panic
}

// TestEmitToolWrite_SandboxDeniedSkipped 验证：沙箱拒绝（status 仍是 completed，但正文说明被拒）不抽。
func TestEmitToolWrite_SandboxDeniedSkipped(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	// 装一个最小 classifier：命中 "file access denied" + "sandbox" → 判为沙箱拒绝。
	r.toolFailed = fakeFailClassifier{}
	r.rememberToolWrite("s1", "run1", "dsh", protocol.ToolCallInfo{
		ID: "c1", Title: "write", Input: map[string]any{"file_path": "a.txt"},
	})
	pw, _ := r.takeToolWrite("c1")
	term := protocol.ToolCallInfo{ID: "c1", Status: protocol.ToolStatusCompleted,
		ContentText: "[sandbox: file access denied under workspace-write mode]"}
	r.emitToolWrite(pw, term)

	// 给 goroutine 一点时间确认它**没有**被调用。
	time.Sleep(50 * time.Millisecond)
	if calls := ex.snapshot(); len(calls) != 0 {
		t.Fatalf("沙箱拒绝不应触发抽取，实际 %d 次", len(calls))
	}
}

// fakeFailClassifier 最小实现：含 sandbox 的 file access denied 判为沙箱拒绝（对齐 dsh 真实现）。
type fakeFailClassifier struct{}

// ClassifyToolFailure 对齐**真实 runtime.Registry** 的语义（这正是真机 bug 的教训）：
// 非空文本一律 `ok=true`；认识的给 reason，**不认识的是 reason=""（成功工具的常态）**。
// 早先的 fake 对不认识文本返回 ok=false，与真实注册表不符 → 测试假绿，掩盖了「成功工具被
// 当沙箱拒绝跳过」的 bug。
func (fakeFailClassifier) ClassifyToolFailure(_ string, text string) (runtime.ToolFailure, bool) {
	if text == "" {
		return runtime.ToolFailure{}, false
	}
	if containsAll(text, "file access denied", "sandbox") {
		return runtime.ToolFailure{Reason: "sandbox_denied", BizCode: "sandbox_file_access_denied"}, true
	}
	return runtime.ToolFailure{}, true // ok=true，reason=""（不认识，但底座接了表）
}

// TestEmitToolWrite_SuccessNotMistakenAsFailure（回归）：底座接了失败签名表（ok=true）但文本
// 不认识（reason=""，成功工具的常态）时，**必须仍然抽取**。此前误用 ok 判断沙箱拒绝，
// 让每一次成功的工具都被跳过（真机走查抓到的 bug，2026-11-04）。
func TestEmitToolWrite_SuccessNotMistakenAsFailure(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	r.toolFailed = fakeFailClassifier{}
	r.rememberToolWrite("s1", "run1", "dsh", protocol.ToolCallInfo{
		ID: "c1", Title: "write", Input: map[string]any{"file_path": "a.txt"},
	})
	pw, _ := r.takeToolWrite("c1")
	term := protocol.ToolCallInfo{ID: "c1", Status: protocol.ToolStatusCompleted,
		ContentText: "Created file"} // 成功正文，不含任何失败签名
	r.emitToolWrite(pw, term)
	calls := waitCalls(t, ex, 1)
	if len(calls) != 1 {
		t.Fatalf("成功的工具必须被抽取（不能被当成失败跳过），实际 %d 次", len(calls))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestEventSink_TriggersExtractOnCompleted 端到端：经真实 EventSink 喂 in_progress + completed 两帧，
// 验证抽取被触发一次、且带 rawInput（证明接线走的是 eventsink 的真实分支，不是只测私有方法）。
func TestEventSink_TriggersExtractOnCompleted(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	r.handles.BindProvider("native-1", "conv-1") // 原生 session id → 平台 uuid
	sink := r.EventSink("dsh")

	// in_progress（带 rawInput.file_path）
	sink(protocol.Event{SessionID: "native-1", Kind: protocol.KindToolCall, Payload: protocol.ToolCallInfo{
		ID: "c1", Title: "write", Status: protocol.ToolStatusInProgress,
		Input: map[string]any{"file_path": "out.txt", "content": "hi"},
	}})
	// completed（不带 rawInput）
	sink(protocol.Event{SessionID: "native-1", Kind: protocol.KindToolUpdate, Payload: protocol.ToolCallInfo{
		ID: "c1", Status: protocol.ToolStatusCompleted, ContentText: "Created file",
	}})

	calls := waitCalls(t, ex, 1)
	if len(calls) != 1 {
		t.Fatalf("应触发 1 次，实际 %d", len(calls))
	}
	m, _ := calls[0].Input.(map[string]any)
	if m == nil || m["file_path"] != "out.txt" {
		t.Fatalf("抽取应带 in_progress 帧的 rawInput.file_path，实际 %+v", calls[0].Input)
	}
}

// TestEventSink_FailedDoesNotExtract 端到端：failed 终态不触发抽取。
func TestEventSink_FailedDoesNotExtract(t *testing.T) {
	ex := &fakeExtractor{}
	r, _ := newExtractRunner(t, ex)
	r.handles.BindProvider("native-1", "conv-1")
	sink := r.EventSink("dsh")
	sink(protocol.Event{SessionID: "native-1", Kind: protocol.KindToolCall, Payload: protocol.ToolCallInfo{
		ID: "c1", Title: "write", Status: protocol.ToolStatusInProgress,
		Input: map[string]any{"file_path": "out.txt"},
	}})
	sink(protocol.Event{SessionID: "native-1", Kind: protocol.KindToolUpdate, Payload: protocol.ToolCallInfo{
		ID: "c1", Status: protocol.ToolStatusFailed,
	}})
	time.Sleep(50 * time.Millisecond)
	if calls := ex.snapshot(); len(calls) != 0 {
		t.Fatalf("failed 不应触发抽取，实际 %d 次", len(calls))
	}
}
