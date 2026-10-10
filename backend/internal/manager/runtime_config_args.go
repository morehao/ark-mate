package manager

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// ——— T5 的 `cli_patch_arg` 通道：把平台的 overlay 作为启动参数交给底座（dsh） ———
//
// 为什么这条通道比"写底座自己的 home patch"好（ADR-14）：平台写的是**自己的**文件，
// 由启动参数递入，于是
//
//	① 归属干净：那个文件只有平台会写（dsh 自己的 config-editor / plugin_manager 都只写 profile 层）；
//	② **可撤销**：坏了把参数去掉，底座就回到出厂配置——不需要修复任何 dsh 的文件；
//	③ 层序最高：bundle → profile → home → CLI（`--patch` 在最后）。
//
// 三条硬约束（写错任何一条都不是"没生效"而是"拒启"）：
//
//	① 路径必须**绝对**：dsh 侧是 `resolve(file)`（相对**调用目录**），而平台拉底座时的 cwd 是
//	   用户会话的工作目录——相对路径会指到用户的仓库里去。
//	② **绝不传空文件**：dsh 逐字说「an empty or comments-only file fails boot — disable the layer
//	   with `[]` instead」。所以文件不存在 / 只有注释时，参数必须**不存在**。
//	③ 参数只影响**新进程**：与 T5 的 next_process 语义一致（配合代际键里的配置指纹）。

// patchFlag 是 dsh 的覆盖层参数名。
const patchFlag = "--patch"

// SyncRuntimeConfigArgs 按"overlay 是否存在且非空"同步各 runtime 的启动参数。
//
// 启动期调用一次（装配后、任何拨号之前），物化之后也调用一次——两条路径都必须幂等：
// 参数要么恰好出现一次，要么完全不出现。
func (s *Service) SyncRuntimeConfigArgs() {
	for _, spec := range s.reg.List() {
		proj, declared, err := runtime.ResolveConfigProjection(spec)
		if err != nil || !declared || proj.Channel != runtime.ConfigChannelCLIPatchArg {
			continue
		}
		if err := s.syncPatchArg(spec.ID, proj.Path); err != nil {
			slog.Warn("runtime config: 同步 --patch 参数失败", "runtime", spec.ID, "err", err)
		}
	}
}

// syncPatchArg 把 `--patch <path>` 按文件状态装进/摘出该 runtime 的启动参数。
func (s *Service) syncPatchArg(runtimeID, path string) error {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return fmt.Errorf("runtime %q 未注册", runtimeID)
	}
	base := stripPatchArg(spec.Launch.Args)
	usable, err := patchFileUsable(path)
	if err != nil {
		return err
	}
	if !usable {
		return s.reg.SetLaunchArgs(runtimeID, base)
	}
	abs, err := absPath(path)
	if err != nil {
		return err
	}
	return s.reg.SetLaunchArgs(runtimeID, append(append([]string{}, base...), patchFlag, abs))
}

// stripPatchArg 去掉已有的 `--patch[=]value`（幂等的第一步）。
func stripPatchArg(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == patchFlag:
			i++ // 跳过它的值
		case strings.HasPrefix(args[i], patchFlag+"="):
			// `--patch=…` 形态：只丢这一个
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// patchFileUsable 文件存在、非空、且**不只有注释**（只有注释的文件会让 dsh 拒启）。
func patchFileUsable(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, ln := range splitLines(b) {
		t := trimSpace(ln)
		if t == "" || hasPrefix(t, "#") {
			continue
		}
		return true, nil
	}
	return false, nil
}

// absPath 把路径规范成绝对路径（`--patch` 必须是绝对的，见文件头约束①）。
func absPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		return p, nil
	}
	return filepath.Abs(p)
}
