package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/infra/protocol/acp"
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
	// Scope 授权形态（v28；今天恒 `session`）。
	Scope string `json:"scope"`
	// GrantInputDigest 当初那条工具入参的摘要（**不是**原文；见 `grantInputDigest`）。
	GrantInputDigest string `json:"grant_input_digest,omitempty"`
}

// NewMemoryItem 造一条记忆项。
//
// 为什么用构造函数而不是把 `sessionID` 导出：该字段**没有 json tag**，一旦导出，
// 序列化就会多出一个 `SessionID` 键——那是**改对外契约**（本方案的硬约束是行为零变化）。
// 域外只需要"按会话整体装载"，不需要各自拼装这个结构。
func NewMemoryItem(sessionID, title, kind, decision string, since time.Time) MemoryItem {
	return MemoryItem{sessionID: sessionID, Title: title, Kind: kind, Decision: decision, Since: since}
}

// WithGrantShape 补上授权形态（v28；装载路径用——`Remember` 走的是另一条构造）。
func (it MemoryItem) WithGrantShape(scope, grantInputDigest string) MemoryItem {
	it.Scope = scope
	it.GrantInputDigest = grantInputDigest
	return it
}

// SessionID 该记忆归属的会话（域外装载/落库要它；**没有 json tag**，故不会进对外载荷）。
func (it MemoryItem) SessionID() string { return it.sessionID }

// memoryItemOf 造一条**带授权形态**的记忆项（`Remember` 用；v28）。
func memoryItemOf(sessionID string, it MemoryItem) MemoryItem {
	it.sessionID = sessionID
	return it
}

// SetMemoryHooks 注入会话级记忆持久化挂钩（sqlite 模式；可为 nil 表示不持久化）。
// 挂钩在锁外调用（DB 写是 best-effort，不入关键路径）。
func (m *PermissionManager) SetMemoryHooks(add func(it MemoryItem),
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
func alwaysKey(t acp.ToolCallInfo) string { return alwaysKeyOf(t.Title, t.Kind) }

// AutoDecision 决定「无需征询」的快速路径：会话级记忆命中 → 直接放行/拒绝。
// 返回 (decision, ok)：ok=false 表示需要真正挂起等待用户。
func (m *PermissionManager) AutoDecision(sessionID string, req acp.PermissionRequest) (acp.PermissionDecision, bool) {
	// **只给"可以被记住"的请求自动裁决**（A1 / v28）。判据：这次请求的选项里有没有 `always` 那一档。
	//
	// 为什么这条短路是必须的：会话级记忆记的是"这个会话里这类工具**始终**允许"。而宿主（底座）
	// 有时会发一次**只给 `once` 档**的请求——被沙箱边界挡下之后的**升权请求**正是这一种
	// （探针实测：options 恒 allow-once / reject-once）。若让记忆替它裁决，等于把用户当时那句
	// "这一类都行"自动扩张成一个底座**刻意只肯给一次**的动作——那是本仓最不该发生的一类静默授权。
	//
	// 反过来的方向也是对的：给了 `always` 档却仍走审批，只是多问一次（可恢复）；
	// 而错放一次升权不可恢复。
	//
	// 判据是**"宿主明确只给了 once 档"**，不是"没有 always 档"：
	//
	//	有 `_always` 档                    → 可以被记住（常态请求）
	//	没有任何 `_always`、且有 `_once` 档 → **不可以**（升权请求：宿主刻意只肯给一次）
	//	选项为空 / 选项的 kind 都没写        → 当"未知"处理，仍可按记忆裁决
	//
	// 最后一档是刻意宽松的：`Kind` 为空的选项是历史构造点与不提供选项面的路径，"没写"不是
	// "只给一次"的证据。把它当证据会让那些调用方**静默失去**记忆能力——那是把一条安全判据
	// 变成了一个功能回退（而回退比误拦更难发现）。
	if offersOnlyOnceOptions(req) {
		return acp.PermissionDecision{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookupLocked(sessionID, req.ToolCall)
	if e == nil {
		return acp.PermissionDecision{}, false
	}
	switch e.decision {
	case memDecisionAllow:
		return acp.PermissionDecision{Allowed: true, OptionID: e.optionID}, true
	case memDecisionReject:
		return acp.PermissionDecision{Rejected: true, OptionID: e.optionID}, true
	default:
		return acp.PermissionDecision{}, false
	}
}

// lookupLocked 查某会话对某工具的会话级记忆（须持锁）。
func (m *PermissionManager) lookupLocked(sessionID string, t acp.ToolCallInfo) *memoryEntry {
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

// MemoryScopeSession 会话内始终（v28 的**唯一**可写授权形态）。
//
// "只允许这一次"没有对应取值：`Remember` 只被 always 决策调用，once 决策**从不落库**。
// 给它留一个永远不会被写入的枚举值，只会让下一个读它的人以为平台支持那种记忆。
const MemoryScopeSession = "session"

// Remember 记录（或覆盖）一条会话级权限记忆。decision ∈ allow|reject。
// sqlite 模式下同步旁路持久化（permission_memories 表，同键覆盖）。
//
// v28 起还带两样**描述这次授权**的东西（都**不进**唯一键）：
//
//	scope             授权形态（今天恒 `session`）
//	grantInputDigest  当初那条工具入参的摘要
//
// 后者是为了**可复核**：出事时能回答"当初那句话，是在哪次具体调用上说的"。入参本身不进库
// （它可能含密钥），只进摘要。
func (m *PermissionManager) Remember(sessionID string, t acp.ToolCallInfo, decision, optionID string) {
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
		add(memoryItemOf(sessionID, MemoryItem{
			Title: t.Title, Kind: t.Kind, Decision: decision, Since: since,
			Scope: MemoryScopeSession, GrantInputDigest: grantInputDigest(t.Input),
		}))
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

// offersOnlyOnceOptions 这次请求是否**明确只给了 `once` 档**（= 升权请求，不可被记忆裁决）。
//
// 判据落在**选项的 kind** 上，而不是标题/工具名里的字：标题是模型写的自由文本，
// 拿它判"这是不是升权请求"只会得到一条时灵时不灵的规则。选项是协议字段。
//
// 三档语义见 `AutoDecision` 的注释：有 always → 可记住；无 always 且有 once → 不可；都没有 → 未知。
func offersOnlyOnceOptions(req acp.PermissionRequest) bool {
	hasAlways, hasOnce := false, false
	for _, o := range req.Options {
		if strings.HasSuffix(o.Kind, "_always") {
			hasAlways = true
		}
		if strings.HasSuffix(o.Kind, "_once") {
			hasOnce = true
		}
	}
	return !hasAlways && hasOnce
}

// grantInputDigest 当初那条工具入参的摘要（`sha256` 前 16 位十六进制；空入参 → 空串）。
//
// 为什么是摘要而不是原文：工具入参里可能有路径、token、甚至命令里的密钥。审计要的是
// "**是不是同一次调用**"，那是摘要能回答的，而原文只会让库里多一份敏感数据。
//
// 为什么截到 16 位：它是**审计锚点**不是密码学承诺——碰撞在这条链上没有攻击价值
// （能改库的人已经能改一切），而 16 位让它在日志里读得下去。
func grantInputDigest(in any) string {
	if in == nil {
		return ""
	}
	b, err := json.Marshal(in)
	if err != nil || len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
