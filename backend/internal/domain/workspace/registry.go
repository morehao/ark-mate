package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/code"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/object/objid"
	"github.com/morehao/golib/glog"
)

// 本文件是**工作空间登记表**（v14）的领域实现。
//
// 它与 `workspace.go`（解析与校验）、`workspace_view.go`（只读派生面）的分工：
//
//	workspace.go      ：路径的**校验与规范化**（三条件 + 符号链接 + 根白名单）——一条实现，
//	                    建会话、浏览、登记三处共用，所以"能登记"与"能建会话"不可能分叉；
//	workspace_view.go ：从既有会话/白名单**派生**的只读视图（最近使用、资料库、目录内容）；
//	本文件            ：**登记项**的增删改查与展示投影（用户显式产出的那份状态）。
//
// 三条纪律（对应设计 ADR-1/ADR-5/ADR-6）：
//   - 登记**不豁免白名单**：创建走的就是 ValidateWorkspace(path, roots)。否则"登记时能过、
//     建会话被拒"就是一条死路——点了会得到一个必然失败的会话；
//   - 归属只由 `sessions.workspace_id` 表达，这里不维护第二份成员账；
//   - 删登记**只删登记**：目录、文件、会话历史一概不动，那些会话读面归默认工作空间。

// WorkspaceRegistry 列出全部工作空间登记（按展示顺序）。
//
// 目录状态、会话数、最近使用都是**读时现算**：登记表只存身份、标题与顺序（设计 ADR-1），
// 其余是事实的投影——存一份就多一份会与事实漂移的副本。
func (pl *Plane) WorkspaceRegistry() (*WorkspaceList, error) {
	if pl.st == nil {
		// memory 模式：**用户登记表**不存在（没有地方存"用户显式登记的东西"），所以
		// `writable=false`（前端据此不渲染创建入口，设计 ADR-11）。
		//
		// 但**默认工作空间照常列出**：它是平台代建的那一条（见 EnsureDefaultWorkspace），
		// 而不是用户登记项。把它藏起来会让左栏少一个分组、让"未指定 cwd 建的会话"在界面上
		// 无处可挂——而它确实存在、确实可用。
		out := &WorkspaceList{
			Items:        []WorkspaceItem{},
			Writable:     false,
			ProvisionDir: pl.WorkspaceDir(),
			CreateRoot:   pl.CreateRoot(),
		}
		if def, ok, _ := pl.DefaultWorkspace(); ok {
			out.Items = append(out.Items, pl.workspaceRespForMemDefault(def))
			out.DefaultID = def.ID
		}
		return out, nil
	}
	ctx := context.Background()
	rows, err := pl.st.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := pl.st.CountWorkspacesByID(ctx)
	if err != nil {
		return nil, err
	}
	lastUsed, err := pl.st.LastUsedByWorkspaceID(ctx)
	if err != nil {
		return nil, err
	}
	// 历史会话（workspace_id = ''）在**读面**归默认工作空间（设计 ADR-6）：
	// 它们的数量与最近使用要并到默认那一行上，否则界面上默认工作空间永远显示 0 个会话，
	// 而点进去却有会话——那种不一致没有任何解释余地。
	defaultID := ""
	for _, r := range rows {
		if r.IsDefault {
			defaultID = r.ID
			break
		}
	}
	out := &WorkspaceList{
		Items:        make([]WorkspaceItem, 0, len(rows)),
		DefaultID:    defaultID,
		Writable:     true,
		ProvisionDir: pl.WorkspaceDir(),
		CreateRoot:   pl.CreateRoot(),
	}
	for _, r := range rows {
		resp := WorkspaceItem{
			ID:        r.ID,
			Path:      r.Path,
			Title:     r.Title,
			IsDefault: r.IsDefault,
			ListOrder: r.ListOrder,
			DirStatus: dirStatusOf(r.Path),
			CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: r.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		resp.Sessions = int(counts[r.ID])
		resp.LastUsedAt = lastUsed[r.ID]
		if r.IsDefault {
			// 归到默认工作空间的历史会话（workspace_id = ''）。
			resp.Sessions += int(counts[""])
			if v := lastUsed[""]; v > resp.LastUsedAt {
				resp.LastUsedAt = v
			}
		}
		out.Items = append(out.Items, resp)
	}
	return out, nil
}

// CreateWorkspace 创建登记的**唯一入口**：按请求体的形状在两种模式里选一种（ADR-2）。
//
//	`path` 非空 → 登记一个**已存在**的目录（get-or-create；`Strict=true` 时重复报错）
//	`name` 非空 → 在**落盘根**（`create_root`）下建一个目录并登记
//	两个都给 / 都不给 → 40001（**不猜**用户想干嘛：猜错的后果是"我明明点了新建，它却登记了别的目录"）
//
// 为什么在域里而不是控制器里：这是**业务判决**（哪条路、哪条错误码），而控制器的职责只有
// 参数绑定与回响应。放控制器里还会让"两模式"这件事出现在一个不懂工作空间语义的地方。
func (pl *Plane) CreateWorkspace(req CreateRequest) (*WorkspaceItem, error) {
	path := strings.TrimSpace(req.Path)
	name := strings.TrimSpace(req.Name)
	switch {
	case path != "" && name != "":
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"`path` 与 `name` 只能给一个：「选择已有目录」与「新建工作空间」是两条路", nil)
	case path == "" && name == "":
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"需要 `path`（登记已有目录）或 `name`（在落盘根下新建）之一", nil)
	case name != "":
		return pl.CreateNamedWorkspace(name, req.Title)
	default:
		return pl.CreateRegisteredWorkspace(path, req.Title, req.Strict)
	}
}

// CreateNamedWorkspace 在**落盘根**下建一个目录并登记（ADR-2 的 `name` 模式）。
//
// 为什么它必须是一条服务端路径而不是前端串两次调用：`POST /workspaces/dirs`（建目录）与
// `POST /workspaces`（登记）是两次可失败的网络往返，中间任何一步失败都会留下
// 「目录建了但没登记」或「登记失败但目录在」的半成品，而**用户看不到哪一半成了**。
//
// 三件一起做（顺序是有理由的）：
//
//	① 名字校验 —— `ValidateWorkspaceName`（与「新建文件夹」共用同一条实现）；
//	② `Mkdir`（**不递归、不覆盖**）—— 已存在即 40901，绝不静默复用；
//	③ 登记 —— 走与 `path` 模式**同一条** `CreateRegisteredWorkspace` 流水线。
//
// 关于"同一事务"：文件系统与 sqlite 不在一个事务里，这是形态的硬限制，所以这里用**补偿**代替——
// 目录建好而登记失败时把它删回去（`os.Remove` 只能删空目录，正好是刚建出来的那一层）。
// 顺序选「先建目录后登记」而不是反过来，是因为两种残留的代价不对称：
//
//	建了目录没登记 → 一个空目录（应用看不见它，用户可以自己删掉）
//	登记了没目录   → 一条 `dir_status: missing` 的登记（会话建不了，得靠 P1-g 的补救路径修）
//
// 后者更糟：它是一条**指向不存在东西的**记录，而前者只是多了一个空文件夹。
func (pl *Plane) CreateNamedWorkspace(name, title string) (*WorkspaceItem, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	root := pl.CreateRoot()
	if root == "" {
		// 未声明落盘根 → **显式拒绝**，且界面这一档不渲染入口（ADR-3 的入口纪律：
		// 不给一个必然失败的按钮）。
		const codeName = "workspace_create_root_unset"
		return nil, acl.NewError(acl.ClassRequest, codeName,
			"["+codeName+"] 未配置新建工作空间的落盘根（create_root / workspace_dir）：无法按名字新建，请改用「选择已有目录…」", nil)
	}
	cleaned, err := ValidateWorkspaceName(name)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(root, cleaned)
	// 已存在（目录 / 文件 / 符号链接）→ 40901。**不静默复用**：同名目录已存在与"我建了一个新的"
	// 是两件事，界面必须能分辨（与本仓 `CreateWorkspaceDir` 的既有口径逐字一致）。
	if _, statErr := os.Lstat(target); statErr == nil {
		return nil, workspaceExistsErr(target)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, workspaceExistsErr(target)
		}
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("创建目录失败: %s", target), err)
	}
	// 登记走 path 模式那条流水线（`strict=true`）：它与「选择已有目录…」共用同一套路径校验
	// 与同一份响应投影，所以两条路不会在"能建"与"能登记"上分叉。
	resp, regErr := pl.CreateRegisteredWorkspace(target, title, true)
	if regErr != nil {
		// 补偿：把刚建出来的空目录删回去（删不掉只告警——它已经被登记失败的事实带走了）。
		if rmErr := os.Remove(target); rmErr != nil {
			glog.Warnw(context.Background(), "工作空间：登记失败后清理新建目录失败", "dir", target, "err", rmErr)
		}
		return nil, regErr
	}
	return resp, nil
}

// PatchWorkspace 改写一条登记的**可改字段**（展示名 / 两个默认值）。
//
// 三条判据：
//
//	① 默认值只改**传了的**（`nil` = 不改）——把"没写"与"写了空串"合成一个就会出
//	   「只想改标题，结果把默认能力集清空了」；
//	② 展示名沿用既有的 `RenameRegisteredWorkspace`（一条流水线，不在这里再写一份改库）；
//	③ **既有会话一个字节都不动**：能力集与权限模式在会话上是**快照**，改工作空间默认值
//	   就把它们悄悄改掉，表现是「我调了个默认值，昨天那个会话的行为变了」。
//
// **改登记路径不在本批**（`req.Path` 由 W4 / P1-g 接）：那要先定"改路径之后既有会话的 cwd
// 怎么办"，而那不是本批的判断。本批显式拒绝它，而不是悄悄忽略——悄悄忽略等于让前端以为
// 改成功了。
func (pl *Plane) PatchWorkspace(id string, req PatchRequest) (*WorkspaceItem, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	// 改登记路径（W4）：**id 不变、既有会话不动**。走与建会话**同一条**校验（绝对 / 存在 /
	// 是目录 / 符号链接规范化），所以"能改过去"与"能在那里建会话"不可能一个能过一个不能过。
	if req.Path != nil {
		canonical, verr := ValidateWorkspace(*req.Path)
		if verr != nil {
			return nil, verr
		}
		ok, rerr := pl.st.RelocateWorkspace(context.Background(), id, canonical)
		switch {
		case pl.st.IsPathTaken(rerr):
			return nil, workspaceExistsErr(canonical)
		case pl.st.IsDefaultImmutable(rerr):
			return nil, acl.NewError(acl.ClassRequest, "workspace_default_immutable",
				"默认工作空间的路径由装配期决定，不能改", nil)
		case rerr != nil:
			return nil, rerr
		case !ok:
			return nil, WorkspaceNotFoundError(id)
		}
	}
	resp, err := pl.RenameRegisteredWorkspace(id, req.Title)
	if err != nil {
		return nil, err
	}
	if req.DefaultCapability == nil && req.DefaultPermissionMode == nil {
		return resp, nil
	}
	cur, ok, err := pl.workspaceDefaults(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, WorkspaceNotFoundError(id)
	}
	capability, mode := cur.CapabilityJSON, cur.PermissionMode
	if req.DefaultCapability != nil {
		capability = *req.DefaultCapability
	}
	if req.DefaultPermissionMode != nil {
		mode = *req.DefaultPermissionMode
	}
	if _, err := pl.st.SetWorkspaceDefaults(context.Background(), id, capability, mode); err != nil {
		return nil, err
	}
	return pl.RenameRegisteredWorkspace(id, req.Title)
}

// workspaceDefaults 读一个工作空间当前声明的默认值（不存在 → ok=false）。
func (pl *Plane) workspaceDefaults(id string) (WorkspaceDefaults, bool, error) {
	if pl.st == nil {
		return WorkspaceDefaults{}, false, nil
	}
	row, ok, err := pl.st.GetWorkspace(context.Background(), id)
	if err != nil || !ok {
		return WorkspaceDefaults{}, false, err
	}
	return WorkspaceDefaults{
		CapabilityJSON: row.DefaultCapabilityJSON,
		PermissionMode: row.DefaultPermissionMode,
	}, true, nil
}

// WorkspaceDefaultsOf 读某工作空间声明的默认值（建会话那条解析链的**唯一**取值口）。
//
// `ok=false`（memory 模式 / 未登记）与"登记了但没有默认值"（`”`）是两件事：
// 前者交给下游缺省，后者同样交给下游缺省——但它们不该在类型上被合起来，
// 否则将来"工作空间没登记"要报错时就没有位置放了。
func (pl *Plane) WorkspaceDefaultsOf(id string) (WorkspaceDefaults, bool) {
	d, ok, _ := pl.workspaceDefaults(id)
	return d, ok
}

// workspaceExistsErr 同名目标已存在（唯一出口：40901 + `workspace_exists`）。
func workspaceExistsErr(target string) error {
	return acl.NewError(acl.ClassLimit, "workspace_exists",
		fmt.Sprintf("目录已存在: %s", target), nil)
}

// CreateRegisteredWorkspace 登记一个工作空间（**get-or-create**；strict=true 时重复即报错）。
//
// 路径走 `ValidateWorkspace(spec, roots)`：与建会话**同一条校验**。这不是"顺手复用"，
// 而是本条设计的一半——两处各写一套，就必然出现"这里能选、那里建不了"的死路。
func (pl *Plane) CreateRegisteredWorkspace(path, title string, strict bool) (*WorkspaceItem, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	canonical, err := ValidateWorkspace(path)
	if err != nil {
		return nil, code.FromError(err)
	}
	ctx := context.Background()
	if strict {
		if _, ok, err := pl.st.WorkspaceByPath(ctx, canonical); err != nil {
			return nil, err
		} else if ok {
			return nil, code.FromError(acl.NewError(acl.ClassLimit, "workspace_exists",
				fmt.Sprintf("该目录已登记为工作空间: %s", canonical), nil))
		}
	}
	row, err := pl.st.CreateWorkspace(ctx, canonical, title)
	if err != nil {
		return nil, err
	}
	resp, err := pl.workspaceRespByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// RenameRegisteredWorkspace 改写展示名。**不改身份**：id 与 path 都不动。
func (pl *Plane) RenameRegisteredWorkspace(id, title string) (*WorkspaceItem, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	ctx := context.Background()
	if _, ok, err := pl.st.GetWorkspace(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, WorkspaceNotFoundError(id)
	}
	if err := pl.st.RenameWorkspace(ctx, id, title); err != nil {
		return nil, err
	}
	return pl.workspaceRespByID(ctx, id)
}

// DeleteRegisteredWorkspace 删除一条登记（**不删会话、不删目录**，设计 ADR-6）。
//
// 默认工作空间不可删：它是"建会话未指定 cwd"这条路的落点，删掉等于让那条路无路可走。
// 这条判定在 store 层也有一道（任何调用方都删不出一个没有落点的库），这里只是把它翻成
// 一个用户能读懂的错。
func (pl *Plane) DeleteRegisteredWorkspace(id string) (*WorkspaceDelete, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	ctx := context.Background()
	released, err := pl.st.DeleteWorkspace(ctx, id)
	switch {
	case pl.st.IsNotFound(err):
		return nil, WorkspaceNotFoundError(id)
	case pl.st.IsDefaultImmutable(err):
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "workspace_default_immutable",
			"默认工作空间不可删除：它是未指定工作目录的会话的落点", nil))
	case err != nil:
		return nil, err
	}
	// **同步内存镜像**：列表读的是 `s.sessions` 的内存记录，而 store 只改了库里的列。
	// 不同步的后果是实测出来的——删完登记立刻按默认工作空间过滤，那几个会话**看不到**
	// （内存里还挂着已经不存在的 id），要等重启才回来。库是对的、界面是空的，
	// 而中间没有任何报错。
	pl.clearWorkspaceAssignment(id)
	return &WorkspaceDelete{ID: id, ReleasedSessions: released}, nil
}

// MoveRegisteredWorkspace 重排（DOM insertBefore 语义），返回重排后的完整顺序。
func (pl *Plane) MoveRegisteredWorkspace(id, beforeID string) (*WorkspaceMove, error) {
	if pl.st == nil {
		return nil, WorkspaceReadOnlyError()
	}
	ctx := context.Background()
	seq, err := pl.st.MoveWorkspace(ctx, id, beforeID)
	switch {
	case pl.st.IsNotFound(err):
		return nil, WorkspaceNotFoundError(id)
	case err != nil:
		return nil, err
	}
	return &WorkspaceMove{IDs: seq}, nil
}

// EnsureDefaultWorkspace 装配期建立默认工作空间（幂等；代建关闭时什么也不做）。
//
// 它由装配根在**配置装载之后**调用，而不是由迁移调用——迁移发生在 `store.Open` 内部，
// 那时拿不到 `workspace_dir`（见 store/migrate_v14.go 的说明）。sqlite 模式下只在登记表为空时
// 创建：删掉默认工作空间之后不再自动重建（设计 ADR-4）。
//
// **两种持久层模式都要有它**（2026-10-11 修）：默认工作空间是"未指定 cwd 时落在哪"的唯一落点，
// 与"用户的登记表能不能持久化"是两件事。memory 模式下缺了它的表现是
// `POST /sessions {"agent_id":"dsh","prompt":"hi"}` 直接 40001 `workspace_required`
// ——最普通的一条路走不通，而用户什么都没做错。所以 memory 模式也在**装配期**建目录 +
// 铸一个进程内的登记项（每次启动一个新 id：它本来就不跨进程存活）。
func (pl *Plane) EnsureDefaultWorkspace() error {
	dir := pl.WorkspaceDir()
	if dir == "" {
		// 显式关闭代建（`workspace_dir` 为空/off）：该类部署**要求**显式 cwd，这是配置意图。
		return nil
	}
	provision := DefaultWorkspaceDir(dir)
	if pl.st == nil {
		return pl.provisionMemoryDefaultWorkspace(provision)
	}
	if _, err := pl.st.BootstrapDefaultWorkspace(context.Background(), provision); err != nil {
		return code.FromError(err)
	}
	return nil
}

// provisionMemoryDefaultWorkspace 在 memory 模式建出默认工作空间（目录 + 进程内登记项）。
func (pl *Plane) provisionMemoryDefaultWorkspace(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return code.FromError(acl.NewError(acl.ClassService, "workspace_provision_failed",
			fmt.Sprintf("建立默认工作空间目录失败 %s: %v", dir, err), nil))
	}
	canonical := dir
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		canonical = real
	}
	now := time.Now().UTC()
	row := &model.WorkspaceEntity{
		// 每次启动一个新 id：memory 模式的进程内登记项**本来就不跨进程存活**，
		// 与 store 侧的 `BootstrapDefaultWorkspace` 用同一个铸造口径（`wsp_<ULID>`）。
		ID:        objid.New(objid.Workspace),
		Path:      canonical,
		Title:     model.DefaultWorkspaceTitle,
		IsDefault: true,
		ListOrder: 0,
		CreatedAt: now,
		UpdatedAt: now,
	}
	pl.mu.Lock()
	pl.memDefaultWorkspace = row
	pl.mu.Unlock()
	return nil
}

// workspaceRespForMemDefault 把 memory 模式的默认工作空间投影成列表项。
//
// 计数与最近使用从**内存会话**现算（与 sqlite 分支逐字同一口径）：归到默认工作空间的历史会话
// （`workspace_id = ”`）也算在它头上——否则界面上它会永远显示 0 个会话，而点进去却有。
//
// **不持 `s.mu`**（P2b 前置）：循环体只读快照与 `def`，不碰任何 `s.mu` 保护的字段。在这里持锁
// 请求会话快照会把"workspace 锁 → session 锁"这条边固化下来，等 workspace 状态搬进域平面
// 就与 `workspaceDisplayOf` 的反向回调合成**锁序反转**——表现是挂死，不是红（见
// `workspace_view.go` 的 `workspaceUsage` 同一条注释）。
func (pl *Plane) workspaceRespForMemDefault(def model.WorkspaceEntity) WorkspaceItem {
	resp := WorkspaceItem{
		ID:        def.ID,
		Path:      def.Path,
		Title:     def.Title,
		IsDefault: true,
		ListOrder: def.ListOrder,
		DirStatus: dirStatusOf(def.Path),
		CreatedAt: def.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: def.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	for _, rec := range pl.sessionViews() {
		if rec.WorkspaceID == def.ID || rec.WorkspaceID == "" {
			resp.Sessions++
			if at := rec.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"); at > resp.LastUsedAt {
				resp.LastUsedAt = at
			}
		}
	}
	return resp
}

// DefaultWorkspace 返回默认工作空间（不存在 → ok=false）。会话创建路径用它定 cwd。
//
// memory 模式返回装配期铸的那一条（见 EnsureDefaultWorkspace）——"没有库"与"没有默认工作空间"
// 是两件事，把它们混为一谈就是那条 40001 的成因。
func (pl *Plane) DefaultWorkspace() (model.WorkspaceEntity, bool, error) {
	if pl.st == nil {
		pl.mu.Lock()
		def := pl.memDefaultWorkspace
		pl.mu.Unlock()
		if def == nil {
			return model.WorkspaceEntity{}, false, nil
		}
		return *def, true, nil
	}
	return pl.st.DefaultWorkspace(context.Background())
}

// workspaceRespByID 取单条登记的展示投影（创建/改名后回读用）。
func (pl *Plane) workspaceRespByID(ctx context.Context, id string) (*WorkspaceItem, error) {
	list, err := pl.WorkspaceRegistry()
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].ID == id {
			item := list.Items[i]
			return &item, nil
		}
	}
	return nil, WorkspaceNotFoundError(id)
}

// dirStatusOf 现算目录现状（不缓存；见 WorkspaceResp.DirStatus）。
func dirStatusOf(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DirStatusMissing
		}
		return DirStatusUnreadable
	}
	if !st.IsDir() {
		return DirStatusNotDir
	}
	// 目录存在且是目录：再确认**读得动**（权限/挂载故障时 os.Stat 会成功而列出内容会失败，
	// 而用户在界面上看到"可用"却进不去，比看到"不可读"更糟）。
	f, err := os.Open(path)
	if err != nil {
		return DirStatusUnreadable
	}
	_ = f.Close()
	return DirStatusOK
}

// WorkspaceNotFoundError 未知工作空间 id（ClassRequest + 稳定 code 名，由 pkg/code 提档到 40401）。
func WorkspaceNotFoundError(id string) error {
	return code.FromError(acl.NewError(acl.ClassRequest, "workspace_not_found",
		fmt.Sprintf("未知工作空间 %q", id), nil))
}

// WorkspaceReadOnlyError 当前持久层不支持登记写面（memory → 40501）。
//
// 为什么是**明确不支持**而不是"内部错误"：memory 模式下没有 `workspaces` 表，这是形态的
// 确定限制（与 `/usage/summary` 在 memory 下 40501 完全同形），用户该做的是换持久层，
// 而不是看一个 50000 去猜平台坏没坏。
func WorkspaceReadOnlyError() error {
	// 文案里**显式带上稳定 code 名**（方括号形态）：`fromACL` 只透传 Details，而
	// `workspace_schema_read_only` 不在默认文案表里——不写进去，前端就只能靠 40501 猜是
	// "哪一种不支持"（memory 与"该底座不广告模型配置项"是同一个码）。
	const codeName = "workspace_schema_read_only"
	return code.FromError(acl.NewError(acl.ClassUnsupported, codeName,
		"["+codeName+"] 当前持久层为 memory：工作空间登记需要 sqlite（MATE_STORE=sqlite）", nil))
}

// ResolveTargetWorkspace 定出本次建会话的**解析目标**与**归属**。
//
//	reqWorkspaceID 非空 → 用户显式选中一个登记项（target 为空、assignment = 它的 id）
//	reqWorkspaceID 为空 → 默认工作空间（target = 它，assignment 由 ResolveWorkspace 一并给出）
//
// 两者的 `Temporary` 语义**不同**，这就是为什么 target 只在"未指定"时给：
//   - 默认工作空间 → Temporary=true（"这个目录不是你选的"，前端据此标注），
//   - 用户选中 → Temporary=false（他选的），
//
// 把显式选中也走 target 会把用户自己挑的目录标成"平台给的"。
//
// 为什么给了 workspace_id 就不用再看 req.Cwd 定目录：选择器选中一个登记项是更强的意图表达
// （用户点的是一个有名字、有顺序的实体），而 cwd 是给"直接用绝对路径"这条老路的。
// 两者同时给出且不一致时**拒绝**（见 CreateSession）：猜错的后果是会话跑在用户没打算用的
// 目录里（而那个目录正是 workspace-write 的沙箱边界）。
func (pl *Plane) ResolveTargetWorkspace(reqWorkspaceID string) (target *DefaultWorkspaceRef, assignment string, err error) {
	id := strings.TrimSpace(reqWorkspaceID)
	if id == "" {
		def, ok, derr := pl.DefaultWorkspace()
		if derr != nil {
			return nil, "", derr
		}
		if !ok {
			// 没有默认工作空间：由 ResolveWorkspace 按"未指定 + 无落点"报 workspace_required。
			return nil, "", nil
		}
		return &DefaultWorkspaceRef{ID: def.ID, Path: def.Path}, "", nil
	}
	if pl.st == nil {
		return nil, "", WorkspaceReadOnlyError()
	}
	row, ok, gerr := pl.st.GetWorkspace(context.Background(), id)
	if gerr != nil {
		return nil, "", gerr
	}
	if !ok {
		return nil, "", WorkspaceNotFoundError(id)
	}
	return nil, row.ID, nil
}

// clearWorkspaceAssignment 把归属指向 id 的内存会话记录清空（与 store 的库侧改动配对）。
//
// 空串 = 未登记 → 读面归默认工作空间（设计 ADR-6），所以清空即"退回默认分组"。
// 返回受影响的会话数（诊断/测试用）。
func (pl *Plane) clearWorkspaceAssignment(workspaceID string) int {
	if workspaceID == "" || pl.sessions == nil {
		return 0
	}
	// **经端口而不是自己改会话**（P2b）：本域不拥有会话（同层零 import），而要改的语义
	// （"只清归属、不动 cwd"）由会话侧定义——它用 `MutateAll`（会话域**自己取锁**），
	// 而不是"先拿快照再逐条改副本"（后者改的是副本：库里清了、内存还挂着）。
	return pl.sessions.ClearWorkspaceAssignment(workspaceID)
}

// WorkspaceIDByPath 按规范化路径查登记 id（查不到 → 空串 = 归属留空，读面归默认工作空间）。
//
// **只查不建**：显式 cwd 落在一个没登记过的目录上是完全正常的用法（老接口的既有用法），
// 顺手替用户登记会在左栏凭空多出条目——那正是设计 §3.2 明确否掉的做法。
func (pl *Plane) WorkspaceIDByPath(ctx context.Context, path string) string {
	if pl.st == nil || path == "" {
		return ""
	}
	row, ok, err := pl.st.WorkspaceByPath(ctx, path)
	if err != nil || !ok {
		return ""
	}
	return row.ID
}

// WorkspaceDisplayOf 返回一条会话归属的工作空间 id（内存镜像；空串 = 未登记/归默认工作空间）。
//
// 读面用它把 workspace_id 与 workspace_title 一起下发（设计 §4.3）：前端拿 cwd 字符串
// 去比登记表会因规范化差异匹配不上，出现"会话明明在该目录下却不在分组里"；
// 服务端在建会话那一刻就定死归属，读面直出即可。
func (pl *Plane) WorkspaceDisplayOf(workspaceID string) (string, string) {
	id := strings.TrimSpace(workspaceID)

	// memory 模式**必须在这里分流**：下面第一段会直接调 `pl.st.GetWorkspace`，而 memory 模式的
	// `pl.st` 是 nil——2026-10-11 给 memory 模式补上默认工作空间之后，这条路上的 `id` 不再是空串，
	// 于是它当场变成一次空指针解引用（测试抓到的就是这个）。
	if pl.st == nil {
		if def, ok, err := pl.DefaultWorkspace(); ok && err == nil {
			return def.ID, defaultWorkspaceDisplayTitle(def.Title)
		}
		return id, ""
	}

	if id != "" {
		row, ok, err := pl.st.GetWorkspace(context.Background(), id)
		if err == nil && ok {
			return row.ID, defaultWorkspaceDisplayTitle(row.Title)
		}
		// 登记被删了（会话的 workspace_id 本应由 store 置空，这里兜住内存镜像的滞后窗口）：
		// 落到下面的"未登记"分支，读面归默认工作空间——绝不返回一个指向不存在对象的分组。
	}
	// `id == ""`（历史行 / 显式 cwd 未登记）或上面查不到：读面归默认工作空间。
	//
	// **只解析出 id 时也必须返回它**——前端按它渲染分组，返回空串会让那些会话在界面上
	// 消失（没有分组可挂）。所以这里宁可回一个"只有 id"的结果（title 由前端回落渲染）。
	if def, ok, err := pl.st.DefaultWorkspace(context.Background()); err == nil && ok {
		return def.ID, defaultWorkspaceDisplayTitle(def.Title)
	}
	return id, ""
}

// defaultWorkspaceDisplayTitle 把库里的标题转成**展示名**。
//
// 库里默认工作空间的标题是语言中立的 `default-workspace`（跨语言切换时磁盘与库都不变），
// 展示时读作「默认工作空间」——与 dsh 的 workspaceDisplayTitle 同一手法。
// 用户手动改名过的（标题不再是那个串）原样透出。
func defaultWorkspaceDisplayTitle(title string) string {
	if title == model.DefaultWorkspaceTitle {
		return "默认工作空间"
	}
	return title
}
