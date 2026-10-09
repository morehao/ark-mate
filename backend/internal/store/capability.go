package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/morehao/ark-forge/internal/model"
	"gorm.io/gorm"
)

// 本文件是能力定义事实源（M6-1）的门面：**一次写面 = 一行 + 一条审计**。
//
// 为什么跨表不变量必须落在 store 而不是 dao：`skills.revision` 与
// `capability_revisions.seq` 是同一个提交动作的两半——只写前一半，写面就失去了"改过没有"
// 的判据；只写后一半，审计会指向一个没有发生过的变更。两者同事务是这一层的存在理由
// （同 events.seq 的分配与插入、transcript 导入的整体回滚）。
//
// ## 与今天（文件载体）的对应关系
//
//	今天                                    M6 之后
//	openDefinition → map[string]RawMessage  Get…Definition → body → unmarshal 成同一个 map
//	checkUnchanged（mtime+size）            条件更新 revision = expectedRev，rowsAffected == 0
//	DefinitionFile.Save（原子替换）          同事务：UPDATE 定义行 + INSERT 审计
//
// 对外契约不变：**不引入 If-Match**，`expectedRev` 是服务端内部的"读→写窗口"判据，
// 与今天 mtime 所处的位置逐字对应——前端无需改。

// UpsertSkillDefinition 提交一次 skill 定义写入（新建或覆盖）+ 一条审计，同事务。
//
// expectedRev == 0 → 新建（revision 从 1 起；已存在则返回 false）；
// expectedRev > 0  → 覆盖，且**仅当行的 revision 仍等于它**。
// 返回 false = 该提交被并发写抢先（或行已不存在）：调用方翻成同一个
// `definition_changed`(40901)——名字与码都与今天不变。
func (s *Store) UpsertSkillDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindSkill, id, actor, action, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.skills.WithTx(tx).CreateCAS(ctx, &model.SkillDefinition{
					ID: id, Body: body, Revision: 1, UpdatedAt: now,
				})
			}
			return s.skills.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteSkillDefinition 删除一条 skill 定义 + 一条审计，同事务。
//
// 按 revision CAS：**"删掉别人刚改过的那一版"是必须被挡住的动作**——它的后果比覆盖更不可逆。
func (s *Store) DeleteSkillDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindSkill, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.skills.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetSkillDefinition 读一条 skill 定义（不存在 → (nil, false, nil)）。
func (s *Store) GetSkillDefinition(ctx context.Context, id string) (*SkillDefinition, bool, error) {
	return s.skills.Get(ctx, id)
}

// ListSkillDefinitions 列出全部 skill 定义（id 升序）。
func (s *Store) ListSkillDefinitions(ctx context.Context) (model.SkillDefinitionList, error) {
	return s.skills.List(ctx)
}

// CountSkillDefinitions 返回 skill 定义行数（导入与装载用它判断"库里有没有"）。
func (s *Store) CountSkillDefinitions(ctx context.Context) (int64, error) { return s.skills.Count(ctx) }

// UpsertMcpServerDefinition 提交一次 MCP server 定义写入（语义逐字同 UpsertSkillDefinition）。
func (s *Store) UpsertMcpServerDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.CapabilityActionUpdate
	if expectedRev == 0 {
		action = model.CapabilityActionCreate
	}
	return s.commitDefinitionChange(ctx, model.CapabilityKindMcp, id, actor, action, body, expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.mcpServers.WithTx(tx).CreateCAS(ctx, &model.McpServerDefinition{
					ID: id, Body: body, Revision: 1, UpdatedAt: now,
				})
			}
			return s.mcpServers.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteMcpServerDefinition 删除一条 MCP server 定义 + 一条审计，同事务（语义同 DeleteSkillDefinition）。
func (s *Store) DeleteMcpServerDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.CapabilityKindMcp, id, actor, model.CapabilityActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.mcpServers.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetMcpServerDefinition 读一条 MCP server 定义（不存在 → (nil, false, nil)）。
func (s *Store) GetMcpServerDefinition(ctx context.Context, id string) (*McpServerDefinition, bool, error) {
	return s.mcpServers.Get(ctx, id)
}

// ListMcpServerDefinitions 列出全部 MCP server 定义（id 升序）。
func (s *Store) ListMcpServerDefinitions(ctx context.Context) (model.McpServerDefinitionList, error) {
	return s.mcpServers.List(ctx)
}

// CountMcpServerDefinitions 返回 MCP server 定义行数。
func (s *Store) CountMcpServerDefinitions(ctx context.Context) (int64, error) {
	return s.mcpServers.Count(ctx)
}

// ListCapabilityRevisions 列出某条能力的变更历史（seq 升序，含已删除的能力）。
func (s *Store) ListCapabilityRevisions(ctx context.Context, kind, capabilityID string) (model.CapabilityRevisionList, error) {
	return s.capabilityRevision.ListByCapability(ctx, kind, capabilityID)
}

// RecentCapabilityRevisions 列出最近 limit 条变更（seq 降序；limit <= 0 视为 50）。
func (s *Store) RecentCapabilityRevisions(ctx context.Context, limit int) (model.CapabilityRevisionList, error) {
	return s.capabilityRevision.ListRecent(ctx, limit)
}

// MaxCapabilityRevisionSeq 返回当前代际 rev（= 变更审计的最大 seq；空表 → 0）。
func (s *Store) MaxCapabilityRevisionSeq(ctx context.Context) (int64, error) {
	return s.capabilityRevision.MaxSeq(ctx)
}

// commitDefinitionChange 是"定义行 + 审计"的唯一提交路径。
//
// write 返回 false 表示 CAS 没中（并发写抢先 / 行不存在）：**审计也不写**，两个表都不变。
// 这条"要么都变、要么都不变"是测试里 `TestCapabilityDefinitionCAS` 钉住的核心。
func (s *Store) commitDefinitionChange(
	ctx context.Context,
	kind, id, actor, action string,
	body string,
	expectedRev int64,
	write func(tx *gorm.DB, now time.Time) (bool, error),
) (bool, error) {
	now := time.Now().UTC()
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok, err := write(tx, now)
		if err != nil || !ok {
			return err
		}
		applied = true
		return s.capabilityRevision.WithTx(tx).Append(ctx, &model.CapabilityRevision{
			Kind:         kind,
			CapabilityID: id,
			Action:       action,
			Actor:        actor,
			At:           now,
			Digest:       bodyDigest(body),
		})
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// bodyDigest 是审计里那版 body 的摘要（sha256 hex）。
//
// 审计要能回答"那一版到底是什么"，而不是只留一个自增号；删除没有"提交后的 body"，
// 故空串的摘要就是空串（调用方在删除路径上传 ""）。
func bodyDigest(body string) string {
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
