package assembly

// ——— WP-C6：应用生命周期管理器（装配层 L-top，允许持有 apphost）———
//
// 本文件实现两个端口，把「运行期应用包的生命周期」在**装配层**落地（层边界：只有装配根能同时看见
// svcapp(L4) 与 apphost(L5)，故 host 的存在只在这里）：
//
//  1. `svcapp.AppRuntimePort`：install/enable/disable/uninstall 时由 svcapp 委托「起/停/查运行期」——
//     本管理器持有每个运行期包的隔离 Host，enable 起 host + apply 该 app 的 slot，disable 精确撤销
//     （disposeSlot RPC）+ 停 host，回退「无此 app」默认态（设计 §5.5「关得干净」）。
//  2. `sessionruntime.RuntimeAppToolScope`：把「运行期包 app 的 stdio 适配 server 条目」解算给会话
//     注入层——`RuntimeAppServers(appIDs)` 只为**本地有包**的 app id 返回一条 `mate mcp app --apps=...`
//     桥条目（该子命令在独立进程里再起 host 并聚合工具面，见 maintenance/app_mcp.go）。
//
// **与 C5 的关系**：C5 的 `startAppSlots` 在进程启动时 Reconcile 一次并保存撤销栈（`as.appSlotDispose`）；
// 那是「启动快照」。本管理器负责「启动之后的运行期增删」——enable/disable 在运行期实时起停 host。
// 两者不互相覆盖对方的 host 集合：C5 的 host 归 `as.appHosts`（进程级、随 stopAppSlots 关），本管理器的
// host 归它自己的 `hosts`（随 disable/uninstall 关）。为避免同一包被两处各起一个 host：**启动快照里
// 已装的运行期包由 C5 起 host；本管理器按需为 enable 再起**——单机单操作员场景下这两类操作不并发，
// 且每个 host 都是独立进程（隔离边界不变），代价只是片刻并存两个同包进程，可接受（C 期单操作员）。
//
// **状态派生**：`RuntimeState` 汇报本管理器看到的 host 实时态（active/pending/failed + why）；
// svcapp 的 `deriveAppState` 结合「有无本地包 + enabled」拼出对外 state（见 svcapp/lifecycle.go）。

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/core/appslot"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/infra/apphost"
	"github.com/morehao/ark-mate/internal/service/svcapp"
	"github.com/morehao/golib/glog"
)

// 编译期断言：管理器必须同时满足两个端口。
var (
	_ svcapp.AppRuntimePort    = (*appLifecycle)(nil)
	_ runtimeAppToolScopeCheck = (*appLifecycle)(nil)
)

// runtimeAppToolScopeCheck 是 sessionruntime.RuntimeAppToolScope 的结构镜像（避免 assembly 直接引用
// 该接口类型造成无谓耦合；签名须一致：RuntimeAppServers(appIDs []string) []capability.McpServer）。
type runtimeAppToolScopeCheck interface {
	RuntimeAppServers(appIDs []string) []capability.McpServer
}

// appLifecycle 运行期应用包的生命周期管理器（装配根建一次，进程内共享）。
type appLifecycle struct {
	mu sync.Mutex
	// hosts 运行期包 id → 隔离 Host（本管理器起停的）。
	hosts map[string]*apphost.Host
	// enabled 本管理器视角下「已 enable 的 app id 集合」（供 RuntimeAppServers 判定工具面）。
	enabled map[string]bool
	// appsRoot 本地应用根目录（卸载清目录、HasPack 判定）。
	appsRoot string
	// spawn 拉起 host 的方式（生产 ExecSpawn；测试注入样例）。
	spawn apphost.SpawnFunc
	// coreVersion 核心版本（安装 min core version 门槛用；空 = 不设本层门槛，由 apppkg 兜底）。
	coreVersion string
	// baseCtx/baseCancel 宿主**进程级**生命周期上下文：长驻 app-host 子进程必须绑它，
	// **不能绑单次 HTTP 请求 ctx**——否则 handler 一返回、请求 ctx 被 cancel，exec.CommandContext
	// 就把 host 子进程杀掉，enable 立刻变 state=failed（真起 mate serve 验证暴露的 bug）。
	// 只有 closeAll（进程关闭）才调 baseCancel，届时统一收掉所有 host。
	baseCtx    context.Context
	baseCancel context.CancelFunc
}

// newAppLifecycle 构造生命周期管理器。spawn=nil → 生产 ExecSpawn。
func newAppLifecycle(appsRoot string, spawn apphost.SpawnFunc) *appLifecycle {
	if spawn == nil {
		spawn = apphost.ExecSpawn()
	}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	return &appLifecycle{
		hosts:       map[string]*apphost.Host{},
		enabled:     map[string]bool{},
		appsRoot:    appsRoot,
		spawn:       spawn,
		coreVersion: apppkg.CurrentCoreVersion(),
		baseCtx:     baseCtx,
		baseCancel:  baseCancel,
	}
}

// HasPack 报告本地应用目录里是否有该 app 的包。
func (l *appLifecycle) HasPack(appID string) bool {
	if l == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(l.appsRoot, appID))
	return err == nil
}

// EnableApp 起该 app 的隔离 Host 并 apply 其 slot（幂等）。起 host 失败 → 返回 error（svcapp 置 state=failed）。
func (l *appLifecycle) EnableApp(ctx context.Context, ref svcapp.AppRuntimeRef) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if h, ok := l.hosts[ref.ID]; ok && h != nil && h.State() == apphost.StateReady {
		l.enabled[ref.ID] = true
		l.mu.Unlock()
		return nil // 已 active，幂等
	}
	l.mu.Unlock()

	// 找包（本地应用目录）。
	pkg := l.findPackage(ref.ID)
	if pkg == nil {
		// 纯定义 app（无本地包）：无需起 host，标记 enabled（工具面由编译期通道处理）。
		l.mu.Lock()
		l.enabled[ref.ID] = true
		l.mu.Unlock()
		return nil
	}

	h := apphost.NewHost(apphost.HostConfig{
		PackageID:  ref.ID,
		PkgDir:     pkg.Dir,
		APIVersion: pkg.Manifest.APIVersion,
		Spawn:      l.spawn,
	})
	// 关键：用**进程级 baseCtx** 起 host，绝不能用入参 ctx（= HTTP 请求 ctx）。
	// 入参 ctx 在 handler 返回后会被 net/http cancel，而 ExecSpawn 用 exec.CommandContext 拉子进程——
	// 一旦绑请求 ctx，host 会在 enable 请求返回瞬间被杀，state 立刻变 failed。
	// baseCtx 只在 closeAll（进程关闭）时取消。入参 ctx 仍用于下面 slot apply 的观测/日志。
	if err := h.Start(l.baseCtx); err != nil {
		_ = h.Close()
		return err
	}
	// apply 该 app 的每个 slot（运行期 impl 经 host.applySlot RPC）。
	runner := apphost.NewImplRunner(h)
	for _, implID := range ref.ImplIDs {
		if err := runner.Impl(implID)(nopCtx{}); err != nil {
			// 失败隔离：单 slot apply 失败记日志，host 仍可用（工具面照跑）。
			glog.Warnw(ctx, "app enable: slot apply 失败（继续，工具面不受影响）", "app", ref.ID, "impl_id", implID, "err", err)
		}
	}
	l.mu.Lock()
	l.hosts[ref.ID] = h
	l.enabled[ref.ID] = true
	l.mu.Unlock()
	return nil
}

// DisableApp 精确撤销该 app：dispose 每个 slot（disposeSlot RPC）→ 停 Host（幂等，关得干净）。
func (l *appLifecycle) DisableApp(ctx context.Context, ref svcapp.AppRuntimeRef) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	h := l.hosts[ref.ID]
	delete(l.hosts, ref.ID)
	delete(l.enabled, ref.ID)
	l.mu.Unlock()

	if h == nil {
		return nil // 没起过 host，幂等
	}
	// 精确撤销：逐 impl disposeSlot（让子进程撤销它注册过的东西），再关 host（shutdown + kill）。
	runner := apphost.NewImplRunner(h)
	for _, implID := range ref.ImplIDs {
		if err := runner.DisposeImpl(implID); err != nil {
			glog.Warnw(ctx, "app disable: slot dispose 失败（继续，仍会停 host）", "app", ref.ID, "impl_id", implID, "err", err)
		}
	}
	if err := h.Close(); err != nil {
		glog.Warnw(ctx, "app disable: 关闭 host 失败", "app", ref.ID, "err", err)
		return err
	}
	return nil
}

// RuntimeState 汇报该 app 的运行期实时态（供 svcapp 派生 state+why）。
func (l *appLifecycle) RuntimeState(_ context.Context, ref svcapp.AppRuntimeRef) svcapp.RuntimeState {
	if l == nil {
		return svcapp.RuntimeState{State: svcapp.AppStateOff}
	}
	l.mu.Lock()
	h := l.hosts[ref.ID]
	l.mu.Unlock()
	if h == nil {
		return svcapp.RuntimeState{State: svcapp.AppStateOff}
	}
	switch h.State() {
	case apphost.StateReady:
		return svcapp.RuntimeState{State: svcapp.AppStateActive, HasHost: true}
	case apphost.StateFailed:
		return svcapp.RuntimeState{State: svcapp.AppStateFailed, Why: h.Reason(), HasHost: true}
	default:
		return svcapp.RuntimeState{State: svcapp.AppStatePending, Why: h.Reason(), HasHost: true}
	}
}

// UninstallPack 停该 app 的运行期（若在跑）并删除本地包目录（幂等）。
func (l *appLifecycle) UninstallPack(ctx context.Context, appID string) (bool, error) {
	if l == nil {
		return false, nil
	}
	// 先停（幂等）。
	l.mu.Lock()
	h := l.hosts[appID]
	delete(l.hosts, appID)
	delete(l.enabled, appID)
	l.mu.Unlock()
	if h != nil {
		_ = h.Close()
	}
	dir := filepath.Join(l.appsRoot, appID)
	if _, err := os.Stat(dir); err != nil {
		return false, nil // 没有包目录，幂等
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

// RuntimeAppServers 实现 sessionruntime.RuntimeAppToolScope：为「本地有包且在册」的 app id 返回一条
// 合并的 `mate mcp app --apps=...` stdio 桥条目（子命令在独立进程里各自起 host + 聚合工具面）。
//
// 语义（与 WP5 appendAppMcpServers 一致）：只会有非空 appIDs 调进来（默认零注入在上层保证）；
// 未知/无包/停用的 id 自然不贡献——返回空 = 这些 id 里没有运行期包 app（零注入）。
func (l *appLifecycle) RuntimeAppServers(appIDs []string) []capability.McpServer {
	if l == nil || len(appIDs) == 0 {
		return nil
	}
	var ids []string
	l.mu.Lock()
	for _, id := range appIDs {
		// 只对「本地有包」且「本管理器认为已启用」的 app 暴露工具面：双重判据保证
		// disable 后该 app 即便被误传进来也**不贡献工具**（关得干净，设计 §5.9）。enabled 集由
		// enable/disable 维护、启动时由 startAppLifecycle 从 DB 已启用 app 播种（见下）。
		if l.enabled[id] && l.HasPack(id) {
			ids = append(ids, id)
		}
	}
	l.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	exe, err := os.Executable()
	if err != nil {
		glog.Warnw(context.Background(), "运行期 app 工具面不可用：取不到自身可执行文件路径", "err", err)
		return nil
	}
	return []capability.McpServer{{
		ID:        AppRuntimeMcpID,
		Name:      AppRuntimeMcpName,
		Transport: capability.TransportStdio,
		Command:   exe,
		Args:      []string{"mcp", "app", "--apps=" + strings.Join(ids, ",")},
	}}
}

// AppRuntimeMcpID / AppRuntimeMcpName 运行期包工具面桥条目的稳定 id / 展示名（进指纹）。
const (
	AppRuntimeMcpID   = "app-runtime"
	AppRuntimeMcpName = "mate-app-runtime"
)

// findPackage 在本地应用目录里找该 id 的包（清单 + 完整性校验通过者）。
func (l *appLifecycle) findPackage(id string) *apppkg.Package {
	dir := filepath.Join(l.appsRoot, id)
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	scan, err := apppkg.Scan(filepath.Dir(dir))
	if err != nil {
		return nil
	}
	for _, p := range scan.Loaded {
		if p.Manifest != nil && p.Manifest.ID == id {
			return p
		}
	}
	return nil
}

// nopCtx 是一个最小 appslot.Ctx 实现（impl apply 只需一个 Ctx；运行期 impl 不读它）。
type nopCtx struct{}

func (nopCtx) Provide(string, any) {}
func (nopCtx) Service(string) any  { return nil }
func (nopCtx) OnDispose(func())    {}

var _ appslot.Ctx = nopCtx{}

// ——— 装配步骤（WP-C6）：构造生命周期管理器并接线到会话注入面 ———

// startAppLifecycle 构造运行期包生命周期管理器，并把它接到会话注入面（RuntimeAppToolScope）。
//
// 非 critical：构造只做内存装配（不启动任何 host）。svcapp 侧注入在 services.go 里通过
// WithRuntimePort(as.appLifecycle) 完成（该步在「用例与控制器」之前），本步负责把同一个实例交给
// sessionruntime（SetRuntimeAppToolScope），使运行期包工具面能进会话注入（tools/list）。
func (as *assembly) startAppLifecycle(ctx context.Context) error {
	appsRoot, err := apppkg.DefaultAppsDir()
	if err != nil {
		glog.Warnw(ctx, "应用生命周期：解析本地应用根目录失败，运行期能力不可用", "err", err)
		appsRoot = ""
	}
	as.appLifecycle = newAppLifecycle(appsRoot, nil)
	// 播种「已启用」集合：DB 里 enabled 且有本地包的 app 视为已启用（其工具面由 `mate mcp app` 桥
	// 按会话 app scope 独立起 host 暴露；本管理器只在运行期 enable/disable 时持有 host 做 slot apply/dispose）。
	// 从 svcapp 读 enabled app 列表（svcapp 未就绪则空跑）。
	if as.services.App != nil {
		if resps, lerr := as.services.App.ListApps(ctx); lerr == nil {
			for i := range resps {
				if resps[i].Enabled && as.appLifecycle.HasPack(resps[i].ID) {
					as.appLifecycle.markEnabled(resps[i].ID, true)
				}
			}
		} else {
			glog.Warnw(ctx, "应用生命周期：读取已启用 app 播种失败（工具面可能不含启动期已启用的运行期包）", "err", lerr)
		}
	}
	// 接线到会话注入面：运行期包 app 的 stdio 桥条目（否则 RuntimeAppToolScope 未接 = 零注入）。
	as.svc.SetRuntimeAppToolScope(as.appLifecycle)
	glog.Infow(ctx, "应用生命周期管理器就绪", "appsRoot", appsRoot)
	return nil
}

// stopAppLifecycle 关闭生命周期管理器持有的全部运行期 host（进程关闭时；幂等）。
func (as *assembly) stopAppLifecycle(context.Context) error {
	if as.appLifecycle == nil {
		return nil
	}
	as.appLifecycle.closeAll()
	as.appLifecycle = nil
	return nil
}

// markEnabled 设置某 app 的「已启用」记账（启动播种 / enable / disable 用）。
func (l *appLifecycle) markEnabled(id string, on bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if on {
		l.enabled[id] = true
	} else {
		delete(l.enabled, id)
	}
	l.mu.Unlock()
}

// closeAll 关闭本管理器持有的全部 host（进程关闭时；幂等）。
func (l *appLifecycle) closeAll() {
	if l == nil {
		return
	}
	l.mu.Lock()
	hosts := make([]*apphost.Host, 0, len(l.hosts))
	for _, h := range l.hosts {
		hosts = append(hosts, h)
	}
	l.hosts = map[string]*apphost.Host{}
	l.enabled = map[string]bool{}
	l.mu.Unlock()
	// 取消进程级 baseCtx：host 子进程绑的是它，cancel 会连带让 exec.CommandContext 收掉子进程；
	// 下面再显式 Close 一次（幂等，双保险）。
	if l.baseCancel != nil {
		l.baseCancel()
	}
	for _, h := range hosts {
		_ = h.Close()
	}
}

// appRuntimeOptionF 把运行期端口作为 svcapp 构造选项（nil 则返回空选项，纯 A+Slot）。
func appRuntimeOptionF(port svcapp.AppRuntimePort) []svcapp.Option {
	if port == nil {
		return nil
	}
	return []svcapp.Option{svcapp.WithRuntimePort(port)}
}

// asAppRuntimePort 把 *appLifecycle 折成 svcapp.AppRuntimePort（nil 保持 nil 接口）。
func asAppRuntimePort(l *appLifecycle) svcapp.AppRuntimePort {
	if l == nil {
		return nil
	}
	return l
}
