package archtest

import (
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 **挂起提问「超时 ≠ 取消」** 的两条守卫（ADR-11 / P1-d）。
//
// 被钉的两种腐化都不会让任何既有测试变红：
//
//	① **超时那一支被改回 cancel**：卡片消失、用户去倒杯水回来就得把整轮重来一遍。看起来
//	   "更干脆"，而所有测试照旧绿——因为 `cancel` 本来就是这一支的旧行为；
//	② **作废路径不发事件**：重启之后历史里那条 `elicitation_request` 还在，前端回放就渲染出一张
//	   背后没有任何挂起项的卡片——**看起来能点，点下去才发现不行**，或者用户一直等一个不会来的答复。

// TestElicitationTimeoutBecomesPending 超时 → `pending`（不是 `cancel`）。
func TestElicitationTimeoutBecomesPending(t *testing.T) {
	root := backendRoot(t)

	// ① 超时分支铸的是 `pending`，且**不**删挂起项（"仍可回答"的物理前提）。
	src := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "interaction", "elicitation.go")))
	timerCase, ok := switchCaseBlock(src, "case <-timer.C:")
	if !ok {
		t.Fatalf("elicitation.go 里找不到 `case <-timer.C:` 分支（改名/重构了？本守卫 fail-closed）")
	}
	if !strings.Contains(timerCase, "actionPending") {
		t.Fatalf("超时分支没有铸 `actionPending`：\n超时不等于取消（ADR-11）——30 分钟无人回答只说明" +
			"现在没人看着」，不说明「用户不想答」。这一支回到 `cancel` 的后果是卡片消失、\n" +
			"用户去倒杯水回来就得把整轮重来一遍，而**所有测试照旧绿**（`cancel` 本来就是它的旧行为）")
	}
	// 「仍可回答」的物理前提是那个挂起项**还在** map 里，且 `timeout_at` 被清空。
	//
	// 判据看**函数体里的先后**而不是"超时分支里有没有 delete"：今天那句 `delete(m.pending, id)`
	// 落在 `pendingLate` 的 `else` 里（非超时路径要删），按分支扫会把它算成"超时也删了"——假红。
	// 真正要钉的是：`p.late = true` / 清空 `p.timeout` 在**前**，无条件删除在**后**。
	waitBody, ok := goFuncBody(src, "func (m *ElicitationManager) RegisterAndWait(")
	if !ok {
		t.Fatalf("取不到 `RegisterAndWait` 的函数体（fail-closed）")
	}
	atLate := strings.Index(waitBody, "p.late = true")
	atClear := strings.Index(waitBody, "p.timeout = time.Time{}")
	atDelete := strings.Index(waitBody, "delete(m.pending, id)")
	if atLate < 0 || atClear < 0 {
		t.Fatalf("`RegisterAndWait` 里没有「转 pending」的两步（`p.late = true` / `p.timeout = time.Time{}`）：" +
			"卡片保留可回答的物理前提就是它**还在** map 里、且不再起倒计时")
	}
	if atDelete < 0 {
		t.Fatalf("`RegisterAndWait` 里找不到 `delete(m.pending, id)`：非超时路径必须抹掉挂起项" +
			"（否则它永远挂着）")
	}
	if atDelete < atLate || atDelete < atClear {
		t.Fatalf("`delete(m.pending, id)` 排在「转 pending」之前：超时那一刻挂起项就被删了，" +
			"`pending` 只是一句安慰话——用户看到的卡片背后已经什么都没有了")
	}

	// ② 两侧的 wire 取值**逐字相等**（两个包不能互相 import，所以两份常量必然并存）。
	domain := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "events.go")))
	if !strings.Contains(domain, `ElicitationActionPending = "pending"`) {
		t.Fatalf("`domain/session` 里没有 `ElicitationActionPending = \"pending\"`：" +
			"它是 `session.elicitation_resolved` 的 `action` 取值，前端按它决定卡片收不收敛")
	}
	if !strings.Contains(src, `const actionPending = "pending"`) {
		t.Fatalf("`domain/interaction` 里没有 `const actionPending = \"pending\"`：" +
			"两侧取值必须逐字相等，否则事件发的值与前端认的值会静默分叉")
	}

	// ③ `late` 回答入口必须在（超时之后"还能答"的那条路）。
	if !strings.Contains(src, "func (m *ElicitationManager) ResolveLate(") {
		t.Fatalf("elicitation.go 里找不到 `ResolveLate`：转成 pending 之后必须有一条" +
			"「回答一个已经超时的提问」的入口——没有它，pending 只是一个不会收敛的状态")
	}
	if !strings.Contains(src, "m.DeleteSession") && !strings.Contains(src, "delete(m.pending, id)") {
		t.Fatalf("elicitation.go 里没有抹掉挂起项的地方（`ResolveLate` 里那句 `delete(m.pending, id)`）：" +
			"pending 永不收敛 = 卡片永远挂着")
	}

	// ④ 前端：`pending` 必须进 `ElicitationAction`，且请求体有 `late`（迟到回答显式声明）。
	//
	// 这两条都是"服务端说了、前端不认识"的典型：事件发 `pending` 而联合类型里没有它，卡片就
	// 只会被当成未知状态；请求体没有 `late`，迟到回答就只能被当成"还在等"投进一个已关闭的通道
	// （表现是**点了发送、什么都没发生**）。
	fePath := filepath.Join(root, "..", "frontend", "packages", "types", "src", "session.ts")
	fe := stripTSComments(readFileOrFatal(t, fePath))
	if !strings.Contains(fe, "export type ElicitationAction = 'accept' | 'decline' | 'cancel' | 'pending'") {
		t.Fatalf("%s 的 `ElicitationAction` 里没有 `'pending'`：服务端会发它，而界面不认——\n"+
			"那一档是 ADR-11 的全部意义所在（卡片保留可回答），认不出来就等于没实现", fePath)
	}
	if !strings.Contains(fe, "late?: boolean") {
		t.Fatalf("%s 的 `ResolveElicitationReq` 里没有 `late?: boolean`：迟到回答必须显式声明"+
			"（服务端不替你猜——两个方向的猜错都很难看）", fePath)
	}
}

// TestPendingElicitationIsStaleOnRestore 重启/归档 → 作废**并发事件**。
func TestPendingElicitationIsStaleOnRestore(t *testing.T) {
	root := backendRoot(t)

	// ① 作废实现存在、被 `Restore` 调用、且发的是 `session.elicitation_resolved`。
	persist := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "persist.go")))
	if !strings.Contains(persist, "func (pl *Plane) cancelDanglingElicitations(") {
		t.Fatalf("persist.go 里找不到 `cancelDanglingElicitations` 实现")
	}
	if !strings.Contains(persist, "pl.cancelDanglingElicitations(") {
		t.Fatalf("`cancelDanglingElicitations` 没有**被调用**：「有实现没接线」是这一族最典型的静默失效\n" +
			"——重启之后那些卡片照样挂着，而代码看起来「已经处理了」")
	}
	body, ok := goFuncBody(persist, "func (pl *Plane) cancelDanglingElicitations(")
	if !ok {
		t.Fatalf("取不到 `cancelDanglingElicitations` 的函数体（fail-closed）")
	}
	if !strings.Contains(body, "pl.Publish(BusElicitationResolved") {
		t.Fatalf("`cancelDanglingElicitations` 没有发 `session.elicitation_resolved`：" +
			"**事件是前端收敛卡片的唯一事实**——删表项不发事件等于留一张永远回答不了的卡")
	}

	// ② 它必须排在**事件注入之后**（理由与 S7 的收口逐字相同：先发会把 bus 的位次推到最新，
	//    随后逐条 `PublishAt` 回放的历史事件会被逐个顶号——位次是续传游标）。
	restoreBody, ok := goFuncBody(persist, "func (pl *Plane) Restore(")
	if !ok {
		t.Fatalf("取不到 `Restore` 的函数体（fail-closed）")
	}
	atCancel := strings.Index(restoreBody, "pl.cancelDanglingElicitations(")
	atInject := strings.Index(restoreBody, "pl.bus.PublishAt(")
	if atCancel < 0 || atInject < 0 {
		t.Fatalf("`Restore` 里找不到调用点或注入点（atCancel=%d atInject=%d）", atCancel, atInject)
	}
	if atCancel < atInject {
		t.Fatalf("`cancelDanglingElicitations` 排在事件注入**之前**：那些收口事件是新铸的，" +
			"先发会把 bus 的 `nextSeq` 推到最新，随后回放的历史事件被逐个顶号——" +
			"而表面上一切正常")
	}

	// ③ 归档那条路也必须在，且走的是**同一个**收口铸造点（不是另写一份"删掉表项"）。
	meta := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "service", "svcsession", "meta.go")))
	if !strings.Contains(meta, "m.deps.CancelElicitations(id)") {
		t.Fatalf("会话归档时没有作废挂起提问（`m.deps.CancelElicitations(id)`）：归档之后会话不该再接受\n" +
			"回答，而挂起项还在的话**那张卡片既点得动又没人收**")
	}
	domain := stripLineComments(readFileOrFatal(t, filepath.Join(root, "internal", "domain", "session", "events.go")))
	for _, want := range []string{`ElicitationReasonRestart = "restart"`, `ElicitationReasonArchive = "archive"`, `ElicitationReasonLate = "late"`} {
		if !strings.Contains(domain, want) {
			t.Fatalf("`domain/session` 里没有 %q：三个平台侧终局原因必须各有其值——"+
				"「这一轮被取消」「进程重启了」「会话归档了」是三件事，用同一个值就只能靠猜", want)
		}
	}
}
