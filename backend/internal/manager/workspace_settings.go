package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// 工作空间白名单的**设置面**：把"哪些目录能被当作工作空间"从"只能改环境变量"变成"面板也能改"。
//
// 三条边界（动这块前先读）：
//   - 白名单只约束**新**的显式 cwd 与目录浏览；已建会话的 cwd 创建即定稿，平台不回头否决旧会话
//     （旧会话仍可续聊，只是不会再作为"最近/资料库"候选被推荐）；
//   - 平台代建根恒在生效名单内（见 effectiveWorkspaceRootsLocked）：它是平台的目录，不是用户的；
//   - 写入有优先级天花板：进程 env / env 文件 / flag 设过同一个键时，写配置文件必然被覆盖，
//     所以这里**拒绝写**（settings_locked）而不是假装成功——面板据此转为只读展示。
//
// 安全权衡（必须显式说清）：这条接口让面板可以放宽自己的边界，因此对"能调到它的人"来说，
// 白名单退化成"默认视图"而非"边界"。防护等级等于 FORGE_TOKEN；要真正锁死，就在进程 env 里
// 设 FORGE_WORKSPACE_ROOTS（本文件会把它识别为锁定来源，面板立即变只读）。见 docs/deployment.md。

// WorkspaceRootsStore 白名单的持久化端口，由 infra 实现（config.SettingsStore）。
//
// manager 不直接写配置文件：那会把"配置目录布局 + 优先级规则 + 原子写"这套 infra 关切
// 拖进领域层，而这里真正需要的只有两件事——存下去、以及被告知"存了算不算数"。
type WorkspaceRootsStore interface {
	// SaveWorkspaceRoots 落盘显式白名单（空 = 不限制）。
	SaveWorkspaceRoots(roots []string) error
	// Status 返回该键的最终来源与锁定来源（lockedBy 非空 = 写入必被它覆盖）。
	Status() (source, lockedBy string)
}

// SetWorkspaceSettingsStore 注入白名单持久化端口。nil = 只读（GET 可用，PUT 报 settings_readonly）：
// 没有配置目录的进程（acp-serve/acp-demo/demo）不该往磁盘上写设置。
func (s *Service) SetWorkspaceSettingsStore(st WorkspaceRootsStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceRootsStore = st
}

// WorkspaceSettings 返回白名单设置快照（生效名单 + 显式配置名单 + 来源/锁定态 + 可写性）。
//
// Configured 与 Roots 必须分开给：面板编辑的是 Configured，而"代建根"是平台派生的、
// 用户删不掉的那一条；只给生效名单，面板就永远无法表达"删掉这一条"。
func (s *Service) WorkspaceSettings() (*handler.WorkspaceSettingsResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.workspaceRootsStore
	resp := &handler.WorkspaceSettingsResp{
		Configured:        append([]string{}, s.workspaceRoots...),
		Roots:             s.effectiveWorkspaceRootsLocked(),
		Restricted:        len(s.workspaceRoots) > 0,
		ProvisionDir:      s.workspaceDir,
		ProvisionIncluded: len(s.workspaceRoots) > 0 && s.workspaceDir != "",
		Source:            string(sourceUnavailable),
	}
	if store != nil {
		src, lockedBy := store.Status()
		resp.Source, resp.LockedBy = src, lockedBy
		resp.Writable = lockedBy == ""
	}
	return resp, nil
}

// sourceUnavailable 无持久化端口时对外报的来源（面板据此提示"当前进程不可写设置"）。
const sourceUnavailable = "unavailable"

// UpdateWorkspaceSettings 全量替换白名单：校验 → 落盘 → 热更新内存策略。
//
// 顺序有意如此：**先落盘、成功后才改内存**。反过来会在落盘失败时留下"面板生效、重启回滚"
// 的假成功；而落盘成功、内存未改则只是白改一次文件，下次启动仍一致。
//
// 与配置文件入口的严格度差异：这里要求每个根**存在且是目录**（配置文件只校验绝对路径）。
// 理由是交互式设置的反馈必须在当下给（否则面板保存成功、下次进来看到的是错误页），
// 而声明式配置允许"挂载还没到"这种编排顺序——两处的取舍不同是有意的。
func (s *Service) UpdateWorkspaceSettings(roots []string) (*handler.WorkspaceSettingsResp, error) {
	s.mu.Lock()
	store := s.workspaceRootsStore
	s.mu.Unlock()
	if store == nil {
		return nil, acl.NewError(acl.ClassRequest, "settings_readonly",
			"当前进程未启用设置持久化（无配置目录）：工作区根白名单只能由 FORGE_WORKSPACE_ROOTS 或配置文件设置", nil)
	}
	if _, lockedBy := store.Status(); lockedBy != "" {
		return nil, acl.NewError(acl.ClassRequest, "settings_locked",
			fmt.Sprintf("[settings_locked] 工作区根白名单由 %s 锁定（优先级高于配置文件）：请改该来源，或在此只读查看",
				lockedBy), nil)
	}
	normalized, err := normalizeSettingsRoots(roots)
	if err != nil {
		return nil, err
	}
	if err := store.SaveWorkspaceRoots(normalized); err != nil {
		return nil, acl.NewError(acl.ClassService, "settings_save_failed",
			"保存工作区根白名单失败: "+err.Error(), err)
	}
	if err := s.SetWorkspaceRoots(normalized); err != nil {
		return nil, acl.NewError(acl.ClassService, "settings_save_failed",
			"已写入配置文件但热更新失败（重启后生效）: "+err.Error(), err)
	}
	return s.WorkspaceSettings()
}

// normalizeSettingsRoots 校验交互式设置的白名单条目：绝对 + 存在 + 是目录，再按真身去重。
//
// 错误里带上具体是哪一项：面板的输入是多条，只说"非法"等于让用户逐条试。
func normalizeSettingsRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, item := range roots {
		p := strings.TrimSpace(item)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根需为绝对路径，得到 %q", p), nil)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根不存在或不可访问: %s", p), nil)
		}
		if !st.IsDir() {
			return nil, acl.NewError(acl.ClassRequest, "workspace_invalid",
				fmt.Sprintf("工作区根需为目录: %s", p), nil)
		}
		real := resolveRealPath(p)
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out, nil
}
