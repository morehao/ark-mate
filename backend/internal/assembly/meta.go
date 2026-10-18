package assembly

import (
	"context"
	"sort"

	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/pkg/config"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// metaSource 组装 `/v1/mate/meta` 的诊断值（M1-7）。
//
// 资产相关字段由装配根按**已解析的资产**补（M2-4 的 build id 现场读——它是闸门用来发现
// "资产被换过"的字段，缓存住就失去意义）；载荷清单（M3-4）来自壳经 `--payloads` 交来的
// 已解析结果，核心不自己解析载荷（只有壳做了显式配置 → 随包 → PATH 的三级解析）。
func (as *assembly) metaSource() svcsystem.MetaSource {
	payloads := as.opts.Payloads
	if payloads == nil {
		// 非桌面态：恒为空数组而不是 nil，让 `/meta` 的客户端只写一种分支。
		payloads = []dtosystem.PayloadInfo{}
	}
	src := svcsystem.MetaSource{
		CoreVersion: version.Version,
		APIContract: version.APIContract,
		Home:        as.cfg.HomeDir,
		Payloads:    payloads,
		// 产品面事实（产品名/版本/身份版本/定位）：**不读** runtime 段——「关于」面板只消费它。
		Product: svcsystem.ProductInfoOf(version.Version),
	}
	if as.assets.Has() {
		src.Assets = svcsystem.MetaAssets{
			Path:    as.assets.Path,
			Digest:  as.assets.Digest,
			BuildID: as.assets.CurrentBuildID(),
		}
	}
	// 底座身份：执行面是它的第一手事实（装配层没显式给时也要正确，否则页面上的底座名会随
	// 装配方式静默变空）。
	if as.rconn != nil {
		ri := as.rconn.Identity(context.Background())
		if ri.ID != "" {
			src.Runtime = dtosystem.RuntimeInfo{ID: ri.ID, Name: ri.Name, Version: ri.Version}
		}
	}
	return src
}

// loadDefinitions 装载 Agent / 能力定义并重校 skill 物化。
//
// 都放在这里而不是各自的构造里：它们是"配置即事实源"的装载点，
// 失败只告警不阻断（用户目录缺文件是常态），但必须发生在路由起来之前。
func (as *assembly) loadDefinitions(ctx context.Context, mode string) error {
	cfg := as.cfg
	// provider 档案（P2 / v11）：**按持久层模式分派**——sqlite 模式下档案在 `providers` 表里，
	// memory 模式仍是 `<ProviderDir>/providers.json`。两种模式都要拿到 `cfg.ProviderDir`。
	if mode == config.StoreModeSQLite {
		as.planes.Providers.LoadProviderDefinitionsFromStore(ctx, cfg.ProviderDir)
	} else {
		as.planes.Providers.LoadProviderDefinitions(cfg.ProviderDir)
	}
	// 能力数据面（P1 / M6-2）：sqlite 模式下 DB 是事实源，memory 模式下文件仍是装载源。
	if mode == config.StoreModeSQLite {
		as.planes.LoadCapabilityDefinitionsFromStore(ctx, cfg.CapabilityDir)
	} else {
		as.planes.LoadCapabilityDefinitions(cfg.CapabilityDir)
	}
	// automation 定义（M6-7）：sqlite 模式下定义与运行态都在 `automations` 表里。
	if mode == config.StoreModeSQLite {
		as.planes.Automation.LoadAutomationDefinitionsFromStore(ctx, cfg.AutomationDir)
	} else {
		as.planes.Automation.LoadAutomationDefinitions(cfg.AutomationDir)
	}
	// skill 投影（§16.5 切片 D / design §5.4）：把注册表里的 skill 投影到各 runtime 的原生目录。
	as.reconcileSkillProjections()
	return nil
}

// reconcileSkillProjections 枚举已注册 runtime，逐个解析 skill 投影并重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先**全解析一遍**（含失败项），无论成败都登记进平面——登记是审计视图的前提；
//  2. 再对解析成功的逐个重校（解析失败的重校直接短路）；
//  3. 最后把**没人认领的覆盖项**告警出来——MATE_PROJECTION_DIRS 里写了个没注册过的 runtime
//     id 时，配置看着生效了、实际一次都没用过；这属于必须说出来的静默失效。
func (as *assembly) reconcileSkillProjections() {
	specs := as.reg.List()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	projections := make([]skill.Projection, 0, len(specs))
	claimed := make(map[string]bool, len(specs))
	for _, sp := range specs {
		claimed[sp.ID+":"+string(runtime.ProjectionSkill)] = true
		p, ok, err := runtime.Resolve(sp, runtime.ProjectionSkill, as.cfg.ProjectionDirs, nil)
		switch {
		case err != nil:
			glog.Warnw(context.Background(), "serve: skill 投影解析失败（该 runtime 不会收到任何技能）", "runtime", sp.ID, "err", err)
			projections = append(projections, skill.Projection{Runtime: sp.ID, Err: err.Error()})
			continue
		case !ok:
			// 该 runtime 没声明 skill 投影（远程端点即此）：不是错误，什么都不做。
			continue
		}
		proj := skill.Projection{Runtime: sp.ID, Projection: p}
		w, r, mErr := as.planes.Skills.ReconcileSkillProjection(proj)
		if mErr != nil {
			glog.Warnw(context.Background(), "serve: skill 物化重校失败", "runtime", sp.ID, "root", p.Root, "err", mErr)
		} else if w > 0 || r > 0 {
			glog.Infow(context.Background(), "serve: skill 物化重校完成", "runtime", sp.ID, "root", p.Root, "written", w, "removed", r)
		}
		if !p.Tested {
			glog.Infow(context.Background(), "serve: skill 投影形态尚未端到端实测（产物会写，但底座是否读得到未验证）",
				"runtime", sp.ID, "root", p.Root, "note", p.UnverifiedReason())
		}
		projections = append(projections, proj)
	}
	as.planes.Skills.Projections().Set(projections)
	for _, key := range sortedKeys(as.cfg.ProjectionDirs) {
		if !claimed[key] {
			glog.Warnw(context.Background(), "serve: MATE_PROJECTION_DIRS 的这一项没有对应的已注册 runtime:skill，未生效（拼写？该 runtime 未注册？）",
				"key", key, "dir", as.cfg.ProjectionDirs[key])
		}
	}
}

// sortedKeys map 键的稳定顺序（日志与执行顺序可复现）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
