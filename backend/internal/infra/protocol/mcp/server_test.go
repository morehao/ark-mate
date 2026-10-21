package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ——— MCP 服务端（网关协议面）的用例 ———
//
// 三条独立证据，缺一不可：
//  1. 进程内逐方法核对（`TestServerProtocol*`）：帧的形状、分页、错误码分线；
//  2. **自研互测**（`TestServerInteropWithOwnClient`）：用本仓客户端连本仓服务端真跑
//     initialize → tools/list → tools/call。本仓没有 MCP SDK 可依赖，这是唯一能证明
//     "两侧对同一份规范的理解一致"的手段；
//  3. **stdout 纯度**（`TestServerStdoutPurity`）：C10——stdout 只许有 JSON-RPC 帧。

// helperGatewayHandler 测试用工具面：两个工具，一条成功、一条执行失败，外加"未知工具"分线。
func helperGatewayHandler() Handler {
	return HandlerFuncs{
		List: func(context.Context) ([]Tool, error) {
			return []Tool{
				{Name: "alpha__echo", Description: "回显参数", InputSchema: json.RawMessage(`{"type":"object"}`)},
				{Name: "beta__add", Description: "加法"},
			}, nil
		},
		Call: func(_ context.Context, name string, args json.RawMessage) (CallResult, error) {
			switch name {
			case "alpha__echo":
				blk, _ := json.Marshal(map[string]string{"type": "text", "text": string(args)})
				return CallResult{Content: []json.RawMessage{blk}, ServerName: "alpha", ServerVersion: "1.2.3"}, nil
			case "beta__add":
				return CallResult{}, errors.New("下游不可达：connection refused")
			default:
				return CallResult{}, ErrToolNotFound
			}
		},
	}
}

// frameSrc 进程内驱动服务端：两个 io.Pipe 把 Serve 夹在中间（测试当客户端、手写原始帧）。
type frameSrc struct {
	t   *testing.T
	w   *bufio.Writer
	r   *bufio.Reader
	seq int
}

func startServer(t *testing.T, h Handler, opts ServerOptions) *frameSrc {
	t.Helper()
	// 服务端写入 → 测试读出；测试写入 → 服务端读出。
	// io.Pipe() 返回 (reader, writer)：**写进 writer 的字节从 reader 读出来**。
	// 故 testR/srvW 是"服务端写 → 测试读"，srvR/testW 是"测试写 → 服务端读"。
	testR, srvW := io.Pipe()
	srvR, testW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, srvR, srvW, ServerInfo{Name: "test-bridge", Version: "9.9.9"}, h, opts)
	}()
	t.Cleanup(func() {
		cancel()
		_ = testW.Close()
		_ = testR.Close()
		<-done
	})
	return &frameSrc{t: t, w: bufio.NewWriter(testW), r: bufio.NewReader(testR)}
}

// send 写一帧原始请求（返回 id，便于 await 配对）。
func (f *frameSrc) send(method string, params any) string {
	f.t.Helper()
	f.seq++
	id := json.RawMessage(`"` + method + `-` + string(rune('0'+f.seq)) + `"`)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.w.Write(append(b, '\n')); err != nil {
		f.t.Fatalf("写请求: %v", err)
	}
	if err := f.w.Flush(); err != nil {
		f.t.Fatalf("flush: %v", err)
	}
	return string(id)
}

// sendRaw 写一行原始字节（故意发坏帧用）。
func (f *frameSrc) sendRaw(line string) {
	f.t.Helper()
	if _, err := f.w.WriteString(line + "\n"); err != nil {
		f.t.Fatalf("写原始帧: %v", err)
	}
	if err := f.w.Flush(); err != nil {
		f.t.Fatalf("flush: %v", err)
	}
}

// notify 写一条通知（无 id）。
func (f *frameSrc) notify(method string) {
	f.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if _, err := f.w.Write(append(b, '\n')); err != nil {
		f.t.Fatal(err)
	}
	_ = f.w.Flush()
}

// await 读到指定 id 的响应（并发响应可能乱序，按 id 配对）。
func (f *frameSrc) await(id string) rpcMessage {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := f.r.ReadBytes('\n')
		if err != nil {
			f.t.Fatalf("读响应（want %s）: %v", id, err)
		}
		var msg rpcMessage
		if jerr := json.Unmarshal(line, &msg); jerr != nil {
			f.t.Fatalf("响应不是 JSON（stdout 被污染？）: %q", line)
		}
		if msg.ID != nil && string(*msg.ID) == id {
			return msg
		}
	}
	f.t.Fatalf("等不到 %s 的响应", id)
	return rpcMessage{}
}

// TestServerProtocolHandshakeAndList 握手 + tools/list + ping + 未知方法的帧形状。
func TestServerProtocolHandshakeAndList(t *testing.T) {
	f := startServer(t, helperGatewayHandler(), ServerOptions{})

	init := f.await(f.send("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "0"},
	}))
	if init.Error != nil {
		t.Fatalf("initialize 不应报错: %s", init.Error.String())
	}
	var initRes struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(init.Result, &initRes); err != nil {
		t.Fatal(err)
	}
	if initRes.ProtocolVersion != protocolVersion {
		t.Fatalf("应回协商版本 %q，got %q", protocolVersion, initRes.ProtocolVersion)
	}
	if initRes.ServerInfo.Name != "test-bridge" || initRes.ServerInfo.Version != "9.9.9" {
		t.Fatalf("serverInfo 不符: %+v", initRes.ServerInfo)
	}
	if _, ok := initRes.Capabilities["tools"]; !ok {
		t.Fatalf("应声明 tools 能力: %+v", initRes.Capabilities)
	}

	f.notify("notifications/initialized") // 通知不应有响应

	ping := f.await(f.send("ping", nil))
	if ping.Error != nil || string(ping.Result) != "{}" {
		t.Fatalf("ping 应回空结果: result=%s err=%v", ping.Result, ping.Error)
	}

	list := f.await(f.send("tools/list", map[string]any{}))
	if list.Error != nil {
		t.Fatalf("tools/list 不应报错: %s", list.Error.String())
	}
	var listRes struct {
		Tools      []Tool `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(list.Result, &listRes); err != nil {
		t.Fatal(err)
	}
	if len(listRes.Tools) != 2 || listRes.Tools[0].Name != "alpha__echo" || listRes.NextCursor != "" {
		t.Fatalf("工具面不符: %+v", listRes)
	}

	unknown := f.await(f.send("resources/list", nil))
	if unknown.Error == nil || unknown.Error.Code != codeMethodNotFound {
		t.Fatalf("未知方法应回 -32601: %+v", unknown.Error)
	}
}

// TestServerProtocolPagination 分页：页大小 1 时逐页翻，游标不透明但必须可用；
// 乱填游标要显式报错（静默当第一页会让客户端死循环）。
func TestServerProtocolPagination(t *testing.T) {
	f := startServer(t, helperGatewayHandler(), ServerOptions{PageSize: 1})

	first := f.await(f.send("tools/list", nil))
	var p1 struct {
		Tools      []Tool `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(first.Result, &p1); err != nil {
		t.Fatal(err)
	}
	if len(p1.Tools) != 1 || p1.Tools[0].Name != "alpha__echo" || p1.NextCursor == "" {
		t.Fatalf("第一页不符: %+v", p1)
	}

	second := f.await(f.send("tools/list", map[string]any{"cursor": p1.NextCursor}))
	var p2 struct {
		Tools      []Tool `json:"tools"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(second.Result, &p2); err != nil {
		t.Fatal(err)
	}
	if len(p2.Tools) != 1 || p2.Tools[0].Name != "beta__add" || p2.NextCursor != "" {
		t.Fatalf("第二页不符: %+v", p2)
	}

	bad := f.await(f.send("tools/list", map[string]any{"cursor": "不是游标"}))
	if bad.Error == nil || bad.Error.Code != codeInvalidParams {
		t.Fatalf("坏游标应回 -32602: %+v", bad.Error)
	}
}

// TestServerProtocolCallTool tools/call 的三条分线：成功透传、执行失败 → isError 结果、
// 未知工具 → -32602（协议层错误，与"工具跑了但失败"分开）。
func TestServerProtocolCallTool(t *testing.T) {
	f := startServer(t, helperGatewayHandler(), ServerOptions{})

	ok := f.await(f.send("tools/call", map[string]any{
		"name": "alpha__echo", "arguments": map[string]any{"hello": "world"},
	}))
	if ok.Error != nil {
		t.Fatalf("成功调用不应报错: %s", ok.Error.String())
	}
	var res CallResult
	if err := json.Unmarshal(ok.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 || !strings.Contains(res.TextOf(), "hello") {
		t.Fatalf("结果不符: %+v（text=%q）", res, res.TextOf())
	}

	// 执行失败：**结果**里带 isError（模型看得到原因，会话不炸），不是 JSON-RPC 错误。
	bad := f.await(f.send("tools/call", map[string]any{"name": "beta__add", "arguments": map[string]any{}}))
	if bad.Error != nil {
		t.Fatalf("执行失败不该回 JSON-RPC 错误: %s", bad.Error.String())
	}
	var badRes CallResult
	if err := json.Unmarshal(bad.Result, &badRes); err != nil {
		t.Fatal(err)
	}
	if !badRes.IsError || !strings.Contains(badRes.TextOf(), "下游不可达") {
		t.Fatalf("执行失败应是 isError 结果: %+v（text=%q）", badRes, badRes.TextOf())
	}

	missing := f.await(f.send("tools/call", map[string]any{"name": "gamma__nope"}))
	if missing.Error == nil || missing.Error.Code != codeInvalidParams {
		t.Fatalf("未知工具应回 -32602: %+v", missing.Error)
	}
	noname := f.await(f.send("tools/call", map[string]any{"arguments": map[string]any{}}))
	if noname.Error == nil || noname.Error.Code != codeInvalidParams {
		t.Fatalf("缺 name 应回 -32602: %+v", noname.Error)
	}
}

// TestServerProtocolBadFrames 坏帧不终止会话：非 JSON → -32700，之后仍能正常服务；
// stdin EOF → Serve 返回 nil（父进程死亡的退出路径）。
func TestServerProtocolBadFrames(t *testing.T) {
	f := startServer(t, helperGatewayHandler(), ServerOptions{})
	f.sendRaw("this is not json")
	line, err := f.r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读坏帧响应: %v", err)
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		t.Fatal(err)
	}
	// `"id":null` 反序列化回 `*json.RawMessage` 就是 nil（指针字段遇 JSON null 置空），
	// 这正是"我们确实发了 id:null"的检查方式。
	if msg.Error == nil || msg.Error.Code != codeParseError || msg.ID != nil {
		t.Fatalf("坏帧应回 -32700 且 id=null: %+v", msg)
	}
	// 坏帧之后照常服务（一次污染不毁会话）。
	list := f.await(f.send("tools/list", nil))
	if list.Error != nil {
		t.Fatalf("坏帧之后应仍可服务: %s", list.Error.String())
	}
}

// TestServerEOFReturnsNil stdin EOF 是"父进程没了"的可靠信号：Serve 必须返回 nil
// （而不是报错）——网关的退出路径靠它（M7-5 的孙进程回收）。
func TestServerEOFReturnsNil(t *testing.T) {
	testR, srvW := io.Pipe()
	srvR, testW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), srvR, srvW, ServerInfo{}, HandlerFuncs{}, ServerOptions{})
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
		t.Fatal("EOF 后 Serve 未返回")
	}
	_ = testR.Close()
}

// TestServerInteropWithOwnClient 自研互测：本仓客户端 ↔ 本仓服务端（真子进程 + 真 stdio）。
//
// 覆盖两条方向相反的路径：`initialize/initialized/tools/list`（客户端翻页合并）与
// `tools/call`（含 isError 结果的透传）。两侧各自"照规范写"不算数，必须互测。
func TestServerInteropWithOwnClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s := stdioServer(t)
	s.Env = map[string]string{"MATE_MCP_HELPER_MODE": "server"}
	res, err := ListTools(ctx, s)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.ServerName != "test-gateway" || res.ServerVersion != "9.9.9" {
		t.Fatalf("握手自述不符: %+v", res)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "alpha__echo" || res.Tools[1].Name != "beta__add" {
		t.Fatalf("工具面不符: %+v", res.Tools)
	}

	call, err := CallTool(ctx, s, "alpha__echo", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if call.IsError || !strings.Contains(call.TextOf(), `{"n":1}`) {
		t.Fatalf("调用结果不符: %+v（text=%q）", call, call.TextOf())
	}

	// isError 结果是**成功响应**（客户端不该把它当传输/协议错误）。
	bad, err := CallTool(ctx, s, "beta__add", nil)
	if err != nil {
		t.Fatalf("执行失败应是结果而不是错误: %v", err)
	}
	if !bad.IsError || !strings.Contains(bad.TextOf(), "下游不可达") {
		t.Fatalf("isError 结果不符: %+v（text=%q）", bad, bad.TextOf())
	}

	// 未知工具是协议层错误 → 客户端应报错（含 -32602）。
	if _, err := CallTool(ctx, s, "gamma__nope", nil); err == nil {
		t.Fatal("未知工具应报错")
	} else if !strings.Contains(err.Error(), "-32602") {
		t.Fatalf("错误里应带 -32602: %v", err)
	}
}

// TestServerStdoutPurity C10：stdio 服务端的 stdout **逐行**只能是 JSON-RPC 帧。
//
// 为什么单独立一条：底座把 stdout 当协议流读，一行日志就让整套 JSON-RPC 错位，症状是
// "工具莫名不可用"，而根因（`fmt.Print`）在代码评审里极易漏。这里真起子进程、逐行解析
// stdout，并同时断言**诊断确实写出了日志**（否则"stdout 干净"可能只是因为压根没日志）。
//
// 2026-10-10 起诊断出口是 **glog**，而 glog 的 console writer 写的就是 os.Stdout（golib 里
// 硬编码，没有 stderr 选项）——所以真实 `mate mcp` 只能把它配成**文件** writer
// （见 internal/bootstrap/app/command/logging.go）。子进程这里照同一形态初始化，断言落在文件上；
// "mcp 子命令绝不配 console writer"这条本身由 internal/bootstrap/app/command 的守卫直接钉住。
func TestServerStdoutPurity(t *testing.T) {
	logDir := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MATE_MCP_HELPER=1", "MATE_MCP_HELPER_MODE=server",
		"MATE_MCP_HELPER_LOG_DIR="+logDir)
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"alpha__echo","arguments":{"a":1}}}`,
		`这一行是坏帧（应回 -32700，且不终止会话）`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"beta__add"}}`,
	}
	cmd.Stdin = strings.NewReader(strings.Join(frames, "\n") + "\n")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("起子进程: %v", err)
	}
	// 关掉 stdin 之后服务端会读 EOF 退出；等待进程结束（超时即判定为不退出）。
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("服务端进程应正常退出（EOF → 0）: %v（stderr=%s）", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("stdin EOF 后服务端未退出（孙进程泄漏的一种）")
	}

	// 6 帧里 `notifications/initialized` 是通知（不响应），其余 5 条各回一条（坏帧回 -32700）。
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("应恰好 5 条响应（通知不响应、坏帧也回一条），got %d 条：\n%s", len(lines), stdout.String())
	}
	seen := map[float64]bool{}
	for i, line := range lines {
		var msg struct {
			JSONRPC string           `json:"jsonrpc"`
			ID      *json.RawMessage `json:"id"`
			Result  json.RawMessage  `json:"result"`
			Error   *rpcError        `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout 第 %d 行不是 JSON-RPC 帧（stdout 被污染）：%q", i+1, line)
		}
		if msg.JSONRPC != "2.0" {
			t.Fatalf("stdout 第 %d 行 jsonrpc 字段不是 2.0：%q", i+1, line)
		}
		if msg.ID != nil {
			var id float64
			if err := json.Unmarshal(*msg.ID, &id); err == nil {
				seen[id] = true
			}
		}
	}
	for _, want := range []float64{1, 2, 3, 4} {
		if !seen[want] {
			t.Fatalf("缺少 id=%v 的响应：%s", want, stdout.String())
		}
	}
	if got := readLogTree(t, logDir); !strings.Contains(got, "mcp bridge") {
		t.Fatalf("诊断必须真的写进日志出口（否则「stdout 干净」可能只是因为压根没有日志）：%q", got)
	}
}

// readLogTree 把 glog 文件 writer 在 dir 下写出的全部日志读成一段文本
// （布局是 <dir>/<YYYYMMDD>/<service>_full.log）。
func readLogTree(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		sb.Write(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读日志目录 %s: %v", dir, err)
	}
	return sb.String()
}

// TestGatewayArgsRoundTrip 注入端与网关端的参数形态同源：拼出来的必须能解回来，
// 且未知参数/缺参数**报错**（静默忽略会让"网关起来了但 scope 不对"）。
func TestGatewayArgsRoundTrip(t *testing.T) {
	args := GatewayBridgeArgs(GatewayScope{RuntimeID: "r1"})
	if args[0] != "mcp" || args[1] != "bridge" {
		t.Fatalf("子命令形态不符: %v", args)
	}
	// v19：scope 里没有任何 agent 面——条目只由 runtime（+ 会话钉住的白名单）决定。
	for _, a := range args {
		if a == "--agent" {
			t.Fatalf("v19 起 --agent 已撤销（agent 概念整个并入 runtime）: %v", args)
		}
	}
	sc, err := ParseScopeArgs(args[2:])
	if err != nil || sc.RuntimeID != "r1" || sc.McpsPinned != nil {
		t.Fatalf("往返不符: %+v err=%v", sc, err)
	}

	// 会话钉住的白名单：三态必须原样往返（nil = 未钉 / [] = 显式不要 / [ids] = 白名单）。
	pinned := []string{"gh", "pg"}
	withPin := GatewayBridgeArgs(GatewayScope{RuntimeID: "r1", McpsPinned: &pinned})
	sc2, err := ParseScopeArgs(withPin[2:])
	if err != nil || sc2.McpsPinned == nil || strings.Join(*sc2.McpsPinned, ",") != "gh,pg" {
		t.Fatalf("钉住的白名单往返不符: %+v err=%v", sc2, err)
	}
	empty := []string{}
	withEmpty := GatewayBridgeArgs(GatewayScope{RuntimeID: "r1", McpsPinned: &empty})
	sc3, err := ParseScopeArgs(withEmpty[2:])
	if err != nil || sc3.McpsPinned == nil || len(*sc3.McpsPinned) != 0 {
		// `[]` 若解成 nil，"本会话一个远程 MCP 都不要"就退化成"全量远程面"——授权放大。
		t.Fatalf("显式空集必须往返成非 nil 空集: %+v err=%v", sc3, err)
	}

	for _, bad := range [][]string{
		{},                                      // 缺 scope（--runtime 必填）
		{"--runtime"},                           // 缺取值
		{"--mcps", "[]"},                        // 只有白名单、没有 scope
		{"--runtime", "r1", "-x"},               // 未知参数（参数漂移必须当场失败）
		{"--servers", "a,b"},                    // P-2 的反例：server 列表绝不能进参数
		{"--runtime", "r1", "--mcps", "a,b"},    // 非 JSON
		{"--runtime", "r1", "--mcps", `["a",]`}, // 坏 JSON
		{"--runtime", "r1", "--mcps", `[" "]`},  // 空 id
		// v19 撤销的 `--agent`：**未知参数即失败**（静默忽略会让网关"起来了但 scope 不对"）。
		{"--agent", "a1", "--runtime", "r1"},
	} {
		if _, err := ParseScopeArgs(bad); err == nil {
			t.Fatalf("%v 应报错", bad)
		}
	}
	if GatewayServerName != "mate-gateway" {
		t.Fatalf("条目名是底座会话里可见的稳定标识，不该改: %q", GatewayServerName)
	}
}
