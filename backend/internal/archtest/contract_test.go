package archtest

import (
	"sort"
	"testing"

	"github.com/morehao/ark-forge/internal/manager"
)

// frozenEventTypes 是控制面**对外契约**的事件 type 全集（`/v1/forge/ws/events` 帧的
// `type` 字段 + sqlite `events.type` 列 + transcript 回放）。前端 @ark/types 的
// events.ts 按这些字面量做联合类型，历史数据里也存着它们。
//
// 因此：新增事件是**兼容变更**（改这个列表 + 前端），改名/删除是**破坏性变更**
// （会让历史 events 行与前端联合类型同时失配）。本快照让后者无法静默发生。
//
// 权威定义在 internal/manager/events.go（P2/P4 拆包后随之迁移，本测试同步更新）。
var frozenEventTypes = []string{
	"session.created",
	"session.resumed",
	"session.user_message",
	"session.message",
	"session.thought",
	"session.tool_call",
	"session.tool_update",
	"session.usage",
	"session.permission",
	"session.config_options",
	"session.permission_request",
	"session.permission_policy",
	"session.permission_resolved",
	"session.elicitation_request",
	"session.elicitation_resolved",
	"session.runtime",
	"turn.ended",
	"turn.error",
	"run.cancelled",
}

// TestEventTypeContractFrozen 冻结域事件 type 字面量。
func TestEventTypeContractFrozen(t *testing.T) {
	got := []string{
		manager.BusSessionCreated,
		manager.BusSessionResumed,
		manager.BusUserMessage,
		manager.BusMessage,
		manager.BusThought,
		manager.BusToolCall,
		manager.BusToolUpdate,
		manager.BusUsage,
		manager.BusPermission,
		manager.BusConfigOptions,
		manager.BusPermissionRequest,
		manager.BusPermissionPolicy,
		manager.BusPermissionResolved,
		manager.BusElicitationRequest,
		manager.BusElicitationResolved,
		manager.BusRuntime,
		manager.BusTurnEnded,
		manager.BusTurnError,
		manager.BusRunCancelled,
	}
	sort.Strings(got)
	want := append([]string(nil), frozenEventTypes...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("事件 type 数量变化：契约 %d 个，代码 %d 个", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个事件 type 不一致：契约 %q，代码 %q", i, want[i], got[i])
		}
	}
}
