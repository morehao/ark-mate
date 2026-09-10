package manager

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/ark-forge/pkg/acl"
)

// 工作空间的**只读辅助面**（浏览 + 最近使用）。
//
// 存在的理由：Web 面板没有原生目录选择器，用户需要一个"看到目录树再选"的入口；
// 而工作空间是可复用的稳定身份（同一目录下可以有很多会话），所以"最近用过哪些目录"
// 应当由既有会话派生，而不是另存一份状态（goose 维护 recent-dirs.json 是因为它没有
// 会话↔目录的持久关联；ark-forge 有 conversations.cwd，派生即真相）。

// BrowseWorkspace 列出某绝对路径下的子目录（只读）。
//
// path 为空 → 从允许的工作区根（未配置白名单时退化为服务进程家目录）起步；
// includeHidden=false → 跳过 "." 开头的目录。
// 返回的 Path/Parent 与建会话时的 cwd 同形态（ValidateWorkspace 规范化），
// 前端把 entries[i].path 直接当 cwd 提交即可命中同一条会话分组。
//
// 配了 FORGE_WORKSPACE_ROOTS 后这个接口**只有浏览窗口变窄，没有语义变化**：
//   - path 越界 → workspace_outside_roots（与建会话同一条校验、同一个错）；
//   - Parent 只到根为止（到根时留空）——否则"上级"按钮就是白名单的绕过入口；
//   - home 不在白名单内时干脆不返回（前端据此隐藏"主目录"按钮，而不是给一个必然报错的入口）。
func (s *Service) BrowseWorkspace(path string, includeHidden bool) (*handler.WorkspaceBrowseResp, error) {
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
	dir, err := ValidateWorkspace(path, roots)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"目录不可读: "+dir, err)
	}
	out := make([]handler.WorkspaceEntryResp, 0, len(entries))
	for _, e := range entries {
		// 只列目录（含符号链接指向的目录：DirEntry.IsDir 为 false 时再 Stat 一次消歧）。
		if !isDirEntry(e, filepath.Join(dir, e.Name())) {
			continue
		}
		if !includeHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		// 白名单内的符号链接子项若真身落在根外（/data/ws/link -> /etc），列出来就是死路一条：
		// 用户点进去必被拒。不列它。
		if len(roots) > 0 && !withinRoots(resolveRealPath(child), roots) {
			continue
		}
		out = append(out, handler.WorkspaceEntryResp{Name: e.Name(), Path: child})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	resp := &handler.WorkspaceBrowseResp{Path: dir, Entries: out, Roots: roots}
	if home != "" && withinRoots(home, roots) {
		resp.Home = home
	}
	// 上级目录：到根为止（根目录的 Dir 等于自身 → 留空，前端据此禁用"上级"）；
	// 白名单下同样到根为止——越过根向上等于把用户带出允许范围。
	if parent := filepath.Dir(dir); parent != dir && withinRoots(parent, roots) {
		resp.Parent = parent
	}
	return resp, nil
}

// WorkspaceLibraries 返回**资料库**列表：工作区根下的一级子目录。
//
// 为什么是"根的一级子目录"而不是一张登记表：配好 FORGE_WORKSPACE_ROOTS 之后，运维挂进来的
// 每个一级目录本来就是一堆资料的集合（/srv/docs/合同归档、/srv/docs/技术资料）。于是这一层
// **零新状态**——不引入 projects.json 之类的登记表，也就没有"登记项与磁盘不一致"这类要维护的
// 脏数据；目录名即资料库名，用户不必面对绝对路径（资料/文档场景的主要痛点）。
//
// 未配 roots 时返回空列表：资料库是"策展"能力，前提是先有人把边界画出来（否则只能靠浏览）。
// 每个资料库带上该目录下的会话数与最近使用时间（与「最近目录」共用 workspaceUsage 一份口径），
// 让"我在这儿聊过没"不必再点进去看。
//
// 平台代建根**及其下的一切**都不算资料库（见循环里的注释）：代建根本身恒在生效名单里，
// 它下面又全是按会话 id 命名的沙箱目录，两者都是平台实现细节。反过来说，若运维只把
// 代建根配成白名单，资料库列表就是空的——这是有意的：那一层从来不是"策展内容"。
func (s *Service) WorkspaceLibraries() ([]handler.WorkspaceLibraryResp, error) {
	roots := s.WorkspaceRoots()
	usage := s.workspaceUsage(roots)
	provision := ""
	if d := s.WorkspaceDir(); d != "" {
		provision = resolveRealPath(d)
	}

	out := make([]handler.WorkspaceLibraryResp, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			// 某个根暂时不可读（挂载掉了/权限变了）不该让整个列表失败：其余根照常返回，
			// 平台在这里的选择是"少显示一个"而不是"整页报错"。
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := filepath.Join(root, e.Name())
			if !isDirEntry(e, child) {
				continue
			}
			real := resolveRealPath(child)
			// 根内指向根外的符号链接不是资料库：点进去必被拒（与 BrowseWorkspace 同一判据）。
			if !withinRoots(real, roots) {
				continue
			}
			// 平台自己的代建根与其**下**的一级子目录都要排除，且必须按真身判定：
			//   - 代建根自身：它现在恒在生效名单里（隐式并入），会作为某个根的一级子目录出现，
			//     用户看到的第一个资料库会叫 "tmp"——那是平台的实现细节，不是他的资料；
			//   - 代建根之下：那是**按会话 id 命名的沙箱目录**（ses_xxx），代建根自己成为根之后
			//     它的第一层子目录就会被扫到，若不排除，资料库列表里会冒出一串平台内部命名。
			if provision != "" && withinRoots(real, []string{provision}) {
				continue
			}
			if seen[child] {
				continue // 根的嵌套配置可能让同一个目录出现两次
			}
			seen[child] = true
			item := handler.WorkspaceLibraryResp{Name: e.Name(), Path: child, Root: root}
			if u, ok := usage[child]; ok {
				item.Conversations = u.count
				item.LastUsedAt = u.latest
			}
			out = append(out, item)
		}
	}
	// 按名字排序：资料库是一份策展列表，稳定顺序比"最近"更可预期（最近有用次数/时间可读）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// ListWorkspaceFiles 列出目录**内容**（文件 + 子目录，带大小与修改时间）——只读的"看资料"面。
//
// 与 BrowseWorkspace 的分工必须说清，否则两个接口会被当成重复：
//   - BrowseWorkspace = **选工作空间**：只列目录，entries[].path 可直接当 cwd 提交（契约成立）；
//   - ListWorkspaceFiles = **看内容**：列文件，条目**不可**当 cwd（文件不是合法工作空间）。
//
// 把两者合成一个接口会破坏上面那条"entries[].path 可直接提交"的契约，所以宁可多一个只读接口。
//
// 边界与浏览面完全一致（ValidateWorkspace + withinRoots）：越界目录拒绝、根外真身的符号链接不列。
func (s *Service) ListWorkspaceFiles(path string, includeHidden bool) (*handler.WorkspaceFilesResp, error) {
	roots := s.WorkspaceRoots()
	if strings.TrimSpace(path) == "" {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
			"未指定 path：列目录内容必须给出要看的目录", nil)
	}
	dir, err := ValidateWorkspace(path, roots)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, acl.NewError(acl.ClassRequest, "workspace_invalid", "目录不可读: "+dir, err)
	}
	out := make([]handler.WorkspaceFileEntryResp, 0, len(entries))
	for _, e := range entries {
		if !includeHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if len(roots) > 0 && !withinRoots(resolveRealPath(child), roots) {
			continue
		}
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
		out = append(out, handler.WorkspaceFileEntryResp{
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
	return &handler.WorkspaceFilesResp{Path: dir, Roots: roots, Entries: out}, nil
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
// 排除两类，依据都是会话上的**权威字段**而不是路径形状：
//   - 平台**代建**的临时工作空间（WorkspaceTemporary）：某会话独占的一次性目录，复用无意义；
//   - 白名单外的工作空间：允许列表是后配的，旧会话的 cwd 可能落在外面。旧会话仍可继续（cwd 不可变，
//     平台不回头否决已建会话），但**不该**把它当成"可再次选用"的候选推给用户——点了必被拒。
func (s *Service) workspaceUsage(roots []string) map[string]*wsUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	byPath := make(map[string]*wsUsage, len(s.conversations))
	for _, r := range s.conversations {
		if r.Cwd == "" || r.WorkspaceTemporary {
			continue
		}
		if !withinRoots(r.Cwd, roots) {
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
func (s *Service) RecentWorkspaces(limit int) ([]handler.WorkspaceRecentResp, error) {
	if limit <= 0 {
		limit = 10
	}
	roots := s.WorkspaceRoots()
	byPath := s.workspaceUsage(roots)

	out := make([]handler.WorkspaceRecentResp, 0, len(byPath))
	for path, a := range byPath {
		out = append(out, handler.WorkspaceRecentResp{
			Path:          path,
			Name:          filepath.Base(path),
			LastUsedAt:    a.latest,
			Conversations: a.count,
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
