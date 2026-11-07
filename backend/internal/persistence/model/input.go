package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameInput 待投递的用户输入队列（§6 busy input 队列）。
	TableNameInput = "inputs"
)

// InputEntity 是一条「排队待投递」的用户输入（§6 busy input 队列）。
//
// 为什么落库：会话正忙时用户仍能提交（after_run / interrupt_then_prompt），这条输入在
// 当前轮结束前必须**活过进程重启之外的一切**——内存队列会让「提交成功但进程重启」变成静默丢失。
// 台账行同时是审计：谁在什么时候排了什么、最终投递了还是被撤下/作废。
type InputEntity struct {
	ID        string `gorm:"column:id;primaryKey"`
	SessionID string `gorm:"column:session_id;not null"`
	// Text 用户原文（投递时原样作为 prompt 发出）。
	Text string `gorm:"column:text;not null"`
	// Attachments 本轮携带的附件 id 列表（JSON 数组，如 `["att_a","att_b"]`；空串 = 无附件）。
	//
	// v38 起落库：忙时带附件排队若只存 text 会在入队时丢附件——投递时也无从还原。存的是
	// **附件 id 引用**（`att_…`），字节在附件链路（filestore）里，投递时由既有解析路径读回。
	Attachments string `gorm:"column:attachments;not null;default:''"`
	// Delivery 投递模式。取值见 InputDeliveryAfterRun / InputDeliveryInterruptThenPrompt
	// （入队路径不会写 reject：reject 是「不排队，直接报错」）。
	Delivery string `gorm:"column:delivery;not null"`
	// TargetRunID 入队时正在执行的那一轮（审计：用户排在哪一轮后面；不作投递判据——
	// 那一轮必然先结束，否则队列永远不会被消费）。
	TargetRunID string `gorm:"column:target_run_id;not null"`
	// Seq 会话内位次（v22 起；0 起、`UNIQUE(session_id, seq)`，D4）。
	//
	// 它取代 `created_at` 成为**队列顺序**的判据：时间戳同刻要靠 id 兜底，而"排第几个"是
	// 队列本身的语义，不该由两个不相关的列拼出来。`TargetRunID` 从此只是审计快照。
	Seq int64 `gorm:"column:seq;not null"`
	// Generation 入队时会话执行代际。投递前比对：不等即作废（旧队列项不得投进
	// 已重建过底座会话的上下文，见 manager 的 SessionRec.Generation）。
	Generation int64 `gorm:"column:generation;not null"`
	// Status 队列项状态。取值见 InputStatusQueued / InputStatusDelivered / InputStatusStale。
	Status    string    `gorm:"column:status;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
	// ClaimedAt 被 claim（投递）的时刻；未投递 → 零值（落库为空串）。
	ClaimedAt time.Time `gorm:"column:claimed_at;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (InputEntity) TableName() string { return TableNameInput }

// InputEntityList 是 InputEntity 切片类型（gormdao 泛型参数要求具名切片）。
type InputEntityList []InputEntity
