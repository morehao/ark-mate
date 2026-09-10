// Package store 是 ark-forge 的 SQLite 持久层门面（gorm + 纯 Go dialector）。
//
// 设计约束：
//   - schema_migrations 自管迁移（对齐 zacp store.go 经验：事务 + 幂等 + 版本拒绝启动）；
//   - events.seq 全局单调（meta.events_last_seq 计数行，单写者 Service.publish 在事务内分配），跨重启连续；
//   - payload 一律序列化为 JSON 文本（含权限决策留痕等结构体载荷，修复「非 map 载荷不入库」缺陷）；
//   - 迁移导入（transcript → DB）走单事务：staging + 校验 + 失败整体回滚 + meta 导入标记幂等。
//
// 分层：本包**只做三件事**——持有句柄、兜住跨表事务、暴露读模型 API。
// 单表读写一律经 `internal/dao`（gormdao），实体定义在 `internal/model`；
// 本包不自建 SQL 拼接，也不在 dao 之外碰 `*gorm.DB` 的裸查询。
//
// 为什么还需要这一层（而不是让调用方直接用 dao）：这里承载的全是**跨表不变量**——
// 全局序号分配与事件插入必须同事务、导入必须整体回滚、会话水位随事件推进。
// 这些语义横跨多个 DAO，落进任何一个 DAO 都会让它变成「不是 DAO 的东西」。
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/morehao/ark-forge/internal/dao"
	"github.com/morehao/ark-forge/internal/model"
	"github.com/morehao/ark-forge/pkg/dbclient"
	"gorm.io/gorm"
)

// 实体类型别名：权威定义在 internal/model（gorm 映射 + 列与取值约定）。
// 这里做别名而不是复制结构体，是为了让调用方继续写 `store.Conversation` 的同时
// 不必在两层之间来回搬运字段——两套等价结构体迟早会漂移。
type (
	// Conversation 会话权威元数据行。
	Conversation = model.Conversation
	// Event 事件事实源行。
	Event = model.Event
	// Memory 会话级权限记忆行。
	Memory = model.Memory
	// Run 单轮 prompt 的物化行。
	Run = model.Run
	// Message 消息物化投影行。
	Message = model.Message
	// ConversationMetaPatch 会话元数据子集更新（nil = 不更新）。
	ConversationMetaPatch = model.ConversationMetaPatch
)

// 常见错误。
var (
	// ErrAlreadyImported 目录指纹与 meta.imported_transcripts 一致（已导入，幂等跳过）。
	ErrAlreadyImported = errors.New("store: transcripts already imported (marker match)")
)

// Store 是持久层门面：持有 gorm 句柄，聚合 schema 迁移与各领域 DAO。
type Store struct {
	db *gorm.DB

	conversations *dao.ConversationDao
	events        *dao.EventDao
	runs          *dao.RunDao
	messages      *dao.MessageDao
	memories      *dao.MemoryDao
	meta          *dao.MetaDao
	migrations    *dao.MigrationDao
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
		db:            db,
		conversations: dao.NewConversationDao(getDB),
		events:        dao.NewEventDao(getDB),
		runs:          dao.NewRunDao(getDB),
		messages:      dao.NewMessageDao(getDB),
		memories:      dao.NewMemoryDao(getDB),
		meta:          dao.NewMetaDao(getDB),
		migrations:    dao.NewMigrationDao(getDB),
	}
}

// DB 暴露底层 gorm 句柄（调试/高级查询用；写路径请走本包方法以保持跨表不变量）。
func (s *Store) DB() *gorm.DB { return s.db }

// Close 关闭数据库。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ——— meta（计数 / 导入标记 / 任意键值） ———

// GetMeta 读取任意 meta 键（不存在 → ("", false, nil)）。
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	return s.meta.Get(ctx, key)
}

// SetMeta 写任意 meta 键。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.meta.Set(ctx, key, value)
}

// LastSeq 返回 events_last_seq（0 = 尚无事件）。
func (s *Store) LastSeq(ctx context.Context) (int64, error) {
	v, ok, err := s.meta.Get(ctx, model.MetaKeyEventsLastSeq)
	if err != nil || !ok {
		return 0, err
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, fmt.Errorf("store: corrupt seq meta %q: %w", v, err)
	}
	return n, nil
}
