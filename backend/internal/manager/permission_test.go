package manager

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-forge/pkg/acl"
)

// TestPermissionResolveOnce verify 注册挂起 → 决策投递链路（allow/reject，once）。
func TestPermissionResolveOnce(t *testing.T) {
	m := NewPermissionManager(5 * time.Second)
	req := acl.PermissionRequest{ToolCall: acl.ToolCallInfo{ID: "tc1", Title: "运行命令", Kind: "bash"},
		Options: []acl.PermissionOption{{OptionID: "o1", Name: "允许", Kind: permKindAllowOnce}}}
	exited := make(chan acl.PermissionDecision, 1)
	go func() {
		exited <- m.RegisterAndWait(context.Background(), "s1", req, nil)
	}()
	// 等注册就绪后投递「允许」。
	waitRegistered(t, m, "s1")
	if !m.ResolveAllow("", "o1", false) {
		// 未知 call_id（空）应返回 false；这里我们从 pendingForConversation 取真实 call_id。
	}
	callID := pendingCallID(t, m, "s1")
	if !m.ResolveAllow(callID, "o1", false) {
		t.Fatalf("ResolveAllow 未命中 call_id=%s", callID)
	}
	d := <-exited
	if !d.Allowed || d.OptionID != "o1" || d.Always {
		t.Fatalf("允许决策不符: %+v", d)
	}
	if m.pendingCount("s1") != 0 {
		t.Fatalf("完成后应无挂起，got %d", m.pendingCount("s1"))
	}
}

// TestPermissionTimeoutAutoReject verify 超时自动拒绝。
func TestPermissionTimeoutAutoReject(t *testing.T) {
	m := NewPermissionManager(50 * time.Millisecond)
	req := acl.PermissionRequest{ToolCall: acl.ToolCallInfo{ID: "tc2", Title: "写文件", Kind: "write"}}
	start := time.Now()
	d := m.RegisterAndWait(context.Background(), "s2", req, nil)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("未等到超时即返回: %v", elapsed)
	}
	if d.Allowed || d.Always {
		t.Fatalf("超时自动拒绝失败: %+v", d)
	}
}

// TestPermissionAllowAlways记忆 verify allow_always 记录会话级始终允许，二次请求放行。
func TestPermissionAllowAlwaysMemory(t *testing.T) {
	m := NewPermissionManager(5 * time.Second)
	tool := acl.ToolCallInfo{ID: "tc3", Title: "执行脚本", Kind: "bash"}
	req := acl.PermissionRequest{ToolCall: tool}
	exited := make(chan acl.PermissionDecision, 1)
	go func() { exited <- m.RegisterAndWait(context.Background(), "s3", req, nil) }()
	waitRegistered(t, m, "s3")
	callID := pendingCallID(t, m, "s3")
	if !m.ResolveAllow(callID, "", true) {
		t.Fatalf("ResolveAllow(always) 未命中")
	}
	d := <-exited
	if !d.Allowed || !d.Always {
		t.Fatalf("always 允许决策不符: %+v", d)
	}
	// 二次请求命中始终允许记忆 → 直接放行（AutoDecision 快路径）。
	if d2, ok := m.AutoDecision("s3", req); !ok || !d2.Allowed {
		t.Fatalf("始终允许记忆未命中: ok=%v d=%+v", ok, d2)
	}
}

// TestPermissionReject verify 拒绝路径。
func TestPermissionReject(t *testing.T) {
	m := NewPermissionManager(5 * time.Second)
	req := acl.PermissionRequest{ToolCall: acl.ToolCallInfo{ID: "tc4", Title: "删除", Kind: "bash"}}
	exited := make(chan acl.PermissionDecision, 1)
	go func() { exited <- m.RegisterAndWait(context.Background(), "s4", req, nil) }()
	waitRegistered(t, m, "s4")
	callID := pendingCallID(t, m, "s4")
	if !m.ResolveReject(callID, "", false) {
		t.Fatalf("ResolveReject 未命中")
	}
	d := <-exited
	if d.Allowed || d.Always || !d.Rejected {
		t.Fatalf("拒绝决策不符: %+v", d)
	}
}

// TestPermissionCancelConversation verify conversation/cancel 后全部挂起被取消（ACP 规范）。
func TestPermissionCancelSession(t *testing.T) {
	m := NewPermissionManager(5 * time.Second)
	for i := 0; i < 2; i++ {
		req := acl.PermissionRequest{ToolCall: acl.ToolCallInfo{ID: "tc", Title: "op", Kind: "bash"}}
		exited := make(chan acl.PermissionDecision, 1)
		go func() { exited <- m.RegisterAndWait(context.Background(), "s5", req, nil) }()
	}
	waitRegistered(t, m, "s5")
	// 两个注册 goroutine 各自抢锁写入；等齐 2 条再断言（避免全量并行负载下的调度抖动）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && m.pendingCount("s5") < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if m.pendingCount("s5") != 2 {
		t.Fatalf("期望 2 条挂起，got %d", m.pendingCount("s5"))
	}
	m.CancelSession("s5")
	if m.pendingCount("s5") != 0 {
		t.Fatalf("取消后应无挂起，got %d", m.pendingCount("s5"))
	}
}

// ——— helpers ———

func waitRegistered(t *testing.T, m *PermissionManager, conversationID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.pendingForSession(conversationID)) > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("权限请求未在限期内注册")
}

func pendingCallID(t *testing.T, m *PermissionManager, conversationID string) string {
	t.Helper()
	ps := m.pendingForSession(conversationID)
	if len(ps) == 0 {
		t.Fatal("无挂起请求")
	}
	return ps[0].callID
}

// TestResolveDecisionReportsEffectiveOutcome 决策回执是「实际投递结果」而非请求参数回显：
// option_id 缺省/无效时兜底为第一个选项，显式拒绝无 deny 选项时降级 cancelled。
func TestResolveDecisionReportsEffectiveOutcome(t *testing.T) {
	cases := []struct {
		name     string
		options  []acl.PermissionOption
		d        acl.PermissionDecision
		kind     string
		selected string
	}{
		{"放行+指定选项", []acl.PermissionOption{{OptionID: "allow_once"}, {OptionID: "allow_always"}},
			acl.PermissionDecision{Allowed: true, OptionID: "allow_always"}, acl.OutcomeSelected, "allow_always"},
		{"放行+缺省→第一个", []acl.PermissionOption{{OptionID: "allow_once"}, {OptionID: "allow_always"}},
			acl.PermissionDecision{Allowed: true}, acl.OutcomeSelected, "allow_once"},
		{"放行+无效→第一个", []acl.PermissionOption{{OptionID: "allow_once"}},
			acl.PermissionDecision{Allowed: true, OptionID: "nope"}, acl.OutcomeSelected, "allow_once"},
		{"拒绝+deny 选项→selected", []acl.PermissionOption{{OptionID: "reject_once"}},
			acl.PermissionDecision{Rejected: true, OptionID: "reject_once"}, acl.OutcomeSelected, "reject_once"},
		{"拒绝+无 deny 选项→cancelled", []acl.PermissionOption{{OptionID: "allow_once"}},
			acl.PermissionDecision{Rejected: true}, acl.OutcomeCancelled, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewPermissionManager(2 * time.Second)
			req := acl.PermissionRequest{ToolCall: acl.ToolCallInfo{ID: "tc1", Title: "运行命令"}, Options: tc.options}
			got := make(chan acl.PermissionDecision, 1)
			go func() { got <- m.RegisterAndWait(context.Background(), "s1", req, nil) }()
			waitRegistered(t, m, "s1")
			callID := pendingCallID(t, m, "s1")
			found, out := m.ResolveDecision(callID, tc.d)
			if !found {
				t.Fatal("决策未命中 call_id")
			}
			if out.Kind != tc.kind || out.OptionID != tc.selected {
				t.Fatalf("回执期望 (%s,%q)，得到 (%s,%q)", tc.kind, tc.selected, out.Kind, out.OptionID)
			}
			<-got // 等消费者退出，避免泄漏
		})
	}
	// 未知 call_id：found=false + 零值回执（不猜测）。
	m := NewPermissionManager(time.Second)
	if found, out := m.ResolveDecision("no-such", acl.PermissionDecision{Allowed: true}); found || out.Kind != "" {
		t.Fatalf("未知 call_id 应为 (false, 零值)，得到 (%v,%+v)", found, out)
	}
}
