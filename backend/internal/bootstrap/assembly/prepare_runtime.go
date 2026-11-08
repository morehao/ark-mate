package assembly

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// PrepareRuntimeEnv 是"拉底座前"的统一准备动作：给 dsh 底座
//
//  1. 落盘平台自建的 mate-acp profile（office 能力覆写层，见 dsh.ProvisionProfile）；
//  2. 把随包 runtime 根派生成 DSH_PRIMARY_RUNTIME / DSH_BUNDLED_OFFICE_* 注入其 Launch.Env。
//
// 为什么单独抽出来、且要在**每一处真正拉起底座之前**调用（serve 的 Build、mate demo、以及任何
// 会 spawn 底座的控制面命令）：provision 若只写在 serve 装配里，`mate demo` 这类路径就拉不到
// mate-acp profile（实测踩过：demo 报 profile "mate-acp" does not exist）。
//
// 只对**本机拉子进程**且**构建期选中的就是 dsh**的底座做：远程端点不读 Launch.Env，别碰。
// 失败不返回错误——缺 office 能力是降级（agent 退回自带工具链），不该让启动失败；调用方按需记日志。
func PrepareRuntimeEnv(ctx context.Context, reg *runtime.Registry, homeDir string) {
	if reg == nil {
		return
	}
	spec, ok := reg.Get(runtime.SelectedRuntime())
	if !ok || !runtime.LaunchesLocally(spec) {
		return
	}
	// 仅 dsh 需要这份 office profile / 随包 env（当前唯一内置底座；新增底座时按 id 判定再扩）。
	if runtime.SelectedRuntime() != dsh.ID {
		return
	}
	if written, err := dsh.ProvisionProfile(homeDir); err != nil {
		glog.Warnw(ctx, "serve: 初始化 mate-acp profile 失败（office 能力可能不可用）", "err", err)
	} else {
		glog.Infow(ctx, "serve: mate-acp profile 就绪", "dir", config.DisplayHome(dsh.ProfileDir(homeDir)), "files_written", written)
	}
	if root := dshBundledRuntimeRoot(homeDir); root != "" {
		id := runtime.SelectedRuntime()
		for _, kv := range dsh.BundledEnv(root) {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			if err := reg.SetLaunchEnv(id, kv[:eq], kv[eq+1:]); err != nil {
				glog.Warnw(ctx, "serve: 注入 dsh office 随包 env 失败", "key", kv[:eq], "err", err)
			}
		}
		glog.Infow(ctx, "serve: 已注入 dsh office 随包 env", "runtime", id, "bundled_root", config.DisplayHome(root))
	} else {
		glog.Warnw(ctx, "serve: 未找到 dsh 随包 runtime 根，office 能力将不可用（可用 MATE_DSH_BUNDLED_RUNTIME 指定）")
	}
}

// dshBundledRuntimeRoot 返回 dsh 随包 runtime 根（`Resources/runtime`），供注入 office 能力 env。
//
// 取值优先级（逐级回退，任一命中即返回）：
//  1. `MATE_DSH_BUNDLED_RUNTIME`（显式覆盖，最高优先）；
//  2. 由 PATH 里 dsh 可执行位置反推——**必须 EvalSymlinks**：dev 态 dsh 常是 `.cache/dev-bin/dsh`
//     shim（`exec /usr/local/bin/dsh`）或 `/usr/local/bin/dsh` 软链，`LookPath` 拿到的是 shim/软链
//     本身，不 follow 就会把根算错。跟随软链后指向 `<root>/runtime/cli/bin/dsh`，`<root>/runtime` 即答案；
//  3. 已知安装位置 `/Applications/DeepSeek Harness.app/Contents/Resources/runtime`（macOS 打包态兜底）。
//
// 全部落空（如 npx / 裸 npm 装的 dsh、或非 macOS）→ 返回空，调用方跳过注入（如实降级，绝不注入
// 一个错路径让底座 ENOENT）。
func dshBundledRuntimeRoot(homeDir string) string {
	if v := strings.TrimSpace(os.Getenv("MATE_DSH_BUNDLED_RUNTIME")); v != "" {
		return v
	}
	if p, err := exec.LookPath(dsh.Command); err == nil && p != "" {
		if real, rerr := filepath.EvalSymlinks(p); rerr == nil && real != "" {
			p = real
		}
		bin := filepath.Dir(p)    // .../cli/bin
		cli := filepath.Dir(bin)  // .../cli
		root := filepath.Dir(cli) // .../runtime
		if filepath.Base(root) == "runtime" {
			return root
		}
	}
	const macRuntime = "/Applications/DeepSeek Harness.app/Contents/Resources/runtime"
	if fi, err := os.Stat(filepath.Join(macRuntime, "primary-runtime", "runtime.json")); err == nil && !fi.IsDir() {
		return macRuntime
	}
	return ""
}
