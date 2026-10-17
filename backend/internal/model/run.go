package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameRun 每轮 prompt 的物化行。
	TableNameRun = "runs"
)

// RunEntity 是 runs 表的一行：**一轮 prompt 的实体**（v22 起开轮即落行，终态只 UPDATE）。
//
// 生命周期（设计 §D2）：
//
//	开轮：`run.started` 事件落库拿到起点位次 → INSERT（status='in_progress'、first_seq=该位次、
//	      last_seq=-1、ended_at 空）——`UNIQUE(session_id, first_seq)` 保证重放不会插出第二行；
//	终态：UPDATE（last_seq=终态位次、status/stop_reason/error_code/usage_json/ended_at）。
//
// 崩溃或进程被停时，进行中的轮**留在 status='in_progress'**（会话侧记为 interrupted）：这是事实，
// 不是缺陷——那一轮既没成功也没失败，伪造终态会污染失败轮列表与重试入口。
type RunEntity struct {
	// ID 平台铸号 `run_<26 位 ULID>`（v22 起重铸；此前是派生的 `r-<session>-<first_seq>`）。
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	// UserText 本轮的用户输入（**开轮时**写入，权威来源是 `run.started` 载荷）。
	UserText string `gorm:"column:user_text;not null"`
	// Status 轮次状态。in_progress = 进行中（v30 起单一取值；历史 `running` 行已由 v30 归一）；
	// 终态取值权威定义在 internal/manager/events.go。
	Status     string `gorm:"column:status;not null"`
	ErrorCode  string `gorm:"column:error_code;not null"`
	StopReason string `gorm:"column:stop_reason;not null"`
	UsageJSON  string `gorm:"column:usage_json;not null"`
	// FirstSeq 本轮窗口起点 = 本轮 `run.started` 事件的**会话内**位次（开轮即定，此后不变）。
	FirstSeq int64 `gorm:"column:first_seq"`
	// LastSeq 本轮窗口终点 = 终态事件的会话内位次；**-1 = 还没收口**（进行中的轮次）
	// ——0 是合法位次，不能当"未知"用（v21 起）。
	LastSeq int64 `gorm:"column:last_seq"`
	// FirstTokenMS 本轮从 `run.started` 到首个 assistant 输出（文本/思考/工具）的耗时（ms）；
	// 0 = 未记录。
	FirstTokenMS int64     `gorm:"column:first_token_ms"`
	StartedAt    time.Time `gorm:"column:started_at;not null;serializer:arkmate_rfc3339"`
	// EndedAt 终态时间；未结束 → 零值（落库为空串）。
	EndedAt time.Time `gorm:"column:ended_at;serializer:arkmate_rfc3339"`
}

// RunFinishPatch 是一次 run **收口**要写回的整组列（终态 + 窗口右端 + 首令牌耗时）。
//
// 为什么成群传而不是八个位置参数：这几列是同一件事（"这一轮到此为止"）的各个面，
// 分成八个参数时"少传一个"编译期无感（`last_seq` 与 `first_token_ms` 都是 int64），
// 而写错的后果是静默的——窗口停在 -1、或首令牌耗时恒为 0。
type RunFinishPatch struct {
	// LastSeq 本轮窗口终点（终态事件的会话内位次；进行中 = -1）。
	LastSeq int64
	// Status 终态：completed / failed / cancelled（权威定义在 internal/manager/events.go）。
	Status     string
	StopReason string
	ErrorCode  string
	UsageJSON  string
	// FirstTokenMS 本轮从起点到首个 assistant 输出的耗时（ms）；0 = 无输出/未记录。
	FirstTokenMS int64
	// EndedAt 终态时刻（零值 → 落库为空串，见 model.FormatTimeOrNow 的调用方）。
	EndedAt time.Time
}

// TableName 实现 gormdao.Entity。
func (RunEntity) TableName() string { return TableNameRun }

// RunEntityList 是 RunEntity 切片类型（gormdao 泛型参数要求具名切片）。
type RunEntityList []RunEntity
