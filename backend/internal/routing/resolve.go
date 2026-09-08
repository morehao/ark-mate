// Package routing 实现运行时路由（design doc §7 路由引擎）。
//
// 已落地切片：FirstAvailable（默认）与 CapabilityRouted（能力匹配：Agent 显式 MCP 白名单
// 只选能承载其 transport 的 runtime）。会话粘滞（Sticky，RuntimeHint 优先）与 handoff 迁移
// 依赖 Runtime 接口与持久会话数据，留待后续（见 design doc §15 剩余清单）。
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

// Strategy 选择策略（§7）。默认 FirstAvailable。
type Strategy string

const (
	StrategyFirstAvailable   Strategy = "first_available"
	StrategyCapabilityRouted Strategy = "capability_routed"
)

// ParseStrategy 解析 agent 声明的 routing_policy；未知/空值回退默认并返回 false
// （上层按默认策略处理，见 design doc §15）。
func ParseStrategy(s string) (Strategy, bool) {
	switch Strategy(s) {
	case StrategyFirstAvailable:
		return StrategyFirstAvailable, true
	case StrategyCapabilityRouted:
		return StrategyCapabilityRouted, true
	default:
		return StrategyFirstAvailable, false
	}
}

// SupportsTransports 报告 spec 是否能承载给定 MCP 传输集合（§5.2 传输过滤口径）：
// spec.McpTransports 为空 = 保守按仅 stdio 处理。
func SupportsTransports(spec runtime.Spec, required []string) bool {
	allowed := spec.McpTransports
	if len(allowed) == 0 {
		allowed = []string{"stdio"}
	}
	have := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		have[t] = true
	}
	for _, r := range required {
		if !have[r] {
			return false
		}
	}
	return true
}

// Availability 记录未被采用的候选分类（供上层把不可用原因映射为错误码/日志）。
type Availability struct {
	NotFound           []string // 未注册
	NotInstalled       []string // 已注册但启动命令不可执行
	CapabilityMismatch []string // 已注册可执行，但不满足能力匹配（CapabilityRouted）
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

// CapabilityRouted 与 FirstAvailable 同序，但候选还须能承载 requiredTransports
// （Agent 显式 MCP 白名单 → 能力匹配，§7 CapabilityRouted）。requiredTransports 为空 =
// 等价 FirstAvailable。被能力排除的候选记入 Availability.CapabilityMismatch。
func CapabilityRouted(reg *runtime.Registry, candidates []string, requiredTransports []string) (string, Availability) {
	var av Availability
	for _, id := range candidates {
		spec, ok := reg.Get(id)
		if !ok {
			av.NotFound = append(av.NotFound, id)
			continue
		}
		if !Installed(spec) {
			av.NotInstalled = append(av.NotInstalled, id)
			continue
		}
		if !SupportsTransports(spec, requiredTransports) {
			av.CapabilityMismatch = append(av.CapabilityMismatch, id)
			continue
		}
		return id, av
	}
	return "", av
}
