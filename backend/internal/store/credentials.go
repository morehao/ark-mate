package store

import (
	"context"

	"github.com/morehao/ark-mate/internal/dao"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ——— 凭据（v20：DB 是唯一权威） ———
//
// v20 之前"密钥"由 `internal/vault` 拥有（库里一份引用、`vault/` 下一份密文），于是每个读
// 密钥的地方都要先问 vault"能不能解开"，而备份与诊断面永远解释不了"到底用哪把钥匙"。
// v20 把权威搬进库：供应商密钥 = `providers.key`，MCP 引用 = `mcp_secrets`。盘上只剩一份
// **可重建**的托管文档（`<MATE_HOME>/credentials.json`，0600），它服务"注入环境"那条路，
// 不是读密钥的必经之路——删掉它不会丢任何东西。
//
// 明文进库是明确的取舍（v20 设计 §2.1/§6）：备份文件与诊断面里**会有明文**，所以
// `backups/` 0700 / 备份文件 0600 从"整洁"变成了**硬要求**。

// SetProviderKey 写某供应商的明文密钥（空串 = 清除）。返回是否命中该行。
//
// 它与档案写路径（`ProviderColumns` 的 CAS）是两次独立的写，但**同样 bump revision**：
// 否则"这个供应商改过没有"在设置页会看错——密钥也是一次档案变更。审计由调用方写，
// 且只记"密钥被设置/清除"这个事实，值绝不进 `definition_revisions`。
func (s *Store) SetProviderKey(ctx context.Context, providerID, key string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&model.ProviderEntity{}).
		Where("id = ?", providerID).
		Updates(map[string]any{
			"key":        key,
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": model.NowTS(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ProviderKey 读某供应商的明文密钥（行不存在 → ("", false, nil)；行在但没密钥 → ("", true, nil)）。
//
// "有没有密钥"于是只剩一个判据（`key` 非空），不再有"库里有引用、盘上解不开"这种中间态。
func (s *Store) ProviderKey(ctx context.Context, providerID string) (string, bool, error) {
	row, err := s.providers.GetByID(ctx, providerID)
	if err != nil {
		return "", false, err
	}
	if row == nil {
		return "", false, nil
	}
	return row.Key, true, nil
}

// SetMcpSecret 写入一个引用串的明文值（存在则覆盖，revision +1）。
//
// revision 单调是为了让"这个引用变过几次"可解释；覆盖而不是删了重建，是为了让
// 一条被清空的值（空串）也留下痕迹——"清掉密钥"与"从来没有过"在库里应当可分。
func (s *Store) SetMcpSecret(ctx context.Context, ref, value string) error {
	at := model.NowTSTime()
	return s.mcpSecrets.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ref"}},
		DoUpdates: clause.Assignments(map[string]any{
			"value":      value,
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": at,
		}),
	}).Create(&model.McpSecretEntity{Ref: ref, Value: value, Revision: 1, UpdatedAt: at}).Error
}

// McpSecret 读一个引用串的明文值（不存在 → ("", false, nil)）。
func (s *Store) McpSecret(ctx context.Context, ref string) (string, bool, error) {
	row, err := s.mcpSecrets.GetByCond(ctx, &dao.McpSecretCond{Ref: ref})
	if err != nil || row == nil {
		return "", false, err
	}
	return row.Value, true, nil
}

// ListMcpSecrets 列出全部 MCP 密钥（ref 升序）。
//
// 用 `BaseCond` 而不是 `McpSecretCond`：后者的 `Ref` 总是参与筛选（按键取一行时才是那个语义），
// 拿它来列表会退化成"只查空引用串"。
func (s *Store) ListMcpSecrets(ctx context.Context) (model.McpSecretEntityList, error) {
	return s.mcpSecrets.GetListByCond(ctx, &gormdao.BaseCond{OrderField: "ref ASC"})
}

// DeleteMcpSecret 删除一个引用串（返回是否真删掉了一行）。
func (s *Store) DeleteMcpSecret(ctx context.Context, ref string) (bool, error) {
	res := s.mcpSecrets.DB(ctx).Where("ref = ?", ref).Delete(&model.McpSecretEntity{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// CountMcpSecrets 现有密钥行数。
func (s *Store) CountMcpSecrets(ctx context.Context) (int64, error) {
	return s.mcpSecrets.CountByCond(ctx, &gormdao.BaseCond{})
}
