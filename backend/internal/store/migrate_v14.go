package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/morehao/ark-mate/internal/model"
	"gorm.io/gorm"
)

// 本文件是 **v14 的数据迁移**（设计 §3.1/§3.2）。
//
// 它做两件事，全在同一事务里（失败整体回滚，不存在"迁移了一半"）：
//
//	① DDL：新表 `workspaces`（登记项）+ `conversations.workspace_id` 列 + 索引；
//	② 数据：既有会话的 `workspace_id` 保持 `''`（= 归默认工作空间）。
//
// **为什么不在迁移里建默认工作空间**：迁移发生在 `store.Open` 内部，而那时**配置还没装载**
// （`workspace_dir` 在 config 里，装配顺序是 store → config → app）。迁移里建目录就得猜一个
// 路径，而"猜一个平台目录"正是本仓反复拒绝的事（`ResolveWorkspace` 宁报 workspace_required
// 也不回落 serve 进程目录）。所以**默认工作空间的建立归装配期**：
// `Store.BootstrapDefaultWorkspace` 在拿到配置之后调用，并且**只在登记表为空时**创建
// （设计 ADR-4：删掉它之后不再自动重建）。
//
// 由此推出一条**必须讲清的后果**：升级到 v14 的**既有安装**（已有会话、登记表空）
// 在配置装载后才拿到默认工作空间；迁移本身不建它，`MigrationNotesV14.DefaultWorkspacePath`
// 因此为空。这不是"漏了"——它是"迁移不该知道配置"的直接结果，如实登记。
//
// **为什么既有会话的归属一律留空**：设计 §3.2 的处置 —— 不把 `DISTINCT cwd` 批量登记成
// 工作空间（那会产生一批用户从未主动选过的条目）。留空即归默认工作空间（读面解析），
// 于是存量会话不会掉进"未分组"这种没有落点的状态。
//
// `backup: "v14"`：本迁移把 `conversations.workspace_id` 的**语义**从"不存在这一列"变成
// "归属默认工作空间"，是就地改写既有数据的语义。备份只管 DB 文件（vault 不在其内，而 v14
// 不触碰 vault），与 v12/v13 同一口径。

// MigrationNotesV14 v14 迁移留下的人工可读事实（落 `meta.MetaKeyMigrationNotesV14`）。
//
// 它**不承载错误**：v14 没有需要用户动手修的东西。它记的是"这次迁移把既有会话放到了哪"
// 以及"磁盘上还留着什么"，供 `mate doctor` 与排障读。
type MigrationNotesV14 struct {
	// ExistingConversations 迁移时已经存在的会话数（它们的 `workspace_id` 仍是 `''`）。
	ExistingConversations int64 `json:"existing_conversations"`
	// WorkspacesCreated 迁移时创建的登记行数（恒为 0；见本文件顶部"为什么不在迁移里建
	// 默认工作空间"）。留着这个字段是为了让"迁移什么都没建"成为**可读的零**，
	// 而不是一个要靠读代码才能得出的结论。
	WorkspacesCreated int `json:"workspaces_created"`
}

// 关于"旧版本按会话代建的 `<会话id>/` 目录"：本结构里**刻意没有**承载它的字段。
//
// 那些目录不会被删（删除用户数据不是迁移该做的事），但迁移**拿不到 `workspace_dir`**
// （见本文件顶部：配置在 store 之后装载），因此也无法诚实地登记"它们在哪"——写一个恒为空的
// 字段比不写更坏：读的人会以为查过。这件事的可读出口是部署文档（`docs/deployment.md` §2.2.1
// 明确写了旧目录不会被删、新会话不再新增这类目录），以及磁盘本身。

// migrateV14 v14 的数据步骤：既有会话归属留空 + 记一条可读事实。
func migrateV14(ctx context.Context, tx *gorm.DB, s *Store) error {
	notes := &MigrationNotesV14{}

	// ① 既有会话数（DDL 里新列带 `DEFAULT ''`，所以历史行**天然**是"归默认工作空间"，
	// 不需要 UPDATE 刷一遍——少一条全表写就少一次中断面）。
	if err := tx.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM conversations` /* 历史表名，勿跟随实体改名 */).Scan(&notes.ExistingConversations).Error; err != nil {
		return fmt.Errorf("统计既有会话数: %w", err)
	}

	// ② 登记行数必须是 0：本迁移只建表、不建行。真出现非 0 说明有人在这里插了行——
	// 那与"归属留空即默认"这条语义冲突（谁的默认？），当场报错比留个说不清的库好。
	var n int64
	if err := tx.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM workspaces` /* 历史表名，勿跟随实体改名 */).Scan(&n).Error; err != nil {
		return fmt.Errorf("统计工作空间登记: %w", err)
	}
	if n != 0 {
		return fmt.Errorf("v14 迁移后 workspaces 应为空表，实际 %d 行", n)
	}
	notes.WorkspacesCreated = 0

	notesJSON, err := json.Marshal(notes)
	if err != nil {
		return fmt.Errorf("序列化 v14 迁移说明: %w", err)
	}
	return writeMetaTx(ctx, tx, model.MetaKeyMigrationNotesV14, string(notesJSON))
}
