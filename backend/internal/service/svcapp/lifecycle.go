package svcapp

// ——— WP-C6：应用生命周期与精确撤销（install / enable / disable / uninstall）———
//
// 一期 A+Slot 的 `/apps` 六接口管的是「**定义**」（App 的字段 CRUD + 启用开关）。二期 C 引入运行期
// 应用包（apppkg 从本地目录装载的第三方包）后，一个 app 不再只是「一行定义」——它可能对应一个**装到
// 本地的包目录** + 一个**隔离子进程 Host**。于是「启用/停用」不再只是翻一个 DB 布尔，而是要：
//
//	install   → 装包到本地应用目录（复用 C1/C8 的 apppkg 校验：清单合法性 + 完整性 sha256 + min core version 门槛）
//	enable    → 起该包的隔离 Host + Reconcile apply 该 app 的 slot/tool（回退到「有此 app」态）
//	disable   → dispose slot（disposeSlot RPC）+ 停 Host（回退到「无此 app」默认态，设计 §5.5「关得干净」）
//	uninstall → 清掉包目录（先 disable 再清，保证子进程先撤销）
//
// 状态机（对齐 magpie `pluginrt`，设计 §11）：`off / pending / active / failed / disposed`。
// **状态是派生的，不是新落库列**：它由三份事实拼成——① 是否有本地包目录（installed）、② DB 的 enabled
// 布尔、③ 运行期 Host 的实时状态（由 `AppRuntimePort` 提供）。派生而非落库的理由：落一列 `state` 会
// 与「host 实际活没活」两个事实源分叉（host 崩了列还写 active），而面板要的是真实态；派生保证每一格
// 都可从当下一眼看穿来源（设计 §11「不靠猜」）。
//
// **层边界（硬）**：本包是 L4（用例层），**不能 import `internal/infra/apphost`（L5）**。故「持有 Host、
// 起停子进程、实现 RuntimeAppToolScope」的部分在装配根（`bootstrap/assembly/app_lifecycle.go`），本包只经
// 一个**窄端口 `AppRuntimePort`** 委托「起/停/查运行期」，不认识 Host/子进程/RPC 任何细节——host 的陌生
// 代码永远不进本层进程的调用栈（守卫 TestUntrustedCodeNeverInProc 的上游约束）。

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/apppkg"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/dto/dtoapp"
)

// App 生命周期状态（设计 §11：off/pending/active/failed；C6 追加 disposed 表「已卸载/待回收」）。
//
// 语义（`deriveAppState` 派生，见下）：
//
//	off      该 app 无运行期包（纯定义 / 编译期内置），或未启用（enabled=false）→ 不注入、不起 host。
//	pending  运行期包已启用、Host 正在拉起/重连（暂无工具面）。
//	active   运行期包已启用、Host 就绪（工具/slot 可用）。
//	failed   运行期包已启用、但 Host 拉起失败/崩溃且重试耗尽（该 app 工具面不可用，why 指明原因）。
//	disposed 运行期包已被 uninstall 清目录但定义仍在（或定义已删但运行期残留待回收）——终态、无工具。
const (
	AppStateOff      = "off"
	AppStatePending  = "pending"
	AppStateActive   = "active"
	AppStateFailed   = "failed"
	AppStateDisposed = "disposed"
)

// RuntimeState 是 `AppRuntimePort` 报回来的运行期实时状态（Host 视角）。
type RuntimeState struct {
	// State 运行期状态：off/pending/active/failed/disposed（与 AppState* 同一套词，避免两处漂移）。
	State string
	// Why 人话原因（failed/pending 时非空：如「子进程崩溃：exit status 3」）。
	Why string
	// HasHost 运行期是否真正持有一个 Host（active/pending/failed 时为真）。
	HasHost bool
}

// AppRuntimeRef 是给运行期端口的 app 快照（只带端口需要的字段，不泄漏持久化行/字段全集）。
type AppRuntimeRef struct {
	ID          string
	Name        string
	ToolDomains []string
	ImplIDs     []string
}

// AppRuntimePort 是「运行期包生命周期」的窄端口（装配根 app_lifecycle.go 实现；nil = 无运行期能力）。
//
// 本包只经它委托「起/停/查运行期」，**不 import apphost**（层边界）。三个方法都必须幂等：
// enable 一个已 active 的 app、disable 一个已停的 app 都不该报错（REST PATCH 可能重放）。
//
// 起停失败语义（fail-closed 但失败隔离）：EnableApp 起 host 失败 → 返回 error，本层把状态置 failed
// （why 带原因），但**不**回滚 enabled 布尔（用户意图是「要启用」，失败是运行期事实，面板显 failed）。
type AppRuntimePort interface {
	// EnableApp 确保该 app 的 Host 已起、slot 已 apply（回退到「有此 app」态）。幂等。
	EnableApp(ctx context.Context, app AppRuntimeRef) error
	// DisableApp 撤销该 app 的 slot 并停 Host（回退到「无此 app」默认态）。幂等。
	DisableApp(ctx context.Context, app AppRuntimeRef) error
	// RuntimeState 查该 app 的运行期实时状态（供 GetApp 回 state+why）。
	RuntimeState(ctx context.Context, app AppRuntimeRef) RuntimeState
	// HasPack 报告该 app 是否在本地应用目录里有包（用于状态派生「有没有运行期包」）。
	HasPack(appID string) bool
	// UninstallPack 释放运行期并清掉该 app 的本地包目录（uninstall 的「清目录」动作）。幂等。
	// 返回 removed=true 表示确实删了一个存在的包目录。
	UninstallPack(ctx context.Context, appID string) (removed bool, err error)
}

// runtimeRefOf 从强类型 App 取一个运行期引用快照。
func runtimeRefOf(app *capability.App) AppRuntimeRef {
	ref := AppRuntimeRef{ID: app.ID, Name: app.Name, ToolDomains: append([]string(nil), app.ToolDomains...)}
	for _, sl := range app.Slots {
		ref.ImplIDs = append(ref.ImplIDs, sl.ImplID)
	}
	return ref
}

// ——— 派生状态 ———

// deriveAppState 由「有无本地包 / 是否启用 / 运行期实时态」拼出对外的 state+why（设计 §11）。
func deriveAppState(enabled, hasPack bool, rt RuntimeState, runtimeWired bool) (string, string) {
	if !runtimeWired {
		return AppStateOff, ""
	}
	if rt.State == AppStateDisposed {
		return AppStateDisposed, rt.Why
	}
	if !hasPack && !rt.HasHost {
		// 纯定义 app（无本地包、无 host）：运行期生命周期不适用 → off（与一期一致）。
		return AppStateOff, ""
	}
	if !enabled {
		// 有包但停用：回退「无此 app」默认态 → off（不是 disposed：包还在，可再启用）。
		return AppStateOff, ""
	}
	switch rt.State {
	case AppStateActive, AppStatePending, AppStateFailed:
		return rt.State, rt.Why
	default:
		return AppStatePending, rt.Why
	}
}

// stateOfApp 是读面派生状态的便捷封装（nil 端口 → runtimeWired=false → off）。
func (s *appSvc) stateOfApp(ctx context.Context, app *capability.App, enabled bool) (string, string) {
	if s.runtime == nil {
		return AppStateOff, ""
	}
	hasPack := s.runtime.HasPack(app.ID)
	rt := s.runtime.RuntimeState(ctx, runtimeRefOf(app))
	st, why := deriveAppState(enabled, hasPack, rt, true)
	return st, why
}

// ——— install / uninstall ———

// InstallApp 从来源目录安装一个应用包到本地应用目录，并（可选）立即启用。
//
// 步骤：读来源父目录做 Distribute（清单+完整性+min core version 门槛，复用 C1/C8）→ 找到该来源包 →
// 原子拷到 `<appsRoot>/<id>/`（先 .tmp 再 rename，避免半包被扫描）→（若非零定义则建/更新定义行）→
// （enabled=true 时）起运行期。任何一步失败即 fail-closed（装坏不落地）。
func (s *appSvc) InstallApp(ctx context.Context, req dtoapp.AppInstallReq) (*dtoapp.AppInstallResp, error) {
	if s.store == nil || s.runtime == nil {
		return nil, failure.NewError(failure.ClassRequest, "app_runtime_unavailable",
			"本进程未接运行期应用能力（无法安装应用包）", nil)
	}
	srcDir := filepath.Clean(req.Dir)
	// 复用 C1/C8 的扫描（父目录）；Distribute 施加完整性 + 版本门槛。
	src, err := apppkg.Distribute(filepath.Dir(srcDir), apppkg.CurrentCoreVersion())
	if err != nil {
		return nil, failure.NewError(failure.ClassRequest, "app_invalid",
			fmt.Sprintf("读取来源包目录 %q 失败：%v", srcDir, err), err)
	}
	var found *apppkg.Package
	for _, p := range src.Loaded {
		if filepath.Clean(p.Dir) == srcDir {
			found = p
			break
		}
	}
	if found == nil {
		reason := "来源目录不是合法应用包（清单/完整性/版本门槛未通过）"
		for _, f := range src.Failed {
			if filepath.Clean(f.Dir) == srcDir {
				reason = f.Reason
				break
			}
		}
		return nil, failure.NewError(failure.ClassRequest, "app_invalid", "安装被拒绝："+reason, nil)
	}
	id := found.Manifest.ID

	// 装到本地应用目录（覆盖安装：先拷 .tmp 再原子替换）。
	dstDir := filepath.Join(s.appsRoot(), id)
	if err := copyPackAtomic(srcDir, dstDir); err != nil {
		return nil, failure.NewError(failure.ClassService, "app_install_failed",
			fmt.Sprintf("装入本地应用目录失败：%v", err), err)
	}

	// 同步定义行：若既有定义不存在，用清单派生一个最小 App 定义（id/name/api_version），使 app 立即可见可启用。
	created := false
	if _, ok, gerr := s.store.GetAppDefinition(ctx, id); gerr != nil {
		return nil, gerr
	} else if !ok {
		if err := s.ensurePackDefinition(ctx, id, id, found.Manifest); err != nil {
			return nil, err
		}
		created = true
	}

	resp := &dtoapp.AppInstallResp{ID: id, Dir: dstDir, Installed: true}
	// 目标启用态与当前不一致时驱动一次（enable=true 启用；enable=false 且当前误为启用则显式停用）。
	// 注意：新建定义走 CreateCAS + gorm `default:1`，零值 enabled=0 在 INSERT 时被 gorm 省略而落成默认 1，
	// 故 **显式** SetAppEnabled(false) 用 map 更新把它真正写成 0（这就是为什么这里要再走一次写面，
	// 而不是只依赖新建时的 row.Enabled=0）。
	//
	// 关键（真起 serve 验证暴露的第二个 bug）：**新建定义时不能信 cur.Enabled**——gorm default:1 会让
	// 新建行读回 enabled=true，导致 `cur.Enabled != req.Enable`（install.enable=true 时 true!=true）为 false，
	// 于是跳过 SetAppEnabled → **运行期 host 从未被拉起**，install 返回 enabled=true 但 state 停在 pending。
	// 因此新建定义（created）时无条件按 req.Enable 驱动一次；覆盖安装（非新建）才用 cur.Enabled 比对避免无谓起停。
	if cur, ok2 := s.GetApp(ctx, id); ok2 == nil && cur != nil {
		if created || cur.Enabled != req.Enable {
			tog, terr := s.SetAppEnabled(ctx, id, req.Enable)
			if terr != nil {
				return nil, terr
			}
			resp.Enabled = req.Enable
			resp.Revision = tog.Revision
		} else {
			resp.Enabled = cur.Enabled
			resp.Revision = cur.Revision
		}
	}
	return resp, nil
}

// ensurePackDefinition 为已装包建一条最小 app 定义（仅当尚无定义时）。工具域/impl 从清单派生，
// 使「运行期包」也能被 `/apps` 读到并走 enable。校验走同一闸门（ValidateApp + 白名单在册）。
func (s *appSvc) ensurePackDefinition(ctx context.Context, id, name string, m *apppkg.Manifest) error {
	// 新装包默认**不启用**：install 只装包+建定义，enable 由显式动作（或 install.enable=true）触发，
	// 避免「装个包就自动起隔离进程」的意外。enabled=false → state=off（有包未启用）。
	disabled := false
	app := &capability.App{ID: id, Name: name, APIVersion: "^1", Enabled: &disabled}
	if m != nil {
		for _, td := range m.Provides.Tools {
			app.ToolDomains = append(app.ToolDomains, td.Domain)
		}
		for _, im := range m.Provides.Impls {
			app.Slots = append(app.Slots, capability.AppSlot{ImplID: im.ImplID})
		}
		if m.APIVersion != "" {
			app.APIVersion = m.APIVersion
		}
		// 使用说明随包装进定义（v42）：让装进来的应用也自带「怎么开口」。
		app.Tagline = m.Tagline
		app.Examples = append([]string(nil), m.Examples...)
		app.UsageGuide = m.UsageGuide
	}
	if err := capability.ValidateApp(app); err != nil {
		return invalidErr(id, err)
	}
	row, members, err := encodeApp(app)
	if err != nil {
		return err
	}
	// 市场下载 / 装包建的定义来源恒为「市场」（v41）。本函数只在定义尚不存在时被调用
	// （覆盖安装已存在定义时走的是 SetAppEnabled，不经此处），故不会改掉既有来源。
	row.Source = AppSourceMarket
	if _, err := s.store.UpsertAppDefinition(ctx, id, row, members, 0, true, "app-install"); err != nil {
		return err
	}
	return nil
}

// UninstallApp 卸载一个 app：先停运行期（dispose+停 host）+ 清本地包目录，再删定义行。幂等。
func (s *appSvc) UninstallApp(ctx context.Context, id string) (*dtoapp.AppUninstallResp, error) {
	if s.store == nil || s.runtime == nil {
		return nil, failure.NewError(failure.ClassRequest, "app_runtime_unavailable",
			"本进程未接运行期应用能力（无法卸载应用包）", nil)
	}
	row, ok, err := s.store.GetAppDefinition(ctx, id)
	if err != nil {
		return nil, err
	}
	var app *capability.App
	if ok {
		app, err = rowToApp(row)
		if err != nil {
			return nil, err
		}
	}
	// 关得干净：先停运行期（dispose slot + 停 host），再清目录。
	if app != nil {
		_ = s.runtime.DisableApp(ctx, runtimeRefOf(app))
	}
	removed, perr := s.runtime.UninstallPack(ctx, id)
	if perr != nil {
		return nil, failure.NewError(failure.ClassService, "app_uninstall_failed",
			fmt.Sprintf("清理应用包目录失败：%v", perr), perr)
	}
	defDeleted := false
	if ok {
		resp, derr := s.DeleteApp(ctx, id)
		if derr != nil {
			return nil, derr
		}
		defDeleted = resp != nil && resp.Deleted
	}
	return &dtoapp.AppUninstallResp{ID: id, Uninstalled: removed || defDeleted, PackRemoved: removed, DefinitionDeleted: defDeleted}, nil
}

// ——— install 辅助：应用根目录 / 原子拷贝 ———

// appsRoot 本地应用根目录（与 apppkg.DefaultAppsDir 同源；测试可用 s.appsRootOverride 覆盖）。
func (s *appSvc) appsRoot() string {
	if s.appsRootOverride != "" {
		return s.appsRootOverride
	}
	dir, err := apppkg.DefaultAppsDir()
	if err != nil {
		return ".apps"
	}
	return dir
}

// copyPackAtomic 把 srcDir 复制到 dstDir（先 dstDir.tmp 再原子 rename；覆盖时先删旧 dstDir）。
// 只复制常规文件与子目录（拒绝符号链接上跳——包目录来自受信本地目录，但仍 fail-closed 到包根内）。
func copyPackAtomic(srcDir, dstDir string) error {
	tmp := dstDir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := copyTree(srcDir, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.RemoveAll(dstDir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, dstDir)
}

// copyTree 递归复制目录（只常规文件；遇到符号链接直接 fail-closed：包目录不该含符号链接）。
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("包含符号链接 %q（拒绝安装含符号链接的包）", rel)
		}
		if !info.Mode().IsRegular() {
			return nil // 跳过设备/管道等非常规文件
		}
		return copyFile(path, target, info.Mode())
	})
}

// copyFile 复制一个常规文件（保留权限位）。
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
