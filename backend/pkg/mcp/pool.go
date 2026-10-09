package mcp

// ——— 连接池（M7-2 任务 4："必做项，不是优化项"） ———
//
// 为什么必须池化：`CallTool` 今天是「建连 → initialize → call → 关闭」，一次调用一次完整
// 握手（stdio 还要起进程、HTTP 还要 TLS）。没有网关时这笔成本由底座承担、且它自己决定何时
// 重连；网关在环上之后，**底座的每一次工具调用都会经它**，不池化就是把重连成本直接乘上
// 调用量。故这里按 server 池化已握手的会话。
//
// 四条语义（每条都有对应的测试）：
//
//  1. **同一会话串行**：MCP 会话是服务端有状态资源，请求/响应虽按 id 配对，但会话本身不可
//     并发使用。故每个池化会话带自己的互斥量：同一 server 的调用串行、不同 server 并行。
//  2. **服务端错误不作废会话**：`-32602`/`-32601` 这类 JSON-RPC 错误响应说明会话健康
//     （`IsRPCResponseError`）；只有传输层故障才作废重建。
//  3. **定义/密钥一变，旧会话立刻作废**：池按 server **id** 索引，但会话里存着建连时的
//     `Server` 快照；下次取用发现快照与当下定义不同（URL、args、密钥、header 任一变化）
//     就丢掉重建。这是"密钥轮换无需换会话/重启"（M7-2 验收）的落地方式：**不缓存凭据**，
//     只缓存"用某一份凭据建立的那条连接"。
//  4. **有界**：会话数上限 + 空闲回收。网关是底座拉起的子进程，不能无限攒远端连接。
//
// 快照比较用 `json.Marshal` 的字节（含密钥）：它只在**进程内存**里比较，绝不进日志、不进
// 指纹、不进任何对外面（本文件没有任何一处把 `serverKey` 写进日志）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// PoolDefaultMaxSessions 在册会话上限（同时连着的远端 server 数）。
	PoolDefaultMaxSessions = 16
	// PoolDefaultIdleTimeout 空闲回收阈值（超过即关闭，下次用重建）。
	PoolDefaultIdleTimeout = 5 * time.Minute
)

// PoolOptions 连接池参数（零值即可用）。
type PoolOptions struct {
	MaxSessions int
	IdleTimeout time.Duration
	// Now 时钟注入（测试用；nil → time.Now）。空闲回收不靠 sleep 断言，靠它。
	Now func() time.Time
	// Logger 诊断出口（nil → slog.Default()）。只记 server id 与原因，不记任何凭据。
	Logger *slog.Logger
}

// PoolStats 池的运行计数（诊断/测试用）。
type PoolStats struct {
	Sessions int // 在册会话数
	Spawns   int // 累计建连次数（含重建）
	Reused   int // 累计复用次数
	Evicted  int // 累计因上限/空闲/定义变化而作废的次数
}

// pooledSession 一条已握手的会话 + 它的使用状态。
type pooledSession struct {
	id   string
	key  string // 建连时的 Server 快照（含密钥；内存内比较用）
	tr   transport
	init initializeResult

	mu sync.Mutex // 会话级串行（语义 1）

	// lastUsed 是**原子**的纳秒时间戳，不是 time.Time 字段。
	//
	// 为什么：它在两条不同的锁下被访问——持会话锁的 `with` 写它（每次调用都会刷新），持池锁的
	// `idle`/`evictForRoomLocked`/`ReapIdle` 读它。用普通字段就是 data race（`-race` 实测坐实，
	// M7-5 的并发用例抓到的）：读写跨两把锁，谁也不能保证对方不在同时动它。用原子量把它降级成
	// "一个数字的快照"——空闲判定本来就只需要近似值。
	lastUsed atomic.Int64
}

// touch 刷新最后使用时间（纳秒时间戳）。
func (ps *pooledSession) touch(now time.Time) { ps.lastUsed.Store(now.UnixNano()) }

// usedAt 读最后使用时间。
func (ps *pooledSession) usedAt() time.Time { return time.Unix(0, ps.lastUsed.Load()) }

// Pool 按 server 池化 MCP 会话（零值不可用，用 NewPool）。
type Pool struct {
	opts PoolOptions

	mu       sync.Mutex
	sessions map[string]*pooledSession
	zombies  []*pooledSession // 摘掉了但当时正被占用、还没关掉的（下次回收再试）
	closed   bool
	stats    PoolStats
}

// NewPool 构造连接池。
func NewPool(opts PoolOptions) *Pool {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = PoolDefaultMaxSessions
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = PoolDefaultIdleTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Pool{opts: opts, sessions: make(map[string]*pooledSession)}
}

// Stats 取运行计数。
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.Sessions = len(p.sessions)
	return s
}

// ListTools 池化版 `tools/list`（握手复用，翻页与一次性版本同源）。
func (p *Pool) ListTools(ctx context.Context, s Server) (Result, error) {
	var res Result
	err := p.with(ctx, s, func(ctx context.Context, tr transport, init initializeResult) error {
		res.ServerName, res.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
		tools, err := listToolsPages(ctx, tr)
		if err != nil {
			return wrapProbe(ctx, "mcp_tools_list_failed", "tools/list 失败", err)
		}
		res.Tools = tools
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// CallTool 池化版 `tools/call`（参数校验与错误码与一次性版本一致）。
func (p *Pool) CallTool(ctx context.Context, s Server, name string, args json.RawMessage) (CallResult, error) {
	if strings.TrimSpace(name) == "" {
		return CallResult{}, errf("mcp_tool_name_required", "工具名必填")
	}
	var out CallResult
	err := p.with(ctx, s, func(ctx context.Context, tr transport, init initializeResult) error {
		out.ServerName, out.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
		params := map[string]any{"name": name}
		if len(args) > 0 {
			var obj map[string]any
			if err := json.Unmarshal(args, &obj); err != nil {
				return errf("mcp_tool_args_invalid", "arguments 须为 JSON 对象：%v", err)
			}
			if obj != nil {
				params["arguments"] = obj
			}
		}
		var res struct {
			Content           []json.RawMessage `json:"content"`
			IsError           bool              `json:"isError"`
			StructuredContent json.RawMessage   `json:"structuredContent"`
		}
		if err := tr.call(ctx, "tools/call", params, &res); err != nil {
			return wrapProbe(ctx, "mcp_tools_call_failed", "tools/call 失败", err)
		}
		out.Content, out.IsError, out.StructuredContent = res.Content, res.IsError, res.StructuredContent
		return nil
	})
	if err != nil {
		return CallResult{}, err
	}
	return out, nil
}

// with 取会话（必要时建连）→ 串行执行 → 按结果决定是否作废。
func (p *Pool) with(ctx context.Context, s Server, fn func(ctx context.Context, tr transport, init initializeResult) error) error {
	ps, err := p.acquire(ctx, s)
	if err != nil {
		return err
	}
	// acquire 返回时**已持会话锁**（会话级串行，语义 1）：命中与新建两条路径都在自己的
	// 临界区里加好锁再返回，避免"检查—加锁"之间被换出。
	defer ps.mu.Unlock()
	ps.touch(p.opts.Now())

	err = fn(ctx, ps.tr, ps.init)
	if err != nil && !IsRPCResponseError(err) {
		// 传输层故障（读失败/进程死/HTTP 5xx）→ 会话不可信，就地作废。
		// 服务端错误响应（-32602 等）落在上面那个判断之外：会话是好的，不作废（语义 2）。
		p.dropLocked(ps)
	}
	return err
}

// acquire 返回**已加锁**的会话（调用方负责 Unlock）：命中即复用，定义变了/死了/超时就重建。
func (p *Pool) acquire(ctx context.Context, s Server) (*pooledSession, error) {
	key := serverKey(s)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errf("mcp_pool_closed", "连接池已关闭")
	}
	if ps, ok := p.sessions[s.ID]; ok {
		switch {
		case ps.key != key:
			// 定义或密钥变了：旧会话必须死——这是"密钥轮换对底座无感"的落点（语义 3）。
			p.opts.Logger.Info("mcp pool: 定义已变化，作废旧会话", "server", s.ID)
			p.detachLocked(ps)
		case p.idle(ps):
			p.opts.Logger.Debug("mcp pool: 会话空闲超时，回收", "server", s.ID)
			p.detachLocked(ps)
		default:
			p.stats.Reused++
			p.mu.Unlock()
			ps.mu.Lock()
			return ps, nil
		}
	}
	p.mu.Unlock()

	// 建连在锁外（可能要起进程 / TLS 握手，几百毫秒级，不能挡住整池）。
	tr, init, err := openSession(ctx, s)
	if err != nil {
		return nil, err
	}
	ps := &pooledSession{id: s.ID, key: key, tr: tr, init: init}
	ps.touch(p.opts.Now())

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		tr.close()
		return nil, errf("mcp_pool_closed", "连接池已关闭")
	}
	// 并发下可能已有别的 goroutine 建好了同一条：用它，关掉我们这条（不浪费远端连接）。
	if exist, ok := p.sessions[s.ID]; ok && exist.key == key {
		p.mu.Unlock()
		tr.close()
		exist.mu.Lock()
		return exist, nil
	}
	p.evictForRoomLocked()
	p.sessions[s.ID] = ps
	p.stats.Spawns++
	p.mu.Unlock()

	ps.mu.Lock()
	return ps, nil
}

// detachLocked 把会话从池里摘掉并关闭（调用方持 p.mu；可能转成 zombie）。
func (p *Pool) detachLocked(ps *pooledSession) {
	if cur, ok := p.sessions[ps.id]; ok && cur == ps {
		delete(p.sessions, ps.id)
	}
	p.stats.Evicted++
	p.closeOrZombieLocked(ps)
}

// dropLocked 传输故障后的作废（调用方**持 ps.mu**）。
//
// 这里会去拿 p.mu：锁序是 ps.mu → p.mu，而反方向（p.mu → ps.mu）只出现在
// `evictForRoomLocked`/`detachLocked` 的 `TryLock`（非阻塞），故不会死锁。
func (p *Pool) dropLocked(ps *pooledSession) {
	p.mu.Lock()
	if cur, ok := p.sessions[ps.id]; ok && cur == ps {
		delete(p.sessions, ps.id)
	}
	p.stats.Evicted++
	p.mu.Unlock()
	ps.tr.close() // 持 ps.mu：此刻没有别人在用这条会话
}

// closeOrZombieLocked 能立刻拿到会话锁就关闭，否则登记为 zombie（下次回收再试）。
//
// 为什么不用阻塞加锁：本函数在持 p.mu 时被调用（换出/腾位路径），阻塞等一条正在使用的
// 会话会把整个池卡住。取不到锁说明"有人正在用"，那就下次再说（池仍然不会漏：zombie 列表
// 每次 ReapIdle/Close 都会重试）。
func (p *Pool) closeOrZombieLocked(ps *pooledSession) {
	if ps.mu.TryLock() {
		ps.tr.close()
		ps.mu.Unlock()
		return
	}
	p.zombies = append(p.zombies, ps)
}

// evictForRoomLocked 腾一个位子（调用方持 p.mu）：优先回收最久未用的会话。
func (p *Pool) evictForRoomLocked() {
	if len(p.sessions) < p.opts.MaxSessions {
		return
	}
	var oldest *pooledSession
	for _, ps := range p.sessions {
		if oldest == nil || ps.usedAt().Before(oldest.usedAt()) {
			oldest = ps
		}
	}
	if oldest == nil {
		return
	}
	p.opts.Logger.Warn("mcp pool: 会话数达上限，回收最久未用的那条", "server", oldest.id, "max", p.opts.MaxSessions)
	p.detachLocked(oldest)
}

// idle 报告会话是否超过空闲阈值（调用方持 p.mu）。
func (p *Pool) idle(ps *pooledSession) bool {
	return p.opts.Now().Sub(ps.usedAt()) >= p.opts.IdleTimeout
}

// ReapIdle 回收空闲超时的会话（返回关闭条数），并重试之前没关掉的 zombie。
//
// 显式方法而不是后台 goroutine：网关的回收时机可测（测试直接调它 + 注入时钟），
// 生命周期也少一条需要收尾的 goroutine。调用方（网关）按需起 ticker 调它。
func (p *Pool) ReapIdle() int {
	now := p.opts.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, ps := range p.sessions {
		if now.Sub(ps.usedAt()) >= p.opts.IdleTimeout {
			delete(p.sessions, ps.id)
			p.stats.Evicted++
			p.closeOrZombieLocked(ps)
			n++
		}
	}
	// zombie 重试（只保留仍拿不到锁的）。
	keep := p.zombies[:0]
	for _, ps := range p.zombies {
		if ps.mu.TryLock() {
			ps.tr.close()
			ps.mu.Unlock()
			continue
		}
		keep = append(keep, ps)
	}
	p.zombies = keep
	return n
}

// Close 关闭全部会话（幂等）。网关退出路径调用它（M7-5）。
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	victims := make([]*pooledSession, 0, len(p.sessions)+len(p.zombies))
	for _, ps := range p.sessions {
		victims = append(victims, ps)
	}
	victims = append(victims, p.zombies...)
	p.sessions = map[string]*pooledSession{}
	p.zombies = nil
	p.mu.Unlock()

	// 逐个**阻塞**取锁再关：这是退出路径，等一会儿好过把远端连接留成孤儿。
	for _, ps := range victims {
		ps.mu.Lock()
		ps.tr.close()
		ps.mu.Unlock()
	}
}

// serverKey 建连相关字段的快照（含密钥）。
//
// 它是"定义变了就重建"的判据，因此必须覆盖**所有影响连接**的字段：传输、命令/参数、URL、
// header、env。map 经 `json.Marshal` 按键排序，同内容必然同字节。
//
// **纪律**：返回值只用于进程内比较——不得进日志、事件、指纹或任何对外面（它由密钥派生）。
func serverKey(s Server) string {
	b, err := json.Marshal(struct {
		ID        string
		Transport string
		Command   string
		Args      []string
		URL       string
		Headers   map[string]string
		Env       map[string]string
	}{s.ID, s.Transport, s.Command, s.Args, s.URL, s.Headers, s.Env})
	if err != nil {
		// 全是 string/[]string/map[string]string，Marshal 不可能失败；真失败了也不能回一个
		// "看起来相等"的键（那会让旧会话被错误复用）——退回 id 之外再加一个随机性来源。
		return s.ID + "\x00!" + fmt.Sprint(len(s.Env)+len(s.Headers)) + s.URL + s.Command + stringsJoin(s.Args)
	}
	return s.ID + "\x00" + string(b)
}

// stringsJoin/sortedKeysOfEnv 小工具（避免为一行比较引入新依赖）。
func stringsJoin(in []string) string {
	out := ""
	for _, v := range in {
		out += "\x1f" + v
	}
	return out
}

// sortedEnvKeys 诊断用：只回键名（值可能是密钥）。
func sortedEnvKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
