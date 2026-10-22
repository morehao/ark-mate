package installed

import (
	"os"
	"os/exec"
	"strings"

	"github.com/morehao/ark-mate/internal/runtime"
)

// Package installed 是**本机可执行性探测**：这一个底座的启动命令现在跑得起来吗。
//
// 它在 `infra` 而不是 `runtime`：`runtime` 是契约包（Spec/Adapter/注册表/隔离投影，纯数据与纯规则），
// 而这里要真的去 `stat` 与 `LookPath`——那是"跟本机文件系统说话"。2026-10-21 从 `runtime` 迁出
// （P4），判据见 `layer_roles_test.go` 的 `TestInnerCircleAvoidsInfraProtocol` 与
// `.dsh/docs/specs/2026-10-21-backend-layer-roles-design.md` §3 P4。
//
// 唯一的消费方是诊断面（`GET /runtime/diagnostics` 的 remediation）：它要用它区分
// 「runtime 未装配」与「已装配但启动命令不可执行」两种**不同的修复动作**。

// Installed 报告 runtime 启动命令当前可执行：
//   - 远程 runtime（A5）：没有本地启动命令，可达性由拨号阶段判定（配置加载时已校验 URL 形态与
//     传输匹配），这里只要求端点非空——否则远程 runtime 永远无法被采用；
//   - 本地 runtime：含路径分隔符 → 直接 stat（如随包载荷的绝对路径）；否则按 PATH 查找。
func Installed(spec runtime.Spec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.Launch.Transport)) {
	case "http", "ws", "websocket", "streamable_http", "streamable-http":
		return strings.TrimSpace(spec.Launch.Endpoint) != ""
	}
	return CommandExecutable(spec.Launch.Command)
}

// CommandExecutable 报告某启动命令当前可执行：
//   - 含路径分隔符 → 直接 stat（如远程/自带底座的绝对路径）；
//   - 否则按 PATH 查找（声明成"裸命令名"的底座就是在 PATH 里找的那个 CLI）。
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
