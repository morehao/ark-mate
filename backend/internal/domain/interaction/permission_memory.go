package interaction

import (
	"sort"
	"time"

	"github.com/morehao/ark-mate/pkg/acl"
)

// 会话级权限记忆的决策侧（与 allow_always / reject_always 选项一一对应）。
const (
	memDecisionAllow  = "allow"  // 会话级「始终允许」
	memDecisionReject = "reject" // 会话级「始终拒绝」
)

// memoryEntry 是一条会话级权限记忆（allow_always / reject_always 双侧对称，同键互斥覆盖）。
type memoryEntry struct {
	title    string
	kind     string
	decision string // memDecisionAllow | memDecisionReject
	optionID string // 用户当初选择的选项（auto-decision 时尽量沿用）
	since    time.Time
	// seq 写入序号：`List` 的排序键是 (since, seq)。
	//
	// 为什么需要它：`time.Now()` 的分辨率在平台/负载下可能让两次 Remember 拿到**同一个**时间戳，
	// 此时只按 since 排序就退化成 map 迭代顺序（`sort.Slice` 不稳定）——表现为"权限记忆列表偶尔换序"，
	// 而 `TestPermissionMemoryBothSides` 正是断言首条的（实测在 `go test ./...` 并行负载下偶发失败）。
	// 语义上这条也更正确：同一时刻的授予，按用户点的先后。
	seq uint64
}

// MemoryItem 是一条会话级权限记忆的对外表示（REST/详情载荷；decision 区分始终允许/始终拒绝）。
// sessionID 仅用于装载（sqlite 恢复），不进对外载荷。
type MemoryItem struct {
	sessionID string
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Decision  string    `json:"decision"` // allow | reject
	Since     time.Time `json:"since"`
}

// NewMemoryItem 造一条记忆项。
//
// 为什么用构造函数而不是把 `sessionID` 导出：该字段**没有 json tag**，一旦导出，
// 序列化就会多出一个 `SessionID` 键——那是**改对外契约**（本方案的硬约束是行为零变化）。
// 域外只需要"按会话整体装载"，不需要各自拼装这个结构。
func NewMemoryItem(sessionID, title, kind, decision string, since time.Time) MemoryItem {
	return MemoryItem{sessionID: sessionID, Title: title, Kind: kind, Decision: decision, Since: since}
}

// SetMemoryHooks 注入会话级记忆持久化挂钩（sqlite 模式；可为 nil 表示不持久化）。
// 挂钩在锁外调用（DB 写是 best-effort，不入关键路径）。
func (m *PermissionManager) SetMemoryHooks(add func(sessionID, title, kind, decision string, since time.Time),
	del func(sessionID, title, kind string), delAll func(sessionID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memAdd = add
	m.memDel = del
	m.memDelAll = delAll
}

// alwaysKeyOf 归一化「action+identifier」集合键：以 tool title + kind 作为可辨识键。
func alwaysKeyOf(title, kind string) string { return title + "|" + kind }

// alwaysKey 由工具信息算集合键。
func alwaysKey(t acl.ToolCallInfo) string { return alwaysKeyOf(t.Title, t.Kind) }

// AutoDecision 决定「无需征询」的快速路径：会话级记忆命中 → 直接放行/拒绝。
// 返回 (decision, ok)：ok=false 表示需要真正挂起等待用户。
func (m *PermissionManager) AutoDecision(sessionID string, req acl.PermissionRequest) (acl.PermissionDecision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookupLocked(sessionID, req.ToolCall)
	if e == nil {
		return acl.PermissionDecision{}, false
	}
	switch e.decision {
	case memDecisionAllow:
		return acl.PermissionDecision{Allowed: true, OptionID: e.optionID}, true
	case memDecisionReject:
		return acl.PermissionDecision{Rejected: true, OptionID: e.optionID}, true
	default:
		return acl.PermissionDecision{}, false
	}
}

// lookupLocked 查某会话对某工具的会话级记忆（须持锁）。
func (m *PermissionManager) lookupLocked(sessionID string, t acl.ToolCallInfo) *memoryEntry {
	set := m.mem[sessionID]
	if set == nil {
		return nil
	}
	e, ok := set[alwaysKey(t)]
	if !ok {
		return nil
	}
	cp := e
	return &cp
}

// Remember 记录（或覆盖）一条会话级权限记忆。decision ∈ allow|reject。
// sqlite 模式下同步旁路持久化（permission_memories 表，同键覆盖）。
func (m *PermissionManager) Remember(sessionID string, t acl.ToolCallInfo, decision, optionID string) {
	m.mu.Lock()
	set := m.mem[sessionID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[sessionID] = set
	}
	k := alwaysKey(t)
	prev, ok := set[k]
	since := time.Now().UTC()
	seq := m.nextSeq()
	if ok && prev.decision == decision {
		since = prev.since // 同侧重复授权不刷新授予时间
		seq = prev.seq     // 也不改变它在列表里的相对位置
	}
	set[k] = memoryEntry{title: t.Title, kind: t.Kind, decision: decision, optionID: optionID, since: since, seq: seq}
	add := m.memAdd
	m.mu.Unlock()
	if add != nil {
		add(sessionID, t.Title, t.Kind, decision, since)
	}
}

// LoadMemories 恢复会话级权限记忆（sqlite 启动恢复：从 permission_memories 表装载，
// 双侧 allow/reject 一并重建；重复装载按 since 覆盖，行为与 Remember 一致）。
func (m *PermissionManager) LoadMemories(items []MemoryItem) {
	for _, it := range items {
		m.memPut(it)
	}
}

// memPut 装载单条记忆（给定 since，复用同键覆盖语义）。
func (m *PermissionManager) memPut(it MemoryItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.mem[it.sessionID]
	if set == nil {
		set = make(map[string]memoryEntry)
		m.mem[it.sessionID] = set
	}
	k := alwaysKeyOf(it.Title, it.Kind)
	since := it.Since
	if since.IsZero() {
		since = time.Now().UTC()
	}
	set[k] = memoryEntry{title: it.Title, kind: it.Kind, decision: it.Decision, since: since, seq: m.nextSeq()}
}

// ForgetSession 清除整会话的权限记忆（会话结束/重置/一键撤销）。
// sqlite 模式下同步旁路删除（permission_memories 表整会话）。
func (m *PermissionManager) ForgetSession(sessionID string) {
	m.mu.Lock()
	delete(m.mem, sessionID)
	delAll := m.memDelAll
	m.mu.Unlock()
	if delAll != nil {
		delAll(sessionID)
	}
}

// Revoke 撤销一条会话级权限记忆（title+kind 精确匹配，不区分 decision）；返回该条是否存在。
// sqlite 模式下同步旁路删除（permission_memories 表同键）。
func (m *PermissionManager) Revoke(sessionID, title, kind string) bool {
	m.mu.Lock()
	set := m.mem[sessionID]
	if set == nil {
		m.mu.Unlock()
		return false
	}
	k := alwaysKeyOf(title, kind)
	if _, ok := set[k]; !ok {
		m.mu.Unlock()
		return false
	}
	delete(set, k)
	if len(set) == 0 {
		delete(m.mem, sessionID)
	}
	del := m.memDel
	m.mu.Unlock()
	if del != nil {
		del(sessionID, title, kind)
	}
	return true
}

// nextSeq 取下一个写入序号（调用方必须已持锁）。
func (m *PermissionManager) nextSeq() uint64 {
	m.seq++
	return m.seq
}

// List 列出某会话的全部权限记忆（按授予时间升序，同一时刻按写入先后；decision 区分 allow/reject）。
func (m *PermissionManager) List(sessionID string) []MemoryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.mem[sessionID]
	// 排序键里带 seq（内部字段），所以先按"带序号的内部行"排，再投影成对外的 MemoryItem——
	// seq 是实现细节，不进 REST 载荷。
	type row struct {
		item MemoryItem
		seq  uint64
	}
	rows := make([]row, 0, len(set))
	for _, e := range set {
		rows = append(rows, row{
			item: MemoryItem{Title: e.title, Kind: e.kind, Decision: e.decision, Since: e.since},
			seq:  e.seq,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].item.Since.Equal(rows[j].item.Since) {
			return rows[i].item.Since.Before(rows[j].item.Since)
		}
		return rows[i].seq < rows[j].seq // 时刻相同 → 按写入先后（见 memoryEntry.seq）
	})
	out := make([]MemoryItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.item)
	}
	return out
}
