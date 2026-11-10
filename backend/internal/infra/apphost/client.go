package apphost

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxResponseBytes 是单次 RPC 响应的硬上限（默认 8MiB，T-C3.1/T-C3.2）。
//
// 超限即掐断该次 RPC 并回 ErrCodeResponseTooLarge（父侧合成）——陌生代码可能吐一个巨大帧把父进程
// 撑爆（OOM 在内核进程里是全站事故）。流式 credit 窗口（分块回传，magpie 512KiB/窗口）留
// [增强项]：本期先「非流式 + 单次上限」，够挡住"一帧撑爆内存"这条最危险路径。
const MaxResponseBytes = 8 << 20 // 8 MiB

// ErrClientClosed 客户端已关闭（子进程退出 / 主动 Close）。
var ErrClientClosed = errors.New("apphost: RPC 客户端已关闭")

// Client 是一条 host 子进程的 JSON-lines RPC 客户端（多请求并发安全）。
//
// 生命周期：由一个 reader goroutine 持续从子进程 stdout 读行、按 id 把响应派发给等待中的请求；
// 子进程 stdout 关闭（进程退出/崩溃）时，reader 退出并把所有在途请求以 ErrCodeHostGone 唤醒——
// 这样调用方不会无限等待一条永远不会来的响应。
type Client struct {
	// stdin 是往子进程写请求的管道。
	stdin io.WriteCloser
	// stdout 是读子进程响应的管道（reader goroutine 独占）。
	stdout io.ReadCloser

	writeMu sync.Mutex // 串行化写 stdin（一帧一行，帧不可交错）

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan readItem // 在途请求：id → 结果通道（容量 1）

	closed    chan struct{} // 关闭信号
	closeOnce sync.Once

	// maxResponse 单次响应字节上限（默认 MaxResponseBytes；测试可调小以验证超限路径）。
	maxResponse int
}

// readItem 是 reader 派发给某请求的一条结果（响应或读错误）。
type readItem struct {
	resp *Response
	err  error
}

// NewClient 用子进程的 stdin/stdout 管道构造 RPC 客户端，并启动 reader goroutine。
//
// stderr 由调用方另行消费（诊断日志），本客户端不碰它。
func NewClient(stdin io.WriteCloser, stdout io.ReadCloser) *Client {
	return NewClientWithLimit(stdin, stdout, MaxResponseBytes)
}

// NewClientWithLimit 同 NewClient，但可指定单次响应字节上限（测试/特殊场景调小以验证超限熔断）。
func NewClientWithLimit(stdin io.WriteCloser, stdout io.ReadCloser, maxResponse int) *Client {
	if maxResponse <= 0 {
		maxResponse = MaxResponseBytes
	}
	c := &Client{
		stdin:       stdin,
		stdout:      stdout,
		pending:     make(map[int64]chan readItem),
		closed:      make(chan struct{}),
		maxResponse: maxResponse,
	}
	go c.readLoop()
	return c
}

// readLoop 持续读子进程 stdout 的 JSON-lines，按 id 派发；进程退出时唤醒所有在途请求。
func (c *Client) readLoop() {
	// 用 bufio.Scanner 但放大缓冲以容纳大帧（Scan 单行上限由 Buffer 的 max 决定）。
	sc := bufio.NewScanner(c.stdout)
	// 初始 64KiB，最大 = maxResponse + 少量帧头冗余（result 本身不超过响应上限；留 4KiB 给
	// JSON 帧头 id/result 包裹）。超过它即判为该帧过大——reader 退出、客户端熔断为 host_gone，
	// 挡住"陌生代码一帧把父进程撑爆"这条最危险路径。
	maxTok := c.maxResponse + 4*1024
	sc.Buffer(make([]byte, 64*1024), maxTok)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// 子进程写坏了一行：无法配回请求。丢弃并继续（协议错由父侧超时/host 崩溃兜底）。
			// 真正严重的「帧超限」由 Scanner 的 too-long 报错走下面 sc.Err() 分支。
			continue
		}
		c.deliver(resp.ID, readItem{resp: &resp})
	}
	// 读结束：要么 stdout EOF（子进程退出），要么 Scanner 报错（帧超上限 / 读错）。
	readErr := sc.Err()
	var reason error = ErrClientClosed
	if readErr != nil {
		// 帧超过 maxToken（单帧过大）：判为该客户端不可继续用。
		reason = fmt.Errorf("apphost: 读取子进程响应失败: %w", readErr)
	}
	c.shutdown(reason)
}

// deliver 把一条结果交给等待该 id 的请求；无人等待（迟到/未知 id）则丢弃。
func (c *Client) deliver(id int64, item readItem) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		// 非阻塞投递（通道容量 1，且 id 唯一，正常情况下必有空位）。
		select {
		case ch <- item:
		default:
		}
	}
}

// shutdown 关闭客户端并把所有在途请求以 reason 唤醒（幂等）。
func (c *Client) shutdown(reason error) {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[int64]chan readItem)
		c.mu.Unlock()
		for _, ch := range pending {
			select {
			case ch <- readItem{err: reason}:
			default:
			}
		}
		// 尽力关管道（stdin 关闭让子进程见 EOF；stdout 由调用方/内核清理）。
		_ = c.stdin.Close()
	})
}

// Call 发一条 RPC 并等待它的响应（**不做超时**——超时由上层 Host 用每次调用包裹，见 Host 的
// callWithTimeout）。返回 (result, rpcErr, err)：rpcErr 非空 = 子进程回了一条错误帧（业务/RPC 错误）；
// err 非空 = 传输层失败（客户端已关闭 / 写失败 / 读失败）。二者互斥。
func (c *Client) Call(method string, params any) (raw json.RawMessage, rpcErr *RPCError, err error) {
	select {
	case <-c.closed:
		return nil, nil, ErrClientClosed
	default:
	}

	// 参数序列化。
	var paramsJSON RawParams
	if params != nil {
		b, mErr := json.Marshal(params)
		if mErr != nil {
			return nil, nil, fmt.Errorf("apphost: 序列化请求参数失败: %w", mErr)
		}
		paramsJSON = RawParams(b)
	}

	// 铸造请求号 + 登记等待通道。
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	wait := make(chan readItem, 1)
	c.pending[id] = wait
	c.mu.Unlock()

	// 保证异常返回时清掉等待登记。
	cleanup := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	// 写一帧。
	frame, mErr := json.Marshal(Request{ID: id, Method: method, Params: paramsJSON})
	if mErr != nil {
		cleanup()
		return nil, nil, fmt.Errorf("apphost: 序列化请求帧失败: %w", mErr)
	}
	c.writeMu.Lock()
	_, wErr := c.stdin.Write(append(frame, '\n'))
	c.writeMu.Unlock()
	if wErr != nil {
		cleanup()
		return nil, nil, fmt.Errorf("apphost: 写请求到子进程失败: %w", wErr)
	}

	// 等响应。
	item := <-wait
	if item.err != nil {
		return nil, nil, item.err
	}
	if item.resp == nil {
		return nil, nil, fmt.Errorf("apphost: 空响应")
	}
	if item.resp.Error != nil {
		return nil, item.resp.Error, nil
	}
	return json.RawMessage(item.resp.Result), nil, nil
}

// ReceiveEvent 保留给 [增强项] 子进程主动事件（null id 帧）。本期无事件，恒返回 (nil, false)。
//
// 保留这个方法名是给 C7/流式一个明确挂点，避免将来在 readLoop 里散落特判。
func (c *Client) ReceiveEvent() (json.RawMessage, bool) {
	return nil, false
}

// Close 主动关闭客户端（发完 shutdown 后调用）；幂等。
func (c *Client) Close() {
	c.shutdown(ErrClientClosed)
}

// Done 返回一个在客户端关闭时被关闭的 channel（供上层 select 监视子进程存活）。
func (c *Client) Done() <-chan struct{} {
	return c.closed
}
