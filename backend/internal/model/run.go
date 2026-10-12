package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameRun 每轮 prompt 的物化行。
	TableNameRun = "runs"
)

// RunEntity 是 runs 表的一行（一轮 prompt 的物化行，run 收口处写入）。
type RunEntity struct {
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	UserText  string `gorm:"column:user_text;not null"`
	// Status 轮次终态。取值权威定义在 internal/manager/events.go。
	Status     string `gorm:"column:status;not null"`
	ErrorCode  string `gorm:"column:error_code;not null"`
	StopReason string `gorm:"column:stop_reason;not null"`
	UsageJSON  string `gorm:"column:usage_json;not null"`
	FirstSeq   int64  `gorm:"column:first_seq"`
	LastSeq    int64  `gorm:"column:last_seq"`
	// FirstTokenMS 本轮从用户消息到首个 assistant 输出（文本/思考/工具）的耗时（ms）；0 = 未记录。
	FirstTokenMS int64     `gorm:"column:first_token_ms"`
	StartedAt    time.Time `gorm:"column:started_at;not null;serializer:arkmate_rfc3339"`
	// EndedAt 终态时间；未结束 → 零值（落库为空串）。
	EndedAt time.Time `gorm:"column:ended_at;serializer:arkmate_rfc3339"`
}

// TableName 实现 gormdao.Entity。
func (RunEntity) TableName() string { return TableNameRun }

// RunEntityList 是 RunEntity 切片类型（gormdao 泛型参数要求具名切片）。
type RunEntityList []RunEntity
