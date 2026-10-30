package workspace

import (
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"os"
	"path/filepath"
	"strings"
)

// 工作空间**起始点偏好**的设置面：把"目录浏览从哪儿起步"从"只能改环境变量"变成"面板也能改"。
//
// 三条边界（动这块前先读）：
//   - 白名单只约束**新**的显式 cwd 与目录浏览；已建会话的 cwd 创建即定稿，平台不回头否决旧会话
//     （旧会话仍可续聊，只是不会再作为"最近/资料库"候选被推荐）；
//   - 平台代建根恒在生效名单内（见 effectiveWorkspaceRootsLocked）：它是平台的目录，不是用户的；
//   - 写入有优先级天花板：进程 env / env 文件 / flag 设过同一个键时，写配置文件必然被覆盖，
//     所以这里**拒绝写**（settings_locked）而不是假装成功——面板据此转为只读展示。
//
// 安全权衡（必须显式说清）：这条接口让面板可以放宽自己的边界，因此对"能调到它的人"来说，
// 白名单退化成"默认视图"而非"边界"。平台不做鉴权，真正的防护是运行用户文件权限与反向代理；
// 要真正锁死，就在进程 env 里
// 设 MATE_WORKSPACE_ROOTS（本文件会把它识别为锁定来源，面板立即变只读）。见 docs/deployment.md。
//
// P2b：从 `internal/manager/workspace_settings.go` 搬进本域——它读写的是**域自己的**策略状态
// （`Plane` 的 roots/dir），留在 manager 只会让那边隔着一层转发去改域的字段。

// WorkspaceRootsStore 白名单的持久化端口，由 infra 实现（config.SettingsStore）。
//
// 域不直接写配置文件：那会把"配置目录布局 + 优先级规则 + 原子写"这套 infra 关切拖进领域层，
// 而这里真正需要的只有两件事——存下去、以及被告知"存了算不算数"。
type WorkspaceRootsStore interface {
	// SaveWorkspaceRoots 落盘显式白名单（空 = 不限制）。
	SaveWorkspaceRoots(roots []string) error
	// Status 返回该键的最终来源与锁定来源（lockedBy 非空 = 写入必被它覆盖）。
	Status() (source, lockedBy string)
}

// SetRootsStore 注入白名单持久化端口。nil = 只读（GET 可用，PUT 报 settings_readonly）：
// 没有配置目录的进程（如 demo 冒烟）不该往磁盘上写设置。
func (pl *Plane) SetRootsStore(st WorkspaceRootsStore) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.rootsStore = st
}

// rootsStoreRef 取端口引用（**不持锁调用它的方法**：那是 infra I/O，握着域锁去调用会把
// "配置目录写盘"这段时间算进临界区，与"面板保存时别的读面全堵住"是同一种症状）。
func (pl *Plane) rootsStoreRef() WorkspaceRootsStore {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.rootsStore
}

// SourceUnavailable 无持久化端口时对外报的来源（面板据此提示"当前进程不可写设置"）。
const SourceUnavailable = "unavailable"

// Settings 返回**起始点偏好**的设置快照（生效名单 + 显式配置名单 + 来源/锁定态 + 可写性）。
//
// v14.5 起这份名单只影响"目录浏览从哪儿起步"与"面板里的快捷入口"，不再拒绝任何路径。
//
// Configured 与 Roots 必须分开给：面板编辑的是 Configured，而"代建根"是平台派生的、
// 用户删不掉的那一条；只给生效名单，面板就永远无法表达"删掉这一条"。
func (pl *Plane) Settings() *SettingsView {
	effective, configured, provisionDir, createRoot := pl.PolicySnapshot()
	store := pl.rootsStoreRef()
	resp := &SettingsView{
		Configured:        append([]string{}, configured...),
		Roots:             effective,
		PreferRoots:       len(configured) > 0,
		ProvisionDir:      provisionDir,
		ProvisionIncluded: len(configured) > 0 && provisionDir != "",
		CreateRoot:        createRoot,
		Source:            SourceUnavailable,
	}
	if store != nil {
		src, lockedBy := store.Status()
		resp.Source, resp.LockedBy = src, lockedBy
		resp.Writable = lockedBy == ""
	}
	return resp
}

// UpdateSettings 全量替换白名单：校验 → 落盘 → 热更新内存策略。
//
// 顺序有意如此：**先落盘、成功后才改内存**。反过来会在落盘失败时留下"面板生效、重启回滚"
// 的假成功；而落盘成功、内存未改则只是白改一次文件，下次启动仍一致。
//
// 与配置文件入口的严格度差异：这里要求每个根**存在且是目录**（配置文件只校验绝对路径）。
// 理由是交互式设置的反馈必须在当下给（否则面板保存成功、下次进来看到的是错误页），
// 而声明式配置允许"挂载还没到"这种编排顺序——两处的取舍不同是有意的。
func (pl *Plane) UpdateSettings(roots []string) (*SettingsView, error) {
	store := pl.rootsStoreRef()
	if store == nil {
		return nil, failure.NewError(failure.ClassRequest, "settings_readonly",
			"当前进程未启用设置持久化（无配置目录）：工作区根白名单只能由 MATE_WORKSPACE_ROOTS 或配置文件设置", nil)
	}
	if _, lockedBy := store.Status(); lockedBy != "" {
		return nil, failure.NewError(failure.ClassRequest, "settings_locked",
			fmt.Sprintf("[settings_locked] 工作区根白名单由 %s 锁定（优先级高于配置文件）：请改该来源，或在此只读查看",
				lockedBy), nil)
	}
	normalized, err := NormalizeSettingsRoots(roots)
	if err != nil {
		return nil, err
	}
	if err := store.SaveWorkspaceRoots(normalized); err != nil {
		return nil, failure.NewError(failure.ClassService, "settings_save_failed",
			"保存工作区根白名单失败: "+err.Error(), err)
	}
	if err := pl.SetWorkspaceRoots(normalized); err != nil {
		return nil, failure.NewError(failure.ClassService, "settings_save_failed",
			"已写入配置文件但热更新失败（重启后生效）: "+err.Error(), err)
	}
	return pl.Settings(), nil
}

// NormalizeSettingsRoots 校验交互式设置的白名单条目：绝对 + 存在 + 是目录，再按真身去重。
//
// 错误里带上具体是哪一项：面板的输入是多条，只说"非法"等于让用户逐条试。
//
// 导出：它是"交互式设置这一条入口的严格度"的**唯一实现**，装配侧（`app` 的同名启动校验走的是
// 更宽的那条 `NormalizePolicyRoots`）与测试都要照同一条判。
func NormalizeSettingsRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, item := range roots {
		p := strings.TrimSpace(item)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, failure.NewError(failure.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根需为绝对路径，得到 %q", p), nil)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, failure.NewError(failure.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根不存在或不可访问: %s", p), nil)
		}
		if !st.IsDir() {
			return nil, failure.NewError(failure.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根需为目录: %s", p), nil)
		}
		real := ResolveRealPath(p)
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out, nil
}
