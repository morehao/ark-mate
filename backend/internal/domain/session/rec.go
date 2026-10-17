package session

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/glog"
)

// SessionRec 是进程内会话终态记录（内存镜像；sqlite 模式下与 sessions 行同源）。
//
// **身份只有 runtime**（v19）：v17 时这里写着"P0 起不再记录 runtime 身份"，本轮反过来——
// 底座由构建期固定、用户不可自定义，所以 `runtime_id` 就是唯一身份事实（它同时是 DB 列
// `sessions.runtime_id`、事件 payload 的 `runtime`、诊断面的 `runtime`）。v19 之前那半个
// `agent_id` 与它指向同一个东西，收成一个。
type SessionRec struct {
	ID string
	// Cwd 会话工作空间（canonical 绝对路径）。它同时是 ACP session/new 的 cwd 与
	// 原生权限 workspace-write 的边界（见 workspace.go）。
	Cwd string
	// WorkspaceTemporary 该工作空间是否由**平台**给出（= 默认工作空间）。
	// 落库 + 写 transcript header：**权威标志**，前端与离线回放都不得从路径形状反推。
	WorkspaceTemporary bool
	// WorkspaceID 归属的工作空间登记 id（v14；'' = 未登记/登记已删 → 读面归默认工作空间）。
	//
	// 与 Cwd 的分工（设计 ADR-2，最容易踩的一处）：Cwd 是**执行事实**（底座 ACP session 的
	// cwd、原生权限 workspace-write 的边界、不可变），WorkspaceID 是**归属事实**（左栏分组）。
	// 两者不一致是允许的（工作空间登记被删、或目录后来不可用），执行**恒**用 Cwd。
	WorkspaceID string
	// ModelID 会话引用的**平台模型行 id**（`mdl_…`；空 = 底座默认）。
	//
	// v26 起语义是"存身份、运行时值现算"（定案「辰」，设计 §5.4）：库/内存里只留平台行 id，
	// 底座认的不透明值由 adapter 每次现算（`modelValueFor`）——路由键改名后旧会话照样活得下来。
	// 它**不是** `models.model`（那是发给上游的模型名，一个外部世界的字符串）。
	//
	// 这里曾有 `ProviderID`：v19 删 agent 面后它没有任何写入点（恒空），v26 删列——
	// 供应商由**模型行**给出（`models.provider_id`），再存一份就是第二事实源。
	ModelID string
	// RuntimeID 本会话的 runtime 身份（v17 起列 `runtime_id`；v19 起它是**唯一的**身份事实）。
	RuntimeID string
	// Generation 会话**执行代际**：每次重建执行上下文（连接断开后重连 / 会话被重开）自增。
	// §6 排队输入据此判断「排队时依赖的那一轮上下文还在不在」——不等即作废（见 inputqueue.go）。
	// 只在内存：重启本身就让所有排队行作废，没有跨重启比对的语义。
	Generation int64
	// RuntimeSessionID 原生底座会话 id（P3 双 id；session id = 平台 uuid，本字段存
	// 原生 id 供 load/resume 恢复承载，v17 前叫 ProviderSessionID / `provider_session_id`。
	// memory 模式下仅在活跃集合期间可用）。
	RuntimeSessionID string
	// Fingerprint 该会话钉住的**能力指纹**（代际；空 = 未声明能力选择）。
	// 恢复路径据此回到同一代际。M3-1 起落库列 `capability_fingerprint`（v7），
	// RestoreFromStore 原样读回——否则重启后指纹变空、会话悄悄换到默认能力集。
	Fingerprint string
	// Origin 会话来源（'' = 人工；'automation:<id>' = 该 automation 触发产生）。M4 写入，M3 只落列。
	Origin string
	// CapabilityJSON 创建时解析出的**能力选定明细**（JSON；'' = 未声明 → 全量按 runtime 过滤）。
	// 与 Fingerprint 的分工：指纹回答"是不是同一套"（比较/回收用），明细回答"到底是哪几个"
	// （回显、诊断、以及指纹算法变化后的一次性迁移判据）。
	CapabilityJSON string
	Status         string
	Title          string // 会话标题（首条用户消息截断生成；可被 PATCH 覆盖）
	Pinned         bool   // 置顶（侧栏/管理面用）
	Archived       bool   // 归档（软删除；会话列表默认不展示）
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RegisterSession 登记一个新会话（已存在则覆盖）；未提供的时间落为当前 UTC。
// loadTranscripts 回灌时保留原 created_at/updated_at。
// sqlite 模式下（st != nil 且非恢复过程）同步写库。
func (pl *Plane) RegisterSession(rec *SessionRec) {
	pl.mu.Lock()
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	pl.sessions[rec.ID] = rec
	restoring := pl.restoring
	pl.mu.Unlock()
	if !restoring {
		pl.persistSession(rec)
	}
}

// bumpGeneration 会话执行代际自增（执行上下文重建时调用；见 SessionRec.Generation）。
func (pl *Plane) BumpGeneration(id string) int64 {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.sessions[id]
	if !ok {
		return 0
	}
	rec.Generation++
	return rec.Generation
}

// sessionGeneration 读会话当前执行代际（会话不存在 → 0，此时任何排队项都不匹配 → 作废）。
func (pl *Plane) SessionGeneration(id string) int64 {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.sessions[id]
	if !ok {
		return 0
	}
	return rec.Generation
}

// sessionRec 返回某会话的进程内记录副本（断点恢复/内部查询用；不存在返回 nil）。
func (pl *Plane) Rec(id string) *SessionRec {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.sessions[id]
	if !ok {
		return nil
	}
	cp := *rec
	return &cp
}

// MarkSession 更新会话终态（sqlite 模式下同步写库 status）。
func (pl *Plane) MarkSession(id, status string) {
	pl.mu.Lock()
	if r, ok := pl.sessions[id]; ok {
		r.Status = status
		r.UpdatedAt = time.Now().UTC()
	}
	pl.mu.Unlock()
	if pl.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := pl.st.UpdateSessionMeta(ctx, id, model.SessionMetaPatch{Status: &status}); err != nil {
			glog.Warnw(context.Background(), "store: mark session failed", "session", id, "err", err)
		}
	}
}

// HasSession 判断会话是否在本进程登记。
func (pl *Plane) HasSession(id string) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	_, ok := pl.sessions[id]
	return ok
}

// Lookup 取某会话记录的**副本**（`ok=false` = 本进程没登记；读面用）。
func (pl *Plane) Lookup(id string) (SessionRec, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.sessions[id]
	if !ok {
		return SessionRec{}, false
	}
	return *rec, true
}

// Mutate 在**本域的锁**下就地改一条会话记录（返回 false = 本进程没登记这条）。
//
// 回调里只许动记录字段、**不许做 I/O**：持锁读库会与本域"先写内存、再落库"的顺序互相卡住
// （这正是读面把展示字段的解析放到**释放锁之后**的原因）。
func (pl *Plane) Mutate(id string, fn func(rec *SessionRec)) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	rec, ok := pl.sessions[id]
	if !ok {
		return false
	}
	fn(rec)
	return true
}

// ByRuntimeSessionID 按**底座那边的会话 id**（带 `rt_` 壳）反查平台会话 id（查不到 = false）。
//
// 内存没登记时调用方会退到库里再查一遍（历史会话）——所以这里只回答"本进程登记过的那一条"。
func (pl *Plane) ByRuntimeSessionID(wrappedRuntimeSessionID string) (string, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for _, r := range pl.sessions {
		if r.RuntimeSessionID == wrappedRuntimeSessionID {
			return r.ID, true
		}
	}
	return "", false
}

// MutateAll 在**本域的锁**下遍历全部会话记录并就地改（回调返回 false 表示不改这一条）。
//
// 与 `Mutate` 同规矩：回调里只许动字段、不许做 I/O。它服务的是"库那一列改了，内存镜像要跟着改"
// 这条同步面——不同步的后果是实测出来的：删掉工作空间登记后立刻按默认工作空间过滤，那几个会话
// **看不到**（内存里还挂着已不存在的 id），要等重启才回来。
func (pl *Plane) MutateAll(fn func(rec *SessionRec) bool) int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	n := 0
	for _, rec := range pl.sessions {
		if fn(rec) {
			n++
		}
	}
	return n
}
