// Package store 是 ark-mate 的 SQLite 持久层门面（gorm + golib 的 SQLite dialector；v1.32.22 起是纯 Go 的 ncruces 方言，不依赖 CGO）。
//
// 设计约束：
//   - schema_migrations 自管迁移（对齐 zacp store.go 经验：事务 + 幂等 + 版本拒绝启动）；
//   - events.seq 是**会话内**位次（v21；`event_sequence(session_id, seq)` 计数行在单写者事务内分配），
//     跨重启连续、会话内稠密（0 起）；
//   - payload 一律序列化为 JSON 文本（含权限决策留痕等结构体载荷，修复「非 map 载荷不入库」缺陷）；
//   - 迁移导入（transcript → DB）走单事务：staging + 校验 + 失败整体回滚 + meta 导入标记幂等。
//
// 分层（v3.3 起）：
//
//	internal/persistence/dao   列词汇（`XxxCond`）+ gormdao 泛型方法 + 机械原语（KV / 取号 / CAS / 只追加插入）
//	internal/persistence/store **业务口径**：排序、筛选、状态迁移、聚合，以及跨表事务
//
// 也就是说：DAO **不认识业务**（"展示顺序是什么"、"谁是队首"、"哪些行算进行中"都不在那边），
// 它只回答"这张表有哪些列可用于筛选"；具体查询由本包用 `GetByCond`/`GetListByCond`/
// `CountByCond`/`UpdateMap` 拼出来。实体定义在 `internal/persistence/model`。
//
// 本包允许出现为**跨表/有界窗口/聚合**服务的裸查询（`s.db` 或 `xxxDao.DB(ctx)`）；
// 单表的简单读写仍然优先走泛型方法 + Cond。
//
// 实体类型用 `model.*Entity`（`SessionEntity` / `RunEntity` / …），本包**不再 re-export**
// ——一个表行类型只能有一个名字（v15 的 Q8：别名块已删；别名与目标同名等于什么都没省）。
//
// 为什么还需要这一层（而不是让调用方直接用 dao）：这里承载的除了单表读写口径，还有
// **跨表不变量**——位次分配与事件插入必须同事务、导入必须整体回滚、会话水位随事件推进。
// 这些语义横跨多个 DAO，落进任何一个 DAO 都会让它变成「不是 DAO 的东西」。
package store

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/infra/dbclient"
	"github.com/morehao/ark-mate/internal/persistence/dao"
	"gorm.io/gorm"
)

// 常见错误。
var (
	// ErrAlreadyImported 目录指纹与 meta.imported_transcripts 一致（已导入，幂等跳过）。
	ErrAlreadyImported = errors.New("store: transcripts already imported (marker match)")
	// ErrAttachmentNotFound 附件行不存在（fail-closed 出口用；svcattachment 映射成 attachment_not_found）。
	ErrAttachmentNotFound = errors.New("store: attachment not found")
)

// Store 是持久层门面：持有 gorm 句柄，聚合 schema 迁移与各领域 DAO。
type Store struct {
	db *gorm.DB
	// path 库文件路径（"" 或 ":memory:" = 进程内临时库）。
	// 迁移备份与 doctor 的"该恢复哪个文件"都要用它，所以它必须在 Store 上而不只是 Open 的入参。
	path string
	// migrationBackupPath 本次进程启动时那次迁移备份的路径（空 = 本次没迁移过）。
	migrationBackupPath string
	// destructiveRebuildNote 本进程真的执行过 v15 的**破坏性重建**时记一句话（否则空）。
	//
	// 它**不是**"给内核加能力"（§8.3.2 C1）：不改变任何迁移决策、不可配置、不可调用——只是一条
	// "刚刚发生了什么"的观察值。§8.5 R2 要求启动时打一条 WARN 把这件事说出来，而"7 张表被清空"
	// 绝不能只体现在那几句 `DROP TABLE` 里：用户看到的应当是**日志里的一句话**。
	destructiveRebuildNote string

	sessions       *dao.SessionDao
	events         *dao.EventDao
	eventSequences *dao.EventSequenceDao
	runs           *dao.RunDao
	messages       *dao.MessageDao
	memories       *dao.PermissionMemoryDao
	inputs         *dao.InputDao
	meta           *dao.MetaDao
	migrations     *dao.SchemaMigrationDao

	// 能力定义事实源（M6-1）：两张定义表 + 一张只追加的变更审计。
	skills             *dao.SkillDao
	mcpServers         *dao.McpServerDao
	automations        *dao.AutomationDao
	experts            *dao.ExpertDao
	definitionRevision *dao.DefinitionRevisionDao

	// 应用定义（一期 A+Slot，v40）：类型化多列 apps 本体 + app_members 归属关联表。
	// 与上面那组"body + revision"窄列定义表不同——apps 无 revision 列，CAS 以 definition_revisions.seq
	// 为准（见 dao/app.go 与 store/app.go）。
	apps       *dao.AppDao
	appMembers *dao.AppMemberDao

	// 设置 · 模型（v11）：供应商凭据 + 平台模型清单。
	providers *dao.ProviderDao
	models    *dao.ModelDao

	// 凭据（v20）：库是唯一权威——供应商明文密钥 + MCP 引用串的明文值。
	mcpSecrets *dao.McpSecretDao

	// 工作空间登记（v14）：用户的工作目录这条线的持久身份。
	workspaces *dao.WorkspaceDao

	// 附件业务索引（v32）：会话归属 + 投递形态 + 生命周期；字节落盘去重归 golib filestore。
	attachments *dao.AttachmentDao

	// 资料库目录树（v33）：library_nodes（树）/ library_attachments（节点↔附件）/ library_tables（表格）。
	// 字节仍走 attachments/filestore；本组只承载目录归属与节点语义。
	libraryNodes       *dao.LibraryNodeDao
	libraryAttachments *dao.LibraryAttachmentDao
	libraryTables      *dao.LibraryTableDao

	// 产物溯源登记（v34）：artifacts。字节仍走 filestore；本组只承载溯源/归属/快照。
	artifacts *dao.ArtifactDao

	// 内部模型调用流水（v37）：llm_calls。一次调用一行，会话/旁路共用，session_id 可空。
	llmCalls *dao.LLMCallDao
}

// Open 打开（必要时创建）SQLite 库并自动迁移到最新 schema。
// path 为文件路径；空串或 ":memory:" 表示进程内临时库（测试）。
// 迁移失败或库版本高于本二进制支持版本时返回错误（拒绝启动，对齐 zacp refuse-start）。
func Open(path string) (*Store, error) {
	db, err := dbclient.Open(path)
	if err != nil {
		return nil, err
	}
	s := New(db)
	s.path = path
	if err := s.Migrate(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// New 用已有句柄构造 Store，并把各 DAO 绑定到该句柄。
//
// 不做迁移：迁移由 Open 或显式 Migrate 负责，便于测试先建库再单独验证迁移行为。
func New(db *gorm.DB) *Store {
	getDB := func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) }
	return &Store{
		db:             db,
		sessions:       dao.NewSessionDao(getDB),
		events:         dao.NewEventDao(getDB),
		eventSequences: dao.NewEventSequenceDao(getDB),
		runs:           dao.NewRunDao(getDB),
		messages:       dao.NewMessageDao(getDB),
		memories:       dao.NewMemoryDao(getDB),
		inputs:         dao.NewInputDao(getDB),
		meta:           dao.NewMetaDao(getDB),
		migrations:     dao.NewMigrationDao(getDB),

		skills:             dao.NewSkillDao(getDB),
		mcpServers:         dao.NewMcpServerDao(getDB),
		automations:        dao.NewAutomationDao(getDB),
		experts:            dao.NewExpertDao(getDB),
		definitionRevision: dao.NewDefinitionRevisionDao(getDB),

		apps:       dao.NewAppDao(getDB),
		appMembers: dao.NewAppMemberDao(getDB),

		providers:  dao.NewProviderDao(getDB),
		models:     dao.NewModelDao(getDB),
		mcpSecrets: dao.NewMcpSecretDao(getDB),

		workspaces: dao.NewWorkspaceDao(getDB),

		attachments: dao.NewAttachmentDao(getDB),

		libraryNodes:       dao.NewLibraryNodeDao(getDB),
		libraryAttachments: dao.NewLibraryAttachmentDao(getDB),
		libraryTables:      dao.NewLibraryTableDao(getDB),

		artifacts: dao.NewArtifactDao(getDB),

		llmCalls: dao.NewLLMCallDao(getDB),
	}
}

// DB 暴露底层 gorm 句柄（调试/高级查询用；写路径请走本包方法以保持跨表不变量）。
func (s *Store) DB() *gorm.DB { return s.db }

// Path 库文件路径（内存库 = 空串）。
//
// 存在的理由是**权限面**：v20 起库内含明文密钥，于是"库文件与它的迁移备份属于密钥面"这件事要能被
// 诊断面问出来（`mate doctor` 按 `<db 目录>/backups/*.bak-*` 展开备份）。内存库没有落点，返回空串
// ——调用方据此跳过，而不是拿到一个看起来像路径的东西。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// BackupDir 迁移备份目录（`<db 目录>/backups`，v19 起）。
//
// 与 `Path()` 同一个理由：诊断面要按密钥面检查它，而它的位置由库文件位置派生（不是配置项）。
func (s *Store) BackupDir() string {
	if s == nil || s.path == "" {
		return ""
	}
	return s.backupDir()
}

// Close 关闭数据库。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ——— meta（导入标记 / 目录级偏好 / 任意键值） ———

// GetMeta 读取任意 meta 键（不存在 → ("", false, nil)）。
//
// meta 是"目录级/进程级配置与标记"的收口（导入标记、目录级偏好、读模型重建标记等）。
// 事件号序**不在这里**：v21 起它是每会话一行的 `event_sequence`（见 events.go 的 appendEventTx）。
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	return s.meta.Get(ctx, key)
}

// SetMeta 写任意 meta 键。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.meta.Set(ctx, key, value)
}

// DeleteMeta 删任意 meta 键（不存在时是 no-op；读模型重建标记消费后清掉用它）。
func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	return s.meta.Delete(ctx, key)
}
