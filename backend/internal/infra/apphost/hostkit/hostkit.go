// Package hostkit 是**子进程侧**的应用 host SDK 骨架：`mate app-host --pkg=<dir>` 进程用它把
// 一个已装载的应用包「跑起来」，在 stdin/stdout 上应答父进程的 JSON-lines RPC。
//
// 分工（设计 §17.2；本包只做骨架，真业务逻辑由各 app 的包填充 Handlers）：
//   - 本包负责**协议循环**：读一行请求、分发到 Handlers 的对应方法、写一行响应；坏行/recover
//     都在这里兜住，尽量把「协议错误」转成错误帧而不是让进程崩掉。
//   - 各 app 的 Go 包实现 `Handlers` 接口（提供 impl/tool 的清单与执行），由 `mate app-host`
//     在启动时按加载到的包注入。
//
// **为什么要子进程**：运行期第三方包的代码是陌生代码，绝不与内核同进程（决策 v37 安全基线）。
// 崩溃隔离由父进程侧的进程回收保证（见 infra/apphost 的 Host）；本包只管「活着的时候把协议说对」。
//
// 本包**不 import** capability/apppkg 等上层——它只认 lineprotocol（apphost 的帧类型），保持
// 子进程侧尽可能"薄"：越少的依赖面，陌生代码越难借 SDK 摸到内核其它东西。
package hostkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/infra/apphost"
)

// Handlers 是一个应用包在子进程里要回答的四类问题（由各 app 包实现，注入 Serve）。
//
// 全部方法都应**返回错误而不是 panic**：Serve 会 recover panic 并转成 ErrCodeInternal，但那是
// 兜底网，不是让实现随意 panic 的许可（一个 panic 意味着该 app 的这次调用失败，且污染其内部状态）。
type Handlers interface {
	// Describe 自述：包 id/版本/api_version、提供的 impl_id 列表、工具名列表。握手时回给父进程。
	Describe() DescribeInfo
	// ListImpls 本进程提供的 impl_id 列表（可与 Describe 一致）。
	ListImpls() []string
	// ListTools 本进程提供的工具清单（name + 描述 + 参数 schema）。
	ListTools() []apphost.ToolSpec
	// InvokeTool 执行一个工具。返回的 (content, isError, err)：err 非空 = RPC 层失败（协议/未知工具），
	// isError=true 表示工具"正常返回了一个错误结果"（业务层），二者语义不同，不要混。
	InvokeTool(name string, args json.RawMessage) (content json.RawMessage, isError bool, err error)
	// ApplySlot apply 一个 impl（implID）。ctxSnapshot 是父侧可序列化的 ctx 快照。
	ApplySlot(implID string, ctxSnapshot map[string]string) error
	// DisposeSlot 撤销一个 impl（implID）。
	DisposeSlot(implID string) error
}

// DescribeInfo 是 Handlers.Describe 的返回：包自述。
type DescribeInfo struct {
	PackageID      string
	PackageVersion string
	APIVersion     string
	Impls          []string
	Tools          []string
}

// Server 是一个已装配好的 host 服务端（Hold Handlers）。
type Server struct {
	handlers Handlers
	// startTime 进程启动时刻（health.uptime 用）。
	startTime time.Time
	stopOnce  sync.Once
	stopped   chan struct{}
}

// NewServer 用一组 Handlers 构造 host 服务端。
func NewServer(h Handlers) *Server {
	return &Server{handlers: h, startTime: time.Now(), stopped: make(chan struct{})}
}

// Serve 在 r 上读 JSON-lines 请求、往 w 写 JSON-lines 响应，直到读到 EOF / 收到 shutdown。
//
// 纪律（与 `mate mcp` 的 stdout 纪律同源）：
//   - **w 只走协议帧**：诊断/日志一律走调用方传入的 stderr（本函数不写任何非帧内容到 w）。
//   - **坏行不炸进程**：一行无法解析 → 回一条 id=0 的 bad_params 错误帧，继续读下一行。
//   - **单请求 panic 不炸进程**：dispatch 内 recover，转成该请求的 internal 错误帧，继续服务。
//
// 返回 nil 表示正常结束（EOF / shutdown）；返回 error 仅用于底层的读/写致命失败（管道断了）。
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	writer := bufio.NewWriter(w)
	defer func() { _ = writer.Flush() }()

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// 去掉行尾换行；空行跳过（不产生响应）。
			trimmed := trimNewline(line)
			if len(trimmed) > 0 {
				resp := s.handleLine(trimmed)
				if err := writeResponse(writer, resp); err != nil {
					return err
				}
				// 每帧立即 flush：父进程按行读，积压会让 RPC 看起来"卡住"。
				if err := writer.Flush(); err != nil {
					return err
				}
				// shutdown 之后不再读（response 已写出）。
				if resp.isShutdown {
					return nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("hostkit: 读取请求失败: %w", err)
		}
	}
}

// handleLine 解析一行并分发，返回要写回的响应（含一个内部 isShutdown 标记）。
func (s *Server) handleLine(line []byte) responseEnvelope {
	var req apphost.Request
	if err := json.Unmarshal(line, &req); err != nil {
		return responseEnvelope{resp: apphost.Response{
			ID:    0,
			Error: &apphost.RPCError{Code: apphost.ErrCodeBadParams, Message: "请求不是合法 JSON 对象: " + err.Error()},
		}}
	}
	// 单请求 panic 兜底：转成该请求的 internal 错误帧，进程继续服务。
	var resp apphost.Response
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				resp = apphost.Response{
					ID:    req.ID,
					Error: &apphost.RPCError{Code: apphost.ErrCodeInternal, Message: fmt.Sprintf("handler panic: %v", rec)},
				}
			}
		}()
		resp = s.dispatch(req)
	}()
	return responseEnvelope{resp: resp, isShutdown: req.Method == apphost.MethodShutdown}
}

// responseEnvelope 给 Serve 内部用：带一个「这帧之后是否停服」的标记。
type responseEnvelope struct {
	resp       apphost.Response
	isShutdown bool
}

// dispatch 按方法名分发，返回一条响应帧。
func (s *Server) dispatch(req apphost.Request) apphost.Response {
	ok := func(result any) apphost.Response {
		return apphost.Response{ID: req.ID, Result: mustMarshal(result)}
	}
	fail := func(code, msg string) apphost.Response {
		return apphost.Response{ID: req.ID, Error: &apphost.RPCError{Code: code, Message: msg}}
	}

	switch req.Method {
	case apphost.MethodHandshake:
		var p apphost.HandshakeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return fail(apphost.ErrCodeBadParams, "handshake 参数非法: "+err.Error())
			}
		}
		d := s.handlers.Describe()
		return ok(apphost.HandshakeResult{
			PackageID: d.PackageID, PackageVersion: d.PackageVersion, APIVersion: d.APIVersion,
			Impls: d.Impls, Tools: d.Tools,
		})
	case apphost.MethodListImpls:
		return ok(apphost.ListImplsResult{Impls: s.handlers.ListImpls()})
	case apphost.MethodListTools:
		return ok(apphost.ListToolsResult{Tools: s.handlers.ListTools()})
	case apphost.MethodInvokeTool:
		var p apphost.InvokeToolParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return fail(apphost.ErrCodeBadParams, "invokeTool 参数非法: "+err.Error())
			}
		}
		if p.Name == "" {
			return fail(apphost.ErrCodeBadParams, "invokeTool 缺 name")
		}
		content, isErr, err := s.handlers.InvokeTool(p.Name, json.RawMessage(p.Args))
		if err != nil {
			return fail(apphost.ErrCodeToolFailed, err.Error())
		}
		return ok(apphost.InvokeToolResult{Content: apphost.RawResult(content), IsError: isErr})
	case apphost.MethodApplySlot:
		var p apphost.ApplySlotParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return fail(apphost.ErrCodeBadParams, "applySlot 参数非法: "+err.Error())
			}
		}
		if p.ImplID == "" {
			return fail(apphost.ErrCodeBadParams, "applySlot 缺 impl_id")
		}
		if err := s.handlers.ApplySlot(p.ImplID, p.CtxSnapshot); err != nil {
			return fail(apphost.ErrCodeSlotFailed, err.Error())
		}
		return ok(struct{}{})
	case apphost.MethodDisposeSlot:
		var p apphost.DisposeSlotParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return fail(apphost.ErrCodeBadParams, "disposeSlot 参数非法: "+err.Error())
			}
		}
		if p.ImplID == "" {
			return fail(apphost.ErrCodeBadParams, "disposeSlot 缺 impl_id")
		}
		if err := s.handlers.DisposeSlot(p.ImplID); err != nil {
			return fail(apphost.ErrCodeSlotFailed, err.Error())
		}
		return ok(struct{}{})
	case apphost.MethodHealth:
		return ok(apphost.HealthResult{OK: true, UptimeMs: time.Since(s.startTime).Milliseconds()})
	case apphost.MethodShutdown:
		// 正常收尾：不可重入（stopOnce 兜底）。回一条 ok，Serve 见 isShutdown 后停。
		s.stopOnce.Do(func() { close(s.stopped) })
		return ok(apphost.ShutdownResult{OK: true})
	default:
		return fail(apphost.ErrCodeUnknownMethod, "未知方法: "+req.Method)
	}
}

// writeResponse 把一条响应帧编码成一行写出（不 flush，由调用方 flush）。
func writeResponse(w *bufio.Writer, env responseEnvelope) error {
	b, err := json.Marshal(env.resp)
	if err != nil {
		return fmt.Errorf("hostkit: 编码响应失败: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

// trimNewline 去掉行尾的 \n / \r\n。
func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// mustMarshal 序列化一个 result；失败时返回一个内联的 internal 错误帧（几乎不可能发生，
// 仅当 Handlers 返回了不可序列化的值）。
func mustMarshal(v any) apphost.RawResult {
	b, err := json.Marshal(v)
	if err != nil {
		// 兜底：包一个错误对象，避免把整个响应写坏。
		fb, _ := json.Marshal(apphost.RPCError{Code: apphost.ErrCodeInternal, Message: "result 序列化失败: " + err.Error()})
		return apphost.RawResult(fb)
	}
	return apphost.RawResult(b)
}
