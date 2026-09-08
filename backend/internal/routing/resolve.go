// Package routing 实现运行时路由（design doc §7 路由引擎的 FirstAvailable 切片）。
//
// 本轮只落地「首个可用（默认策略）」的可判定部分：候选 runtime 按 agent.Runtimes 顺序，
// 过滤「已注册」+「启动命令当前可执行（Installed）」者取首。能力匹配（CapabilityRouted）、
// 会话粘滞（Sticky，RuntimeHint 优先）与 handoff 迁移留待 P2 后续切片（依赖 Runtime 接口与
// Agent 能力/路由字段，见 §8.1）。
package routing

import (
	"os"
	"os/exec"
	"strings"

	"github.com/morehao/ark-forge/internal/runtime"
)

// Installed 报告 runtime 启动命令当前可执行：
//   - 含路径分隔符 → 直接 stat（如 forge acp-demo 的自派生可执行文件、绝对路径）；
//   - 否则按 PATH 查找（opencode/dsh 等须在 PATH 中的 CLI）。
func Installed(spec runtime.Spec) bool {
	cmd := spec.Launch.Command
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

// Availability 记录未被采用的候选分类（供上层把不可用原因映射为错误码/日志）。
type Availability struct {
	NotFound     []string // 未注册
	NotInstalled []string // 已注册但启动命令不可执行
}

// FirstAvailable 按候选顺序返回首个「已注册 + 启动命令可执行」的 runtime id；
// 未采用候选分类写入 Availability。无候选 / 全部未采用时 chosen 为空串。
func FirstAvailable(reg *runtime.Registry, candidates []string) (string, Availability) {
	var av Availability
	for _, id := range candidates {
		spec, ok := reg.Get(id)
		if !ok {
			av.NotFound = append(av.NotFound, id)
			continue
		}
		if Installed(spec) {
			return id, av
		}
		av.NotInstalled = append(av.NotInstalled, id)
	}
	return "", av
}
