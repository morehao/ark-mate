// Package store 实现 ark-forge 的 SQLite 持久层（P1：events 为唯一事实源，
// conversations/runs/messages/permission_memories 为可重建读模型投影；设计文档
// docs/zacp-alignment-refactor-design-20260908.md「数据模型」）。
//
// 设计约束：
//   - 不用 ORM：schema_migrations 自管迁移（对齐 zacp store.go 经验：事务 + 幂等 + 版本拒绝启动）；
//   - events.seq 全局单调（meta.seq 计数行，单写者 Service.publish 在事务内分配），跨重启连续；
//   - payload 一律序列化为 JSON 文本（含权限决策留痕等结构体载荷，修复「非 map 载荷不入库」缺陷）；
//   - 迁移导入（transcript → DB）走单事务：staging + 校验 + 失败整体回滚 + meta 导入标记幂等。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/glebarez/sqlite" // 纯 Go SQLite driver（database/sql 驱动名 "sqlite"）
)

// 常量键。
const (
	// metaKeySeq 全局单调事件序号计数行（events.seq 的唯一分配源）。
	metaKeySeq = "events_last_seq"
	// metaKeyImportMarker transcript 导入标记：value = 导入目录指纹（幂等依据）。
	metaKeyImportMarker = "imported_transcripts"
)

// Store 是 SQLite 持久层的门面：持有 *sql.DB，聚合 schema 迁移与各 repository。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）SQLite 库并自动迁移到最新 schema。
// path 为文件路径；":memory:" 表示进程内临时库（测试）。
// 迁移失败或库版本高于本二进制支持版本时返回错误（拒绝启动，对齐 zacp refuse-start）。
func Open(path string) (*Store, error) {
	if path == "" {
		path = ":memory:"
	}
	dsn := path
	mem := path == ":memory:"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// SQLite 是单写者模型：单连接即天然串行化所有事务，规避 modernc 多连接竞态。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if !mem {
		// 文件库启用 WAL + busy_timeout，降低读写互斥；内存库不支持 WAL。
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA busy_timeout=5000",
			"PRAGMA synchronous=NORMAL",
		} {
			if _, err := db.Exec(pragma); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("store: pragma %q: %w", pragma, err)
			}
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: pragma foreign_keys: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DB 暴露底层句柄（读/统计/高级查询用；写路径请走本包事务方法保持单写者约束）。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Conversation 是 conversations 表的一行（DB 权威元数据；字段对齐 manager.ConversationRec + transcript header）。
type Conversation struct {
	ID          string
	AgentID     string
	Cwd         string
	RuntimeHint string // 最近/首选 runtime（执行提示，非身份，用户不可见）
	Title       string
	Pinned      bool
	Archived    bool
	Status      string
	LastSeq     int64 // 该会话已落库事件的最后 seq（events.seq 全局单调）
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Event 是 events 表的一行（facts 的唯一事实源）。
type Event struct {
	Seq            int64
	ConversationID string
	Type           string
	PayloadJSON    string
	CreatedAt      time.Time
}

// Memory 是 permission_memories 表的一行（会话级权限记忆，allow/reject 双侧对称）。
type Memory struct {
	ID             int64
	ConversationID string
	Title          string
	Kind           string
	Decision       string // allow | reject
	CreatedAt      time.Time
}

// Run 是 runs 表的一行（一轮 prompt 的物化行，turn 收口处写入）。
type Run struct {
	ID             string
	ConversationID string
	UserText       string
	Status         string // running | completed | failed | cancelled | interrupted
	ErrorCode      string
	StopReason     string
	UsageJSON      string
	FirstSeq       int64
	LastSeq        int64
	StartedAt      time.Time
	EndedAt        time.Time
}

// Message 是 messages 表的一行（消息物化：role/text + 交错时间线 + 工具最终态）。
type Message struct {
	ID              int64
	ConversationID  string
	RunID           string
	Role            string // user | assistant
	Text            string
	EventsJSON      string
	ToolDetailsJSON string
	SeqLo           int64
	SeqHi           int64
	CreatedAt       time.Time
}

// 常见错误。
var (
	// ErrAlreadyImported 目录指纹与 meta.imported_transcripts 一致（已导入，幂等跳过）。
	ErrAlreadyImported = errors.New("store: transcripts already imported (marker match)")
)

// ——— meta（计数 / 导入标记 / 任意键值） ———

func (s *Store) setMetaTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) getMetaTx(ctx context.Context, tx *sql.Tx, key string) (string, bool, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// GetMeta 读取任意 meta 键（不存在 → ("", false)）。
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta 写任意 meta 键。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// LastSeq 返回 events_last_seq（0 = 尚无事件）。
func (s *Store) LastSeq(ctx context.Context) (int64, error) {
	v, ok, err := s.GetMeta(ctx, metaKeySeq)
	if err != nil || !ok {
		return 0, err
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, fmt.Errorf("store: corrupt seq meta %q: %w", v, err)
	}
	return n, nil
}

// ——— 时间 helpers（TEXT RFC3339Nano UTC 存储，与 REST ISO 形态同源） ———

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func ts(s string) string {
	if s == "" {
		return nowRFC3339()
	}
	return s
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intBool(i int) bool { return i != 0 }
