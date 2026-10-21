package store

// ——— mcp_call_audits 表（v31：MCP 网关的调用审计）———
//
// 这一层只有三个动作：**追加一行**、**按会话读最近若干行**、**裁剪**。它没有 REST 读面、
// 不进事件总线、不参与前端——所以也没必要长成一个聚合（没有"一条审计的完整生命周期"这回事）。
//
// 三条取舍写在这里，否则下一个人会把它们当漏做：
//
//	① **写失败只 WARN、不阻断调用**（与事件持久化的 C32 同一取舍，见 `docs/unimplemented.md`）：
//	   调用已经把结果拿回来了，因为"审计行没落地"把它变成失败，比少一条审计更糟；
//	② **保留期 + 行数上限**（`McpCallAuditRetention` / `McpCallAuditMaxRows`）：审计表不做清理
//	   会无限长，最终拖垮的是一张没人看的表。两个上限都是**平台口径**，改它们等于改"链上还能
//	   回溯多久"，改之前先看本段；
//	③ **按写入序（`id`）读、按时间只做范围裁剪**：`created_at` 是 TEXT/RFC3339Nano（末尾的 0 被
//	   裁掉 ⇒ 变长），字符串比较在同一秒内**不严格等于时间序**。所以裁剪用 `julianday()` 比，
//	   读面一律按 `id`。代价是裁剪用不上 `created_at` 索引（≤ 两万行的全扫，且由网关侧按小时
//	   节流），换来的是"边界那一秒删错一行"这种事不会发生。

import (
	"context"
	"database/sql"
	"time"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
)

const (
	// McpCallAuditRetention 审计行保留期：超过它的行在裁剪时删除（30 天）。
	McpCallAuditRetention = 30 * 24 * time.Hour
	// McpCallAuditMaxRows 审计表行数上限（先按保留期删，再按这个上限截掉最旧的行）。
	McpCallAuditMaxRows = 20000

	// 读面的默认/最大条数（复盘读一次看最近多少行；上限挡住"一次读全表"）。
	mcpAuditDefaultReadLimit = 200
	mcpAuditMaxReadLimit     = 1000
)

// 落库的 outcome 取值（与网关日志行的 outcome 同一套词；**此处是唯一事实源**）。
const (
	McpCallOutcomeOK    = "ok"
	McpCallOutcomeError = "error"
	McpCallOutcomeDeny  = "deny"
)

// McpCallRecord 一次网关工具调用的审计记录（写入值与读面值共用）。
//
// 刻意不含工具入参（见 `model.McpCallAuditEntity` 的注释）：本结构体唯一的用途是"复盘时
// 能对上哪台 server 的哪个工具、成没成、多久"。
type McpCallRecord struct {
	SessionID string
	RunID     string
	ServerID  string
	Tool      string
	Outcome   string
	ErrorKind string
	LatencyMS int64
	At        time.Time
}

// AppendMcpCallAudit 追加一行审计（零值时间 → 取当前时刻）。
//
// **调用方负责"失败不阻断"**：本方法如实返回错误，让网关那侧 WARN 出来——把它在这里降级成
// "只打日志、返回 nil"会让"写失败"这件事在测试里看不见。
func (s *Store) AppendMcpCallAudit(ctx context.Context, rec McpCallRecord) error {
	row := model.McpCallAuditEntity{
		SessionID: rec.SessionID,
		RunID:     rec.RunID,
		ServerID:  rec.ServerID,
		Tool:      rec.Tool,
		Outcome:   rec.Outcome,
		ErrorKind: rec.ErrorKind,
		LatencyMS: rec.LatencyMS,
		CreatedAt: nonZeroTime(rec.At),
	}
	return s.mcpCallAudits.Insert(ctx, &row)
}

// McpCallAuditsBySession 按会话读最近若干条审计（写入序倒序；limit <= 0 → 200，> 1000 → 1000）。
//
// **读面不进 DAO**（排序与有界窗口归 store）：这里用 `id` 排序而不是 `created_at`，理由见文件头 ③。
func (s *Store) McpCallAuditsBySession(ctx context.Context, sessionID string, limit int) ([]McpCallRecord, error) {
	if limit <= 0 {
		limit = mcpAuditDefaultReadLimit
	}
	if limit > mcpAuditMaxReadLimit {
		limit = mcpAuditMaxReadLimit
	}
	var rows model.McpCallAuditEntityList
	err := s.mcpCallAudits.DB(ctx).Model(&model.McpCallAuditEntity{}).
		Where("session_id = ?", sessionID).
		Order("id DESC").Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]McpCallRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, McpCallRecord{
			SessionID: r.SessionID, RunID: r.RunID, ServerID: r.ServerID, Tool: r.Tool,
			Outcome: r.Outcome, ErrorKind: r.ErrorKind, LatencyMS: r.LatencyMS, At: r.CreatedAt,
		})
	}
	return out, nil
}

// PruneMcpCallAudits 裁剪审计表：先删超过保留期的行，再按行数上限截掉最旧的行；返回删除行数。
//
// 第二个动作按 `id` 阈值截断（`id` 是 AUTOINCREMENT、单调），而不是"数一遍再按 id 取第 N 个"：
// 审计表只进不出、行数上限是防失控的，不需要"恰好 N 行"——差值几行不值得多一次全表排序。
//
// 幂等：无行可删时两次 DELETE 都是 0 行，返回 (0, nil)。
func (s *Store) PruneMcpCallAudits(ctx context.Context, now time.Time) (int64, error) {
	return s.pruneMcpCallAudits(ctx, now, McpCallAuditRetention, McpCallAuditMaxRows)
}

// pruneMcpCallAudits 是 `PruneMcpCallAudits` 的**带参形态**（两个上限作为入参）。
//
// 拆出来只为一件事：行数上限是两万——拿它写测试意味着种两万零一行，而那条判据要验的是
// "超过上限就截掉最旧的"，与"上限具体是多少"是两个问题。上限的取值由 `McpCallAuditMaxRows`
// 单点给出，测试只走"小上限"这一支。
func (s *Store) pruneMcpCallAudits(ctx context.Context, now time.Time, retention time.Duration, maxRows int) (int64, error) {
	db := s.db.WithContext(ctx)
	cutoff := model.FormatTime(nonZeroTime(now).Add(-retention))
	byAge := db.Exec(`DELETE FROM `+model.TableNameMcpCallAudit+` WHERE julianday(created_at) < julianday(?)`, cutoff)
	if byAge.Error != nil {
		return 0, byAge.Error
	}
	var maxID sql.NullInt64
	if err := db.Raw(`SELECT MAX(id) FROM ` + model.TableNameMcpCallAudit).Row().Scan(&maxID); err != nil {
		return byAge.RowsAffected, err
	}
	if !maxID.Valid || maxRows <= 0 || maxID.Int64 <= int64(maxRows) {
		return byAge.RowsAffected, nil
	}
	byCap := db.Exec(`DELETE FROM `+model.TableNameMcpCallAudit+` WHERE id <= ?`, maxID.Int64-int64(maxRows))
	if byCap.Error != nil {
		return byAge.RowsAffected, byCap.Error
	}
	return byAge.RowsAffected + byCap.RowsAffected, nil
}
