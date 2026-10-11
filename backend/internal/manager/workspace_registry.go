package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
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
//   - 归属只由 `conversations.workspace_id` 表达，这里不维护第二份成员账；
//   - 删登记**只删登记**：目录、文件、会话历史一概不动，那些会话读面归默认工作空间。

// WorkspaceRegistry 列出全部工作空间登记（按展示顺序）。
//
// 目录状态、会话数、最近使用都是**读时现算**：登记表只存身份、标题与顺序（设计 ADR-1），
// 其余是事实的投影——存一份就多一份会与事实漂移的副本。
func (s *Service) WorkspaceRegistry() (*dtoworkspace.WorkspaceListResp, error) {
	if s.st == nil {
		// memory 模式：登记表不存在。返回**空列表 + 不可写**，不是错误——
		// 前端据 writable=false 不渲染创建入口（设计 ADR-11），而"没有登记"本身是合法状态。
		return &dtoworkspace.WorkspaceListResp{
			Items:    []dtoworkspace.WorkspaceResp{},
			Writable: false,
		}, nil
	}
	ctx := context.Background()
	rows, err := s.st.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.st.CountWorkspacesByID(ctx)
	if err != nil {
		return nil, err
	}
	lastUsed, err := s.st.LastUsedByWorkspaceID(ctx)
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
	out := &dtoworkspace.WorkspaceListResp{
		Items:        make([]dtoworkspace.WorkspaceResp, 0, len(rows)),
		DefaultID:    defaultID,
		Writable:     true,
		ProvisionDir: s.WorkspaceDir(),
	}
	for _, r := range rows {
		resp := dtoworkspace.WorkspaceResp{
			ID:        r.ID,
			Path:      r.Path,
			Title:     r.Title,
			IsDefault: r.IsDefault,
			ListOrder: r.ListOrder,
			DirStatus: dirStatusOf(r.Path),
			CreatedAt: r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: r.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		resp.Conversations = int(counts[r.ID])
		resp.LastUsedAt = lastUsed[r.ID]
		if r.IsDefault {
			// 归到默认工作空间的历史会话（workspace_id = ''）。
			resp.Conversations += int(counts[""])
			if v := lastUsed[""]; v > resp.LastUsedAt {
				resp.LastUsedAt = v
			}
		}
		out.Items = append(out.Items, resp)
	}
	return out, nil
}

// CreateRegisteredWorkspace 登记一个工作空间（**get-or-create**；strict=true 时重复即报错）。
//
// 路径走 `ValidateWorkspace(spec, roots)`：与建会话**同一条校验**。这不是"顺手复用"，
// 而是本条设计的一半——两处各写一套，就必然出现"这里能选、那里建不了"的死路。
func (s *Service) CreateRegisteredWorkspace(path, title string, strict bool) (*dtoworkspace.WorkspaceResp, error) {
	if s.st == nil {
		return nil, workspaceReadOnlyError()
	}
	canonical, err := ValidateWorkspace(path, s.WorkspaceRoots())
	if err != nil {
		return nil, code.FromError(err)
	}
	ctx := context.Background()
	if strict {
		if _, ok, err := s.st.WorkspaceByPath(ctx, canonical); err != nil {
			return nil, err
		} else if ok {
			return nil, code.FromError(acl.NewError(acl.ClassLimit, "workspace_exists",
				fmt.Sprintf("该目录已登记为工作空间: %s", canonical), nil))
		}
	}
	row, err := s.st.CreateWorkspace(ctx, canonical, title)
	if err != nil {
		return nil, err
	}
	resp, err := s.workspaceRespByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// RenameRegisteredWorkspace 改写展示名。**不改身份**：id 与 path 都不动。
func (s *Service) RenameRegisteredWorkspace(id, title string) (*dtoworkspace.WorkspaceResp, error) {
	if s.st == nil {
		return nil, workspaceReadOnlyError()
	}
	ctx := context.Background()
	if _, ok, err := s.st.GetWorkspace(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, workspaceNotFoundError(id)
	}
	if err := s.st.RenameWorkspace(ctx, id, title); err != nil {
		return nil, err
	}
	return s.workspaceRespByID(ctx, id)
}

// DeleteRegisteredWorkspace 删除一条登记（**不删会话、不删目录**，设计 ADR-6）。
//
// 默认工作空间不可删：它是"建会话未指定 cwd"这条路的落点，删掉等于让那条路无路可走。
// 这条判定在 store 层也有一道（任何调用方都删不出一个没有落点的库），这里只是把它翻成
// 一个用户能读懂的错。
func (s *Service) DeleteRegisteredWorkspace(id string) (*dtoworkspace.WorkspaceDeleteResp, error) {
	if s.st == nil {
		return nil, workspaceReadOnlyError()
	}
	ctx := context.Background()
	released, err := s.st.DeleteWorkspace(ctx, id)
	switch {
	case errors.Is(err, store.ErrWorkspaceNotFound):
		return nil, workspaceNotFoundError(id)
	case errors.Is(err, store.ErrWorkspaceDefaultImmutable):
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "workspace_default_immutable",
			"默认工作空间不可删除：它是未指定工作目录的会话的落点", nil))
	case err != nil:
		return nil, err
	}
	// **同步内存镜像**：列表读的是 `s.conversations` 的内存记录，而 store 只改了库里的列。
	// 不同步的后果是实测出来的——删完登记立刻按默认工作空间过滤，那几个会话**看不到**
	// （内存里还挂着已经不存在的 id），要等重启才回来。库是对的、界面是空的，
	// 而中间没有任何报错。
	s.clearWorkspaceAssignment(id)
	return &dtoworkspace.WorkspaceDeleteResp{ID: id, ReleasedSessions: released}, nil
}

// MoveRegisteredWorkspace 重排（DOM insertBefore 语义），返回重排后的完整顺序。
func (s *Service) MoveRegisteredWorkspace(id, beforeID string) (*dtoworkspace.WorkspaceMoveResp, error) {
	if s.st == nil {
		return nil, workspaceReadOnlyError()
	}
	ctx := context.Background()
	seq, err := s.st.MoveWorkspace(ctx, id, beforeID)
	switch {
	case errors.Is(err, store.ErrWorkspaceNotFound):
		return nil, workspaceNotFoundError(id)
	case err != nil:
		return nil, err
	}
	return &dtoworkspace.WorkspaceMoveResp{IDs: seq}, nil
}

// EnsureDefaultWorkspace 装配期建立默认工作空间（幂等；代建关闭时什么也不做）。
//
// 它由装配根在**配置装载之后**调用，而不是由迁移调用——迁移发生在 `store.Open` 内部，
// 那时拿不到 `workspace_dir`（见 store/migrate_v14.go 的说明）。只在登记表为空时创建：
// 删掉默认工作空间之后不再自动重建（设计 ADR-4）。
func (s *Service) EnsureDefaultWorkspace() error {
	if s.st == nil {
		return nil
	}
	dir := s.WorkspaceDir()
	if dir == "" {
		return nil
	}
	if _, err := s.st.BootstrapDefaultWorkspace(context.Background(),
		store.WorkspaceProvision{Dir: defaultWorkspaceDir(dir)}); err != nil {
		return code.FromError(err)
	}
	return nil
}

// DefaultWorkspace 返回默认工作空间（不存在 → ok=false）。会话创建路径用它定 cwd。
func (s *Service) DefaultWorkspace() (store.Workspace, bool, error) {
	if s.st == nil {
		return store.Workspace{}, false, nil
	}
	return s.st.DefaultWorkspace(context.Background())
}

// workspaceRespByID 取单条登记的展示投影（创建/改名后回读用）。
func (s *Service) workspaceRespByID(ctx context.Context, id string) (*dtoworkspace.WorkspaceResp, error) {
	list, err := s.WorkspaceRegistry()
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].ID == id {
			item := list.Items[i]
			return &item, nil
		}
	}
	return nil, workspaceNotFoundError(id)
}

// dirStatusOf 现算目录现状（不缓存；见 WorkspaceResp.DirStatus）。
func dirStatusOf(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dtoworkspace.DirStatusMissing
		}
		return dtoworkspace.DirStatusUnreadable
	}
	if !st.IsDir() {
		return dtoworkspace.DirStatusNotDir
	}
	// 目录存在且是目录：再确认**读得动**（权限/挂载故障时 os.Stat 会成功而列出内容会失败，
	// 而用户在界面上看到"可用"却进不去，比看到"不可读"更糟）。
	f, err := os.Open(path)
	if err != nil {
		return dtoworkspace.DirStatusUnreadable
	}
	_ = f.Close()
	return dtoworkspace.DirStatusOK
}

// workspaceNotFoundError 未知工作空间 id（ClassRequest + 稳定 code 名，由 pkg/code 提档到 40401）。
func workspaceNotFoundError(id string) error {
	return code.FromError(acl.NewError(acl.ClassRequest, "workspace_not_found",
		fmt.Sprintf("未知工作空间 %q", id), nil))
}

// workspaceReadOnlyError 当前持久层不支持登记写面（memory → 40501）。
//
// 为什么是**明确不支持**而不是"内部错误"：memory 模式下没有 `workspaces` 表，这是形态的
// 确定限制（与 `/usage/summary` 在 memory 下 40501 完全同形），用户该做的是换持久层，
// 而不是看一个 50000 去猜平台坏没坏。
func workspaceReadOnlyError() error {
	// 文案里**显式带上稳定 code 名**（方括号形态）：`fromACL` 只透传 Details，而
	// `workspace_schema_read_only` 不在默认文案表里——不写进去，前端就只能靠 40501 猜是
	// "哪一种不支持"（memory 与"该底座不广告模型配置项"是同一个码）。
	const codeName = "workspace_schema_read_only"
	return code.FromError(acl.NewError(acl.ClassUnsupported, codeName,
		"["+codeName+"] 当前持久层为 memory：工作空间登记需要 sqlite（MATE_STORE=sqlite）", nil))
}

// resolveTargetWorkspace 定出本次建会话的**解析目标**与**归属**。
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
// 两者同时给出且不一致时**拒绝**（见 CreateConversation）：猜错的后果是会话跑在用户没打算用的
// 目录里（而那个目录正是 workspace-write 的沙箱边界）。
func (s *Service) resolveTargetWorkspace(reqWorkspaceID string) (target *DefaultWorkspaceRef, assignment string, err error) {
	id := strings.TrimSpace(reqWorkspaceID)
	if id == "" {
		def, ok, derr := s.DefaultWorkspace()
		if derr != nil {
			return nil, "", derr
		}
		if !ok {
			// 没有默认工作空间：由 ResolveWorkspace 按"未指定 + 无落点"报 workspace_required。
			return nil, "", nil
		}
		return &DefaultWorkspaceRef{ID: def.ID, Path: def.Path}, "", nil
	}
	if s.st == nil {
		return nil, "", workspaceReadOnlyError()
	}
	row, ok, gerr := s.st.GetWorkspace(context.Background(), id)
	if gerr != nil {
		return nil, "", gerr
	}
	if !ok {
		return nil, "", workspaceNotFoundError(id)
	}
	return nil, row.ID, nil
}

// clearWorkspaceAssignment 把归属指向 id 的内存会话记录清空（与 store 的库侧改动配对）。
//
// 空串 = 未登记 → 读面归默认工作空间（设计 ADR-6），所以清空即"退回默认分组"。
// 返回受影响的会话数（诊断/测试用）。
func (s *Service) clearWorkspaceAssignment(workspaceID string) int {
	if workspaceID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, rec := range s.conversations {
		if rec.WorkspaceID == workspaceID {
			rec.WorkspaceID = ""
			n++
		}
	}
	return n
}

// workspaceIDByPath 按规范化路径查登记 id（查不到 → 空串 = 归属留空，读面归默认工作空间）。
//
// **只查不建**：显式 cwd 落在一个没登记过的目录上是完全正常的用法（老接口的既有用法），
// 顺手替用户登记会在左栏凭空多出条目——那正是设计 §3.2 明确否掉的做法。
func (s *Service) workspaceIDByPath(ctx context.Context, path string) string {
	if s.st == nil || path == "" {
		return ""
	}
	row, ok, err := s.st.WorkspaceByPath(ctx, path)
	if err != nil || !ok {
		return ""
	}
	return row.ID
}

// WorkspaceIDOf 返回一条会话归属的工作空间 id（内存镜像；空串 = 未登记/归默认工作空间）。
//
// 读面用它把 workspace_id 与 workspace_title 一起下发（设计 §4.3）：前端拿 cwd 字符串
// 去比登记表会因规范化差异匹配不上，出现"会话明明在该目录下却不在分组里"；
// 服务端在建会话那一刻就定死归属，读面直出即可。
func (s *Service) workspaceDisplayOf(workspaceID string) (string, string) {
	id := strings.TrimSpace(workspaceID)
	if id != "" {
		row, ok, err := s.st.GetWorkspace(context.Background(), id)
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
	if s.st == nil {
		return id, ""
	}
	if def, ok, err := s.st.DefaultWorkspace(context.Background()); err == nil && ok {
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
	if title == store.DefaultWorkspaceTitle {
		return "默认工作空间"
	}
	return title
}
