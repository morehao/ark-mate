package session

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/core/object/objtime"
)

// IsInternalOrigin 判断会话是否由**内部模型网关**派生（`origin = 'internal:llm'` 一类）。
//
// 内部会话是辅助调用的一次执行，不是用户在聊的会话：会话列表 / 全文搜索 / 用户维度用量 UI
// 都应过滤掉它们（按 `origin` 前缀判定，与既有 `'automation:<id>'` 同一轴）。
func IsInternalOrigin(origin string) bool {
	return strings.HasPrefix(origin, "internal:")
}

// isInternalSession 某会话是否内部（模型网关）派生（查内存会话表；未知会话按非内部处理）。
func (pl *Plane) isInternalSession(sessionID string) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	r := pl.sessions[sessionID]
	return r != nil && IsInternalOrigin(r.Origin)
}

// ListSessions 返回会话概要；archived=false 仅未归档（默认），true 仅归档。
// cwd 非空 = 只返回工作空间精确等于该路径的会话（按工作空间过滤；路径须为绝对路径，会话 cwd 已规范化为 canonical 形式，调用方传同一个字符串即可命中）。
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
func (pl *Plane) ListSessions(archived bool, cwd, workspaceID string) ([]SessionItem, error) {
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
	out := make([]SessionItem, 0, len(pl.sessions))
	for _, r := range pl.sessions {
		if r.Archived != archived {
			continue
		}
		// 内部（模型网关）会话不进用户会话列表：它们不是"用户在聊的会话"，是辅助调用的一次执行。
		if IsInternalOrigin(r.Origin) {
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
	// 模型展示名同理：一次清单查询建表，不逐行取（列表刷新是高频路径）。
	pl.fillModelDisplay(out)
	// 过程状态的投影同理在**释放锁之后**补（`RunSnapshot` 自己取锁）：持锁读它会与
	// `recordRunStatus` 的取锁路径互相卡住——这条分界与 `fillWorkspaceDisplay` 是同一条。
	pl.fillRunPhase(out)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// fillRunPhase 给一批会话行补上当前轮次的过程状态（侧栏阶段文案的来源）。
//
// 必须在**释放锁之后**调：`RunSnapshot` 自己取锁，而 `recordRunStatus` 与 `ListSessions`
// 的读循环是两条会互相等待的边（持锁读域状态 → 详情写点取锁 = 经典的锁序反转）。
func (pl *Plane) fillRunPhase(items []SessionItem) {
	for i := range items {
		items[i].Run = pl.RunPhaseOf(items[i].ID)
	}
}

// fillWorkspaceDisplay 给一批响填补归属展示字段（v14；读面解析，见设计 §4.3）。
func (pl *Plane) fillWorkspaceDisplay(items []SessionItem) {
	for i := range items {
		id, title := pl.workspaceDisplayOf(items[i].WorkspaceID)
		items[i].WorkspaceID = id
		items[i].WorkspaceTitle = title
	}
}

// fillModelDisplay 给一批会话行补上模型**展示名**（`ModelName`）。
//
// 与 `fillWorkspaceDisplay` 同一分界：必须在**释放锁之后**调（要读库）。只对**有模型引用**
// 的行查，且整批只取一次 `ListModelDefinitions`——模型清单是几十行的小表，逐行 `Get` 会把
// 一次列表刷新变成 N 次查询。
//
// 取不到（内存模式 `st == nil`、读库失败、行已删除）就留空：前端据此回落成「已设置」，
// **不把 `mdl_…` 摊给用户**——那是平台行身份，不是模型名。
func (pl *Plane) fillModelDisplay(items []SessionItem) {
	if pl.st == nil {
		return
	}
	need := false
	for i := range items {
		if items[i].Model != "" {
			need = true
			break
		}
	}
	if !need {
		return
	}
	rows, err := pl.st.ListModelDefinitions(context.Background())
	if err != nil {
		return
	}
	label := make(map[string]string, len(rows))
	for i := range rows {
		name := strings.TrimSpace(rows[i].Name)
		if name == "" {
			name = rows[i].Model
		}
		label[rows[i].ID] = name
	}
	for i := range items {
		if items[i].Model != "" {
			items[i].ModelName = label[items[i].Model]
		}
	}
}

// SessionSnapshot 一次取锁返回单会话的读面：轻量表示 + 过程状态 + 存在性。
//
// 为什么一次取锁而不是三个方法各取一次：详情页要的是**同一时刻**的"这条会话 + 它这一轮在
// 干什么"。分三次取得到三个瞬间的快照（resp 属于时刻 A、run 属于时刻 B），而两段在界面上
// 是同一行字。列表路径仍走 `fillRunPhase`（锁外逐条补，见那里的注释）。
//
// 返回的 `RunPhase` 为 nil = 没有投影（历史会话 / 本轮还没开始），与 `RunPhaseOf` 同一口径。
func (pl *Plane) SessionSnapshot(id string) (SessionItem, *RunPhase, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	r, ok := pl.sessions[id]
	if !ok {
		return SessionItem{}, nil, false
	}
	resp := pl.ToSessionRespOf(r)
	var run *RunPhase
	if snap, ok := pl.runStatusLatest[id]; ok {
		run = runPhaseOfSnapshot(snap)
	}
	return resp, run, true
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

// toSessionResp 把 SessionRec 折叠为 SessionItem。
//
// **不含归属展示字段**（workspace_id / workspace_title）：那两个要读库，而本函数在持 pl.mu 的
// 调用点里跑。需要它们的调用点用 `fillWorkspaceDisplay` 在释放锁之后补——这条分界必须显式，
// 否则下一个人会顺手在这里加一次查库，而症状是偶发死锁。
func (pl *Plane) ToSessionRespOf(r *SessionRec) SessionItem {
	return SessionItem{
		Capabilities:       pl.SessionCapabilitiesRespOf(r),
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
		NativeMode:         r.NativeMode,
		CreatedAt:          objtime.Milli(r.CreatedAt),
		UpdatedAt:          objtime.Milli(r.UpdatedAt),
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
