package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameEvent 事件事实源（唯一写入口见 store.AppendEvent）。
	TableNameEvent = "events"
)

// EventEntity 是 events 表的一行（facts 的唯一事实源）。
//
// **两列各司其职**（v17 / D1，2026-10-12；v21 把 `Seq` 的**值域**从"全库"收回到"会话内"）：
//
//   - `ID` 回答"**是哪一条事件**"（身份）：`evt_<26 位 ULID>`，平台铸造，永不当游标；
//   - `Seq` 回答"**它在哪**"（位次）：**会话内**位次（0 起、稠密），
//     `event_sequence(session_id, seq)` 是该会话唯一的分配源。
//
// v16 的判据是"会被单独引用 → 给 id；只被按序消费 → 用游标"，结论是 events 只被按序消费。
// 那条判据本身没错，错的是事实前提：**memory 模式下身份与位次都没随数据流动**——transcript
// 行里写了 seq，回读时却被丢掉、按行号重铸（`transcript_load.go`），于是同一个事件每次重启
// 换一个 seq，客户端书签里的 `since_seq` 随之失效。所以 v17 补齐的是 J4「身份与位置都必须
// 随数据流动到每一个载体」：transcript 升 v2 带 `id`，装载用 `PublishAt` 恢复 `id`+`seq`，
// 导入按 `id` 判重与分叉检测。
//
// id 的消费者（三条，都在已有面上）：① 帧 / `GET /sessions/:id/history` / 导出带上 `id`，
// 于是"某一帧"可被单独引用（诊断包、报障定位）；② `mate migrate` 的事件级判重（同 id 同行
// → 跳过，同 id 不同内容 → 报错，不静默写第二份）；③ memory 模式重启后同一事件的身份不变。
// **没有** `GET /events/:id`（无消费者，明确不做）。
//
// v17 把 `seq` 定成**全局**位次（`idx_events_seq` UNIQUE + `meta.events_last_seq` 取号），
// v21 **推翻了那半条**（设计 `.dsh/docs/specs/2026-10-14-data-model-v21-design.md` §1）：
// 全局序的三个"看似需要的消费者"实际都只服务"让 `PublishAt` 不倒退"，换成 per-agg 计数后
// **今天没有任何消费者**，而它的代价是"单个会话内的 seq 有洞"（会话 A 可能是 1、5、9…）——
// 用户看到的「第一条消息的 seq 为什么不是 1」正是这个症状。现在 `session.created` = 0、
// 首条 `session.user_message` = 1，与 opencode 逐字同形；「这是第几条**消息**」由
// `messages.ordinal`（v21 / D3）回答——一列一义（J5），不再让位次兼职。
type EventEntity struct {
	// ID 行身份（`evt_<26 位 ULID>`，objid.Event）。
	ID string `gorm:"column:id;primaryKey"`
	// Seq **会话内**位次（0 起、稠密）。分配源是 `event_sequence(session_id, seq)`
	// （单写者事务内 upsert 取号，见 store 的 appendEventTx）。
	//
	// 它**不再是主键**：位次由一个权威分配（唯一约束 `UNIQUE(session_id, seq)` 保证不重号），
	// 而行的身份是 `ID`。排序、比较、水位一律用它（守卫 G1 禁止按 id 游标）。
	Seq       int64  `gorm:"column:seq;not null"`
	SessionID string `gorm:"column:session_id;not null"`
	// Type 事件类型。取值权威定义在 internal/domain/session/events.go。
	Type string `gorm:"column:type;not null"`
	// PayloadJSON 事件载荷（一律序列化为 JSON 文本，含结构体载荷）。
	PayloadJSON string    `gorm:"column:payload_json;not null"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (EventEntity) TableName() string { return TableNameEvent }

// EventEntityList 是 EventEntity 切片类型（gormdao 泛型参数要求具名切片）。
type EventEntityList []EventEntity
