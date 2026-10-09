package store

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/model"
	"gorm.io/gorm"
)

// ——— 存量定义的**一次性导入**（M6-4 能力面；M6-6 起 agent 面共用） ———
//
// 与写面（Upsert*Definition）的三点不同，都是刻意的：
//
//  1. **绝不覆盖**（写面是覆盖）：导入是"把存量文件搬进来"，不是"以文件为准回滚面板上的修改"。
//     已有行一律保留并计数（Existing），需要覆盖就走写面；
//  2. **一次导入一批**（写面是一次一条）：全部行 + 审计 + marker 在一个事务里，失败整体回滚
//     ——部分导入比不导入更难排查（用户看到"一半技能回来了"）；
//  3. **审计动作是 `import`**（写面是 create/update/delete）：逐条一行，`capability_id` 指向
//     具体那条定义，于是 `ListCapabilityRevisions(kind, id)` 能回答"这一行是从哪个目录进来的"。
//     不共用一条聚合行：`capability_id` 是这张表的主查询键，聚合行谁也查不到。

// DefinitionImportItem 一条待导入的定义（调用方已逐条过闸门）。
type DefinitionImportItem struct {
	// Kind 见 model.CapabilityKindSkill / CapabilityKindMcp / CapabilityKindAgent。
	Kind string
	ID   string
	// Body 定义正文（承载未知键的完整 JSON）。
	Body string
}

// DefinitionImportResult 一次导入的逐条计数。
type DefinitionImportResult struct {
	Inserted int // 真的插进去的行
	Existing int // 库里已有、按"绝不覆盖"保留的行
}

// ImportDefinitionRows 单事务导入一批定义。
//
// `markerKey`/`marker` 非空时作为幂等依据：`meta.<markerKey>` 与 `marker` 一致 →
// `ErrAlreadyImported`（**一个字节都不写**）；不一致 → 导入结束后把 marker 写进去。
// `marker == ""` = 不做标记判据（显式 CLI 导入，每次按行判重）。
//
// marker 的键由**域**决定（`imported_capabilities` / `imported_agents` / …）：两个目录的指纹
// 各自演进，共用一个键会让"只改 agent 目录"把能力面的导入也带着重跑一遍。
func (s *Store) ImportDefinitionRows(ctx context.Context, items []DefinitionImportItem, markerKey, marker string) (DefinitionImportResult, error) {
	var res DefinitionImportResult
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if marker != "" && markerKey != "" {
			cur, ok, err := s.meta.WithTx(tx).Get(ctx, markerKey)
			if err != nil {
				return err
			}
			if ok && cur == marker {
				return ErrAlreadyImported
			}
		}
		for _, it := range items {
			inserted, err := s.createDefinitionTx(ctx, tx, it, now)
			if err != nil {
				return err
			}
			if !inserted {
				res.Existing++
				continue
			}
			res.Inserted++
			if err := s.capabilityRevision.WithTx(tx).Append(ctx, &model.CapabilityRevision{
				Kind:         it.Kind,
				CapabilityID: it.ID,
				Action:       model.CapabilityActionImport,
				Actor:        model.CapabilityActorImport,
				At:           now,
				Digest:       bodyDigest(it.Body),
			}); err != nil {
				return err
			}
		}
		if marker != "" && markerKey != "" {
			return s.meta.WithTx(tx).Set(ctx, markerKey, marker)
		}
		return nil
	})
	if err != nil {
		return DefinitionImportResult{}, err
	}
	return res, nil
}

// createDefinitionTx 在事务里**只插入缺失的行**（CreateCAS：已存在 → false，不覆盖）。
func (s *Store) createDefinitionTx(ctx context.Context, tx *gorm.DB, it DefinitionImportItem, now time.Time) (bool, error) {
	switch it.Kind {
	case model.CapabilityKindSkill:
		return s.skills.WithTx(tx).CreateCAS(ctx, &model.SkillDefinition{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	case model.CapabilityKindMcp:
		return s.mcpServers.WithTx(tx).CreateCAS(ctx, &model.McpServerDefinition{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	case model.CapabilityKindAgent:
		return s.agents.WithTx(tx).CreateCAS(ctx, &model.AgentDefinition{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	default:
		// 不可达（调用方按 model 常量构造 item）：宁可整体回滚也不要静默丢一条定义。
		return false, fmt.Errorf("store: 未知定义种类 %q（导入）", it.Kind)
	}
}
