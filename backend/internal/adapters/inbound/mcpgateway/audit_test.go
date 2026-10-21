package mcpgateway

// ——— T2 第一刀验收：调用审计（`mcp_call_audits`） ———
//
// 与 source_test.go 同一套脚手架（真 store + 假远端），因为三条判据都是跨进程事实：
//   - 一次调用**恰好**一行，字段能对上（会话/轮次/server/工具/三态/耗时）；
//   - 上游报错与"调用被拒"各走各的 outcome，而不是都落成"失败"；
//   - **写审计失败不阻断调用**（C32 同一取舍）——这条只能靠一个必然失败的 sink 验。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/morehao/ark-mate/internal/adapters/outbound/dsh"
	"github.com/morehao/ark-mate/internal/adapters/outbound/mcp"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/domain/session/run"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"github.com/morehao/ark-mate/internal/platform/object/objid"
	"github.com/morehao/ark-mate/internal/runtime"
)

// sourceWithAudit 装配数据面并**注入**审计落点（其余与 `sourceRig.source` 逐字相同）。
//
// 单独写一个而不是给 `source()` 加参数：源头那边是"生产形态"（落点 = 注入的持久层），
// 这里要的是"坏落点"这一种测试专用的形态，混进默认装配会让生产路径多一个分支。
func (r *sourceRig) sourceWithAudit(sink AuditSink) *MateSource {
	r.t.Helper()
	src, err := NewSource(context.Background(), SourceOptions{
		Adapters: []runtime.Adapter{dsh.New()}, NewPlanes: testPlanes(),
		Cfg: r.cfg, Store: r.st, Audit: sink,
	})
	if err != nil {
		r.t.Fatalf("装配数据面（注入审计落点）: %v", err)
	}
	r.t.Cleanup(func() { _ = src.Close() })
	return src
}

// seedSessionAndRun 落一个会话 + 一个**进行中**的轮（审计行的 session_id / run_id 都要有归属）。
func seedSessionAndRun(t *testing.T, st *store.Store, sessionID, runID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertSession(ctx, sessionrow.Item{ID: sessionID, Status: "in_progress"}); err != nil {
		t.Fatalf("落会话: %v", err)
	}
	if err := st.CreateRun(ctx, run.Item{ID: runID, SessionID: sessionID, Status: "in_progress", LastSeq: -1}); err != nil {
		t.Fatalf("落 run: %v", err)
	}
}

// TestGatewayCallWritesOneAuditRow 一次成功的工具调用 → 审计表里**恰好一行**，字段逐条对得上。
//
// 会话归属来自**核心下发的 env**（`mcp.GatewaySessionEnv`）——网关自己不知道谁拉起了它，
// 所以这条用例刻意走 env 那条路径（与 `mate mcp bridge` 的真实形态同一份代码）。
func TestGatewayCallWritesOneAuditRow(t *testing.T) {
	rig := newSourceRig(t)
	ctx := context.Background()
	remote := newFakeRemote(t, "echo")
	t.Setenv("MCP_AUDIT_TOKEN", "tok-audit")
	rig.seedMcp("srv1", httpDef("srv1", remote.srv.URL, "MCP_AUDIT_TOKEN"), 0)

	sessionID := objid.New(objid.Session)
	const runID = "run_audit0000000000000000000"
	seedSessionAndRun(t, rig.st, sessionID, runID)
	t.Setenv(mcp.GatewaySessionEnv, sessionID) // 核心经条目 env 交下来的归属

	src := rig.source(nil)
	if src.sessionID != sessionID {
		t.Fatalf("会话归属应取自 %s：got %q", mcp.GatewaySessionEnv, src.sessionID)
	}
	if _, err := src.Tools(ctx); err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	if _, err := src.Call(ctx, "srv1__echo", json.RawMessage(`{"q":"x"}`)); err != nil {
		t.Fatalf("代理调用: %v", err)
	}

	rows, err := rig.st.McpCallAuditsBySession(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("一次调用必须**恰好**一行审计，got %d 行：%+v", len(rows), rows)
	}
	got := rows[0]
	if got.SessionID != sessionID || got.RunID != runID {
		t.Fatalf("归属不对：session=%q run=%q（want %q / %q）", got.SessionID, got.RunID, sessionID, runID)
	}
	if got.ServerID != "srv1" || got.Tool != "echo" {
		t.Fatalf("server/tool 应记**原始名**（不带 server 前缀）：%q / %q", got.ServerID, got.Tool)
	}
	if got.Outcome != store.McpCallOutcomeOK || got.ErrorKind != "" {
		t.Fatalf("成功的调用应是 outcome=ok 且 error_kind 为空：%q / %q", got.Outcome, got.ErrorKind)
	}
	if got.At.IsZero() {
		t.Fatal("审计行必须带写入时刻（保留期裁剪就按它算）")
	}
}

// TestGatewayAuditSeparatesErrorFromDeny 两条失败分线各记各的：
// 上游报错 = `error` + `error_kind=upstream`；不在当下工具面 = `deny`（没有发起远端调用）。
//
// 它们对复盘的结论完全不同（"远端坏了" vs "平台收窄掉了它"），落成一个词就等于把这层信息丢掉。
func TestGatewayAuditSeparatesErrorFromDeny(t *testing.T) {
	rig := newSourceRig(t)
	ctx := context.Background()
	remote := newFakeRemote(t, "echo")
	t.Setenv("MCP_AUDIT_TOKEN2", "tok-audit")
	rig.seedMcp("srv1", httpDef("srv1", remote.srv.URL, "MCP_AUDIT_TOKEN2"), 0)

	sessionID := objid.New(objid.Session)
	seedSessionAndRun(t, rig.st, sessionID, "run_audit_error00000000000000")
	t.Setenv(mcp.GatewaySessionEnv, sessionID)

	src := rig.source(nil)
	if _, err := src.Tools(ctx); err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	// ① 远端报错（JSON-RPC -32603）。
	remote.setCallFail(true)
	if _, err := src.Call(ctx, "srv1__echo", nil); err == nil {
		t.Fatal("远端报错时调用必须失败")
	}
	// ② 名字指向一台**不在当下工具面**的 server（停用/删除后的形态；收窄到工具级时名字本身
	//    仍然解析得出来，那条路会走到远端去）。
	if _, err := src.Call(ctx, "ghost__echo", nil); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("未知工具应回 ErrToolNotFound，got %v", err)
	}

	rows, err := rig.st.McpCallAuditsBySession(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("两条失败分线应各留一行，got %d 行：%+v", len(rows), rows)
	}
	// 读面按写入序**倒序**：rows[0] = 最近的（deny），rows[1] = 先前的（error）。
	byOutcome := map[string]store.McpCallRecord{}
	for _, r := range rows {
		byOutcome[r.Outcome] = r
	}
	errRow, ok := byOutcome[store.McpCallOutcomeError]
	if !ok || errRow.ErrorKind != "upstream" || errRow.ServerID != "srv1" || errRow.Tool != "echo" {
		t.Fatalf("上游报错应记 outcome=error / error_kind=upstream / server+原始工具名：%+v", rows)
	}
	denyRow, ok := byOutcome[store.McpCallOutcomeDeny]
	if !ok {
		t.Fatalf("被拒的调用也要留痕（否则「模型调了不存在的工具」在审计里看不见）：%+v", rows)
	}
	if denyRow.Tool != "ghost__echo" || denyRow.LatencyMS != 0 {
		t.Fatalf("deny 行应保留**请求名**且耗时为 0（没发起远端调用）：%+v", denyRow)
	}
}

// failingAuditSink 一个必然写失败的审计落点（"写审计失败"这条判据的唯一观测方式）。
type failingAuditSink struct{ calls int }

func (f *failingAuditSink) AppendMcpCallAudit(context.Context, store.McpCallRecord) error {
	f.calls++
	return errors.New("审计表写不进去（模拟磁盘满/库被锁）")
}

// TestAuditWriteFailureDoesNotBlockCall 审计写失败**不阻断调用**（C32 同一取舍）。
//
// 反证（实跑过）：把 `auditCall` 的错误改成 return → 本用例当场红（调用结果变成 audit error）。
// 这条判据的意义：结果已经拿回来了，因为"审计行没落地"把它变成失败，比少一行审计糟得多。
func TestAuditWriteFailureDoesNotBlockCall(t *testing.T) {
	rig := newSourceRig(t)
	ctx := context.Background()
	remote := newFakeRemote(t, "echo")
	t.Setenv("MCP_AUDIT_TOKEN3", "tok-audit")
	rig.seedMcp("srv1", httpDef("srv1", remote.srv.URL, "MCP_AUDIT_TOKEN3"), 0)

	sessionID := objid.New(objid.Session)
	seedSessionAndRun(t, rig.st, sessionID, "run_audit_fail000000000000000")
	t.Setenv(mcp.GatewaySessionEnv, sessionID)

	sink := &failingAuditSink{}
	src := rig.sourceWithAudit(sink)
	if _, err := src.Tools(ctx); err != nil {
		t.Fatalf("取工具面: %v", err)
	}
	res, err := src.Call(ctx, "srv1__echo", nil)
	if err != nil {
		t.Fatalf("审计写失败**不得**让调用失败（C32 同一取舍），got %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("调用结果应原样返回：%+v", res)
	}
	if sink.calls != 1 {
		t.Fatalf("审计写入应被尝试恰好一次，got %d", sink.calls)
	}
	// 库里没有行是**预期**的（落点就是坏的）——这条断言防的是"写失败但静默写到了别处"。
	rows, err := rig.st.McpCallAuditsBySession(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("注入的落点失败时不应用真库兜底（否则「写失败」永远测不出来）：%+v", rows)
	}
}

// TestResolveSessionID 归属优先级：**显式入参 > 环境变量 > 空**（空就留空，不猜）。
func TestResolveSessionID(t *testing.T) {
	if got := resolveSessionID("ses_option", func(string) string { return "ses_env" }); got != "ses_option" {
		t.Fatalf("显式入参应最优先，got %q", got)
	}
	if got := resolveSessionID("", func(k string) string {
		if k == mcp.GatewaySessionEnv {
			return "ses_env"
		}
		return ""
	}); got != "ses_env" {
		t.Fatalf("环境变量应生效，got %q", got)
	}
	if got := resolveSessionID("  ", func(string) string { return "  " }); got != "" {
		t.Fatalf("空白值不得算作归属（宁可为空也不记到别人的会话上），got %q", got)
	}
}
