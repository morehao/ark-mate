package dao

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// McpCallAuditDao 访问 mcp_call_audits 表（v31：MCP 网关的一次工具调用一行）。
//
// 本 DAO 只提供泛型写原语（`Insert`）。读面（按会话倒序取最近若干行）与保留期/行数上限的
// 裁剪口径都在 `internal/adapters/outbound/sqlite/store/mcp_audit.go`——排序与有界窗口不进 DAO
// 这条纪律由 `TestDaoKeepsNoBusinessReadFace` 钉着，这里刻意不给自己留"就查这一次"的口子。
type McpCallAuditDao struct {
	*gormdao.Dao[model.McpCallAuditEntity, model.McpCallAuditEntityList, int64]
}

// NewMcpCallAuditDao 构造 McpCallAuditDao。
func NewMcpCallAuditDao(getDB gormdao.DBGetter) *McpCallAuditDao {
	return &McpCallAuditDao{Dao: newDao[model.McpCallAuditEntity, model.McpCallAuditEntityList, int64]("McpCallAuditDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *McpCallAuditDao) WithTx(tx *gorm.DB) *McpCallAuditDao {
	return &McpCallAuditDao{Dao: d.Dao.WithTx(tx)}
}
