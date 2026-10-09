package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// ——— stdio 传输（换行分隔 JSON-RPC 2.0） ———
//
// 生命周期：spawn 子进程 → 探测 → close（kill + wait）。读取依赖 exec.CommandContext 的
// ctx 取消：ctx 到期杀进程 → stdout EOF → 阻塞读立即返回（无需额外 deadline 机制）。

type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	rd     *bufio.Reader
	stderr *tailBuffer

	mu     sync.Mutex
	nextID int
}

// tailBuffer 有界 stderr 捕获（保留尾部，用于失败诊断；不污染 stdout 协议流）。
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
	cap  int
}

func newTailBuffer(capBytes int) *tailBuffer { return &tailBuffer{cap: capBytes} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.cap {
		b.data = b.data[len(b.data)-b.cap:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// dialStdio 拉起 stdio MCP server 子进程并接好 JSON-RPC 管道（生命周期由调用方 close）。
func dialStdio(ctx context.Context, s Server) (transport, error) {
	if !filepath.IsAbs(s.Command) {
		return nil, errf("mcp_command_invalid", "stdio server %q 的 command 须为绝对路径（当前 %q）", s.ID, s.Command)
	}
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	setupProcAttr(cmd) // unix: 独立进程组，close 时连孙进程一起回收（npx → node → server）
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errf("mcp_spawn_failed", "stdin 管道创建失败：%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errf("mcp_spawn_failed", "stdout 管道创建失败：%v", err)
	}
	tail := newTailBuffer(4 << 10)
	cmd.Stderr = tail
	// Server.Env 是**追加**在进程环境之上（保留继承语义：HOME/PATH 之类仍来自父进程）。
	// 只在非空时设 Env：`cmd.Env = nil` 与"继承"虽等价，但会让"这里到底动没动环境"看不出来。
	if len(s.Env) > 0 {
		env := os.Environ()
		names := make([]string, 0, len(s.Env))
		for n := range s.Env {
			names = append(names, n)
		}
		sort.Strings(names) // 确定性：同一份定义每次拉起的环境序一致（便于比对与排障）
		for _, n := range names {
			env = append(env, n+"="+s.Env[n])
		}
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		return nil, errf("mcp_spawn_failed", "启动 %q 失败：%v", s.Command, err)
	}
	return &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		rd:     bufio.NewReaderSize(stdout, 64<<10),
		stderr: tail,
	}, nil
}

func (t *stdioTransport) close() {
	_ = t.stdin.Close()
	// 杀**整棵进程树**（unix 下按进程组，见 proc_unix.go）。此前只 Process.Kill()，对
	// `npx -y @modelcontextprotocol/server-*` 这类会自己 fork 的命令会留下孤儿孙进程。
	_ = killProc(t.cmd)
	_ = t.cmd.Wait()
}

func (t *stdioTransport) write(msg rpcMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := t.stdin.Write(b); err != nil {
		return err
	}
	return nil
}

func (t *stdioTransport) call(_ context.Context, method string, params any, out any) error {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	rawID := json.RawMessage(strconv.Itoa(id))
	if err := t.write(rpcMessage{JSONRPC: "2.0", ID: &rawID, Method: method, Params: rawParams}); err != nil {
		return err
	}
	return t.await(id, out)
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return t.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: rawParams})
}

// await 读到 id 匹配的响应为止；期间忽略通知，并应答服务端 ping（其余反向请求回 method_not_found）。
func (t *stdioTransport) await(id int, out any) error {
	want := strconv.Itoa(id)
	for {
		line, err := t.rd.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return fmt.Errorf("读取响应失败：%v", err)
		}
		if len(line) > maxResponseBytes {
			return fmt.Errorf("响应超过 %d 字节上限", maxResponseBytes)
		}
		var msg rpcMessage
		if jerr := json.Unmarshal(line, &msg); jerr != nil {
			return fmt.Errorf("响应非 JSON：%v", jerr)
		}
		// 服务端反向请求（有 method + id）：按最小语义应答，避免 server 等待超时。
		if msg.Method != "" && msg.ID != nil {
			t.reply(*msg.ID, msg.Method)
			continue
		}
		if msg.ID == nil || string(*msg.ID) != want {
			continue // 通知或其它请求的响应：探测流程串行，丢弃
		}
		if msg.Error != nil {
			return newRPCResponseError(msg.Error)
		}
		if out == nil {
			return nil
		}
		if len(msg.Result) == 0 {
			return errors.New("响应缺少 result")
		}
		return json.Unmarshal(msg.Result, out)
	}
}

func (t *stdioTransport) reply(id json.RawMessage, method string) {
	var res json.RawMessage
	if method == "ping" {
		res = json.RawMessage(`{}`)
	}
	if method != "ping" {
		_ = t.write(rpcMessage{JSONRPC: "2.0", ID: &id, Error: &rpcError{Code: -32601, Message: "method not found: " + method}})
		return
	}
	_ = t.write(rpcMessage{JSONRPC: "2.0", ID: &id, Result: res})
}

// lastLine 取文本最后一行（stderr 诊断精简）。
func lastLine(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
