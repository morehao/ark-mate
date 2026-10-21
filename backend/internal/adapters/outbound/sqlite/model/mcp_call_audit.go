package model

import "time"

// 表名常量（与实体同文件——starter 口径：不做汇总 table.go）。
const (
	// TableNameMcpCallAudit MCP 网关的**调用审计**（v31）：一次 `tools/call` 一行。
	TableNameMcpCallAudit = "mcp_call_audits"
)

// McpCallAuditEntity 一次经平台 MCP 网关的 `tools/call`（v31）。
//
// 它回答的是**复盘**问题："这个会话到底调了哪台 server 的哪个工具、成没成、等了多久"。
// 网关在 stderr 留的那一行日志回答不了它：日志按日期轮转、会话结束后就散了，无法按会话聚合。
//
// **刻意不记工具入参正文**（T2 的风险段）：入参里常带路径与令牌，写进库等于把库变成第二份
// 凭据面。要摘要的话另开一列并把摘要算法写进文档，不要"先把 args 存下来再说"。
//
// 这也不是事件流：本表没有 REST 读面、不进总线、不参与前端（T2 第一刀刻意不加事件——加事件
// 要同时动 `domain/session/events.go` 的常量、`TestEventTypeContractFrozen` 的契约快照与前端
// `packages/types/src/events.ts`，那是第二刀的事）。
type McpCallAuditEntity struct {
	// ID 自增主键。它只当**写入序**与裁剪阈值用，不对外（本表没有按 id 取一行的读面）。
	ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
	// SessionID 平台会话 id（`ses_…`）。空 = 网关没拿到会话归属（见 `mcp.GatewaySessionEnv`），
	// 这种行仍要写下来——"有一次调用但不知道属于谁"比"这一行不见了"更接近事实。
	SessionID string `gorm:"column:session_id;not null;default:''"`
	// RunID 调用发生时该会话**正在进行**的那一轮（`run_…`）；空 = 不在轮内、或有多条开轮行
	// （崩溃残轮）——这时"最近一条"只是猜测，宁可留空（同 `store.RunningRuns` 的口径）。
	RunID string `gorm:"column:run_id;not null;default:''"`
	// ServerID 目标 MCP server 的定义 id；空 = 调用在选址之前就被拒（`outcome=deny`）。
	ServerID string `gorm:"column:server_id;not null;default:''"`
	// Tool 工具名：远端调用存**原始名**（不带 `server_…__` 前缀，前缀由 ServerID 列回答）；
	// 被拒的调用存**请求里的名字**（此时它可能是一个任意字符串，也正是要留痕的东西）。
	Tool string `gorm:"column:tool;not null"`
	// Outcome 三态：`ok` / `error` / `deny`（与网关日志行的 outcome 同一套词，
	// 落库形态的唯一事实源是 `store.McpCallOutcome*`）。
	Outcome string `gorm:"column:outcome;not null"`
	// ErrorKind 失败分类（`timeout` / `canceled` / `upstream`）；空 = 成功或被拒。
	ErrorKind string `gorm:"column:error_kind;not null;default:''"`
	// LatencyMS 一次调用的墙钟耗时（毫秒）；`deny` 行是 0（没有发起远端调用）。
	LatencyMS int64 `gorm:"column:latency_ms;not null;default:0"`
	// CreatedAt 调用结束（写入）时刻，RFC3339Nano(UTC)（与全库时间列同一格式）。
	CreatedAt time.Time `gorm:"column:created_at;not null;serializer:arkmate_rfc3339;autoCreateTime:false;autoUpdateTime:false"`
}

// TableName 实现 gormdao.Entity。
func (McpCallAuditEntity) TableName() string { return TableNameMcpCallAudit }

// McpCallAuditEntityList 是 McpCallAuditEntity 切片类型（gormdao 泛型参数要求具名切片）。
type McpCallAuditEntityList []McpCallAuditEntity
