package session

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/platform/object/objid"
)

// P3b-2b：本文件是 `manager/transcript_load_test.go` 的**装载面那一半**，随装载代码搬进本域。
// 那些要 manager 装配链（`SessionHistory` / `enableStore` 逃生门）的用例留在 manager 侧，
// 按同一份形状夹具验同一件事——两边不互相 import，判据各自独立可跑。

// TestLoadTranscriptsReseed 回读 transcript → 会话列表重建（标题/状态/时间）+ 事件注入 bus。
func TestLoadTranscriptsReseed(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyA + `","agent_id":"agent-rtx","runtime":"rtx","cwd":"/tmp","created_at":"2026-09-07T03:12:40Z"}`
	lines := []string{
		header,
		`{"id":"` + evtA1 + `","seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"列出当前目录文件"}}`,
		`{"id":"` + evtA2 + `","seq":3,"ts":"2026-09-07T03:12:42Z","type":"session.thought","payload":{"text":"thinking"}}`,
		`{"id":"` + evtA3 + `","seq":4,"ts":"2026-09-07T03:12:43Z","type":"run.ended","payload":{"stop_reason":"end_turn"}}`,
	}
	writeTranscriptJSONL(t, dir, sesLegacyA+".jsonl", lines)

	pl, bus := testPlane(t, dir)

	sessions, err := pl.ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望回灌 1 个会话，got %d: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	if s.ID != sesLegacyA || s.Title != "列出当前目录文件" || s.Status != SessionStatusCompleted {
		t.Fatalf("回灌元数据不符: %+v", s)
	}
	if s.CreatedAt == "" || s.UpdatedAt == "" {
		t.Fatalf("回灌时间缺失: %+v", s)
	}

	// 事件已注入 bus（v17：身份与位次都**原样恢复**，不按行号重铸 seq）。
	evts := bus.EventsAll()
	if len(evts) != 3 {
		t.Fatalf("期望注入 3 条事件，got %d", len(evts))
	}
	if evts[0].AggregID != sesLegacyA || evts[2].Type != BusRunEnded {
		t.Fatalf("注入事件不符: %+v", evts)
	}
	for i, want := range []struct {
		id  string
		seq int64
	}{{evtA1, 2}, {evtA2, 3}, {evtA3, 4}} {
		if evts[i].ID != want.id || evts[i].Seq != want.seq {
			t.Fatalf("第 %d 条事件的 id/seq 未原样恢复: got (%q,%d) want (%q,%d)",
				i, evts[i].ID, evts[i].Seq, want.id, want.seq)
		}
	}
}

// TestMemoryModeRestartKeepsEventIdentity 是 v17 要修的那个缺陷的**端到端**判据：
// memory 模式下重启一次，同一个事件的 id 与 seq 必须**逐字不变**。
//
// 旧实现在这里必红（三处叠在一起）：
//   - transcript 行只写 `type`/`payload`（`seq` 写了但读回时被丢掉）；
//   - 装载时 `version` 用 `len(lines)+1` 重推、`seq` 用 `bus.Publish` 现取号 → 每次重启重编号；
//   - 逐文件注入让跨会话的顺序按**文件名字典序**决定。
//
// 为什么这条不能只靠"装载后 bus 里有 3 条事件"那种断言：条数一样、类型一样、甚至 seq 常常
// 也"看起来对"（从 1 开始重编），而用户手里的书签/引用指的是**上一轮**那些号——只有把两轮
// 的 (id, seq) 逐条比对才能看出区别。
func TestMemoryModeRestartKeepsEventIdentity(t *testing.T) {
	dir := t.TempDir()

	// —— 第一轮：memory 模式（无 store）**交错**跑两个会话，让位次不是"文件内 1..n"。
	//
	// 交错是关键：单会话 transcript 的位次恰好是 1,2,3…，于是"装载时重新取号"与"原样恢复"
	// 会给出**一模一样**的结果（这正是这个缺陷能活这么久的原因）。两个会话交错时，按文件顺序
	// 装载会把 B 的位次排到 A 后面，与 seq 序不同——判据才立得住。
	pl1, bus1 := testPlane(t, dir)
	pl1.BeginTranscript(sesLegacyA, "rtx", "/tmp", false)
	pl1.Publish(BusUserMessage, sesLegacyA, map[string]any{"session_id": sesLegacyA, "text": "a1"})
	pl1.BeginTranscript(sesLegacyB, "rtx", "/tmp", false)
	pl1.Publish(BusMessage, sesLegacyB, map[string]any{"session_id": sesLegacyB, "text": "b1"})
	pl1.Publish(BusMessage, sesLegacyA, map[string]any{"session_id": sesLegacyA, "text": "a2"})
	pl1.CloseTranscripts()
	before := bus1.EventsAll()
	if len(before) != 3 {
		t.Fatalf("第一轮应有 3 条事件，got %d", len(before))
	}
	// 前提自检：**发布顺序 ≠ `(会话, 位次)` 注入顺序**（否则这条用例区分不了对错实现）。
	// 两个会话交错发布时必然不同：发布序是 0,1,2 那种跨会话交替，注入序是把同一会话的
	// 位次收在一起（v21 起位次只在会话内可比，跨会话相对顺序不承载语义）。
	grouped := []eventbus.Event{}
	for _, sid := range []string{sesLegacyA, sesLegacyB} {
		for _, e := range before {
			if e.AggregID == sid {
				grouped = append(grouped, e)
			}
		}
	}
	sameOrder := true
	for i := range before {
		if grouped[i].ID != before[i].ID {
			sameOrder = false
			break
		}
	}
	if sameOrder {
		t.Fatalf("夹具没造成「发布序 ≠ 注入序」：%+v", before)
	}

	// —— 第二轮：**新的** Plane + 新的 bus（= 重启），transcript 目录不变。
	pl2, bus2 := testPlane(t, dir)
	if sessions, _ := pl2.ListSessions(false, "", ""); len(sessions) != 2 {
		t.Fatalf("重启后应从 transcript 回灌 2 个会话，got %d", len(sessions))
	}
	after := bus2.EventsAll()
	if len(after) != len(before) {
		t.Fatalf("重启后事件数应不变：%d → %d", len(before), len(after))
	}
	// 按**事件身份**比对（v21 起不能按下标：注入顺序是 `(会话, 位次)` 分组有序，与第一轮的
	// 发布顺序不同，按下标比会把"顺序变了"误报成"身份/位次变了"）。
	byID := make(map[string]eventbus.Event, len(after))
	for _, e := range after {
		byID[e.ID] = e
	}
	for _, b := range before {
		a, ok := byID[b.ID]
		if !ok {
			t.Fatalf("重启后少了事件 %s（身份没随数据走）", b.ID)
		}
		if a.Seq != b.Seq || a.Type != b.Type || a.AggregID != b.AggregID {
			t.Fatalf("重启后事件的 (seq,type,agg) 变了：\n 重启前 (%s,%d,%s,%s)\n 重启后 (%s,%d,%s,%s)\n"+
				"（同一个事件跨重启必须是同一支身份与同一个位次——这正是 v17 要钉住的东西）",
				b.ID, b.Seq, b.Type, b.AggregID, a.ID, a.Seq, a.Type, a.AggregID)
		}
	}
	// v21 的注入顺序契约：同一会话内位次严格升序（分组有序）。
	prev := map[string]int64{}
	for _, e := range after {
		if p, ok := prev[e.AggregID]; ok && e.Seq <= p {
			t.Fatalf("会话 %s 的注入位次未升序: %d 之后是 %d", e.AggregID, p, e.Seq)
		}
		prev[e.AggregID] = e.Seq
	}
	// 身份必须真的是平台形状（不是空串、不是从别处抄来的会话 id）。
	for _, e := range after {
		if !objid.IsPlatformID(e.ID) || !strings.HasPrefix(e.ID, objid.Event) {
			t.Fatalf("重启后的事件身份形状不对：%q", e.ID)
		}
	}
}

// TestLoadTranscriptsKeepsPerSessionSeqOrder 装载必须按 **`(会话, seq)` 升序**注入
// （v17 的"位次不重铸"在 v21 的口径下要的那个顺序）。
//
// 为什么这条必须单独测：装载是**逐文件**读的，而文件名的字典序与事件位次无关——
// 同一个会话理论上只有一份 transcript，但外部同步/复制会造出第二份。逐文件 `PublishAt` 时，
// 先读到的那份会把 bus 里**该会话**的水位抬高，后读到那份的小位次只能走"取号回落"——
// **位次被改写**，同一个事件每次重启换一个 seq，客户端书签随之失效。
//
// 夹具就是这么造的：会话 A 有两份文件，字典序在前的那份（`-a`）持有**较大**的位次（5），
// 在后那份（`-b`）持有较小的位次（1）。按文件名顺序注入 → 5 在前、1 只能被顶到 6；
// 按 `(会话, 位次)` 注入 → 1、5 原样。
func TestLoadTranscriptsKeepsPerSessionSeqOrder(t *testing.T) {
	dir := t.TempDir()
	headerA := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyA + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	headerB := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyB + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyA+"-a.jsonl", []string{
		headerA,
		`{"id":"` + evtA2 + `","seq":5,"ts":"2026-09-07T03:12:45Z","type":"session.user_message","payload":{"text":"a5"}}`,
	})
	writeTranscriptJSONL(t, dir, sesLegacyA+"-b.jsonl", []string{
		headerA,
		`{"id":"` + evtA1 + `","seq":1,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"a1"}}`,
	})
	writeTranscriptJSONL(t, dir, sesLegacyB+".jsonl", []string{
		headerB,
		`{"id":"` + evtB1 + `","seq":2,"ts":"2026-09-07T03:12:42Z","type":"session.user_message","payload":{"text":"b2"}}`,
	})

	_, bus := testPlane(t, dir)

	all := bus.EventsAll()
	if len(all) != 3 {
		t.Fatalf("期望注入 3 条事件（A 两份文件各 1 条 + B 1 条），got %d: %+v", len(all), all)
	}
	want := []struct {
		seq int64
		agg string
		id  string
	}{
		{1, sesLegacyA, evtA1},
		{5, sesLegacyA, evtA2},
		{2, sesLegacyB, evtB1},
	}
	for i, w := range want {
		if all[i].Seq != w.seq || all[i].AggregID != w.agg || all[i].ID != w.id {
			t.Fatalf("第 %d 条事件的 (seq,agg,id) = (%d,%s,%s)，期望 (%d,%s,%s)——"+
				"同一会话必须先按位次升序再注入（否则小位次会被取号回落顶掉）",
				i, all[i].Seq, all[i].AggregID, all[i].ID, w.seq, w.agg, w.id)
		}
	}
}

// TestLoadTranscriptsSkipsV1Schema v17：**v1 格式整份跳过 + 计数**（不代铸 id/seq）。
//
// 为什么不能"宽容回读"：v1 行里的 `seq` 只是**文件内位次**，而装载面现在把它当全局水位注入
// bus——两套语义混起来，结果是"位次看起来对、跨重启却对不上"，正是 v17 要消灭的那件事。
func TestLoadTranscriptsSkipsV1Schema(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v1","version":1,"session_id":"` + sesLegacyC + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyC+".jsonl", []string{
		header,
		`{"seq":1,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})

	before := LegacySchemaSkips()
	pl, bus := testPlane(t, dir)
	sessions, _ := pl.ListSessions(false, "", "")
	if len(sessions) != 0 {
		t.Fatalf("v1 格式文件不该被装载: %+v", sessions)
	}
	if got := LegacySchemaSkips() - before; got != 1 {
		t.Fatalf("被跳过的 v1 文件计数不符: got %d want 1", got)
	}
	if events := bus.EventsAll(); len(events) != 0 {
		t.Fatalf("v1 文件不该注入任何事件，got %d 条", len(events))
	}
}

// TestLoadTranscriptsRejectsLineWithoutIdentity：header 是 v2 但事件行缺 id/seq（半截写入 /
// 手工改动）→ 整份跳过，而不是"补一个"。补出来的身份是装载面自己发明的。
func TestLoadTranscriptsRejectsLineWithoutIdentity(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyC + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyC+".jsonl", []string{
		header,
		`{"seq":1,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})

	pl, bus := testPlane(t, dir)
	if sessions, _ := pl.ListSessions(false, "", ""); len(sessions) != 0 {
		t.Fatalf("缺身份的事件行应让整份文件被跳过: %+v", sessions)
	}
	if events := bus.EventsAll(); len(events) != 0 {
		t.Fatalf("跳过的文件不该注入事件，got %d 条", len(events))
	}
}

// TestLoadTranscriptsSkipsLegacyShapeID v15（Q10）：旧形状的会话 id **跳过 + 计数**，不做派生。
//
// 为什么必须拒收：旧 transcript 的 header 里是裸 uuid，放行会让数据面同时存在
// `ses_…` 与裸 uuid 两种平台会话 id —— 形状不变式只写在代码里，数据里的形状没有任何守卫看得见。
func TestLoadTranscriptsSkipsLegacyShapeID(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"c-6f1e-legacy","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, "legacy.jsonl", []string{
		legacy,
		`{"id":"` + evtC1 + `","seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})

	before := LegacyTranscriptSkips()
	pl, _ := testPlane(t, dir)
	sessions, _ := pl.ListSessions(false, "", "")
	if len(sessions) != 0 {
		t.Fatalf("旧形状会话不该被装载: %+v", sessions)
	}
	if got := LegacyTranscriptSkips() - before; got != 1 {
		t.Fatalf("被跳过的旧形状文件计数不符: got %d want 1", got)
	}
}

// TestLoadTranscriptsNoTerminal 无终态事件 → 状态 in_progress（供 UI 停止自愈）。
func TestLoadTranscriptsNoTerminal(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyB + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyB+".jsonl", []string{
		header,
		`{"id":"` + evtB1 + `","seq":2,"ts":"2026-09-07T03:12:41Z","type":"session.user_message","payload":{"text":"hi"}}`,
	})
	pl, _ := testPlane(t, dir)
	sessions, _ := pl.ListSessions(false, "", "")
	if len(sessions) != 1 || sessions[0].Status != SessionStatusInProgress {
		t.Fatalf("无终态应为 in_progress: %+v", sessions)
	}
}

// TestLoadTranscriptsIdempotent 对同一会话重复回灌不重复注册。
func TestLoadTranscriptsIdempotent(t *testing.T) {
	dir := t.TempDir()
	header := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesLegacyC + `","agent_id":"agent-rtx","runtime":"rtx","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesLegacyC+".jsonl", []string{header})
	pl, _ := testPlane(t, dir)
	pl.SetTranscriptDir(dir) // 二次调用：也应幂等（无重复注入/覆盖）
	sessions, _ := pl.ListSessions(false, "", "")
	if len(sessions) != 1 {
		t.Fatalf("重复回灌导致的会话数异常: %d", len(sessions))
	}
}

// TestLoadTranscriptsWorkspace memory 模式以 transcript 为事实源：工作空间与其
// 「是否平台代建」权威标志都从 header 回灌；旧文件缺该字段 → 非临时（历史会话语义正确）。
func TestLoadTranscriptsWorkspace(t *testing.T) {
	dir := t.TempDir()
	temporaryHeader := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesWsTemp + `","agent_id":"agent-rtx","runtime":"rtx","cwd":"/home/u/.ark-mate/workspaces/` + sesWsTemp + `","workspace_temporary":true,"created_at":"2026-09-07T03:12:40Z"}`
	legacyHeader := `{"schema":"ark-mate.wire-transcript.v2","version":2,"session_id":"` + sesWsLegacy + `","agent_id":"agent-rtx","runtime":"rtx","cwd":"/work/alpha","created_at":"2026-09-07T03:12:40Z"}`
	writeTranscriptJSONL(t, dir, sesWsTemp+".jsonl", []string{temporaryHeader})
	writeTranscriptJSONL(t, dir, sesWsLegacy+".jsonl", []string{legacyHeader})

	pl, _ := testPlane(t, dir)

	byID := map[string]struct {
		cwd string
		tmp bool
	}{}
	sessions, err := pl.ListSessions(false, "", "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, c := range sessions {
		byID[c.ID] = struct {
			cwd string
			tmp bool
		}{c.Cwd, c.WorkspaceTemporary}
	}
	if got := byID[sesWsTemp]; got.cwd != "/home/u/.ark-mate/workspaces/"+sesWsTemp || !got.tmp {
		t.Fatalf("代建标记/工作空间未回灌: %+v", got)
	}
	if got := byID[sesWsLegacy]; got.cwd != "/work/alpha" || got.tmp {
		t.Fatalf("旧文件应视为非临时: %+v", got)
	}

	// 按工作空间过滤对回灌会话同样生效（recent/browse 面依赖此能力）。
	only, _ := pl.ListSessions(false, "/work/alpha", "")
	if len(only) != 1 || only[0].ID != sesWsLegacy {
		t.Fatalf("回灌会话按 cwd 过滤不符: %+v", only)
	}
}
