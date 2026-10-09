package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-mate/pkg/acl"
)

// 工作空间（workspace）= 会话的 ACP cwd：一个**规范化后的绝对路径**。
//
// 它同时是 dsh 原生权限 `workspace-write` 的边界（见 native_permission.go），
// 因此默认值不得是家目录，也不得是 serve 进程目录——那等于把边界交给"平台自己站在哪"。
// 对外契约见 README「控制面接口摘要」，运维要点见 docs/deployment.md §2.2.1。
//
// 三个入口，全部收在同一条白名单判定内（多入口共用一条校验，避免"创建能过、浏览不能过"这类裂缝）：
//   - ValidateWorkspace：客户端显式指定 → 绝对 + 存在 + 是目录 + EvalSymlinks 规范化 + 根白名单；
//   - ResolveWorkspace：未指定 → 平台在 workspaceDir 下按会话 id 代建临时工作空间；
//   - BrowseWorkspace（workspace_view.go）：只读目录枚举，复用 ValidateWorkspace。
//
// 「临时」必须由调用方原样透出到响应（WorkspaceResolution.Temporary），
// 前端**不得**从路径形状反推——路径前缀是可配置的，靠形状猜必然失效。

// WorkspaceResolution 是一次工作空间解析的结果。
type WorkspaceResolution struct {
	// Path 规范化后的绝对路径（会话 cwd）。
	Path string
	// Temporary 是否由平台代建（客户端未指定）。
	Temporary bool
}

// ValidateWorkspace 校验并规范化客户端显式指定的工作空间路径。
//
// 三条件对齐 goose 的 validate_absolute_cwd（绝对 / 存在 / 是目录），并额外做符号链接规范化：
// macOS 上 /var 与 /private/var 指向同一目录，不规范化就会把同一工作空间存成两个字符串，
// 导致按工作空间分组与会话过滤出现重复项（AionUi 的 normalizeWatchPath 注释即此坑）。
//
// allowedRoots 非空时再收一道：路径必须落在某个允许的工作区根之内（见 withinRoots）。
// 浏览面与创建面共用这个函数，所以白名单对两条路同时生效。
func ValidateWorkspace(spec string, allowedRoots []string) (string, error) {
	p := strings.TrimSpace(spec)
	if p == "" {
		return "", acl.NewError(acl.ClassRequest, "workspace_invalid", "工作空间路径不能为空", nil)
	}
	if !filepath.IsAbs(p) {
		return "", acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("工作空间需为绝对路径，得到 %q", p), nil)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("工作空间不存在或不可访问: %s", p), nil)
	}
	if !st.IsDir() {
		return "", acl.NewError(acl.ClassRequest, "workspace_invalid",
			fmt.Sprintf("工作空间需为目录: %s", p), nil)
	}
	resolved := filepath.Clean(p)
	if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
		resolved = real
	}
	// 白名单判定放在**规范化之后**：先解析真身再比较，否则根内一个指向外部的符号链接就是绕过通道。
	if !withinRoots(resolved, allowedRoots) {
		return "", workspaceOutsideRootsError(resolved, allowedRoots)
	}
	return resolved, nil
}

// ResolveWorkspace 解析建会话时应使用的 cwd。
//
//	spec 非空 → 校验后原样使用（Temporary=false；同时受 allowedRoots 约束）
//	spec 为空 → 在 provisionDir 下建 <conversationID>/（Temporary=true）
//	spec 为空且 provisionDir 为空 → 报错 workspace_required（**不回落 os.Getwd()**：
//	  serve 进程目录是"平台的目录"而非"用户的目录"，全局共享会让沙箱边界失效；
//	  运维若要强制显式指定，把 workspace_dir 设为空即可）
//
// 代建路径不再逐次校验 allowedRoots：传进来的就是**生效**名单（`Service.WorkspaceRoots()`），
// 而平台代建根已被隐式并入其中（见 effectiveWorkspaceRootsLocked）——逐次重算只是重复劳动。
func ResolveWorkspace(spec, provisionDir string, allowedRoots []string, conversationID string) (WorkspaceResolution, error) {
	if strings.TrimSpace(spec) != "" {
		p, err := ValidateWorkspace(spec, allowedRoots)
		if err != nil {
			return WorkspaceResolution{}, err
		}
		return WorkspaceResolution{Path: p}, nil
	}
	if strings.TrimSpace(provisionDir) == "" {
		return WorkspaceResolution{}, acl.NewError(acl.ClassRequest, "workspace_required",
			"未指定工作空间，且平台代建已关闭（workspace_dir 为空）：请显式传入 cwd", nil)
	}
	if strings.TrimSpace(conversationID) == "" {
		return WorkspaceResolution{}, acl.NewError(acl.ClassService, "workspace_unavailable",
			"内部错误：缺少会话 id，无法代建工作空间", nil)
	}
	dir := filepath.Join(provisionDir, conversationID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return WorkspaceResolution{}, acl.NewError(acl.ClassService, "workspace_unavailable",
			fmt.Sprintf("代建工作空间失败: %s", dir), err)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return WorkspaceResolution{Path: dir, Temporary: true}, nil
}

// withinRoots 判定 path 是否落在允许的工作区根之内（含根本身）。
//
// 两道防线，缺一不可：
//   - **逐段比较**（filepath.Rel）而不是字符串前缀：否则 /data/ws-evil 会被判成落在 /data/ws 之内；
//   - **两侧都先解析真身**：path 由调用方规范化过，root 在这里解析一次，否则根内一个指向外部的
//     符号链接（/data/ws/link -> /etc）就成了绕过白名单的通道。
//
// allowedRoots 为空 = 不限制（本机单操作员零配置的默认行为）。
func withinRoots(path string, allowedRoots []string) bool {
	if len(allowedRoots) == 0 {
		return true
	}
	real := resolveRealPath(path)
	for _, root := range allowedRoots {
		r := resolveRealPath(root)
		if real == r {
			return true
		}
		rel, err := filepath.Rel(r, real)
		if err != nil {
			continue // 不同卷（Windows）等无法表达相对关系 → 视为不在根内
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveRealPath 取路径「真身」：从最长的**已存在**祖先开始解析，再把剩余段原样接回。
//
// 为什么不直接对整条路径 EvalSymlinks、失败就退化 Clean：装配时校验的代建根**往往还没建出来**
// （workspace_dir 由平台自己按需创建），而符号链接的差异恰好出现在"已存在的那一段"。
// 真实教训：macOS 上 /var 与 /private/var 是同一目录，未建出的 /var/.../ws/tmp 若整条退化 Clean，
// 就会被判成不在已解析的根 /private/var/.../ws 之内——一个完全自洽的配置启动失败。
func resolveRealPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur { // 一路到根都解析不了（几乎不发生）：退化为 Clean 结果
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// workspaceOutsideRootsError 统一的越界错误（浏览面与创建面同一文案，运维一眼知道该改哪个配置）。
//
// 消息里带 `[workspace_outside_roots]` 是有意的（对齐 usage_stats_unsupported 的写法）：
// restErr 只把 acl 的 Details 当消息透出，语义码本身不进信封，所以要让调用方能稳定识别这类
// 拒绝（而不是去猜中文文案），就把码放进消息里。
func workspaceOutsideRootsError(path string, allowedRoots []string) error {
	return acl.NewError(acl.ClassRequest, "workspace_outside_roots",
		fmt.Sprintf("[workspace_outside_roots] 工作空间 %s 不在允许的工作区根之内（MATE_WORKSPACE_ROOTS=%s）",
			path, strings.Join(allowedRoots, ",")), nil)
}

// normalizeWorkspaceFilter 把「按工作空间过滤」的入参归一化到与会话 cwd 同一形态。
//
// 会话 cwd 一律是 canonical 绝对路径（ValidateWorkspace 已 EvalSymlinks），
// 而客户端可能仍按自己熟悉的写法传（macOS 上 /tmp 与 /private/tmp 是同一目录）。
// 不归一化就会出现「明明在同一个目录里，却查不到会话」——路径字符串不该决定身份。
// 路径不存在时按 Clean 处理（不报错）：过滤一个没人用过的工作空间，正确结果本就是空集。
func normalizeWorkspaceFilter(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return filepath.Clean(cwd)
}

// DiscardWorkspace 在会话最终建立失败时清理代建目录（best-effort）。
//
// 只删自己代建的那一层（ResolveWorkspace 保证该层是本会话独占的），
// 非临时工作空间与用户显式指定的目录一律不动。
func DiscardWorkspace(res WorkspaceResolution) {
	if !res.Temporary || res.Path == "" {
		return
	}
	_ = os.Remove(res.Path) // 非空则不删：用户/agent 已写入内容时宁可留下，也不误删
}

// SetWorkspacePolicy 设置工作空间的整体策略：代建根目录 + 允许的工作区根白名单。
//
// dir 空 = 关闭代建：此时未指定 cwd 的建会话请求直接报 workspace_required，
// **绝不回落 os.Getwd()**（serve 进程目录是"平台的目录"，全局共享会让沙箱边界失效）。
// roots 空 = 不限制（本机单操作员的默认行为）。
//
// 两者一起设置而不是各设各的：白名单生效时代建根会**隐式并入**生效名单
// （见 effectiveWorkspaceRootsLocked），于是"代建根不在白名单里"不再是配置错误——
// 它是平台自己的目录，本来就该天然合法，不该要运维把它手写进 MATE_WORKSPACE_ROOTS
// （旧行为：不自洽则 serve 启动失败）。分开设置则结果依赖调用顺序，容易漏，故仍收在一个入口。
func (s *Service) SetWorkspacePolicy(dir string, roots []string) error {
	dir = strings.TrimSpace(dir)
	normalized, err := normalizePolicyRoots(roots)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceDir = dir
	s.workspaceRoots = normalized
	return nil
}

// SetWorkspaceRoots 只更新白名单（面板 PUT /settings/workspace 的落地动作，见 workspace_settings.go）。
//
// 与 SetWorkspacePolicy 分工：那个是装配期整体策略（含代建根），这个是运行期改名单。
// 已建会话不受影响（cwd 创建即定稿，平台不回头否决旧会话），新校验立即生效。
func (s *Service) SetWorkspaceRoots(roots []string) error {
	normalized, err := normalizePolicyRoots(roots)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceRoots = normalized
	return nil
}

// normalizePolicyRoots 规范化策略层的白名单条目：只校验**形式**（绝对路径）并解析真身。
//
// 有意不要求目录存在：白名单是运维画的边界，而挂载可能晚于 serve 起来（容器编排里很常见），
// 因"目录还没建出来"就拒绝启动等于把顺序耦合强加给运维。真正需要存在性的是使用路径
// （建会话的显式 cwd 与目录浏览），那里报 workspace_invalid 才是用户能理解的位置。
func normalizePolicyRoots(roots []string) ([]string, error) {
	normalized := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("工作区根需为绝对路径，得到 %q", r)
		}
		real := resolveRealPath(r)
		if seen[real] {
			continue
		}
		seen[real] = true
		normalized = append(normalized, real)
	}
	return normalized, nil
}

// WorkspaceDir 返回代建工作空间的根目录（空 = 代建关闭）。
func (s *Service) WorkspaceDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceDir
}

// WorkspaceRoots 返回**生效**的工作区根白名单（空 = 不限制；含隐式并入的代建根）。
func (s *Service) WorkspaceRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effectiveWorkspaceRootsLocked()
}

// effectiveWorkspaceRootsLocked 生效白名单 = 显式配置的根 ∪ 平台代建根（调用方须持 s.mu）。
//
// 代建根隐式入名单的理由：它是**平台自己的目录**（平台按会话 id 在里面建会话沙箱），
// 要运维把它手写进配置才自洽，等于把平台的实现细节泄漏成运维负担。
// 顺序有意保持"配置项在前、代建根在后"：browse 的起步点取 roots[0]，不能被派生根顶掉。
//
// 未配置（空）时返回 nil（= 不限制）：不能因为存在代建根就把"不限制"悄悄缩成
// "只允许代建目录"——那会让默认配置下的用户突然选不了自己的项目目录。
func (s *Service) effectiveWorkspaceRootsLocked() []string {
	if len(s.workspaceRoots) == 0 {
		return nil
	}
	out := append([]string(nil), s.workspaceRoots...)
	if s.workspaceDir == "" {
		return out
	}
	if real := resolveRealPath(s.workspaceDir); !withinRoots(real, out) {
		out = append(out, real)
	}
	return out
}
