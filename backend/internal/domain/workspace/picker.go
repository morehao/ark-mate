package workspace

import (
	"context"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 宿主原生目录选择器（v14.5，对齐 dsh 的 `directory-picker-auto` + `directory-picker-native`）。
//
// **它解决的是"谁能看见宿主屏幕"这个事实问题，而不是"客户端是不是桌面 App"**。参照实现把这件事
// 判在宿主侧：web 入口（`dsh web`）只要绑回环、不是 SSH 启动、有可服务的显示会话，选目录照样走
// 系统面板——因为面板是**宿主进程**抬起来的（macOS `osascript`、Linux zenity/kdialog、Windows COM），
// 与客户端是浏览器还是 Electron 窗口无关。远程/SSH/无显示会话才退化成应用内浏览。
//
// 本文件把那条判定与命令配方做成**纯函数**（可单测、不 spawn），执行放在 `Picker.PickDirectoryHost`。
//
// 与 dsh 的一处差异（如实记）：dsh 在**启动时**采样一次并把 backend 挂成能力；本仓在**每次调用**时
// 采样（配置目录里没有可挂载面，而这几个事实在进程生命周期内不会变），判定结果经 `/meta` 的
// `native_dialog` 能力下发给前端，前端据此决定调原生还是开应用内对话框。

// Picker 原生目录选择器（**自足**：只持"本进程实际监听的地址"一个事实）。
//
// 并发审查（搬进域之前必须做的一步，见 domain/skill 的先例）：`mu` 只保护 `bindAddr`——
// 写入点在 `SetBindAddr`（装配期一次），读取点在 `pickEnv`（每次判定）。**它的临界区不碰
// 会话、不碰库、不碰注册表**，所以这里自持一把锁是安全的，而且顺带把一处"与 sessions 共用
// `Service.mu`"的假耦合去掉（"监听地址"与"会话表"本来没有必须原子这一说）。
type Picker struct {
	mu       sync.Mutex
	bindAddr string
	// host 是宿主能力端口（跑面板进程 / 按 PATH 找命令），装配期注入。
	// 它是**一方法的端口**而不是具体实现：域不认识 os/exec，执行落 `infra/host`。
	host HostRunner
}

// HostRunner 是"在宿主上跑一个短命进程"的端口。
//
// 签名全是原语（stdout/stderr/退出码/是否超时），所以实现方 `infra/host` **不需要 import 本包**，
// 域也不需要 import 它——两侧只在装配点相遇。
type HostRunner interface {
	Run(ctx context.Context, exe string, args []string, timeout time.Duration) (stdout, stderr string, exitCode int, timedOut bool, err error)
	LookPath(name string) (string, error)
}

// SetHostRunner 装配期注入宿主能力（与 SetBindAddr 同批调用）。
func (p *Picker) SetHostRunner(r HostRunner) {
	p.mu.Lock()
	p.host = r
	p.mu.Unlock()
}

// NewPicker 造一个选择器（零值即可用：未注入地址时判定保守地落 browse）。
func NewPicker() *Picker { return &Picker{} }

// NativePickPlan 一次原生拾取要跑的**命令配方**（纯数据，便于断言）。
type NativePickPlan struct {
	// Exe 可执行文件名（走 PATH 解析；POSIX 下的名字本身没有歧义，见 isolation 纪律）。
	Exe string
	// Args 参数列表（**不经过 shell**：参数是常量，不存在注入面）。
	Args []string
	// CancelExit 取消时进程的退出码（dsh 逐字：osascript 取消是 1 / -128 形态）。
	CancelExit int
	// CancelStderr 取消的 stderr 特征（大小写不敏感的包含判定；与 CancelExit 同时成立才算取消）。
	CancelStderr string
}

// ErrNativePickUnsupported 宿主上**没有**可用的原生选择器（平台不支持 / 缺 zenity/kdialog）。
//
// 它是可判定的一类拒绝：前端据此回落到应用内浏览（`native_dialog` 能力为 false 时本就不该走到这里，
// 这个错是"能力声明与实际不一致"或直接打接口的兜底）。
// 消息里带 `[native_pick_unsupported]` 是有意的：acp 的 Details 是唯一能进信封的字段，要让调用方
// **稳定识别**这一类拒绝（而不是去猜中文文案），就把码放进消息里。
var ErrNativePickUnsupported = failure.NewError(failure.ClassUnsupported, "native_pick_unsupported",
	"[native_pick_unsupported] 宿主上没有可用的原生目录选择器（当前平台不支持，或缺少 zenity/kdialog）：请改用控制面提供的目录浏览", nil)

// nativePickPrompt 面板标题（macOS 的 choose folder 与 zenity/kdialog 共用一句）。
const nativePickPrompt = "选择工作目录"

// planNativePick 按平台给出命令配方。
//
//	platform  darwin → osascript（NSOpenPanel）
//	          linux  → zenity，找不到再 kdialog（两者的取消语义不同，见各自赋值）
//	          win32  → **明确不支持**：dsh 那条路是 koffi 驱动的 COM `IFileOpenDialog` 子进程，
//	                   本仓没有这个依赖；退回应用内浏览比写一个未经验证的 PowerShell 版更诚实
//	         其它    → 不支持
//
// lookPath 注入是为了让判定可测（测试里给不出真 PATH 时不该假装 zenity 在）。
func planNativePick(platform string, lookPath func(string) (string, error)) (NativePickPlan, error) {
	switch platform {
	case "darwin":
		return NativePickPlan{
			Exe: "osascript",
			Args: []string{
				"-e", fmt.Sprintf("set selectedFolder to choose folder with prompt %q", nativePickPrompt),
				"-e", "POSIX path of selectedFolder",
			},
			// AppleScript 的用户取消：osascript 以 1 退出、stderr 里带 "User canceled" / "-128"。
			CancelExit:   1,
			CancelStderr: "cancel",
		}, nil
	case "linux":
		if _, err := lookPath("zenity"); err == nil {
			return NativePickPlan{
				Exe:          "zenity",
				Args:         []string{"--file-selection", "--directory", "--title=" + nativePickPrompt},
				CancelExit:   1, // zenity 取消：退出码 1，无 stderr
				CancelStderr: "",
			}, nil
		}
		if _, err := lookPath("kdialog"); err == nil {
			return NativePickPlan{
				Exe:          "kdialog",
				Args:         []string{"--getexistingdirectory", ".", "--title", nativePickPrompt},
				CancelExit:   1, // kdialog 取消：退出码 1
				CancelStderr: "",
			}, nil
		}
		return NativePickPlan{}, ErrNativePickUnsupported
	case "win32":
		return NativePickPlan{}, ErrNativePickUnsupported
	default:
		return NativePickPlan{}, ErrNativePickUnsupported
	}
}

// NativePickerMode 前端该用哪一套选目录交互（对齐 dsh 的 `DirectoryPickerBackendKind`）。
type NativePickerMode string

const (
	// NativePickerHost 宿主能抬系统面板：客户端只调原生那一条路。
	NativePickerHost NativePickerMode = "native"
	// NativePickerBrowse 宿主抬不起来（远程/SSH/无显示会话/平台不支持）：走应用内浏览。
	NativePickerBrowse NativePickerMode = "browse"
)

// pickEnv 判定用到的环境事实（**注入**而不是直接读 os.Getenv：判定必须是纯函数、可测）。
type pickEnv struct {
	platform string
	// bindAddr 本进程**实际监听**的地址（`ln.Addr()`，不是配置里的 `:0`——桌面态端口由内核分配）。
	bindAddr string
	// ssh 是否 SSH 会话（远程操作者的屏幕上抬面板没有意义）。
	ssh bool
	// display 桌面会话：Linux 要求 DISPLAY/WAYLAND_DISPLAY；darwin/win32 恒真。
	display  string
	lookPath func(string) (string, error)
}

// nativePickerMode 由宿主事实解析出交互形态（dsh 的 `resolveDirectoryPickerBackend` 同构）。
//
// 三条判据与 dsh 逐条对应，顺序也一致：
//  1. **绑回环**：对外监听（`0.0.0.0` 等）时客户端可能是另一台机器上的浏览器——那里的人看不见
//     宿主屏幕，面板会在没人看的地方弹出来；
//  2. **非 SSH**：SSH 下同样"看得见的屏幕"不在操作者面前（端口转发场景尤其典型）；
//  3. **有可服务的显示会话**：darwin/win32 假定有；Linux 要 DISPLAY/WAYLAND_DISPLAY **且**有
//     zenity/kdialog；其它平台一律 browse。
//
// 任何一条不成立即 `browse`——那半永远可用，所以"拿不准就退化"是安全的默认。
func nativePickerMode(env pickEnv) NativePickerMode {
	if env.ssh {
		return NativePickerBrowse
	}
	if !loopbackBind(env.bindAddr) {
		return NativePickerBrowse
	}
	if _, err := planNativePick(env.platform, env.lookPath); err != nil {
		return NativePickerBrowse
	}
	if env.platform == "linux" && env.display == "" {
		return NativePickerBrowse
	}
	return NativePickerHost
}

// loopbackBind 报告监听地址是否是**回环**（主机部分是 127.0.0.1 / ::1 / localhost）。
//
// 与 middleware 的 Host 白名单同一口径（那三个名字是"本机"的通用写法），但不共用实现：
// 那边的判定挂在 HTTP 请求上下文里（Host 头 + 端口），这里的输入是 `net.Listener` 的地址。
// 地址解析不出来（几乎不发生）时返回 false——**拿不准就当对外**，退化成 browse。
func loopbackBind(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// sshSession 报告本进程是否跑在 SSH 会话里（与 dsh 的 `launchedThroughSsh` 同口径：只看
// 那几个由 sshd 注入的环境键；**不**读配置文件，因为 `.env` 里的值表达不了"这次是怎么起的"）。
func sshSession(getenv func(string) string) bool {
	for _, k := range []string{"SSH_CONNECTION", "SSH_TTY", "SSH_CLIENT"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return true
		}
	}
	return false
}

// SetBindAddr 记下本进程**实际监听**的地址（装配期在 net.Listen 之后调用）。
//
// 为什么不让域自己读配置里的 `--addr`：桌面态是 `127.0.0.1:0`，真端口只有 Listen 之后才有，
// 而"是不是回环"恰恰是判定原生选择器的第一条。装配顺序因此是"先绑定 → 再告诉选择器"。
func (p *Picker) SetBindAddr(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindAddr = addr
}

// NativePickerMode 返回本宿主该用哪一套选目录交互（前端经 `/capabilities` 的 `native-dialog` 读它）。
//
// 返回字符串而不是枚举类型：消费方（原来是 `svcworkspace` 端口，P4a 正文第三十三刀起是
// 控制器的本地接口 `DirectoryPicker`）不该 import 本域的类型，而这两个取值就是
// wire 上的两个词。枚举只在本包内部用于判定。
func (p *Picker) NativePickerMode() string {
	return string(nativePickerMode(p.pickEnv()))
}

// pickEnv 采样一次宿主事实（**每次调用都采**：进程生命周期内不会变，所以不必缓存）。
func (p *Picker) pickEnv() pickEnv {
	p.mu.Lock()
	addr := p.bindAddr
	p.mu.Unlock()
	return pickEnv{
		platform: runtime.GOOS,
		bindAddr: addr,
		ssh:      sshSession(os.Getenv),
		display:  firstNonEmpty(os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY")),
		lookPath: p.lookPath,
	}
}

// lookPath 按 PATH 找可执行文件（只用于"这个平台有没有 zenity/kdialog"的存在性判定）。
//
// 未注入宿主能力时一律返回"找不到"——那正是"抬不起面板"的语义，前端会回落到应用内浏览。
func (p *Picker) lookPath(name string) (string, error) {
	p.mu.Lock()
	r := p.host
	p.mu.Unlock()
	if r == nil {
		return "", errNoHostRunner
	}
	return r.LookPath(name)
}

// errNoHostRunner 装配期没注入宿主能力（本进程不该抬面板）。
var errNoHostRunner = errors.New("host runner not wired")

// nativePickTimeout 原生面板的墙钟上限。
//
// 为什么要有：面板会一直等到用户动手，而一次 HTTP 请求不该无限挂着（客户端断开也未必能收回
// 已弹出的面板）。到点即杀进程并**按取消处理**——面板还开着时把它收掉是用户能理解的语义
// （"没选"），比回一个 500 好。
const nativePickTimeout = 5 * time.Minute

// PickDirectoryHost 抬一次**宿主原生**目录选择器（REST `POST /workspaces/pick-directory`）。
//
// 返回 `(path, false)` = 用户选定；`("", true)` = 用户取消（或超时被收回）——**取消不是错误**，
// 前端据此静默收手（与 dsh 的 `pickDirectory() → null` 同义）。
//
// 面板由**核心进程**（它是本机用户自己的进程）抬起来，所以浏览器入口与桌面窗口都能用到它；
// 这正是 dsh `dsh web` 的行为。
func (p *Picker) PickDirectoryHost(ctx context.Context) (string, bool, error) {
	env := p.pickEnv()
	if nativePickerMode(env) != NativePickerHost || p.host == nil {
		// 没注入宿主能力 = 抬不起面板（与"平台不支持"同一类拒绝，前端回落到应用内浏览）。
		return "", false, ErrNativePickUnsupported
	}
	plan, err := planNativePick(env.platform, env.lookPath)
	if err != nil {
		return "", false, err
	}

	stdout, stderrOut, exitCode, timedOut, runErr := p.host.Run(ctx, plan.Exe, plan.Args, nativePickTimeout)
	if runErr == nil {
		if strings.TrimSpace(stdout) == "" {
			return "", true, nil // 面板正常结束但没给路径：按取消处理
		}
		return stdout, false, nil
	}
	// 取消：退出码与 stderr 特征两者都成立（只看退出码会把"脚本语法错"也当成取消）。
	if exitCode == plan.CancelExit && (plan.CancelStderr == "" ||
		strings.Contains(strings.ToLower(stderrOut), plan.CancelStderr)) {
		return "", true, nil
	}
	// 超时被收回：面板已经关了，对用户就是"没选"。
	if timedOut {
		return "", true, nil
	}
	// 其余失败：报服务侧错误，并把命令名带上——不带上就只剩一句"退出码 1"，
	// 而这台机器上可能是缺 osascript、权限被拒、或面板被系统策略挡住。
	return "", false, failure.NewError(failure.ClassService, "native_pick_failed",
		fmt.Sprintf("原生目录选择器执行失败（%s）: %v", plan.Exe, runErr), runErr)
}

// firstNonEmpty 返回第一个非空值（显示会话的两个变量选一个）。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
