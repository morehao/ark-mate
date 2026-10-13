package manager

import (
	"testing"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestBuiltinAgentSeedsMatchBuiltinSpecs 种子 agent 表必须与内置 adapter 清单**逐条对齐**。
//
// # 它挡的是什么
//
// `builtinAgents` 与 `runtime.AllBuiltinSpecs()` 是**两份**手工维护的清单，而它们之间的错位完全
// 静默：
//
//   - 多一条（清单里没有的 id）→ 播出一个指向未装配底座的 agent，建会话时才报 runtime_not_found；
//   - 少一条（清单里有、表里没有）→ 那个内置底座没有可用 agent，`defaultAgentID()` 回落成它的 id
//     之后 `requireDefaultAgent` 报「无可用 Agent」——一个**假**错误（分明确实有一个可用 agent）。
//
// 两个方向都不会让别的测试变红：其余用例要么看 `AllBuiltinSpecs()`、要么自己登记夹具 agent，
// 谁都不去比对这两份表。所以这里逐条比。
//
// # 三个子判据（缺一不可）
//
//	① 数量相等（多/少都红）；
//	② 每条 `runtime` 都是内置清单里的 id，且**表里没有重复**；
//	③ 每条 `id` 与它的 `runtime` **同形**——`defaultAgentID()` 直接返回 `runtime.SelectedRuntime()`，
//	   而种子 agent 的查找键是 `id`：两者不同形时"构建期选中的底座"会指向一个不存在的 agent。
//
// fail-closed：`AllBuiltinSpecs()` 为空即失败（否则两条空清单"恰好相等"，判据空转）。
func TestBuiltinAgentSeedsMatchBuiltinSpecs(t *testing.T) {
	builtin := runtime.AllBuiltinSpecs()
	if len(builtin) == 0 {
		t.Fatal("AllBuiltinSpecs() 返回空：本判据没有对照面（fail-closed）")
	}
	want := map[string]string{} // runtime id → agent id
	for _, sp := range builtin {
		if _, dup := want[sp.ID]; dup {
			t.Fatalf("内置清单里 %q 重复：清单本身有问题，先修它", sp.ID)
		}
		want[sp.ID] = sp.ID
	}
	got := map[string]string{}
	for _, b := range builtinAgents {
		if _, dup := got[b.runtime]; dup {
			t.Errorf("builtinAgents 里 runtime %q 出现两次：每次装配都会多播一条同 id 的 agent", b.runtime)
		}
		got[b.runtime] = b.id
		if b.id != b.runtime {
			t.Errorf("种子 agent %q 的 id 与它服务的 runtime %q 不同形——`defaultAgentID()` 按 runtime id "+
				"找 agent，不同形会让「构建期选中的那个」指向一个不存在的 agent", b.id, b.runtime)
		}
	}
	for rt, agentID := range want {
		if g, ok := got[rt]; !ok {
			t.Errorf("内置 adapter %q 没有对应的种子 agent：这个底座没有任何可用 agent，"+
				"而 `defaultAgentID()` 会回落成它的 id → `requireDefaultAgent` 报「无可用 Agent」"+
				"（一个假错误：分明确实有一个可用 agent）", rt)
		} else if g != agentID {
			t.Errorf("内置 adapter %q 的种子 agent id 应为 %q，表中是 %q", rt, agentID, g)
		}
	}
	for rt := range got {
		if _, ok := want[rt]; !ok {
			t.Errorf("builtinAgents 里的 %q 不在内置清单里：会播出一个指向未装配底座的 agent"+
				"（建会话时才报 runtime_not_found），也说明「内置清单只有一个事实源」被破坏了", rt)
		}
	}
}
