package runtime

import (
	"os"
	"os/exec"
	"strings"
)

// ——— runtime 启动面的**可执行性判定** ———
//
// 本文件原先住在 `pkg/routing`（连同 FirstAvailable / CapabilityRouted / RoutingPolicy 那套候选路由
// 引擎）。产品把底座钉成构建期常量之后（见 build.go），路由的每个输入都成了常量，那套引擎整体下线；
// 只剩这两个判定——它们回答的是"**这一个**底座现在拉得起来吗"，属于 Spec 本身的性质。
//
// 唯一的消费方是诊断面（`GET /agents/:id/diagnostics` 的 remediation）：它要用它区分
// 「runtime 未装配」与「已装配但启动命令不可执行」两种**不同的修复动作**。

// Installed 报告 runtime 启动命令当前可执行：
//   - 远程 runtime（A5）：没有本地启动命令，可达性由拨号阶段判定（配置加载时已校验 URL 形态与
//     传输匹配），这里只要求端点非空——否则远程 runtime 永远无法被采用；
//   - 本地 runtime：含路径分隔符 → 直接 stat（如随包载荷的绝对路径）；否则按 PATH 查找。
func Installed(spec Spec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "http", "ws", "websocket", "streamable_http", "streamable-http":
		return strings.TrimSpace(spec.Launch.Endpoint) != ""
	}
	return CommandExecutable(spec.Launch.Command)
}

// CommandExecutable 报告某启动命令当前可执行：
//   - 含路径分隔符 → 直接 stat（如远程/自带底座的绝对路径）；
//   - 否则按 PATH 查找（opencode/dsh 等须在 PATH 中的 CLI）。
//
// 与 Installed 同源；**显式、不探测**：只看声明的那一个命令，找不到就返回 false，
// 绝不去猜别的位置（猜的后果是"机器上恰好装过这个底座"静默顶掉随包那份）。
func CommandExecutable(cmd string) bool {
	if cmd == "" {
		return false
	}
	if strings.ContainsRune(cmd, os.PathSeparator) {
		fi, err := os.Stat(cmd)
		return err == nil && !fi.IsDir()
	}
	_, err := exec.LookPath(cmd)
	return err == nil
}
