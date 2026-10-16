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
//   - ResolveWorkspace：未指定 → 落在**默认工作空间**（一个落库的登记项，见 workspace_registry.go）；
//   - BrowseWorkspace（workspace_view.go）：只读目录枚举，复用 ValidateWorkspace。
//
// 「平台给的」必须由调用方原样透出到响应（WorkspaceResolution.Temporary），
// 前端**不得**从路径形状反推——路径前缀是可配置的，靠形状猜必然失效。
//
// **v14 起不再按会话代建独占目录**（设计 ADR-4）：未指定 cwd 的会话共享默认工作空间。
// 这条变更的代价与理由见设计文档那一节——最要紧的一句是：两个未指定目录的会话
// **能读到对方的文件**（共享一个工作目录的必然结果）。

// WorkspaceResolution 是一次工作空间解析的结果。
type WorkspaceResolution struct {
	// Path 规范化后的绝对路径（会话 cwd）。
	Path string
	// Temporary 是否由**平台**给出（客户端未指定 → 落在默认工作空间）。
	//
	// 字段名沿用历史的 `Temporary`/`workspace_temporary`：它是既有 wire key（前端两处按它
	// 渲染），换名是纯破坏且没有信息增益。语义已从"平台按会话代建的一次性目录"变成
	// "这是默认工作空间（不是你选的）"（设计 ADR-9）。
	Temporary bool
	// WorkspaceID 归属的工作空间登记 id（空 = 该部署没有默认工作空间，或显式 cwd 未登记）。
	//
	// 它与 Path 是两件事（设计 ADR-2）：Path 是**执行事实**（ACP cwd、沙箱边界、不可变），
	// WorkspaceID 是**归属事实**（左栏分组）。显式给 cwd 时归属由调用方按路径查登记表补上。
	WorkspaceID string
}

// DefaultWorkspaceRef 是"默认工作空间"在解析路径上的最小承载。
//
// 刻意不传整个 model.WorkspaceEntity：`ResolveWorkspace` 是**纯函数**（可单测、无副作用），
// 让 store 类型渗进它的签名会把"解析"与"持久层"绑在一起——而这个函数的全部价值
// 恰恰是它只认路径与两个判据。
type DefaultWorkspaceRef struct {
	// ID 登记 id（作为归属写进会话行）。
	ID string
	// Path 规范化后的目录。
	Path string
}

// ValidateWorkspace 校验并规范化客户端显式指定的工作空间路径。
//
// 三条件对齐 goose 的 validate_absolute_cwd（绝对 / 存在 / 是目录），并额外做符号链接规范化：
// macOS 上 /var 与 /private/var 指向同一目录，不规范化就会把同一工作空间存成两个字符串，
// 导致按工作空间分组与会话过滤出现重复项（AionUi 的 normalizeWatchPath 注释即此坑）。
//
// **v14.5 起这里没有"允许的工作区根"这道门了**（产品口径：选工作区是全系统）。`MATE_WORKSPACE_ROOTS`
// 降级为**起始点偏好**——它只决定目录浏览从哪儿起步、以及面板里的根快捷入口，不再拒绝任何路径。
// 于是"能登记/能建会话"只剩这三个与文件系统事实有关的条件；浏览面与创建面仍共用这一个函数
// （"浏览得到、创建被拒"这类裂缝依然不会出现）。
func ValidateWorkspace(spec string) (string, error) {
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
	return resolved, nil
}

// ResolveWorkspace 解析建会话时应使用的 cwd。
//
//	spec 非空    → 校验后原样使用（Temporary=false；v14.5 起只要路径可用，不受任何名单限制）
//	spec 为空    → 落在**默认工作空间**（Temporary=true）
//	spec 为空且没有默认工作空间 → 报 workspace_required
//
// 「没有默认工作空间」有两种成因，文案分开（它们的处置不同）：
//   - 代建关闭（`workspace_dir` 为空）→ 该类部署**要求**显式 cwd，这是配置意图；
//   - 有配置但默认工作空间不存在（被删过 / 首次启动还没 bootstrap / 目录被外部毁掉）
//     → 运维该去看平台数据目录，而不是去改调用方。
//
// **不回落 os.Getwd()** 这条铁律不变：serve 进程目录是"平台的目录"而非"用户的目录"，
// 全局共享会让沙箱边界失效。
//
// 默认工作空间的路径不再逐次校验 allowedRoots：传进来的就是**生效**名单
// （`Service.WorkspaceRoots()`），而平台代建根已被隐式并入其中
// （见 effectiveWorkspaceRootsLocked）——逐次重算只是重复劳动。
func ResolveWorkspace(spec string, def *DefaultWorkspaceRef) (WorkspaceResolution, error) {
	if strings.TrimSpace(spec) != "" {
		p, err := ValidateWorkspace(spec)
		if err != nil {
			return WorkspaceResolution{}, err
		}
		return WorkspaceResolution{Path: p}, nil
	}
	if def == nil || strings.TrimSpace(def.ID) == "" || strings.TrimSpace(def.Path) == "" {
		return WorkspaceResolution{}, acl.NewError(acl.ClassRequest, "workspace_required",
			"未指定工作空间，且没有可用的默认工作空间（未配置 workspace_dir，或默认工作空间已被移除）：请显式传入 cwd", nil)
	}
	dir := def.Path
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return WorkspaceResolution{Path: dir, Temporary: true, WorkspaceID: def.ID}, nil
}

// withinRoots 判定 path 是否落在某个工作区根之内（含根本身）。
//
// **v14.5 起它不再是"边界判定"**——选目录与建会话都不看它了。它现在只服务一处：
// `WorkspaceLibraries` 的**策展过滤**（资料库 = 工作区根下的一级子目录，根外的东西不是资料库）。
// 别把它当成"能不能用这个目录"的判据——那是 ValidateWorkspace 的事，而那里已经没有名录这道门。
//
// 两道防线，缺一不可（对一个**策展列表**同样重要）：
//   - **逐段比较**（filepath.Rel）而不是字符串前缀：否则 /data/ws-evil 会被判成落在 /data/ws 之内；
//   - **两侧都先解析真身**：否则根内一个指向外部的符号链接（/data/ws/link -> /etc）会被列成资料库。
//
// allowedRoots 为空 = 没有策展面（本机单操作员零配置的默认行为）。
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

// DiscardWorkspace 在会话最终建立失败时清理**会话独占**的工作空间目录（best-effort）。
//
// **v14 起它恒为空操作**，且这件事必须写在这里而不是悄悄删掉函数：解析路径已经不再创建
// 任何会话独占目录（未指定 cwd 的会话落在共享的默认工作空间，见设计 ADR-4），所以
// "建会话失败 → 清理刚建的目录"这个动作**不再有对象**。
//
// 它为什么不能顺手改成"删掉默认工作空间"：那是**共享**目录——一次失败的建会话会连带
// 删掉别的会话正在用的文件。这条注释就是防止将来有人"看着像死代码"而把它接到默认工作空间上，
// 保留分支是为了让那个判断（`Temporary` 现在表示"平台给的默认工作空间"）显式可见。
func DiscardWorkspace(res WorkspaceResolution) {
	if !res.Temporary || res.Path == "" {
		return
	}
	// 刻意什么都不做：Temporary=true 现在指默认工作空间（共享），不是会话独占目录。
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

// defaultWorkspaceDir 是**默认工作空间**的目录：代建根下的 `default/` 子目录。
//
// 为什么不是代建根本身：根下面还会按需放别的东西（历史遗留的按会话代建目录就在那里），
// 把根登记成一个工作空间会让"平台代建根恒在生效白名单内"这条规则与"用户的工作目录"
// 混成同一个东西——那样用户删掉这个登记就等于把平台的代建根一起交出去。
//
// 名字固定为 `default`（语言中立）：一次安装跨语言切换时磁盘路径不变（设计 ADR-4）。
func defaultWorkspaceDir(provisionDir string) string {
	return filepath.Join(provisionDir, "default")
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
