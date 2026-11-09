package assembly

import (
	"context"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/scratch"
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// PrepareRuntimeEnv 是"拉底座前"的统一准备动作：给 dsh 底座
//
//  1. 落盘平台自建的 mate-acp profile（office 能力覆写层，见 dsh.ProvisionProfile）；
//  2. 用 ark-mate **自有**的随包资产（primary-runtime + office skill assets）派生并注入
//     DSH_PRIMARY_RUNTIME / DSH_BUNDLED_OFFICE_* 到其 Launch.Env。
//
// 为什么单独抽出来、且要在**每一处真正拉起底座之前**调用（serve 的 Build、mate demo、以及任何
// 会 spawn 底座的控制面命令）：provision 若只写在 serve 装配里，`mate demo` 这类路径就拉不到
// mate-acp profile（实测踩过：demo 报 profile "mate-acp" does not exist）。
//
// **不依赖 DSH 桌面端**：两处资产都由 ark-mate 自己的 payload 提供（见 dsh.ResolveBundledDirs）；
// 解析不到就如实降级（不注入 office env，覆盖层门控把 office 关掉），绝不借用外部桌面应用。
//
// 只对**本机拉子进程**且**构建期选中的就是 dsh**的底座做：远程端点不读 Launch.Env，别碰。
// 失败不返回错误——缺 office 能力是降级（agent 退回自带工具链），不该让启动失败；调用方按需记日志。
func PrepareRuntimeEnv(ctx context.Context, reg *runtime.Registry, homeDir, workspaceDir string) {
	if reg == nil || runtime.SelectedRuntime() != dsh.ID {
		return
	}
	spec, ok := reg.Get(runtime.SelectedRuntime())
	if !ok || !runtime.LaunchesLocally(spec) {
		return
	}
	// 二期：平台托管的过程产物根 `<workspaceDir>/.scratch`。先把它建出来（真实目录、非符号链接），
	// 再把它的绝对路径并入 dsh sandbox-policy 的 additionalWritableRoots，让 workspace-write 允许模型
	// 往会话 cwd 之外的 scratch 写。workspaceDir 为空（代建关闭）时不做（退回一期 cwd 内 .scratch）。
	scratchRoot := ""
	if ws := strings.TrimSpace(workspaceDir); ws != "" {
		scratchRoot = scratch.RootFor(ws) // <workspaceDir>/.scratch
		if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
			glog.Warnw(ctx, "serve: 建过程产物根（scratch）失败，退回 cwd 内 .scratch（不阻塞启动）", "path", scratchRoot, "err", err)
			scratchRoot = ""
		}
	}
	if written, err := dsh.ProvisionProfile(homeDir, scratchRoot); err != nil {
		glog.Warnw(ctx, "serve: 初始化 mate-acp profile 失败（office 能力可能不可用）", "err", err)
	} else {
		glog.Infow(ctx, "serve: mate-acp profile 就绪", "dir", config.DisplayHome(dsh.ProfileDir(homeDir)), "files_written", written)
	}
	primaryRuntime, officeSkills := dsh.ResolveBundledDirs()
	env := dsh.BundledEnv(primaryRuntime, officeSkills)
	if len(env) == 0 {
		glog.Warnw(ctx, "serve: 未解析到 ark-mate 自带的 office 随包资产（office 能力将不可用）",
			"hint", "需随包 primary-runtime 载荷与 dsh-skill-office/assets；或用 MATE_DSH_BUNDLED_RUNTIME / MATE_DSH_BUNDLED_OFFICE_SKILLS 指定")
		return
	}
	id := runtime.SelectedRuntime()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.TrimSpace(k) == "" {
			continue
		}
		if err := reg.SetLaunchEnv(id, k, v); err != nil {
			glog.Warnw(ctx, "serve: 注入 dsh office 随包 env 失败", "key", k, "err", err)
		}
	}
	glog.Infow(ctx, "serve: 已注入 dsh office 随包 env", "runtime", id,
		"primary_runtime", config.DisplayHome(primaryRuntime), "office_skills", config.DisplayHome(officeSkills))
}
