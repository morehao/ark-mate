package gateway

// M7-1 的网关侧用例：MCP 服务端（`pkg/mcp`）已经逐方法验过，这里钉的是**网关这一层**：
// 空数据面必须响亮而不是静默、真数据面的透传与"未知工具"分线、stdin EOF 即退出。
//
// 为什么不重复验帧形状：协议面的用例在 `pkg/mcp/server_test.go`（含自研互测与 stdout 纯度）。
// 这里也不起子进程：进程级形态（stdout 纯度、EOF 退出）由 CLI 侧的用例负责。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/mcp"
)

// fakeSource 一个记账用的数据面（Tools/Call 各自可注入错误）。
type fakeSource struct {
	mu      sync.Mutex
	tools   []mcp.Tool
	listN   int
	callN   int
	listErr error
	callErr error
}

func (f *fakeSource) Tools(context.Context) ([]mcp.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listN++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeSource) Call(_ context.Context, tool string, args json.RawMessage) (mcp.CallResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callN++
	if f.callErr != nil {
		return mcp.CallResult{}, f.callErr
	}
	if tool != "alpha__echo" {
		return mcp.CallResult{}, mcp.ErrToolNotFound
	}
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": string(args)})
	return mcp.CallResult{Content: []json.RawMessage{blk}}, nil
}

// rig 把 Run 夹在两个 io.Pipe 之间当客户端用（与服务端用例同形）。
type rig struct {
	t    *testing.T
	w    *bufio.Writer
	r    *bufio.Reader
	logs func() string
	seq  int
}

func startRig(t *testing.T, opts Options) *rig {
	t.Helper()
	testR, srvW := io.Pipe()
	srvR, testW := io.Pipe()
	logs := testutil.CaptureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, srvR, srvW, opts)
	}()
	t.Cleanup(func() {
		cancel()
		_ = testW.Close()
		_ = testR.Close()
		<-done
	})
	return &rig{t: t, w: bufio.NewWriter(testW), r: bufio.NewReader(testR), logs: logs}
}

func (g *rig) send(method string, params any) string {
	g.t.Helper()
	g.seq++
	id := json.RawMessage(`"` + method + `-` + string(rune('0'+g.seq)) + `"`)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		g.t.Fatal(err)
	}
	if _, err := g.w.Write(append(b, '\n')); err != nil {
		g.t.Fatal(err)
	}
	if err := g.w.Flush(); err != nil {
		g.t.Fatal(err)
	}
	return string(id)
}

// await 读到指定 id 的响应，并顺带断言每一行都是合法 JSON-RPC 帧（网关这一层也不能脏 stdout）。
func (g *rig) await(id string) mcpFrame {
	g.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := g.r.ReadBytes('\n')
		if err != nil {
			g.t.Fatalf("读响应（want %s）: %v", id, err)
		}
		var msg mcpFrame
		if jerr := json.Unmarshal(line, &msg); jerr != nil {
			g.t.Fatalf("stdout 出现非 JSON-RPC 帧：%q", line)
		}
		if msg.JSONRPC != "2.0" {
			g.t.Fatalf("帧缺 jsonrpc=2.0：%q", line)
		}
		if msg.ID != nil && string(*msg.ID) == id {
			return msg
		}
	}
	g.t.Fatalf("等不到 %s 的响应", id)
	return mcpFrame{}
}

type mcpFrame struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  json.RawMessage  `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (g *rig) handshake() mcpFrame {
	g.t.Helper()
	init := g.await(g.send("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "rig", "version": "0"},
	}))
	if init.Error != nil {
		g.t.Fatalf("initialize 失败: %+v", init.Error)
	}
	// 网关自述要交代命名空间规则（模型靠它分辨来源 server）。
	if !strings.Contains(string(init.Result), "mate-gateway") || !strings.Contains(string(init.Result), "__") {
		g.t.Fatalf("initialize 自述应含网关名与命名规则: %s", init.Result)
	}
	return init
}

// TestRunEmptySourceIsLoudButClean 骨架形态（Source == nil）：工具面是**空数组**（不是 null）、
// 调用回"未知工具"，且 stderr 上有一条响亮的 WARN——静默的空面将来会表现成"工具莫名不见了"。
func TestRunEmptySourceIsLoudButClean(t *testing.T) {
	g := startRig(t, Options{Scope: Scope{RuntimeID: "r1"}})
	g.handshake()

	list := g.await(g.send("tools/list", nil))
	if list.Error != nil {
		t.Fatalf("空面不应报错: %+v", list.Error)
	}
	var res struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Tools == nil || len(res.Tools) != 0 {
		t.Fatalf("空面必须是 []（不是 null）: %s", list.Result)
	}

	call := g.await(g.send("tools/call", map[string]any{"name": "x__y"}))
	if call.Error == nil || call.Error.Code != -32602 {
		t.Fatalf("骨架形态下调用应回未知工具（-32602）: %+v", call.Error)
	}
	if !strings.Contains(g.logs(), "数据面未接线") {
		t.Fatalf("空面必须响亮（stderr 要有 WARN）: %s", g.logs())
	}
}

// TestRunWithSourceProxiesTools 真数据面：工具面透传 + 调用透传 + 未知工具分线。
func TestRunWithSourceProxiesTools(t *testing.T) {
	src := &fakeSource{tools: []mcp.Tool{
		{Name: "alpha__echo", Description: "回显"},
		{Name: "beta__add", Description: "加法"},
	}}
	g := startRig(t, Options{Scope: Scope{RuntimeID: "r1"}, Source: src})
	g.handshake()

	list := g.await(g.send("tools/list", nil))
	var res struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "alpha__echo" {
		t.Fatalf("工具面应透传: %+v", res.Tools)
	}

	ok := g.await(g.send("tools/call", map[string]any{"name": "alpha__echo", "arguments": map[string]any{"n": 1}}))
	if ok.Error != nil {
		t.Fatalf("调用不应报错: %+v", ok.Error)
	}
	var call mcp.CallResult
	if err := json.Unmarshal(ok.Result, &call); err != nil {
		t.Fatal(err)
	}
	if call.TextOf() != `{"n":1}` {
		t.Fatalf("调用结果应透传: %q", call.TextOf())
	}

	// Source 返回 ErrToolNotFound（例：工具已被停用）→ 协议层 -32602。
	miss := g.await(g.send("tools/call", map[string]any{"name": "gamma__gone"}))
	if miss.Error == nil || miss.Error.Code != -32602 {
		t.Fatalf("未知工具应回 -32602: %+v", miss.Error)
	}

	// Source 报"执行失败"→ isError 结果（会话不炸）。
	src.mu.Lock()
	src.callErr = errors.New("远端炸了")
	src.mu.Unlock()
	bad := g.await(g.send("tools/call", map[string]any{"name": "alpha__echo"}))
	if bad.Error != nil {
		t.Fatalf("执行失败不该是 JSON-RPC 错误: %+v", bad.Error)
	}
	var badRes mcp.CallResult
	if err := json.Unmarshal(bad.Result, &badRes); err != nil {
		t.Fatal(err)
	}
	if !badRes.IsError || !strings.Contains(badRes.TextOf(), "远端炸了") {
		t.Fatalf("执行失败应是 isError 结果: %+v", badRes)
	}
}

// TestRunToolListFailureIsDegradedNotFatal 取数失败（store 读不动 / 定义全坏）回**空面**而不是
// 炸会话：网关是单点，炸掉整条会话的代价远大于"这一轮没有工具"；错误必须进日志。
func TestRunToolListFailureIsDegradedNotFatal(t *testing.T) {
	src := &fakeSource{listErr: errors.New("store 打不开")}
	g := startRig(t, Options{Scope: Scope{RuntimeID: "r1"}, Source: src})
	g.handshake()

	list := g.await(g.send("tools/list", nil))
	if list.Error != nil {
		t.Fatalf("取数失败应降级为空面而不是报错: %+v", list.Error)
	}
	if !strings.Contains(string(list.Result), `"tools":[]`) {
		t.Fatalf("应回空面: %s", list.Result)
	}
	if !strings.Contains(g.logs(), "取工具面失败") {
		t.Fatalf("降级必须留痕: %s", g.logs())
	}
}

// TestRunExitsOnStdinEOF stdin EOF = 父进程（底座）结束 → Run 返回 nil（M7-5 的退出路径）。
func TestRunExitsOnStdinEOF(t *testing.T) {
	testR, srvW := io.Pipe()
	srvR, testW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), srvR, srvW, Options{
			Scope: Scope{RuntimeID: "r1"},
		})
	}()
	if err := testW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF 应返回 nil，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdin EOF 后网关未退出（孙进程泄漏的一种）")
	}
	_ = testR.Close()
}

// TestRunCancelReturnsContextError ctx 取消（SIGTERM 路径）→ 返回 context.Canceled，CLI 据此
// 静默退出（不打错误行）。
func TestRunCancelReturnsContextError(t *testing.T) {
	testR, srvW := io.Pipe()
	srvR, testW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, srvR, srvW, Options{
			Scope: Scope{RuntimeID: "r1"},
		})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消应返回 context.Canceled，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后网关未退出")
	}
	_ = testW.Close()
	_ = testR.Close()
}
