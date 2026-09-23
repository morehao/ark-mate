package acl

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Transport 抽象「一个 ACP server 进程」的接入面：Start 拉起并接好 stdio，
// Stdin/Stdout 即 ACP 线协议读写端，StderrTail 提供最近诊断输出（透出日志 + 环形缓冲）。
type Transport interface {
	// Start 拉起子进程并接入 stdio。ctx 取消仅影响后续生命周期，进程回收走 Close。
	Start(ctx context.Context) error
	// Stdin 返回进程 stdin（Start 之后可用），Host 在此写 JSON-RPC 请求。
	Stdin() io.Writer
	// Stdout 返回进程 stdout（Start 之后可用），Host 在此读 JSON-RPC 响应/通知。
	Stdout() io.Reader
	// StderrTail 返回最近 stderr 行（进程退出诊断用，自动透出到日志）。
	StderrTail() []string
	// Close 关闭进程：杀进程树（含孙进程）并释放管道。
	Close() error
}

const stderrTailCap = 200

// StdioTransport 是首版唯一 Transport：exec 一个 runtime 命令，接入 stdin/stdout/stderr。
type StdioTransport struct {
	command string
	args    []string
	env     []string // 追加环境（如 DEEPSEEK_API_KEY），叠加在 os.Environ 之上
	dir     string

	startOnce sync.Once
	closeOnce sync.Once
	closeErr  error
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser

	mu      sync.RWMutex
	tail    []string
	logger  *slog.Logger
	started bool
	// stderrDone 在 drainStderr 因 stderr 管道 EOF 退出时关闭。
	// 存在的理由：drainStderr 是独立 goroutine，而子进程"写一行就退出"时，调用方可能
	// 早于它完成就快照 tail（实测偶发拿到 0 行，见 AttachStderr 的等待）。nil = 未启动。
	stderrDone chan struct{}
}

// NewStdioTransport 构造 StdioTransport。env 为附加环境变量（KEY=VALUE 形式）。
func NewStdioTransport(command string, args, env []string) *StdioTransport {
	return &StdioTransport{
		command:    command,
		args:       args,
		env:        env,
		logger:     slog.Default(),
		stderrDone: make(chan struct{}),
	}
}

// LoggerSetter 由可设置诊断日志目标的传输实现（stdio 的 stderr 透出、远程传输的连接诊断）。
type LoggerSetter interface {
	SetLogger(*slog.Logger)
}

// PidInfo 子进程标识（进程注册表 / 孤儿回收用）。
type PidInfo struct {
	PID     int    // 子进程 pid（0 = 未启动）
	PGID    int    // 进程组 id（0 = 平台不支持/未启用进程组）
	Command string // 启动命令（诊断与 PID 复用判定的辅助信息）
}

// PidReporter 由可上报子进程标识的传输实现（stdio）。
type PidReporter interface {
	PidInfo() PidInfo
}

// PidInfo 返回子进程标识；传输不支持时返回零值（调用方据此跳过登记）。
func (t *StdioTransport) PidInfo() PidInfo {
	if !t.started || t.cmd == nil || t.cmd.Process == nil {
		return PidInfo{}
	}
	pid := t.cmd.Process.Pid
	return PidInfo{PID: pid, PGID: procPgid(pid), Command: t.command}
}

// SetDir 设置子进程工作目录（NewSession 的 cwd 在 ACP 层表达，dir 留给诊断场景）。
func (t *StdioTransport) SetDir(dir string) { t.dir = dir }

// SetLogger 设置 stderr 透出日志目标（默认 slog.Default）。
func (t *StdioTransport) SetLogger(l *slog.Logger) {
	if l != nil {
		t.logger = l
	}
}

func (t *StdioTransport) Start(ctx context.Context) error {
	var err error
	t.startOnce.Do(func() {
		err = t.start(ctx)
	})
	return err
}

func (t *StdioTransport) start(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, t.command, t.args...)
	cmd.Dir = t.dir
	cmd.Env = append(os.Environ(), t.env...)
	setupProcAttr(cmd) // unix: 新进程组，Close 时杀整棵进程树

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return NewError(ClassConnection, "pipe_stdin", "创建 stdin 管道失败", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return NewError(ClassConnection, "pipe_stdout", "创建 stdout 管道失败", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return NewError(ClassConnection, "pipe_stderr", "创建 stderr 管道失败", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return NewError(ClassConnection, "spawn_failed",
			"拉起 runtime 子进程失败（命令: "+t.command+"），请确认其已安装且在 PATH 中", err)
	}

	t.cmd = cmd
	t.stdin = stdin
	t.stdout = stdout
	t.stderr = stderr
	t.started = true

	// stderr 透出：逐行写日志 + 保留最近 N 行环形缓冲（进程退出诊断）。
	go t.drainStderr()

	_ = ctx // ctx 取消由外部 Close/CommandContext 语义处理
	return nil
}

func (t *StdioTransport) drainStderr() {
	defer close(t.stderrDone) // 先在 defer 里注册，保证任何退出路径都唤醒等待者
	sc := bufio.NewScanner(t.stderr)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		t.mu.Lock()
		if len(t.tail) >= stderrTailCap {
			copy(t.tail, t.tail[1:])
			t.tail[len(t.tail)-1] = line
		} else {
			t.tail = append(t.tail, line)
		}
		t.mu.Unlock()
		t.logger.Info("runtime stderr", "cmd", t.command, "line", line)
	}
}

// WaitStderr 等待 stderr 抽干（stderr 管道 EOF 即返回），最多等 timeout。
//
// 为什么需要它：`drainStderr` 是独立 goroutine。子进程"往 stderr 写一行就退出"这种最常见的
// 启动失败形态下，调用方（Dial 握手失败 → AttachStderr）完全可能**早于**读取 goroutine 把该行
// 追加进环形缓冲就快照，于是诊断尾巴偶发为空——本仓 `TestDialErrorCarriesStderrTail` /
// `TestAgentDiagnosticsStartFailure` 都因此出现过 flake（冷缓存全量跑时命中率明显升高）。
// 这里等待的是 **EOF** 而不是固定时长：管道关掉（进程退出）即立刻返回，故不给失败路径加
// 可感知延迟；只有进程还活着（stderr 仍开着）时才吃到 timeout 上限。
//
// 未启动（start 从未成功）时没有 goroutine 可等，直接返回。
func (t *StdioTransport) WaitStderr(timeout time.Duration) {
	if t.stderrDone == nil {
		return
	}
	select {
	case <-t.stderrDone:
	case <-time.After(timeout):
	}
}

func (t *StdioTransport) Stdin() io.Writer {
	if !t.started {
		return nil
	}
	return t.stdin
}

func (t *StdioTransport) Stdout() io.Reader {
	if !t.started {
		return nil
	}
	return t.stdout
}

func (t *StdioTransport) StderrTail() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, len(t.tail))
	copy(out, t.tail)
	return out
}

// Close 关闭进程：杀进程树并回收管道（幂等）。
func (t *StdioTransport) Close() error {
	t.closeOnce.Do(func() {
		if !t.started || t.cmd == nil {
			return
		}
		if t.stdin != nil {
			_ = t.stdin.Close()
		}
		if err := killProc(t.cmd); err != nil {
			t.closeErr = NewError(ClassConnection, "kill_failed", "关闭 runtime 进程失败", err)
		}
		_ = t.cmd.Wait()
		if t.stdout != nil {
			_ = t.stdout.Close()
		}
		if t.stderr != nil {
			_ = t.stderr.Close()
		}
	})
	return t.closeErr
}
