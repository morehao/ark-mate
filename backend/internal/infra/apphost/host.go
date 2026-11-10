package apphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/morehao/ark-mate/internal/core/failure"
)

// ——— 隔离 host 的生命周期管理（T-C3.1 / T-C3.3）———
//
// 一个 Host 管理**一个应用包的一个长驻子进程**。父进程（内核）经 JSON-lines 与它交谈；子进程崩溃
// 只让这一个 Host 挂掉（该包的 impl/tool 标记 FAILED），**绝不影响内核与其它包的 Host**——这是
// 「每包一进程」隔离的落点（设计 §17.2 / 决策 v37 安全基线）。
//
// 状态机（Crash 恢复，T-C3.3）：
//
//	starting --spawn+handshake ok--> ready
//	   │                              │
//	   │ spawn/handshake 失败          │ 子进程退出/崩溃
//	   v                              v
//	restarting --退避后重试--> starting  ... 连续失败达到 maxRestarts --> failed（永久，需运维介入）
//
// 单次 RPC 超时：每次 Call 用 ctx 包裹（callTimeout）；超时回 ErrCodeTimeout（父侧合成），并把该
// 次失败计为该 Host 的一次「不健康」信号（是否据此重启由上层策略定，本层只暴露结果）。

// Default 常量：重启退避与超时（可在 HostConfig 覆盖）。
const (
	// DefaultCallTimeout 单次 RPC 默认超时。
	DefaultCallTimeout = 30 * time.Second
	// DefaultHandshakeTimeout 握手默认超时（拉起子进程 + 首次往返）。
	DefaultHandshakeTimeout = 10 * time.Second
	// DefaultMaxRestarts 连续失败多少次后标记 FAILED（不含首次启动）。
	DefaultMaxRestarts = 3
	// DefaultRestartBackoff 首次重启退避；此后每次翻倍（指数退避），封顶 maxRestartBackoff。
	DefaultRestartBackoff = 200 * time.Millisecond
	maxRestartBackoff     = 5 * time.Second
)

// State 是一个 Host 的生命周期状态。
type State string

const (
	// StateStarting 正在拉起/握手。
	StateStarting State = "starting"
	// StateReady 已握手，可服务。
	StateReady State = "ready"
	// StateRestarting 子进程崩溃，正在退避重试。
	StateRestarting State = "restarting"
	// StateFailed 连续失败达上限，永久 FAILED（该包 impl/tool 全部不可用，需运维介入）。
	StateFailed State = "failed"
	// StateClosed 被主动关闭。
	StateClosed State = "closed"
)

// SpawnFunc 拉起一个子进程，返回它的 stdin（父写）与 stdout（父读），以及一个 wait 函数（阻塞到
// 进程退出并返回错误）与一个 kill 函数。抽成函数是为了让测试注入一个"样例 host"二进制/进程，
// 而不是真的依赖 `mate app-host` 已编译（生产用 ExecSpawn 拉起同二进制）。
type SpawnFunc func(ctx context.Context, pkgDir string) (stdin io.WriteCloser, stdout io.ReadCloser, wait func() error, kill func() error, err error)

// HostConfig 是一个 Host 的配置。
type HostConfig struct {
	// PackageID 包 id（诊断/日志/状态用）。
	PackageID string
	// PkgDir 包目录（传给子进程 `--pkg`）。
	PkgDir string
	// APIVersion 父侧期望的核心 API 版本（握手比对）。
	APIVersion string
	// Spawn 拉起子进程的方式（生产用 ExecSpawn；测试注入样例）。
	Spawn SpawnFunc
	// CallTimeout 单次 RPC 超时（0 = DefaultCallTimeout）。
	CallTimeout time.Duration
	// HandshakeTimeout 握手超时（0 = DefaultHandshakeTimeout）。
	HandshakeTimeout time.Duration
	// MaxRestarts 连续失败上限（0 = DefaultMaxRestarts）。
	MaxRestarts int
	// RestartBackoff 首次退避（0 = DefaultRestartBackoff）。
	RestartBackoff time.Duration
	// MaxResponseBytes 单次 RPC 响应上限（0 = MaxResponseBytes 8MiB）。
	MaxResponseBytes int
	// Logger 可选诊断日志出口（nil = 丢弃）。签名 (level, msg)；level ∈ {"info","warn","error"}。
	Logger func(level, msg string)
}

// Host 一个应用包的长驻隔离子进程。
type Host struct {
	cfg HostConfig

	mu       sync.Mutex
	state    State
	client   *Client
	descr    HandshakeResult // 最近一次成功握手的自述
	reason   string          // failed/last 失败原因（人话）
	restarts int             // 已发生的重启次数
	closed   bool
}

// NewHost 构造（不启动）一个 Host。
func NewHost(cfg HostConfig) *Host {
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = DefaultMaxRestarts
	}
	if cfg.RestartBackoff <= 0 {
		cfg.RestartBackoff = DefaultRestartBackoff
	}
	if cfg.Logger == nil {
		cfg.Logger = func(string, string) {}
	}
	return &Host{cfg: cfg, state: StateStarting}
}

// Start 拉起子进程并握手。成功 → StateReady；失败按退避重启，达上限 → StateFailed（返回 error）。
//
// 生成 ctx 用于 Spawn（关 Host 时 cancel，杀子进程）。Start 阻塞到「握手成功」或「达上限 FAILED」。
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.state == StateReady {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	return h.startWithBackoff(ctx)
}

// startWithBackoff 是 Start 的重试主体：拉起 → 握手；失败则退避后重试，达上限 FAILED。
func (h *Host) startWithBackoff(ctx context.Context) error {
	backoff := h.cfg.RestartBackoff
	var lastErr error
	for attempt := 0; attempt <= h.cfg.MaxRestarts; attempt++ {
		if attempt > 0 {
			h.setRestarting(fmt.Sprintf("重启第 %d 次前的退避 %s（上次：%v）", attempt, backoff, lastErr))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				h.setFailed("启动被取消")
				return ctx.Err()
			}
			backoff *= 2
			if backoff > maxRestartBackoff {
				backoff = maxRestartBackoff
			}
			h.mu.Lock()
			h.restarts++
			h.mu.Unlock()
		}
		err := h.spawnAndHandshake(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		h.cfg.Logger("warn", fmt.Sprintf("[apphost] 包 %s 启动失败（第 %d 次）：%v", h.cfg.PackageID, attempt+1, err))
	}
	h.setFailed(fmt.Sprintf("连续 %d 次启动失败，标记 FAILED：%v", h.cfg.MaxRestarts+1, lastErr))
	return fmt.Errorf("apphost: 包 %q 启动失败（已重试 %d 次）：%w", h.cfg.PackageID, h.cfg.MaxRestarts, lastErr)
}

// spawnAndHandshake 拉一次子进程 + 握手 + 装监控。返回 nil = 就绪。
func (h *Host) spawnAndHandshake(ctx context.Context) error {
	spawnCtx, cancel := context.WithCancel(ctx)
	stdin, stdout, wait, kill, err := h.cfg.Spawn(spawnCtx, h.cfg.PkgDir)
	if err != nil {
		cancel()
		return fmt.Errorf("拉起子进程失败: %w", err)
	}

	client := NewClientWithLimit(stdin, stdout, h.cfg.MaxResponseBytes)
	h.mu.Lock()
	h.client = client
	h.mu.Unlock()

	// 握手（带超时）。
	hsCtx, hsCancel := context.WithTimeout(ctx, h.cfg.HandshakeTimeout)
	hsRes, rpcErr, hsErr := callWithContext(hsCtx, client, MethodHandshake, HandshakeParams{APIVersion: h.cfg.APIVersion})
	hsCancel()
	if hsErr != nil {
		client.Close()
		_ = kill()
		cancel()
		return fmt.Errorf("握手传输失败: %w", hsErr)
	}
	if rpcErr != nil {
		client.Close()
		_ = kill()
		cancel()
		return fmt.Errorf("握手被拒（%s）：%s", rpcErr.Code, rpcErr.Message)
	}
	var desc HandshakeResult
	if err := json.Unmarshal(hsRes, &desc); err != nil {
		client.Close()
		_ = kill()
		cancel()
		return fmt.Errorf("握手结果解析失败: %w", err)
	}

	h.mu.Lock()
	h.descr = desc
	h.state = StateReady
	h.reason = ""
	h.mu.Unlock()

	// 监控子进程退出：崩溃即置 FAILED（不自动无限拉起子进程——重启策略在 Start 期处理；运行期
	// 崩溃后该 Host 标记失败，是否需要重连由上层调 Restart 显式触发）。
	go h.supervise(client, wait, kill, cancel)
	return nil
}

// supervise 在后台等子进程退出**或**客户端因协议熔断（帧超限/读错）自行关闭；任一发生且非主动
// 关闭 → 该 Host FAILED（崩溃/熔断隔离到本包）。
func (h *Host) supervise(client *Client, wait func() error, kill func() error, cancel context.CancelFunc) {
	waitErr := make(chan error, 1)
	go func() { waitErr <- wait() }()

	var reason string
	select {
	case err := <-waitErr:
		reason = "子进程已退出/崩溃"
		if err != nil {
			reason = "子进程已退出/崩溃：" + err.Error()
		}
	case <-client.Done():
		// 客户端先关（帧超限熔断 / 读错），子进程可能还活着——一并杀掉并标记失败。
		reason = "host 协议客户端已关闭（响应超限或读错）"
	}
	cancel()
	_ = kill()
	client.Close()

	h.mu.Lock()
	closed := h.closed
	if !closed && h.state != StateFailed {
		h.state = StateFailed
		h.reason = reason
	}
	h.mu.Unlock()
	if !closed {
		h.cfg.Logger("error", fmt.Sprintf("[apphost] 包 %s host 失效：%s（该包 impl/tool 全部 FAILED）", h.cfg.PackageID, reason))
	}
}

// Restart 显式重启一个已 FAILED/崩溃的 Host（运维/C5 重连用）。重置失败计数后走 Start 的重试。
func (h *Host) Restart(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("apphost: Host 已关闭，不能重启")
	}
	h.state = StateStarting
	h.reason = ""
	h.restarts = 0
	h.mu.Unlock()
	return h.startWithBackoff(ctx)
}

// Call 在默认超时内发一条 RPC。返回值同 Client.Call；额外把超时/传输错换算成 RPCError 便于统一处理。
//
// 语义统一：无论成功失败，调用方拿到的是 (result, *RPCError, error)。传输层故障（客户端关闭/超时）
// 以 *RPCError（ErrCodeTimeout / ErrCodeHostGone）返回，`err` 仅保留给「参数序列化」这类本地错误。
func (h *Host) Call(method string, params any) (json.RawMessage, *RPCError, error) {
	h.mu.Lock()
	client := h.client
	state := h.state
	h.mu.Unlock()
	if client == nil || state == StateFailed || state == StateClosed {
		return nil, &RPCError{Code: ErrCodeHostGone, Message: fmt.Sprintf("包 %q 的 host 不可用（state=%s）", h.cfg.PackageID, state)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.CallTimeout)
	defer cancel()
	res, rpcErr, err := callWithContext(ctx, client, method, params)
	if err != nil {
		// 传输层错误：区分超时与 host 消失。
		if ctx.Err() == context.DeadlineExceeded {
			return nil, &RPCError{Code: ErrCodeTimeout, Message: fmt.Sprintf("RPC %s 超时（%s）", method, h.cfg.CallTimeout)}, nil
		}
		return nil, &RPCError{Code: ErrCodeHostGone, Message: fmt.Sprintf("host 传输失败：%v", err)}, nil
	}
	return res, rpcErr, nil
}

// ——— 便捷方法（各 RPC 的强类型包装，供 C4/C5 直接调用）———

// HandshakeResult 返回最近一次成功握手的自述（未就绪时为零值）。
func (h *Host) HandshakeResult() HandshakeResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.descr
}

// ListImpls 返回本进程提供的 impl_id。
func (h *Host) ListImpls() ([]string, error) {
	res, rpcErr, err := h.Call(MethodListImpls, nil)
	if err != nil {
		return nil, err
	}
	if rpcErr != nil {
		return nil, AsFailure(rpcErr)
	}
	var out ListImplsResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("apphost: 解析 listImpls 结果失败: %w", err)
	}
	return out.Impls, nil
}

// ListTools 返回本进程提供的工具清单。
func (h *Host) ListTools() ([]ToolSpec, error) {
	res, rpcErr, err := h.Call(MethodListTools, nil)
	if err != nil {
		return nil, err
	}
	if rpcErr != nil {
		return nil, AsFailure(rpcErr)
	}
	var out ListToolsResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("apphost: 解析 listTools 结果失败: %w", err)
	}
	return out.Tools, nil
}

// InvokeTool 调用一个工具。返回 (content, isError, error)。error 非空时是 *failure.Error（含 class）。
func (h *Host) InvokeTool(name string, args json.RawMessage) (json.RawMessage, bool, error) {
	res, rpcErr, err := h.Call(MethodInvokeTool, InvokeToolParams{Name: name, Args: RawResult(args)})
	if err != nil {
		return nil, false, err
	}
	if rpcErr != nil {
		return nil, false, AsFailure(rpcErr)
	}
	var out InvokeToolResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, false, fmt.Errorf("apphost: 解析 invokeTool 结果失败: %w", err)
	}
	return json.RawMessage(out.Content), out.IsError, nil
}

// ApplySlot apply 一个 impl（implID）。
func (h *Host) ApplySlot(implID string, ctxSnapshot map[string]string) error {
	_, rpcErr, err := h.Call(MethodApplySlot, ApplySlotParams{ImplID: implID, CtxSnapshot: ctxSnapshot})
	if err != nil {
		return err
	}
	if rpcErr != nil {
		return AsFailure(rpcErr)
	}
	return nil
}

// DisposeSlot 撤销一个 impl（implID）。
func (h *Host) DisposeSlot(implID string) error {
	_, rpcErr, err := h.Call(MethodDisposeSlot, DisposeSlotParams{ImplID: implID})
	if err != nil {
		return err
	}
	if rpcErr != nil {
		return AsFailure(rpcErr)
	}
	return nil
}

// Health 探活。
func (h *Host) Health() (HealthResult, error) {
	res, rpcErr, err := h.Call(MethodHealth, nil)
	if err != nil {
		return HealthResult{}, err
	}
	if rpcErr != nil {
		return HealthResult{}, AsFailure(rpcErr)
	}
	var out HealthResult
	if err := json.Unmarshal(res, &out); err != nil {
		return HealthResult{}, fmt.Errorf("apphost: 解析 health 结果失败: %w", err)
	}
	return out, nil
}

// State 返回当前生命周期状态。
func (h *Host) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Reason 返回最近一次 failed/restarting 的人话原因（诊断/面板用）。
func (h *Host) Reason() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reason
}

// Close 停服：发 shutdown（尽力）→ 关客户端 → 杀掉子进程。幂等。标记 StateClosed（不触发崩溃告警）。
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	client := h.client
	h.mu.Unlock()

	if client != nil {
		// 尽力发 shutdown（不等太久，避免退出卡住）。
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _, _ = callWithContext(ctx, client, MethodShutdown, nil)
		cancel()
		client.Close()
	}
	h.mu.Lock()
	h.state = StateClosed
	h.mu.Unlock()
	return nil
}

// ——— 内部状态辅助 ———

func (h *Host) setRestarting(reason string) {
	h.mu.Lock()
	h.state = StateRestarting
	h.reason = reason
	h.mu.Unlock()
}

func (h *Host) setFailed(reason string) {
	h.mu.Lock()
	h.state = StateFailed
	h.reason = reason
	h.mu.Unlock()
}

// callWithContext 在 ctx 期限内完成一次 Client.Call（超时/取消转成 error）。
//
// ctx 到期时 Call 会继续在后台等（reader 仍持有该 pending），到期后返回 ctx.Err()——这时那次
// 迟到的响应会被 deliver 丢弃（id 已无人等待），不会泄漏或串号。
func callWithContext(ctx context.Context, client *Client, method string, params any) (json.RawMessage, *RPCError, error) {
	type result struct {
		raw    json.RawMessage
		rpcErr *RPCError
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		raw, rpcErr, err := client.Call(method, params)
		ch <- result{raw: raw, rpcErr: rpcErr, err: err}
	}()
	select {
	case r := <-ch:
		return r.raw, r.rpcErr, r.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// ——— 生产用 Spawn：拉起同一二进制的 `mate app-host --pkg=<dir>` ———

// ExecSpawn 返回一个 SpawnFunc：用当前可执行文件（`mate` 自身）以 `app-host --pkg=<dir>` 子命令
// 拉起子进程，stdin/stdout 接管道，stderr 接父进程 stderr（诊断）。
//
// 为什么复用当前可执行文件：`mate` 二进制同时是父与子的入口（子进程就是同一个二进制跑另一个子
// 命令）——这与 `mate mcp platform` 被底座经 stdio 拉起是同一个模式。陌生包的**代码**由那个子
// 进程的任务承载（C4 起经 hostkit.Handlers 注入），不在内核进程里跑。
func ExecSpawn() SpawnFunc {
	return func(ctx context.Context, pkgDir string) (io.WriteCloser, io.ReadCloser, func() error, func() error, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("定位 mate 可执行文件失败: %w", err)
		}
		cmd := exec.CommandContext(ctx, exe, "app-host", "--pkg", pkgDir)
		cmd.Stderr = os.Stderr // 子进程诊断落父 stderr；stdout 只走协议。
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("打开子进程 stdin 失败: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("打开子进程 stdout 失败: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("启动子进程失败: %w", err)
		}
		wait := func() error { return cmd.Wait() }
		kill := func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
		return stdin, stdout, wait, kill, nil
	}
}

// ——— 失败分类透传（Host 层错误 → failure.Class 便于上层统一渲染）———

// ClassOfHostError 报告一个 Host 方法返回的 error 的 failure.Class（非 *failure.Error 按 unknown）。
func ClassOfHostError(err error) failure.Class {
	if err == nil {
		return ""
	}
	var fe *failure.Error
	if errors.As(err, &fe) {
		return fe.Class
	}
	return failure.ClassUnknown
}
