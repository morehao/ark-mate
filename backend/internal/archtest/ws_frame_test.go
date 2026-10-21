package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **ADR-13 回放区间帧**的守卫（P0-4：`replay_begin` / `replay_end`）。
//
// 判据三件，各自钉一种静默失效：
//
//	① **两侧都有、且 begin 恒在 end 前**（Go 的 `attach` 与前端 `applyFrame` 都必须认）：
//	   只做一侧的表现是"服务端发了、前端当未知帧丢掉"——加载态照旧靠 12s 超时退出，
//	   而这件事在界面上完全看不出来（只是慢一点）。
//	② **顺序**：begin 必须在回放循环**之前**、end 在**之后**。两帧都存在但位置错了（例如都在
//	   循环之后）仍然"看起来实现了"，而前端会在回放中途就以为回放已结束。
//	③ **它们不是域事件**：`replay_begin` / `replay_end` 不得出现在事件 type 常量与冻结清单里。
//	   这一条最容易被"顺手统一"掉——帧 type 与事件 type 在代码里长得一样（都是字符串），
//	   一旦有人把 `replay_begin` 塞进 `events.go`，它就会进 `events` 表、进 transcript、
//	   进前端 `Ev` 联合类型，而**所有测试照样绿**（事件契约测试只比对"两边相等"，多一条也相等）。
//	   语义上那是错的：回放描述的是"**这次连接**正在做什么"，不是"会话身上发生过什么"——
//	   同一次回放换成两个客户端就是两段不同的区间，写进事件日志等于把"谁的连接"也记进了历史。
//
// fail-closed：任一侧的锚找不到即失败（改名/搬迁 = 判据失锚，不许静默空转）。

// TestReplayFramesBracketHistory 见文件头。
func TestReplayFramesBracketHistory(t *testing.T) {
	root := backendRoot(t)
	feRoot := filepath.Join(root, "..", "frontend")

	// ① + ② Go 侧：`attach` 里 begin 在回放循环前、end 在其后。
	wsPath := filepath.Join(root, "internal", "adapters", "inbound", "ws", "ws.go")
	ws := stripLineComments(readFileOrFatal(t, wsPath))
	beginAt := strings.Index(ws, `Type: "replay_begin"`)
	endAt := strings.Index(ws, `Type: "replay_end"`)
	if beginAt < 0 || endAt < 0 {
		t.Fatalf("%s 里没有 `replay_begin` / `replay_end` 帧（begin=%d end=%d）："+
			"ADR-13 的加载态精确结束就靠这两帧，没有它前端只能靠 12s 超时猜", wsPath, beginAt, endAt)
	}
	if beginAt > endAt {
		t.Fatalf("%s 里 `replay_begin` 在 `replay_end` **之后**：`replay_begin` 恒在前", wsPath)
	}
	// 回放循环必须在两帧之间（判据取 `EventsAfter(` 这个唯一的重放取数点）。
	loopAt := strings.Index(ws, "h.bus.EventsAfter(")
	if loopAt < 0 {
		t.Fatalf("%s 里找不到 `h.bus.EventsAfter(`（回放取数点，fail-closed）", wsPath)
	}
	if !(beginAt < loopAt && loopAt < endAt) {
		t.Fatalf("%s 里回放取数点 `h.bus.EventsAfter(` 没有落在 begin(%d) 与 end(%d) 之间（loop=%d）："+
			"两帧的位置错了 — 前端会在回放**中途**以为回放已结束", wsPath, beginAt, endAt, loopAt)
	}

	// ① 前端两侧：类型联合 + 帧处理分支。
	wsTypesPath := filepath.Join(feRoot, "packages", "types", "src", "ws.ts")
	wsTypes := stripTSComments(readFileOrFatal(t, wsTypesPath))
	for _, typ := range []string{"'replay_begin'", "'replay_end'"} {
		if !strings.Contains(wsTypes, typ) {
			t.Errorf("%s 的 `WsServerFrame` 联合里没有 %s：服务端发了前端不认，帧被当未知丢掉", wsTypesPath, typ)
		}
	}
	storePath := filepath.Join(feRoot, "packages", "console", "src", "store.ts")
	store := stripTSComments(readFileOrFatal(t, storePath))
	for _, branch := range []string{"case 'replay_begin':", "case 'replay_end':"} {
		if !strings.Contains(store, branch) {
			t.Errorf("%s 的 `applyFrame` 里没有 %q：加载态不会由回放帧结束", storePath, branch)
		}
	}

	// ③ 它们**不是**域事件：事件常量与冻结清单里零命中。
	if strings.Contains(ws, `"replay_begin"`) == false {
		t.Fatalf("%s 的帧 type 字面量不见了（fail-closed）", wsPath)
	}
	eventsPath := filepath.Join(root, "internal", "domain", "session", "events.go")
	events := stripLineComments(readFileOrFatal(t, eventsPath))
	for _, banned := range []string{"replay_begin", "replay_end"} {
		if strings.Contains(events, banned) {
			t.Fatalf("%s 里出现了 %q：回放区间帧是**帧 type，不是域事件**——它描述的是"+
				"「这次连接正在做什么」，不是「会话身上发生过什么」（同一次回放换成两个客户端就是"+
				"两段不同的区间）。写进事件常量会让它进 events 表 / transcript / 前端 Ev 联合类型，"+
				"而所有契约测试照样绿（它们只比对「两边相等」）", eventsPath, banned)
		}
	}
	for _, ev := range frozenEventTypes {
		if ev == "replay_begin" || ev == "replay_end" {
			t.Fatalf("冻结事件清单里出现了 %q：帧 type 不得进事件契约", ev)
		}
	}
}
