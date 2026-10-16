package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/dto/dtoworkspace"
	"github.com/morehao/ark-mate/pkg/acl"
)

// 工作空间的**只读辅助面**（浏览 + 最近使用）。
//
// 存在的理由：Web 面板没有原生目录选择器，用户需要一个"看到目录树再选"的入口；
// 而工作空间是可复用的稳定身份（同一目录下可以有很多会话），所以"最近用过哪些目录"
// 应当由既有会话派生，而不是另存一份状态（goose 维护 recent-dirs.json 是因为它没有
// 会话↔目录的持久关联；ark-mate 有 sessions.cwd，派生即真相）。

// browseEntryLimit 单层下发的子目录上限。
//
// 为什么要有上限：一个十万子目录的目录（下载目录、node_modules 的父级）会让 os.ReadDir 的结果、
// JSON 响应与面板列表一起变慢，而用户真正要选的那几个目录在名字序的前部。截断时把 Truncated 置位，
// 界面明说"还有未显示的子目录"——静默少几行是最坏的一种（用户以为目录就这些）。
const browseEntryLimit = 1000

// BrowseWorkspace 列出某绝对路径下的子目录（只读）。**全系统**——没有"可浏览范围"这回事。
//
// path 为空 → 从**起始点偏好**（`MATE_WORKSPACE_ROOTS` 的第一项）起步，没有偏好则从服务进程家目录起步；
// includeHidden=false → 跳过 "." 开头的目录（下发的行带 Hidden 标记，供前端自己做开关）。
// 返回的 Path/Parent 与建会话时的 cwd 同形态（ValidateWorkspace 规范化），
// 前端把 entries[i].path 直接当 cwd 提交即可命中同一条会话分组。
//
// **v14.5：白名单从"可浏览范围"降级为"起始点偏好"**，因此这个接口不再有边界语义：
//   - 任何已存在的目录都能浏览（越界不再是错——原来那条 workspace_outside_roots 已随门一起删除）；
//   - Parent 一直给到文件系统根（盘符/根目录的 Dir 等于自身时才为空，前端据此禁用"上级"）；
//   - Home 恒下发（它就是"主目录"按钮的目标）；
//   - Crumbs 给**完整祖先链**（从文件系统根到当前目录），不再按名录裁剪。
//
// `Roots` 仍然下发，但语义变了：它是面板用来把偏好起点列成快捷入口的，不是"允许范围"。
func (s *Service) BrowseWorkspace(path string, includeHidden bool) (*dtoworkspace.WorkspaceBrowseResp, error) {
	roots := s.WorkspaceRoots()
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(h); rerr == nil {
			home = resolved
		} else {
			home = h
		}
	}
	if strings.TrimSpace(path) == "" {
		// 起步点：有白名单就从第一个根起步（那是"允许工作的目录"，也是运维期望用户看到的入口）。
		if len(roots) > 0 {
			path = roots[0]
		} else if home != "" {
			path = home
		} else {
			return nil, acl.NewError(acl.ClassService, "workspace_unavailable",
				"未指定 path 且无法定位服务进程家目录", nil)
		}
	}
	// 复用建会话同一条校验（绝对 / 存在 / 是目录 + 符号链接规范化 + 根白名单）：
	// 浏览与创建不许有两套语义，否则会出现"浏览得到、创建被拒"的死路。
	dir, err := ValidateWorkspace(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"目录不可读: "+dir, err)
	}
	out := make([]dtoworkspace.WorkspaceEntryResp, 0, len(entries))
	truncated := false
	for _, e := range entries {
		// 只列目录（含符号链接指向的目录：DirEntry.IsDir 为 false 时再 Stat 一次消歧）。
		if !isDirEntry(e, filepath.Join(dir, e.Name())) {
			continue
		}
		hidden := strings.HasPrefix(e.Name(), ".")
		if !includeHidden && hidden {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if len(out) >= browseEntryLimit {
			truncated = true
			break
		}
		out = append(out, dtoworkspace.WorkspaceEntryResp{Name: e.Name(), Path: child, Hidden: hidden})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	resp := &dtoworkspace.WorkspaceBrowseResp{
		Path:      dir,
		Entries:   out,
		Roots:     roots,
		Crumbs:    ancestryCrumbs(dir),
		Truncated: truncated,
	}
	if home != "" {
		resp.Home = home
	}
	// 上级目录：到文件系统根为止（根目录的 Dir 等于自身 → 留空，前端据此禁用"上级"）。
	// 全系统口径下这里不再有"不许越过某个根"的判定。
	if parent := filepath.Dir(dir); parent != dir {
		resp.Parent = parent
	}
	return resp, nil
}

// CreateWorkspaceDir 在某个**已存在**的目录下建一个子目录（`POST /workspaces/dirs`）。
//
// 为什么需要它：目录浏览面（对齐 dsh 的 DirectoryBrowser）自带「新建文件夹」——用户在选目录的
// 那一刻就要能建出"新项目"那个目录，而不是先去别的工具建好再回来选。前端删掉手输路径那条路之后，
// **这是建新目录的唯一入口**。
//
// 四条规则的顺序即实现顺序，每一条都不能省：
//  1. 父目录走**与建会话同一条** ValidateWorkspace（绝对 / 存在 / 是目录 + 符号链接规范化 + 根白名单）：
//     浏览面与写面共用一条判据，才不会出现"看得见但建不了"或"建在允许范围之外"；
//  2. 名字必须是**单段**且不是 "." / ".."：多级（"a/b"）与相对（".."）会把判定摊到每一段上，
//     而"父目录合法 + 结果就是父目录的子项"这一条判据对拼接串是推不出来的；
//  3. 不覆盖、不递归：同名目录已存在 → workspace_exists(40901)，让用户改名而不是被静默复用
//     （"我建的那个新目录"与"原来就有那个目录"是两件事，界面必须能分辨）。
//
// **v14.5：原来的第 4 条（建完再判真身是否在根内，越界则回滚）已删** —— 白名单不再是边界，
// 建目录的落点由用户自己选（与 dsh 的原生选择器同口径）。
func (s *Service) CreateWorkspaceDir(parent, name string) (*dtoworkspace.WorkspaceDirCreateResp, error) {
	dir, err := ValidateWorkspace(parent)
	if err != nil {
		return nil, err
	}
	// TrimSpace 只用来判定"是不是空的"，真正建的是用户原样给的名字：
	// 目录名首尾含空白是合法的（"我的 项目"），替用户裁掉等于建了另一个名字。
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid", "目录名不能为空", nil)
	}
	if strings.ContainsAny(name, `/\`) {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("目录名不能包含路径分隔符: %q", name), nil)
	}
	if name == "." || name == ".." {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("目录名不能是 %q", name), nil)
	}
	target := filepath.Join(dir, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, acl.NewError(acl.ClassLimit, "workspace_exists",
				fmt.Sprintf("目录已存在: %s", target), nil)
		}
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("创建目录失败: %s", target), err)
	}
	return &dtoworkspace.WorkspaceDirCreateResp{Path: workspace.ResolveRealPath(target)}, nil
}

// ancestryCrumbs 从**文件系统根**到 target（含自身）的祖先链，逐级可跳。
//
// 根那一项的 Name 用它自身的路径（POSIX 的 "/"、Windows 的 "C:\"）：根的 basename 是空串，
// 编一个名字只会让面包屑的第一格没有意义。每一段的 Path 都是**绝对路径**，前端不拼。
//
// 输入应当是已规范化的绝对路径（BrowseWorkspace 传的就是 ValidateWorkspace 的结果），
// 所以这里不再做符号链接解析——面包屑要与"当前目录"逐字一致，而不是另一个真身写法。
func ancestryCrumbs(target string) []dtoworkspace.WorkspaceCrumbResp {
	crumbs := make([]dtoworkspace.WorkspaceCrumbResp, 0, 8)
	for cur := target; ; {
		parent := filepath.Dir(cur)
		name := filepath.Base(cur)
		if parent == cur {
			name = cur // 根：basename 为空（POSIX "/" 的 Base 是 "/"，Windows "C:\" 的 Base 是 "\."），用路径当名字
		}
		crumbs = append(crumbs, dtoworkspace.WorkspaceCrumbResp{Name: name, Path: cur})
		if parent == cur {
			break
		}
		cur = parent
	}
	// 反转：从根到当前目录（面包屑的阅读方向）。
	for i, j := 0, len(crumbs)-1; i < j; i, j = i+1, j-1 {
		crumbs[i], crumbs[j] = crumbs[j], crumbs[i]
	}
	return crumbs
}

// ListWorkspaceFiles 列出目录**内容**（文件 + 子目录，带大小与修改时间）——只读的"看资料"面。
//
// 与 BrowseWorkspace 的分工必须说清，否则两个接口会被当成重复：
//   - BrowseWorkspace = **选工作空间**：只列目录，entries[].path 可直接当 cwd 提交（契约成立）；
//   - ListWorkspaceFiles = **看内容**：列文件，条目**不可**当 cwd（文件不是合法工作空间）。
//
// 把两者合成一个接口会破坏上面那条"entries[].path 可直接提交"的契约，所以宁可多一个只读接口。
//
// 边界与浏览面完全一致（只要"绝对 / 存在 / 是目录"）：**v14.5 起不再按名录过滤**——要看内容就能看。
func (s *Service) ListWorkspaceFiles(path string, includeHidden bool) (*dtoworkspace.WorkspaceFilesResp, error) {
	roots := s.WorkspaceRoots()
	if strings.TrimSpace(path) == "" {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"未指定 path：列目录内容必须给出要看的目录", nil)
	}
	dir, err := ValidateWorkspace(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid", "目录不可读: "+dir, err)
	}
	out := make([]dtoworkspace.WorkspaceFileEntryResp, 0, len(entries))
	for _, e := range entries {
		if !includeHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		// 跟随符号链接取真身的属性（-s 是用户要看的大小，不是链接本身那几个字节）。
		st, serr := os.Stat(child)
		if serr != nil {
			continue // 断链/无权限：跳过而不是整个列表失败
		}
		// 只列目录与常规文件：设备/管道/socket 既没有可展示的大小，也不是"资料"。
		isDir := st.IsDir()
		if !isDir && !st.Mode().IsRegular() {
			continue
		}
		out = append(out, dtoworkspace.WorkspaceFileEntryResp{
			Name:       e.Name(),
			Path:       child,
			IsDir:      isDir,
			Size:       st.Size(),
			ModifiedAt: st.ModTime().UTC().Format(time.RFC3339),
		})
	}
	// 目录在前、文件在后，各自按名排序（资料场景先看结构、再看文件）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return &dtoworkspace.WorkspaceFilesResp{Path: dir, Roots: roots, Entries: out}, nil
}

// isDirEntry 判断目录项是否为目录（符号链接再 Stat 一次消歧）。
func isDirEntry(e os.DirEntry, full string) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(full)
	return err == nil && st.IsDir()
}

// wsUsage 一个工作空间上的会话聚合（数量 + 最近使用时间）。
type wsUsage struct {
	last   time.Time
	count  int
	latest string // RFC3339；空 = 该目录下没有可用的时间戳
}

// workspaceUsage 按 cwd 聚合会话数与最近使用时间（"最近"与"资料库"共用这唯一一份口径）。
//
// 只排除一类：平台**代建**的临时工作空间（WorkspaceTemporary）——某会话独占的一次性目录，复用无意义。
// **v14.5 起不再排除"名录外"的目录**：名录已降级为起始点偏好，不再意味着"不可选用"，
// 所以任何用户真正在里面工作过的目录都可以作为候选推回来。
//
// **不持 `s.mu`**（P2b 前置）：这里只读会话快照，不碰任何 `s.mu` 保护的字段，而 s.mu 与
// 会话域自己的锁是两把锁——在这把锁下面去请求会话快照，等于把"workspace 锁 → session 锁"
// 这条边固化下来；等 workspace 状态搬进域平面（域锁下调 `Snapshot()`）就与
// `workspaceDisplayOf` 那条反向回调合成**锁序反转**，而它的表现是 `go test ./...` 挂死、不是红。
func (s *Service) workspaceUsage() map[string]*wsUsage {
	snap := s.sessionPlane.Snapshot()
	byPath := make(map[string]*wsUsage, len(snap))
	for i := range snap {
		r := &snap[i]
		if r.Cwd == "" || r.WorkspaceTemporary {
			continue
		}
		a, ok := byPath[r.Cwd]
		if !ok {
			a = &wsUsage{}
			byPath[r.Cwd] = a
		}
		a.count++
		if r.UpdatedAt.After(a.last) {
			a.last = r.UpdatedAt
			a.latest = r.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	return byPath
}

// RecentWorkspaces 返回最近使用过的工作空间（从既有会话派生，按最近使用倒序）。排除口径见 workspaceUsage。
//
// 排掉两类，否则用户会看到"已经是我的工作空间"的条目出现在"未登记"里：
//   - **默认工作空间**（平台给的，不是用户选的，没有"登记"这一步可做）；
//   - 任何**已登记**的路径（一次查登记表，而不是逐条比对）。
//
// v14.3 起面板已无消费者（选择面只剩登记项 + 「添加工作空间…」）；端点保留为可用的只读派生视图。
func (s *Service) RecentWorkspaces(limit int) ([]dtoworkspace.WorkspaceRecentResp, error) {
	if limit <= 0 {
		limit = 10
	}
	byPath := s.workspaceUsage()

	// 已登记路径 + 默认工作空间路径：后者通常也在登记表里，但"登记表为空 + 代建关闭"
	// 或"登记被删"的部署仍可能只剩默认工作空间，所以两个来源都要并。
	registered := map[string]bool{}
	if s.st != nil {
		ctx := context.Background()
		if rows, err := s.st.ListWorkspaces(ctx); err == nil {
			for _, r := range rows {
				registered[r.Path] = true
			}
		}
		if def, ok, err := s.st.DefaultWorkspace(ctx); err == nil && ok {
			registered[def.Path] = true
		}
	}

	out := make([]dtoworkspace.WorkspaceRecentResp, 0, len(byPath))
	for path, a := range byPath {
		if registered[path] {
			continue
		}
		out = append(out, dtoworkspace.WorkspaceRecentResp{
			Path:       path,
			Name:       filepath.Base(path),
			LastUsedAt: a.latest,
			Sessions:   a.count,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsedAt != out[j].LastUsedAt {
			return out[i].LastUsedAt > out[j].LastUsedAt
		}
		return out[i].Path < out[j].Path // 同一时刻（同秒/RFC3339 相同）时按路径定序，保证可复现
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
