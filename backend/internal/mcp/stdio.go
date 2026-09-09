package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
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

func listToolsStdio(ctx context.Context, s Server) (Result, error) {
	if !filepath.IsAbs(s.Command) {
		return Result{}, errf("mcp_command_invalid", "stdio server %q 的 command 须为绝对路径（当前 %q）", s.ID, s.Command)
	}
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, errf("mcp_spawn_failed", "stdin 管道创建失败：%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, errf("mcp_spawn_failed", "stdout 管道创建失败：%v", err)
	}
	tail := newTailBuffer(4 << 10)
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return Result{}, errf("mcp_spawn_failed", "启动 %q 失败：%v", s.Command, err)
	}
	tr := &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		rd:     bufio.NewReaderSize(stdout, 64<<10),
		stderr: tail,
	}
	res, err := probe(ctx, tr)
	if err != nil {
		if tail := tr.stderr.String(); tail != "" {
			return Result{}, fmt.Errorf("%w（stderr: %s）", err, lastLine(tail))
		}
	}
	return res, err
}

func (t *stdioTransport) close() {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
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
			return fmt.Errorf("JSON-RPC 错误：%s", msg.Error.String())
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
