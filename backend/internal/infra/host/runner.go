// Package host 是**宿主能力面**：需要"在用户自己的机器上起一个短命进程"的动作落在这里。
//
// 它存在的理由与 `infra` 其余包一致——这类动作**跟外面说话**（spawn 进程、走 PATH 查找），
// 而调用它的判定（"该不该抬系统面板""这个平台用什么命令"）是纯规则，留在域里。
// 域通过一个一方法的端口拿到能力，装配期把本包注入（Go 接口是结构匹配，本包不需要 import 域）。
package host

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Runner 跑短命宿主进程（当前唯一用途：抬系统目录选择面板）。
//
// 方法只有两个：跑一次、按名字找可执行文件。返回值全是原语（而不是本包的 struct），
// 这样域侧的端口可以用同样的签名声明，两侧都不需要 import 对方。
type Runner struct{}

// NewRunner 造一个宿主能力执行器。
func NewRunner() *Runner { return &Runner{} }

// LookPath 按 PATH 解析命令名（域用它判断 zenity/kdialog 是否存在）。
//
// **显式、不探测**：只看声明的那个名字，找不到就是找不到——不去猜别的位置。
func (r *Runner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Run 跑一次命令并在超时后收回。
//
// 返回 stdout（已去尾部换行）、stderr（原样，供"取消"判定，**不进日志**）、退出码、
// 是否因超时被收回，以及底层错误。退出码拿不到时返回 -1，绝不与任何"取消码"相等。
func (r *Runner) Run(ctx context.Context, exe string, args []string, timeout time.Duration) (string, string, int, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, exe, args...)
	// 面板要出现在用户面前：stdout 收路径，stderr 只用于"取消"判定（不进日志，
	// 避免把用户目录名播撒到日志里——那是最容易被忽略的一类信息泄漏）。
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	exitCode := -1
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		}
	}
	return strings.TrimRight(out.String(), "\r\n"), errOut.String(), exitCode,
		runCtx.Err() == context.DeadlineExceeded, runErr
}
