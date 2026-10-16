package session

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// ListSessions 返回会话概要；archived=false 仅未归档（默认），true 仅归档。
// cwd 非空 = 只返回工作空间精确等于该路径的会话（按工作空间过滤；路径须为绝对路径，
// 会话 cwd 已规范化为 canonical 形式，调用方传同一个字符串即可命中）。
// 按创建时间倒序（置顶优先）。
// ListSessions 列出会话（archived 过滤；cwd / workspaceID 非空则再收一道）。
//
// 两个过滤参数都是"按工作空间看"的入口，但口径不同（设计 ADR-2/ADR-5）：
//   - `cwd`：**执行事实**的字符串相等（历史行为，兼容保留）；
//   - `workspaceID`：**归属事实**（v14 起左栏分组用它，可索引，且不受路径写法差异影响）。
//
// 归属过滤会同时把"历史会话（workspace_id=”）"算进默认工作空间那一组：那些行在**读面**
// 归默认工作空间（设计 ADR-6），所以按 default id 过滤时它们必须出现——否则界面上默认工作
// 空间显示 N 个会话、点进去却是空的。
func (pl *Plane) ListSessions(archived bool, cwd, workspaceID string) ([]dtosession.SessionResp, error) {
	cwd = NormalizeWorkspaceFilter(cwd)
	workspaceID = strings.TrimSpace(workspaceID)
	// 归属过滤要先把"默认工作空间 id"和"目标 id 是否就是默认"问出来（读面语义的一部分）。
	defaultID := ""
	if pl.st != nil {
		if def, ok, err := pl.st.DefaultWorkspace(context.Background()); err == nil && ok {
			defaultID = def.ID
		}
	}
	pl.mu.Lock()
	out := make([]dtosession.SessionResp, 0, len(pl.sessions))
	for _, r := range pl.sessions {
		if r.Archived != archived {
			continue
		}
		if cwd != "" && r.Cwd != cwd {
			continue
		}
		if workspaceID != "" && !workspaceMatches(r.WorkspaceID, workspaceID, defaultID) {
			continue
		}
		resp := pl.ToSessionRespOf(r)
		// 归属的**原始值**必须先落到响应上：`toSessionResp` 刻意不带它（那会持锁读库），
		// 而下面的 fill 要靠它去解析展示名。漏这一步的表现是"所有会话都归到默认工作空间"
		// ——一个看起来完全正常的错误分组。
		resp.WorkspaceID = r.WorkspaceID
		out = append(out, resp)
	}
	pl.mu.Unlock()
	// 展示字段（workspace_id / workspace_title）在**释放锁之后**补齐：它们要读库，
	// 而持锁读库会与 registerSession 的"先写内存再落库"顺序互相卡住。
	pl.fillWorkspaceDisplay(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// fillWorkspaceDisplay 给一批响填补归属展示字段（v14；读面解析，见设计 §4.3）。
func (pl *Plane) fillWorkspaceDisplay(items []dtosession.SessionResp) {
	for i := range items {
		id, title := pl.workspaceDisplayOf(items[i].WorkspaceID)
		items[i].WorkspaceID = id
		items[i].WorkspaceTitle = title
	}
}

// sessionResp 返回单个会话的轻量表示（须持锁调用）。
func (pl *Plane) SessionResp(id string) dtosession.SessionResp {
	r := pl.sessions[id]
	return pl.ToSessionRespOf(r)
}

// workspaceMatches 归属过滤的判定（设计 ADR-6）：`rWorkspaceID == ”` 的行归默认工作空间。
//
// 单独成函数是为了让这条语义只有一处：把它内联进循环，那个 `== ""` 的特例迟早被漏掉一处，
// 而漏掉的表现是"默认工作空间下面看不到历史会话"——一个没有解释余地的空列表。
func workspaceMatches(recWorkspaceID, wantID, defaultID string) bool {
	if wantID == defaultID {
		return recWorkspaceID == "" || recWorkspaceID == wantID
	}
	return recWorkspaceID == wantID
}

// toSessionResp 把 SessionRec 折叠为 dtosession.SessionResp。
//
// **不含归属展示字段**（workspace_id / workspace_title）：那两个要读库，而本函数在持 pl.mu 的
// 调用点里跑。需要它们的调用点用 `fillWorkspaceDisplay` 在释放锁之后补——这条分界必须显式，
// 否则下一个人会顺手在这里加一次查库，而症状是偶发死锁。
func (pl *Plane) ToSessionRespOf(r *SessionRec) dtosession.SessionResp {
	return dtosession.SessionResp{
		Capabilities:       pl.capabilityViewOf(r),
		ID:                 r.ID,
		RuntimeSessionID:   r.RuntimeSessionID,
		RuntimeID:          r.RuntimeID,
		Status:             r.Status,
		Title:              r.Title,
		Pinned:             r.Pinned,
		Archived:           r.Archived,
		Model:              r.ModelID,
		Cwd:                r.Cwd,
		WorkspaceTemporary: r.WorkspaceTemporary,
		Origin:             r.Origin,
		CreatedAt:          r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// deriveTitle 从首条用户消息推导会话标题（单行、截断、折叠空白）。
// 截断按 rune 数判断（len() 是字节数，CJK 一个字符占 3 字节：按字节判断会
// 在「字节够长但字符不足 48」时对 runes[:48] 越界 panic）。
// normalizeWorkspaceFilter 工作空间过滤值的规范化（符号链接优先，解析不出就 Clean）。
//
// 随读面进域：它是"cwd 过滤怎么比"这一件事的唯一实现（写面也在用同一个语义）。
func NormalizeWorkspaceFilter(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return filepath.Clean(cwd)
}

func DeriveTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 48 {
		text = string(runes[:48]) + "…"
	}
	return text
}
