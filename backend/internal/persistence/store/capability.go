package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/dao"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// definitionListOrder 定义载体的展示顺序（id 升序：调用方看到的是确定的顺序）。
const definitionListOrder = "id ASC"

// 本文件是能力定义事实源（M6-1）的门面：**一次写面 = 一行 + 一条审计**。
//
// 为什么跨表不变量必须落在 store 而不是 dao：`skills.revision` 与
// `definition_revisions.seq` 是同一个提交动作的两半——只写前一半，写面就失去了"改过没有"
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
	action := model.DefinitionActionUpdate
	if expectedRev == 0 {
		action = model.DefinitionActionCreate
	}
	return s.commitDefinitionChange(ctx, model.DefinitionKindSkill, id, actor, action, bodyDigest(body), expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.skills.WithTx(tx).CreateCAS(ctx, &model.SkillEntity{
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
	return s.commitDefinitionChange(ctx, model.DefinitionKindSkill, id, actor, model.DefinitionActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.skills.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetSkillDefinition 读一条 skill 定义（不存在 → (nil, false, nil)）。
func (s *Store) GetSkillDefinition(ctx context.Context, id string) (*model.SkillEntity, bool, error) {
	row, err := s.skills.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
}

// ListSkillDefinitions 列出全部 skill 定义（id 升序）。
func (s *Store) ListSkillDefinitions(ctx context.Context) (model.SkillEntityList, error) {
	return s.skills.GetListByCond(ctx, &gormdao.BaseCond{OrderField: definitionListOrder})
}

// CountSkillDefinitions 返回 skill 定义行数（导入与装载用它判断"库里有没有"）。
func (s *Store) CountSkillDefinitions(ctx context.Context) (int64, error) {
	return s.skills.CountByCond(ctx, &gormdao.BaseCond{})
}

// UpsertMcpServerDefinition 提交一次 MCP server 定义写入（语义逐字同 UpsertSkillDefinition）。
func (s *Store) UpsertMcpServerDefinition(ctx context.Context, id, body string, expectedRev int64, actor string) (bool, error) {
	action := model.DefinitionActionUpdate
	if expectedRev == 0 {
		action = model.DefinitionActionCreate
	}
	return s.commitDefinitionChange(ctx, model.DefinitionKindMcp, id, actor, action, bodyDigest(body), expectedRev,
		func(tx *gorm.DB, now time.Time) (bool, error) {
			if expectedRev == 0 {
				return s.mcpServers.WithTx(tx).CreateCAS(ctx, &model.McpServerEntity{
					ID: id, Body: body, Revision: 1, UpdatedAt: now,
				})
			}
			return s.mcpServers.WithTx(tx).UpdateCAS(ctx, id, body, expectedRev, now)
		})
}

// DeleteMcpServerDefinition 删除一条 MCP server 定义 + 一条审计，同事务（语义同 DeleteSkillDefinition）。
func (s *Store) DeleteMcpServerDefinition(ctx context.Context, id string, expectedRev int64, actor string) (bool, error) {
	return s.commitDefinitionChange(ctx, model.DefinitionKindMcp, id, actor, model.DefinitionActionDelete, "", expectedRev,
		func(tx *gorm.DB, _ time.Time) (bool, error) {
			return s.mcpServers.WithTx(tx).DeleteCAS(ctx, id, expectedRev)
		})
}

// GetMcpServerDefinition 读一条 MCP server 定义（不存在 → (nil, false, nil)）。
func (s *Store) GetMcpServerDefinition(ctx context.Context, id string) (*model.McpServerEntity, bool, error) {
	row, err := s.mcpServers.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	return row, true, nil
}

// ListMcpServerDefinitions 列出全部 MCP server 定义（id 升序）。
func (s *Store) ListMcpServerDefinitions(ctx context.Context) (model.McpServerEntityList, error) {
	return s.mcpServers.GetListByCond(ctx, &gormdao.BaseCond{OrderField: definitionListOrder})
}

// CountMcpServerDefinitions 返回 MCP server 定义行数。
func (s *Store) CountMcpServerDefinitions(ctx context.Context) (int64, error) {
	return s.mcpServers.CountByCond(ctx, &gormdao.BaseCond{})
}

// ListDefinitionRevisions 列出某条能力的变更历史（seq 升序，含已删除的能力）。
func (s *Store) ListDefinitionRevisions(ctx context.Context, kind, definitionID string) (model.DefinitionRevisionEntityList, error) {
	return s.definitionRevision.GetListByCond(ctx, &dao.DefinitionRevisionCond{
		BaseCond:     &gormdao.BaseCond{OrderField: "seq ASC"},
		Kind:         kind,
		DefinitionID: definitionID,
	})
}

// RecentDefinitionRevisions 列出最近 limit 条变更（seq 降序；limit <= 0 视为 50）。
//
// 保留手写查询：`BaseCond` 没有 limit 这个轴（分页要走 GetPageListByCond，那会多一次
// 无意义的 COUNT），而这里要的是"降序取前 N 条"。
func (s *Store) RecentDefinitionRevisions(ctx context.Context, limit int) (model.DefinitionRevisionEntityList, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows model.DefinitionRevisionEntityList
	err := s.definitionRevision.DB(ctx).Order("seq DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// MaxDefinitionRevisionSeq 返回当前代际 rev（= 变更审计的最大 seq；空表 → 0）。
func (s *Store) MaxDefinitionRevisionSeq(ctx context.Context) (int64, error) {
	var seq int64
	// COALESCE：空表时 MAX 返回 NULL，直接扫进 int64 会报错。
	err := s.definitionRevision.DB(ctx).
		Raw(`SELECT COALESCE(MAX(seq), 0) FROM ` + model.TableNameDefinitionRevision).Scan(&seq).Error
	return seq, err
}

// commitDefinitionChange 是"定义/资源行 + 审计"的唯一提交路径。
//
// write 返回 false 表示 CAS 没中（并发写抢先 / 行不存在）：**审计也不写**，两个表都不变。
// 这条"要么都变、要么都不变"是测试里 `TestCapabilityDefinitionCAS` 钉住的核心。
//
// `digest` 由调用方算好（定义载体传 `bodyDigest(body)`，v13 的资源列传 `rowDigest(列)`）：
// 两种行形态的"那一版到底是什么"不同，但"落审计的那一列与那次提交必须原子"这一条相同。
func (s *Store) commitDefinitionChange(
	ctx context.Context,
	kind, id, actor, action string,
	digest string,
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
		return s.definitionRevision.WithTx(tx).Append(ctx, &model.DefinitionRevisionEntity{
			Kind:         kind,
			DefinitionID: id,
			Action:       action,
			Actor:        actor,
			At:           now,
			Digest:       digest,
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

// rowDigest 是**列行**那一版的规范 JSON 摘要（ADR-10；与 `bodyDigest` 同一出口形状）。
//
// 列化把 body 拿掉之后，审计仍要能回答同一个问题，所以摘要的输入换成"行的规范 JSON"：
// 键排序（`json.Marshal(map[string]string)` 天然按键排序）、**固定字段集**（由各资源的
// helper 给出）、且**不含 `revision` / 时间列**——它们是记账，不是内容：
//
//	{"base_url":…,"id":…,"name":…,"note":…,"protocol":…}                        provider
//	{"id":…,"input_modalities":…,"model_id":…,"name":…,"note":…,"provider_id":…}  model
//
// 于是"同一内容写两次 → digest 相同而 revision 变成 2"这条语义与 body 时代逐字相同。
// 空字段集 → 空串（删除路径沿用 `bodyDigest("")` 的口径）。
func rowDigest(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	b, err := json.Marshal(fields)
	if err != nil {
		// map[string]string 不可能 marshal 失败；真失败也不该把空摘要冒充成"那一版是空的"。
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
