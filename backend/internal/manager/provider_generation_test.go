package manager

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtoprovider"
)

// TestProviderKeyChangeSwitchesGenerationNextTurn A2 的**端到端**判据：改密钥/类型在**下一轮**生效。
//
// 设计里 A2 的原文是"建会话 → 发一轮 → PATCH 改 provider `type`（改变注入变量名）→ 再发一轮；
// 断言新连接的 `connKey` 变化、旧代际被回收"。这条走 `genRig` 的**真实入口**
// （`startConversationOn` / `acquireConversation`，也就是 Prompt 与排队投递走的那条路），
// 断言的是**可观察事实**：句柄换了、连接换了、旧代际被回收、注入真的按新类型走。
//
// 为什么不满足于"判定函数返回了 switch"：判定再对，句柄没换也等于没迁（那正是这条用例存在的理由，
// 与 `generation_migration_test.go` 开头那段话同一个口径）。
func TestProviderKeyChangeSwitchesGenerationNextTurn(t *testing.T) {
	ctx := context.Background()
	r := newGenRig(t)
	// 让 vault 可用：密钥要真落盘，拨号基键才会带上注入的那条 env。
	r.svc.mu.Lock()
	r.svc.agentDir = t.TempDir()
	r.svc.mu.Unlock()

	const secret = "sk-a2-secret"
	if _, err := r.svc.CreateProvider(ctx, providerReq("p1", ptrOf(secret))); err != nil {
		t.Fatalf("建档案: %v", err)
	}
	agent, _ := r.svc.AgentByID("a1")
	agent.Provider = "p1"

	convID, as1 := r.start(t, nil)
	// 先立住"没变就不切"这一半：否则下面那次切换可能只是"每轮都换"。
	if got := r.boundary(t, convID); got != as1 {
		t.Fatal("配置未变时不得换代际（每轮都换 = 白丢上下文）")
	}

	// 改密钥：v12 里走 **env 通道**的底座上，"进程级配置面变了"的杠杆就是密钥本身
	// （v11 的 `type` 已删——它决定注入变量名那条路径也一并没了，引用名改为逐供应商派生）。
	//
	// 端点的改动不在这里验：它影响的是**交付物**（配置内容），其代际效应由同步摘要承载
	// （ADR-13：摘要并进代际键），而那个摘要只有真正交付过的 runtime 才有。
	next := "sk-new"
	resp, err := r.svc.UpdateProvider(ctx, "p1", dtoprovider.ProviderPatchReq{APIKey: &next})
	if err != nil {
		t.Fatalf("改密钥: %v", err)
	}
	// 回执如实说明"下一轮生效"（C5：与代际裁决**同源**）。
	if resp.Effect == nil || resp.Effect.Channel != "dial_env" || resp.Effect.Effect != "next_turn" {
		t.Fatalf("生效面回执应为 dial_env/next_turn: %+v", resp.Effect)
	}
	if resp.Effect.AffectedConversations < 1 {
		t.Fatalf("应有会话受这条改动影响: %+v", resp.Effect)
	}

	as2 := r.boundary(t, convID)
	if as2 == as1 {
		t.Fatal("改密钥后应在**下一轮边界**换代际（A2）")
	}
	if as2.client == as1.client {
		t.Fatal("新代际必须落在**另一个**连接上（注入是进程级的，共用连接等于旧密钥还活着）")
	}
	if n := r.waitConns(t, 1); n != 1 {
		t.Fatalf("旧代际无人引用即回收，期望只剩 1 个连接，got %d", n)
	}
	as1.mu.Lock()
	dead := as1.dead
	as1.mu.Unlock()
	if !dead {
		t.Fatal("旧句柄应被标废")
	}
}
