package session

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/infra/acp"
)

// TestErrLooksCancelled 取消类错误归一判定（随 `runner.go` 从 manager 搬入）。
func TestErrLooksCancelled(t *testing.T) {
	if !errLooksCancelled(context.Canceled) {
		t.Fatal("ctx.Canceled 应为取消")
	}
	if !errLooksCancelled(acp.NewError(acp.ClassUnknown, "jsonrpc_error", "Request cancelled", nil)) {
		t.Fatal("Request cancelled 应为取消")
	}
	if errLooksCancelled(acp.NewError(acp.ClassService, "service_error", "boom", nil)) {
		t.Fatal("普通错误不应判为取消")
	}
}

// TestActiveSessionRunLifecycle 句柄的轮次状态机：忙判据、memo 一次性消费、收尾清空、作废幂等。
//
// 这些状态从 manager 搬进本域后**只有一个写口**（BeginRun/FinishRun/MarkDead）。测试因此钉在
// 这些入口上，而不是直接改字段——否则「两份在途」这种失效在域内自己就先复现了。
func TestActiveSessionRunLifecycle(t *testing.T) {
	as := NewActiveSession(HandleParams{ID: "s1", Runtime: "r", Memo: "摘要"})
	if as.Busy() || as.Dead() {
		t.Fatal("新句柄应为空闲且存活")
	}

	run, prefix, err := as.BeginRun(context.Background())
	if err != nil {
		t.Fatalf("开轮: %v", err)
	}
	if prefix.Memo != "摘要" {
		t.Fatalf("首轮应取走 memo，got %q", prefix.Memo)
	}
	if !as.Busy() || as.RunID() != run.ID() {
		t.Fatalf("开轮后应忙且 run_id 指向本轮: busy=%v run=%q", as.Busy(), as.RunID())
	}
	// 忙时再开一轮：必须是 run_in_progress（不是静默覆盖在途轮次）。
	if _, _, err := as.BeginRun(context.Background()); err == nil {
		t.Fatal("忙时第二轮应返回 run_in_progress")
	} else if ae, ok := acp.As(err); !ok || ae.Code != "run_in_progress" {
		t.Fatalf("错误码应为 run_in_progress: %v", err)
	}
	as.FinishRun()
	if as.Busy() || as.RunID() != "" {
		t.Fatal("收尾后应空闲且没有在途轮")
	}
	// memo 只消费一次：收尾后再开一轮，不应再取到它。
	if _, prefix2, err := as.BeginRun(context.Background()); err != nil || !prefix2.Empty() {
		t.Fatalf("memo 只应注入首轮: prefix=%+v err=%v", prefix2, err)
	}
	as.FinishRun()

	// 作废幂等：第一次从活变死并交回在途轮；重复调用不再报告"刚作废"。
	run2, _, err := as.BeginRun(context.Background())
	if err != nil {
		t.Fatalf("再开一轮: %v", err)
	}
	got, newly := as.MarkDead(true)
	if !newly || got != run2 || !got.Intentional() {
		t.Fatalf("首次作废应标记在途轮为 intentional: newly=%v got=%v", newly, got)
	}
	if _, newly := as.MarkDead(true); newly {
		t.Fatal("重复作废必须幂等（拆连路径会对同一句柄跑两遍收尾）")
	}
}

// TestHandlesPutKeepsProviderIndex 双索引同生共死：换句柄时旧原生映射只在原生 id 不同才摘。
func TestHandlesPutKeepsProviderIndex(t *testing.T) {
	h := NewHandles()
	a := NewActiveSession(HandleParams{ID: "s1", ProviderID: "native-1"})
	h.Put(a)
	if got := h.ResolveProvider("native-1"); got != "s1" {
		t.Fatalf("原生 id 应解析到平台 id，got %q", got)
	}

	// 同一条原生会话重新挂上（重启/换代但底座会话没变）：映射仍成立。
	b := NewActiveSession(HandleParams{ID: "s1", ProviderID: "native-1"})
	h.Put(b)
	if got := h.ResolveProvider("native-1"); got != "s1" {
		t.Fatalf("同原生 id 的替换不应摘映射，got %q", got)
	}

	// 换成新的原生会话：旧映射必须摘掉（迟到事件不得落进已顶替的会话）。
	c := NewActiveSession(HandleParams{ID: "s1", ProviderID: "native-2"})
	h.Put(c)
	if got := h.ResolveProvider("native-1"); got != "" {
		t.Fatalf("旧原生映射应被摘除，got %q", got)
	}
	if got := h.ResolveProvider("native-2"); got != "s1" {
		t.Fatalf("新原生映射应生效，got %q", got)
	}
}
