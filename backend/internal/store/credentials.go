package store

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
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
	var row model.ProviderEntity
	err := s.db.WithContext(ctx).Where("id = ?", providerID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Key, true, nil
}

// SetMcpSecret 写入一个引用串的明文值（存在则覆盖，revision +1）。
func (s *Store) SetMcpSecret(ctx context.Context, ref, value string) error {
	return s.mcpSecrets.Set(ctx, ref, value, model.NowTSTime())
}

// McpSecret 读一个引用串的明文值（不存在 → ("", false, nil)）。
func (s *Store) McpSecret(ctx context.Context, ref string) (string, bool, error) {
	row, ok, err := s.mcpSecrets.Get(ctx, ref)
	if err != nil || !ok {
		return "", false, err
	}
	return row.Value, true, nil
}

// ListMcpSecrets 列出全部 MCP 密钥（ref 升序）。
func (s *Store) ListMcpSecrets(ctx context.Context) (model.McpSecretEntityList, error) {
	return s.mcpSecrets.List(ctx)
}

// DeleteMcpSecret 删除一个引用串（返回是否真删掉了一行）。
func (s *Store) DeleteMcpSecret(ctx context.Context, ref string) (bool, error) {
	return s.mcpSecrets.Delete(ctx, ref)
}

// CountMcpSecrets 现有密钥行数。
func (s *Store) CountMcpSecrets(ctx context.Context) (int64, error) {
	return s.mcpSecrets.Count(ctx)
}
