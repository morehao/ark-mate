package mcp

// 连接池用例（M7-2 任务 4）。四条语义各一条，且都**可观测**：
// 池化的证据是"第二次调用不再起进程"（helper 每次启动往 SPAWN_LOG 追加一行 pid），
// 而不是内部计数——内部计数跟着实现走，起进程次数才是收益本身。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// poolServer 造一个池化用例的 server：runner 是测试二进制本身（helper 模式），
// spawnLog 里每次建连追加一行。
func poolServer(t *testing.T, id, mode, spawnLog string) Server {
	t.Helper()
	t.Setenv("FORGE_MCP_HELPER", "1")
	env := map[string]string{"FORGE_MCP_HELPER_SPAWN_LOG": spawnLog}
	if mode != "" {
		env["FORGE_MCP_HELPER_MODE"] = mode
	}
	return Server{ID: id, Transport: "stdio", Command: os.Args[0], Env: env}
}

// spawns 数一数起过几次进程。
func spawns(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

func poolCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestPoolReusesSession 语义：同一 server 的后续调用复用已握手的会话。
func TestPoolReusesSession(t *testing.T) {
	log := filepath.Join(t.TempDir(), "spawn.log")
	ctx := poolCtx(t)
	p := NewPool(PoolOptions{Logger: testLogger()})
	t.Cleanup(p.Close)
	s := poolServer(t, "alpha", "", log)

	for i := 0; i < 3; i++ {
		res, err := p.CallTool(ctx, s, "read_file", json.RawMessage(`{"i":1}`))
		if err != nil {
			t.Fatalf("第 %d 次调用: %v", i+1, err)
		}
		if res.IsError || !strings.Contains(res.TextOf(), "tool=read_file") {
			t.Fatalf("结果不符: %+v", res)
		}
	}
	if got := spawns(t, log); got != 1 {
		t.Fatalf("三次调用只该起一个进程（池化的全部意义），got %d", got)
	}
	// tools/list 与 tools/call 共用同一条会话。
	if _, err := p.ListTools(ctx, s); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := spawns(t, log); got != 1 {
		t.Fatalf("list 也应复用会话，got %d 次起进程", got)
	}
	st := p.Stats()
	if st.Spawns != 1 || st.Reused < 3 || st.Sessions != 1 {
		t.Fatalf("统计不符: %+v", st)
	}
}

// TestPoolRebuildsOnDefinitionChange 语义：定义/密钥一变，旧会话作废重建
// （这是"密钥轮换无需重启、无需换代际"的落地方式）。
func TestPoolRebuildsOnDefinitionChange(t *testing.T) {
	log := filepath.Join(t.TempDir(), "spawn.log")
	ctx := poolCtx(t)
	p := NewPool(PoolOptions{Logger: testLogger()})
	t.Cleanup(p.Close)
	s := poolServer(t, "alpha", "", log)
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	// 只换密钥（env 值）：连接参数变了 → 必须重建，不能拿着旧凭据的连接继续用。
	s.Env["FORGE_MCP_HELPER_NAME"] = "rotated-secret"
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if got := spawns(t, log); got != 2 {
		t.Fatalf("凭据轮换后应重建会话，got %d 次起进程", got)
	}
	if st := p.Stats(); st.Spawns != 2 || st.Evicted != 1 {
		t.Fatalf("统计不符: %+v", st)
	}
	// 只是顺序相同地再调一次（无变化）→ 复用。
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if got := spawns(t, log); got != 2 {
		t.Fatalf("无变化不该重建，got %d", got)
	}
}

// TestPoolRPCErrorKeepsSession 语义：服务端的 JSON-RPC 错误响应**不作废**会话
// （否则一个"未知工具"就能把远端连接打掉，池化收益归零）。
func TestPoolRPCErrorKeepsSession(t *testing.T) {
	log := filepath.Join(t.TempDir(), "spawn.log")
	ctx := poolCtx(t)
	p := NewPool(PoolOptions{Logger: testLogger()})
	t.Cleanup(p.Close)
	s := poolServer(t, "alpha", "call_mixed", log)

	_, err := p.CallTool(ctx, s, "bad", nil)
	if err == nil {
		t.Fatal("应报错")
	}
	if !IsRPCResponseError(err) {
		t.Fatalf("应被识别为服务端错误响应: %v", err)
	}
	if !strings.Contains(err.Error(), "-32602") {
		t.Fatalf("错误里应带码: %v", err)
	}
	res, err := p.CallTool(ctx, s, "good", nil)
	if err != nil {
		t.Fatalf("同一条会话的后续调用应照常: %v", err)
	}
	if !strings.Contains(res.TextOf(), "ok=good") {
		t.Fatalf("结果不符: %q", res.TextOf())
	}
	if got := spawns(t, log); got != 1 {
		t.Fatalf("错误响应不该作废会话，got %d 次起进程", got)
	}
}

// TestPoolReapIdleClosesSession 语义：空闲回收（注入时钟，不 sleep 断言）。
func TestPoolReapIdleClosesSession(t *testing.T) {
	log := filepath.Join(t.TempDir(), "spawn.log")
	ctx := poolCtx(t)
	now := time.Now()
	p := NewPool(PoolOptions{
		IdleTimeout: time.Minute,
		Now:         func() time.Time { return now },
		Logger:      testLogger(),
	})
	t.Cleanup(p.Close)
	s := poolServer(t, "alpha", "", log)
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if n := p.ReapIdle(); n != 0 {
		t.Fatalf("未超时不该回收，got %d", n)
	}
	now = now.Add(2 * time.Minute)
	if n := p.ReapIdle(); n != 1 {
		t.Fatalf("超时应回收 1 条，got %d", n)
	}
	if st := p.Stats(); st.Sessions != 0 {
		t.Fatalf("回收后在册应为 0: %+v", st)
	}
	// 回收后下一次调用重建（旧进程已被关掉）。
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatalf("回收后应能重建: %v", err)
	}
	if got := spawns(t, log); got != 2 {
		t.Fatalf("回收后应重建，got %d 次起进程", got)
	}
}

// TestPoolEvictsWhenFull 语义：在册会话有上限（网关是子进程，不能无限攒远端连接）。
func TestPoolEvictsWhenFull(t *testing.T) {
	log := filepath.Join(t.TempDir(), "spawn.log")
	ctx := poolCtx(t)
	p := NewPool(PoolOptions{MaxSessions: 1, Logger: testLogger()})
	t.Cleanup(p.Close)
	a := poolServer(t, "alpha", "", log)
	b := poolServer(t, "beta", "", log)

	if _, err := p.CallTool(ctx, a, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CallTool(ctx, b, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if st := p.Stats(); st.Sessions != 1 || st.Evicted == 0 {
		t.Fatalf("超上限应换出最久未用的那条: %+v", st)
	}
	// alpha 被换出 → 再调它必须重建。
	if _, err := p.CallTool(ctx, a, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	if got := spawns(t, log); got != 3 {
		t.Fatalf("换出后应重建，got %d 次起进程", got)
	}
}

// TestPoolCloseIsTerminal 退出路径：Close 之后取用一律失败（且幂等）。
func TestPoolCloseIsTerminal(t *testing.T) {
	ctx := poolCtx(t)
	p := NewPool(PoolOptions{Logger: testLogger()})
	s := poolServer(t, "alpha", "", filepath.Join(t.TempDir(), "spawn.log"))
	if _, err := p.CallTool(ctx, s, "read_file", nil); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close() // 幂等
	if _, err := p.CallTool(ctx, s, "read_file", nil); err == nil {
		t.Fatal("Close 之后不该还能取用")
	} else if CodeOf(err) != "mcp_pool_closed" {
		t.Fatalf("错误应可判别（code=mcp_pool_closed）: %v（code=%q）", err, CodeOf(err))
	}
	if st := p.Stats(); st.Sessions != 0 {
		t.Fatalf("关闭后在册应为 0: %+v", st)
	}
}

// TestServerKeyCoversConnectionFields 判据本身：凡影响连接的字段都必须进键
// （漏一个 = 改了定义却复用旧连接；多了不存在的字段只是多一次重建）。
func TestServerKeyCoversConnectionFields(t *testing.T) {
	base := Server{ID: "s", Transport: "stdio", Command: "/bin/x", Args: []string{"a"}, URL: "http://u", Headers: map[string]string{"H": "1"}, Env: map[string]string{"K": "v"}}
	if serverKey(base) != serverKey(base) {
		t.Fatal("同内容必须同键")
	}
	mutate := []func(Server) Server{
		func(s Server) Server { s.Transport = "http"; return s },
		func(s Server) Server { s.Command = "/bin/y"; return s },
		func(s Server) Server { s.Args = []string{"b"}; return s },
		func(s Server) Server { s.URL = "http://u2"; return s },
		func(s Server) Server { s.Headers = map[string]string{"H": "2"}; return s },
		func(s Server) Server { s.Env = map[string]string{"K": "v2"}; return s },
	}
	for i, m := range mutate {
		if serverKey(m(base)) == serverKey(base) {
			t.Fatalf("第 %d 项变化没进键（会让旧连接被错误复用）", i)
		}
	}
}

// testLogger 池的日志走 io.Discard：用例只断言行为，不靠日志文本（日志是给人看的）。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
