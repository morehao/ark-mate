package mcp

// ——— MCP 服务端最小集（远程 MCP 网关的协议面） ———
//
// 为什么服务端也写在这个包里：**帧形状只能有一份**。客户端的 `rpcMessage`/`rpcError` 是
// 本仓自研的，服务端要是另起一套结构体，两侧就会各自"照规范"漂移，而自研互测是唯一能证明
// 帧兼容的手段（本仓没有 MCP SDK 可依赖：`go.mod` 只有 `acp-go-sdk`，那是 ACP 不是 MCP）。
// 方向反转即复用，`server_test.go` 用本包客户端连本包服务端跑通 list/call。
//
// 覆盖范围（M7-1）：`initialize` → `notifications/initialized` → `tools/list`（含分页）
// → `tools/call`，外加 `ping`。**不实现**：resources/prompts 的**对外暴露**（网关 v1 只做
// 工具面）、sampling/roots、listChanged 通知、断线重连。进程级生命周期由调用方负责
// （stdin EOF 即退出，见 Serve 的返回值语义）。
//
// **C10：stdout 纯度**——本文件的 `w` 上只允许出现 JSON-RPC 帧；一切诊断走 `glog`。
//
// 这条不是洁癖：底座把网关的 stdout 当协议流读，一行日志就会让整套 JSON-RPC 错位，表现为
// "工具莫名不可用"。注意 glog 的 console writer 写的就是 **os.Stdout**（golib 里硬编码，
// 没有 stderr 选项），所以 `mate mcp` 只能把出口配成文件（见 internal/app/command/logging.go），
// 由 internal/app/command 的守卫与 `server_test.go` 的纯度用例两侧钉住。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/morehao/golib/glog"
)

const (
	// serverDefaultPageSize tools/list 的默认页大小（客户端可继续用 cursor 翻页；
	// 上限 maxListPages × 本值 = 客户端一侧的可见工具上限，见 client.go）。
	serverDefaultPageSize = 64

	// jsonrpc 标准错误码（MCP 沿用 JSON-RPC 2.0）。
	codeParseError     = -32700
	codeInvalidParams  = -32602
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// ServerInfo 服务端自述（initialize 的 serverInfo）。
type ServerInfo struct {
	Name    string
	Version string
	// Instructions 可选：给模型的一段服务端说明（网关用它交代命名空间规则）。
	Instructions string
}

// Handler 是服务端要暴露的能力面。网关实现它（M7-2 的 Bridge），测试用 HandlerFuncs。
type Handler interface {
	// ListTools 给出该 scope **当下**的全部工具（工具面在网关内存里，分页由本文件切）。
	//
	// 每次调用都要**重读**定义与启用状态（M7-2 的 P-4：收窄/停用对下一次调用立即生效，
	// 不靠缓存失效）。页与页之间定义可能变化，故偏移切页只保证"单次调用内自洽"——
	// 工具面本来远小于一页（serverDefaultPageSize），跨页错位是客户端 maxListPages 兜底的边角。
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool 调用一个工具。args 为 arguments 对象（可能为 nil = 无参数）。
	//
	// 错误语义（M7-1 的分线，与 MCP 规范一致）：
	//   - 工具**执行**失败（远端报错、工具返回 isError）→ 返回 CallResult{IsError:true} 或
	//     error：本文件把 error 包成 `isError:true` 的**结果**（模型看得到失败原因，会话不炸）；
	//   - 工具**不存在**（名字不在工具面里）→ handler 返回 ErrToolNotFound，本文件回 JSON-RPC
	//     `-32602`（协议层错误：客户端的请求本身引用了不存在的工具）。
	CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error)
}

// ErrToolNotFound 工具不在当前工具面里（未知名字 / 已停用 / 已删除）。
//
// 它是**协议层**错误（回 -32602），与"工具跑了但失败"（回 isError 结果）分开——这条分线
// 决定了前端看到的是"参数错误"还是"工具报错"，也决定了底座会不会把它当成协议故障。
var ErrToolNotFound = errors.New("mcp: 工具不存在")

// HandlerFuncs 用函数装配 Handler（`nil` 的那侧回答空面/未知工具）。
type HandlerFuncs struct {
	List func(ctx context.Context) ([]Tool, error)
	Call func(ctx context.Context, name string, args json.RawMessage) (CallResult, error)
}

func (h HandlerFuncs) ListTools(ctx context.Context) ([]Tool, error) {
	if h.List == nil {
		return nil, nil
	}
	return h.List(ctx)
}

func (h HandlerFuncs) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if h.Call == nil {
		return CallResult{}, ErrToolNotFound
	}
	return h.Call(ctx, name, args)
}

// ServerOptions 服务端可选参数（零值即可用）。
type ServerOptions struct {
	// PageSize tools/list 每页条数（<=0 → serverDefaultPageSize）。
	PageSize int
}

// server 一次服务端会话的运行时状态。
type server struct {
	info     ServerInfo
	h        Handler
	pageSize int

	wmu sync.Mutex // 帧写锁：并发请求的响应不能交错（一行一帧）
	w   io.Writer

	initialized bool // 仅用于诊断日志（不据此拒绝请求：底座有先 tools/list 再 initialize 的自由）
}

// Serve 在 stdio 上跑 MCP 服务端：从 r 读换行分隔的 JSON-RPC 帧，把响应写到 w。
//
// 返回语义（网关的生命周期契约，见 M7-5）：
//   - r 读到 EOF → 返回 nil。**stdin EOF 是父进程（底座）死亡的可靠信号**，网关据此退出，
//     不留孤儿进程与泄漏的远端连接；
//   - ctx 取消 → 等在途请求结束（它们拿的是同一个 ctx）后返回 ctx.Err()；
//   - 只有"读失败"（非 EOF）才返回非 nil（解析不了的帧只回 -32700，不终止会话）。
func Serve(ctx context.Context, r io.Reader, w io.Writer, info ServerInfo, h Handler, opts ServerOptions) error {
	s := &server{info: info, h: h, pageSize: opts.PageSize, w: w}
	if s.pageSize <= 0 {
		s.pageSize = serverDefaultPageSize
	}
	if s.info.Name == "" {
		s.info.Name = "ark-mate"
	}
	if s.info.Version == "" {
		s.info.Version = clientVersion
	}

	// 读与调度分开：阻塞读无法观察 ctx，用一条 goroutine 送帧、主循环 select，
	// 这样"父进程关了 stdin"（EOF）与"收到了信号"（ctx）都能干净退出。
	type frame struct {
		line []byte
		err  error
	}
	lines := make(chan frame)
	go func() {
		defer close(lines)
		br := bufio.NewReaderSize(r, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case lines <- frame{line: line}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case lines <- frame{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait() // 退出前收干在途响应：半截会话比晚一秒退出更坏
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f, ok := <-lines:
			if !ok {
				return nil
			}
			if f.err != nil {
				if errors.Is(f.err, io.EOF) {
					glog.Infow(ctx, "mcp bridge: stdin 已关闭，网关退出（父进程结束）")
					return nil
				}
				return fmt.Errorf("mcp server: 读取请求失败：%w", f.err)
			}
			if len(f.line) > maxResponseBytes {
				glog.Warnw(ctx, "mcp bridge: 请求超过单帧上限，已丢弃", "bytes", len(f.line), "limit", maxResponseBytes)
				s.replyParseError(nil, fmt.Sprintf("请求超过 %d 字节上限", maxResponseBytes))
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal(f.line, &msg); err != nil {
				glog.Warnw(ctx, "mcp bridge: 收到非 JSON 帧", "err", err)
				s.replyParseError(nil, "请求不是合法 JSON")
				continue
			}
			// 通知（无 id）不需要响应；有 id 的请求各自一条 goroutine——底座可能并发 tools/call，
			// 串行处理会让一个慢远端工具阻塞其他工具（写侧由 wmu 保证不交错）。
			if msg.ID == nil {
				s.handleNotification(ctx, msg)
				continue
			}
			wg.Add(1)
			go func(msg rpcMessage) {
				defer wg.Done()
				s.handleRequest(ctx, msg)
			}(msg)
		}
	}
}

// handleNotification 处理通知（无响应）。目前只认 `notifications/initialized` 与
// `notifications/cancelled`，其余只记日志（服务端不对通知报错：规范要求忽略未知通知）。
func (s *server) handleNotification(ctx context.Context, msg rpcMessage) {
	switch msg.Method {
	case "notifications/initialized":
		s.initialized = true
		glog.Infow(ctx, "mcp bridge: 底座已完成 MCP 握手")
	case "notifications/cancelled":
		glog.Debugw(ctx, "mcp bridge: 收到取消通知（穿透到下游靠 ctx，不额外记账）")
	default:
		glog.Debugw(ctx, "mcp bridge: 忽略未知通知", "method", msg.Method)
	}
}

// handleRequest 处理一条带 id 的请求并写回响应。
func (s *server) handleRequest(ctx context.Context, msg rpcMessage) {
	switch msg.Method {
	case "initialize":
		s.reply(*msg.ID, s.initializeResult(ctx))
	case "ping":
		s.reply(*msg.ID, json.RawMessage(`{}`))
	case "tools/list":
		s.handleListTools(ctx, msg)
	case "tools/call":
		s.handleCallTool(ctx, msg)
	default:
		s.replyError(*msg.ID, codeMethodNotFound, "method not found: "+msg.Method, nil)
	}
}

// initializeResult 握手结果：声明只有 tools 能力（无 listChanged 推送——工具面变化靠
// 下一轮重新列取，见 README 的"可见性/可执行性"分线）。
func (s *server) initializeResult(ctx context.Context) json.RawMessage {
	res := struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions,omitempty"`
	}{ProtocolVersion: protocolVersion, Instructions: s.info.Instructions}
	res.Capabilities = map[string]any{"tools": map[string]any{"listChanged": false}}
	res.ServerInfo.Name, res.ServerInfo.Version = s.info.Name, s.info.Version
	b, err := json.Marshal(res)
	if err != nil {
		// 三个 string 字段 + 一张字面量 map，Marshal 不可能失败；真失败了也不能回半截 JSON。
		glog.Errorw(ctx, "mcp bridge: 握手结果序列化失败", "err", err)
		return json.RawMessage(`{}`)
	}
	return b
}

// handleListTools 处理 tools/list：取整页 → 按 PageSize 切页 → 给出不透明游标。
func (s *server) handleListTools(ctx context.Context, msg rpcMessage) {
	var params struct {
		Cursor string `json:"cursor"`
	}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.replyError(*msg.ID, codeInvalidParams, "params 非法："+err.Error(), nil)
			return
		}
	}
	offset := 0
	if params.Cursor != "" {
		n, err := strconv.Atoi(params.Cursor)
		if err != nil || n < 0 {
			// 游标不透明，但"认不出来的游标"必须显式报错：静默当第一页会让客户端死循环翻页。
			s.replyError(*msg.ID, codeInvalidParams, "cursor 非法（应由上一次 tools/list 的 nextCursor 原样回传）", nil)
			return
		}
		offset = n
	}
	tools, err := s.h.ListTools(ctx)
	if err != nil {
		glog.Warnw(ctx, "mcp bridge: tools/list 失败", "err", err)
		s.replyError(*msg.ID, codeInternalError, "tools/list 失败："+err.Error(), nil)
		return
	}
	if offset > len(tools) {
		offset = len(tools)
	}
	end := offset + s.pageSize
	if end > len(tools) {
		end = len(tools)
	}
	result := struct {
		Tools      []Tool `json:"tools"`
		NextCursor string `json:"nextCursor,omitempty"`
	}{Tools: tools[offset:end]}
	// 空面必须是 `[]` 而不是 `null`：MCP 规范里 tools 是数组，严格的客户端（dsh）会把
	// `null` 当类型错误——"没工具"与"字段坏了"必须是两种可分辨的响应。
	if result.Tools == nil {
		result.Tools = []Tool{}
	}
	if end < len(tools) {
		result.NextCursor = strconv.Itoa(end)
	}
	b, err := json.Marshal(result)
	if err != nil {
		glog.Errorw(ctx, "mcp bridge: tools/list 结果序列化失败", "err", err)
		s.replyError(*msg.ID, codeInternalError, "tools/list 结果序列化失败", nil)
		return
	}
	s.reply(*msg.ID, b)
}

// handleCallTool 处理 tools/call。
func (s *server) handleCallTool(ctx context.Context, msg rpcMessage) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(msg.Params) == 0 {
		s.replyError(*msg.ID, codeInvalidParams, "tools/call 缺少 params（name 必填）", nil)
		return
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		s.replyError(*msg.ID, codeInvalidParams, "params 非法："+err.Error(), nil)
		return
	}
	if params.Name == "" {
		s.replyError(*msg.ID, codeInvalidParams, "tools/call 的 name 必填", nil)
		return
	}
	res, err := s.h.CallTool(ctx, params.Name, params.Arguments)
	if err != nil {
		if errors.Is(err, ErrToolNotFound) {
			s.replyError(*msg.ID, codeInvalidParams, "未知工具："+params.Name, nil)
			return
		}
		// 工具**执行**失败 → 回 isError 结果（模型看得到原因，会话不炸）。内容按 MCP 的
		// 文本块形状给出，与下游的真实文本块同形，底座无需分辨两种错误。
		glog.Warnw(ctx, "mcp bridge: tools/call 失败", "tool", params.Name, "err", err)
		s.reply(*msg.ID, errorResult(err.Error()))
		return
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		glog.Errorw(ctx, "mcp bridge: tools/call 结果序列化失败", "tool", params.Name, "err", merr)
		s.reply(*msg.ID, errorResult("结果序列化失败："+merr.Error()))
		return
	}
	s.reply(*msg.ID, b)
}

// errorResult 合成 `isError:true` 的工具结果（单条文本块）。
func errorResult(text string) json.RawMessage {
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	res := struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}{Content: []json.RawMessage{blk}, IsError: true}
	b, err := json.Marshal(res)
	if err != nil {
		return json.RawMessage(`{"content":[],"isError":true}`)
	}
	return b
}

// replyParseError 回一条 -32700（id 未知时按规范用 id:null）。
func (s *server) replyParseError(id *json.RawMessage, msg string) {
	if id == nil {
		raw := json.RawMessage("null")
		id = &raw
	}
	s.replyError(*id, codeParseError, msg, nil)
}

func (s *server) reply(id json.RawMessage, result json.RawMessage) {
	s.write(rpcMessage{JSONRPC: "2.0", ID: &id, Result: result})
}

func (s *server) replyError(id json.RawMessage, code int, msg string, data json.RawMessage) {
	s.write(rpcMessage{JSONRPC: "2.0", ID: &id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

// write 写一帧（持有写锁：并发响应不得交错；帧内不含裸换行——json.Marshal 会转义）。
func (s *server) write(msg rpcMessage) {
	// 这一层不穿 ctx：两条日志说的都是"这一帧写不出去"（连接级事实，与某个请求无关），
	// 而为此改 reply/replyError 的签名要动近 20 处调用点。
	ctx := context.Background()
	b, err := json.Marshal(msg)
	if err != nil {
		glog.Errorw(ctx, "mcp bridge: 响应序列化失败", "err", err)
		return
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.w.Write(b); err != nil {
		// 写不出去说明父进程已经走了；不在这里退出（读侧会因 EOF 收敛），只记日志。
		glog.Warnw(ctx, "mcp bridge: 写响应失败（父进程是否已退出？）", "err", err)
	}
}

// ——— 网关条目的参数形态（注入端与网关端**同源**） ———

// GatewayServerName 网关条目在 ACP `mcpServers` 里的 name。
//
// 常量放这里而不是 manager：注入端要用它拼条目、底座会在会话里显示它，未来网关自己
// （instructions/日志）也要用；两份字面量迟早漂移。
const GatewayServerName = "mate-gateway"

// GatewayCredCwdEnv 核心经网关条目的 `env` 下发的**凭据 cwd**（平台装配值，不是密钥）。
//
// 语义：`<cwd>/.env` 那一层的 `cwd` = **用户启动 `mate serve` 的那个目录**。
//
// 为什么必须由核心显式交下去，而不是网关自己 `os.Getwd()`：网关是**底座 spawn 的子进程**，
// 它的 cwd（通常是会话工作区）是用户没选过的目录；两个进程各自取自己的 cwd，同一个引用就会
// 解析出不同的值——而这件事**完全静默**（两边各自的测试都绿）。同一个引用必须给同一个答案。
//
// 为什么走条目的 `env` 而不是命令行参数：`Args` 整体进能力指纹（`mcpFingerprintParts` 逐字带上
// `Command`/`Args`），而 cwd 是**值**——换个目录启动 `mate serve` 不该让所有会话换一次底座进程；
// `Env` 只把**键名**入指纹（`EnvKeys`），键名恒定、取值不入，正好是"改指向不换代际"这条语义。
//
// 刻意**不进** `pkg/runtime/isolation.go` 的隔离表：那张表管的是"别让底座的子进程去读写用户
// 自己的 home"，而这是我们自己的装配值，随条目一对一携带（也因此不依赖进程环境继承是否完整）。
const GatewayCredCwdEnv = "MATE_MCP_CRED_CWD"

// GatewayScope 网关条目的 scope：注入端拼参数、网关端解参数，**两侧共用这一个类型**。
type GatewayScope struct {
	RuntimeID string
	// McpsPinned 会话钉住的 MCP 白名单（`sessions.capability_json` 的 `mcps`）：
	//
	//	nil    = 该会话**没有**钉（全量；缺省路径就是这一支）
	//	非 nil = 以它为准（空切片 = 本会话一个远程 MCP 都不要）
	//
	// 为什么要带上它：会话钉住的白名单是一条**用户约束**（可能比 agent 声明更窄，也可能更宽）。
	// 不带它的后果不是"少几个工具"，而是**按 agent 默认面把授权放大**——钉过 `["a"]` 的会话会
	// 看到全部远程 server。它与 P-2 的反例（`--servers a,b,c`）不是一回事：那条禁的是把
	// **定义集**写进条目（增删一个 server 就换条目/换代际）；这里带的是**用户的选定**，
	// 它不因定义集变化而变化，钉住同一份选定的多个会话得到逐字相同的条目。
	McpsPinned *[]string
}

// GatewayBridgeArgs 拼网关子命令的参数（不含可执行文件名）。
//
// **不含定义集**——这是本计划最关键的一条（P-2）：条目一旦只由 scope（runtime [+ 会话钉住的
// 白名单]）决定，"加一个远程 MCP server"就不改变条目、也就不换代际。反例 `--servers a,b,c`
// 会让每次增删都换一次代际，直接毁掉网关的头号收益。
//
// v19 起 scope 里没有 `--agent`：会话不再有 agent 身份，底座由构建期固定（`--runtime` 就是它）。
func GatewayBridgeArgs(sc GatewayScope) []string {
	args := []string{"mcp", "bridge", "--runtime", sc.RuntimeID}
	if sc.McpsPinned != nil {
		// JSON 数组：能**无歧义**表达"显式空"（`[]` = 一个都不要）与任意 id 词法
		// （server id 允许空格等字符，逗号分隔会歧义）。这里不走 shell，故无需转义。
		b, err := json.Marshal(*sc.McpsPinned)
		if err != nil {
			// []string 的 Marshal 不会失败；真失败了也不能拼出半截参数——回退成显式空集
			// （更窄的一支，宁可少给工具也不放大授权）。
			b = []byte("[]")
		}
		args = append(args, "--mcps", string(b))
	}
	return args
}

// ParseScopeArgs 解析 GatewayBridgeArgs 拼出的那串参数（两侧同形，见 GatewayBridgeArgs）。
//
// 未知参数**报错而不是忽略**：参数漂移（比如将来加了 `--servers` 而网关端还没实现）必须
// 当场失败——静默忽略会让网关"起来了但 scope 不对"，症状是"工具面不对"，最难查的一类。
func ParseScopeArgs(args []string) (GatewayScope, error) {
	var sc GatewayScope
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--runtime", "--mcps":
			if i+1 >= len(args) {
				return GatewayScope{}, fmt.Errorf("%s 缺少取值", args[i])
			}
			v := args[i+1]
			i++
			switch args[i-1] {
			case "--runtime":
				sc.RuntimeID = v
			case "--mcps":
				var pinned []string
				if err := json.Unmarshal([]byte(v), &pinned); err != nil {
					return GatewayScope{}, fmt.Errorf("--mcps 须为 JSON 数组（未知取值 %q）：%w", v, err)
				}
				if pinned == nil {
					pinned = []string{} // `[]` 必须解成"非 nil 的空集"，否则三态退化
				}
				sc.McpsPinned = &pinned
			}
		default:
			return GatewayScope{}, fmt.Errorf("未知参数 %q", args[i])
		}
	}
	if sc.RuntimeID == "" {
		return GatewayScope{}, errors.New("--runtime 必填（网关条目的 scope：注入时由平台拼出）")
	}
	for _, id := range derefPinned(sc.McpsPinned) {
		if strings.TrimSpace(id) == "" {
			return GatewayScope{}, errors.New("--mcps 里不得有空 id（显式空集请写 []）")
		}
	}
	return sc, nil
}

// derefPinned 空安全取值（nil → nil）。
func derefPinned(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}
