package store

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-mate/internal/model"
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
	// Kind 见 model.CapabilityKindSkill / CapabilityKindMcp / CapabilityKindAgent /
	// CapabilityKindAutomation。
	Kind string
	ID   string
	// Body 定义正文（承载未知键的完整 JSON）。
	//
	// **kind=provider 时不用它**：v13 起 provider 是资源表（列 + 无 body），列载荷走 Provider。
	// 留着这个字段是给四个「定义载体」表用的（它们的正文是用户自由文档）。
	Body string
	// Provider 仅 kind=provider 时有意义：列化后的内容列。
	//
	// 单独一个字段而不是把列塞进 `Body`：Body 是"正文 JSON"，而 provider 的键集合封闭且有
	// 库级约束（`CHECK (protocol IN ('openai'))`）——把列值编码回 JSON 再在 store 里解回来，
	// 只是把已经消失的 body 重新造一遍。
	Provider *ProviderColumns
	// AutomationRuntime 仅 kind=automation 时有意义：定时任务的运行态在 DB 载体里是**列**，
	// 而存量文件里它是同文件的一部分（`last_fired_at`/`recent_runs`/…）。导入必须把两部分
	// **同一事务**写进去，否则会出现"定义进来了、运行史没进来"的半截状态。
	// nil = 该定义没有任何运行态（未触发过），落成默认列值。
	AutomationRuntime *AutomationRuntimeState
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
			digest := bodyDigest(it.Body)
			if it.Kind == model.CapabilityKindProvider && it.Provider != nil {
				// provider 是资源表：审计摘要取**列行的规范 JSON**（ADR-10），不是已消失的 body。
				digest = it.Provider.digest()
			}
			if err := s.capabilityRevision.WithTx(tx).Append(ctx, &model.CapabilityRevisionEntity{
				Kind:         it.Kind,
				CapabilityID: it.ID,
				Action:       model.CapabilityActionImport,
				Actor:        model.CapabilityActorImport,
				At:           now,
				Digest:       digest,
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
		return s.skills.WithTx(tx).CreateCAS(ctx, &model.SkillEntity{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	case model.CapabilityKindMcp:
		return s.mcpServers.WithTx(tx).CreateCAS(ctx, &model.McpServerEntity{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	case model.CapabilityKindAgent:
		return s.agents.WithTx(tx).CreateCAS(ctx, &model.AgentEntity{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now,
		})
	case model.CapabilityKindProvider:
		if it.Provider == nil {
			// 不可达（manager 侧构造 item 时必填）：宁可整体回滚也不要写一行空档案。
			return false, fmt.Errorf("store: provider 导入缺少列载荷（%q）", it.ID)
		}
		return s.providers.WithTx(tx).CreateCAS(ctx, newProviderRow(*it.Provider, 1, now, now))
	case model.CapabilityKindAutomation:
		row := &model.AutomationEntity{
			ID: it.ID, Body: it.Body, Revision: 1, UpdatedAt: now, RecentRuns: "[]",
		}
		if it.AutomationRuntime != nil {
			row.LastFiredAt = it.AutomationRuntime.LastFiredAt
			row.LastStatus = it.AutomationRuntime.LastStatus
			row.LastReason = it.AutomationRuntime.LastReason
			row.LastSessionID = it.AutomationRuntime.LastSessionID
			row.ConsecutiveFailures = it.AutomationRuntime.ConsecutiveFailures
			if it.AutomationRuntime.RecentRuns != "" {
				row.RecentRuns = it.AutomationRuntime.RecentRuns
			}
		}
		return s.automations.WithTx(tx).CreateCAS(ctx, row)
	default:
		// 不可达（调用方按 model 常量构造 item）：宁可整体回滚也不要静默丢一条定义。
		return false, fmt.Errorf("store: 未知定义种类 %q（导入）", it.Kind)
	}
}
