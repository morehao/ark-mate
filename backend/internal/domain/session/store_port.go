package session

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
)

// Store 会话聚合的**持久化窄端口**（v3.1 P4）。
//
// 为什么要有它：`domain/session` 是会话聚合的拥有者，它必须能把"这一轮发生了什么"落成行；
// 但它不该认识 `internal/store` 这个门面（那是装配层交下来的实现）。把用到的 32 个方法在这
// 里声明一遍，收益是两条机器可判的事实——① 这一层**只能**做这些持久化动作（多一个方法就要先
// 改本文件，改不动就说明它不该在这一层）；② 换实现（内存镜像、别的库）只换装配，不动领域。
//
// `Store` 结构上满足它，装配根直接交下来；内存模式交 nil（各调用点据此走内存镜像）。
//
// **只许收窄，不许加宽**：新增方法前先问"这个动作是不是会话聚合自己的判决"。是 → 加；
// 不是（例如某个读模型） → 它属于 `service` 的端口，不该从这里出去。
type Store interface {
	// ——— 会话行 ———
	UpsertSession(ctx context.Context, sess model.SessionEntity) error
	SetSessionModelID(ctx context.Context, id, modelID string) error
	UpdateSessionMeta(ctx context.Context, id string, meta model.SessionMetaPatch) error
	ListSessions(ctx context.Context, archived *bool) ([]model.SessionEntity, error)
	DefaultWorkspace(ctx context.Context) (model.WorkspaceEntity, bool, error)

	// ——— 事件 ———
	EventsAfter(ctx context.Context, sessionID string, after, limit int64) ([]model.EventEntity, bool, error)
	EventsAllBySession(ctx context.Context, afterSessionID string, afterSeq, limit int64) ([]model.EventEntity, bool, error)
	AppendEvent(ctx context.Context, id, sessionID, typ string, payload any) (int64, error)

	// ——— 轮次 ———
	CreateRun(ctx context.Context, r model.RunEntity) error
	FinishRun(ctx context.Context, id string, p model.RunFinishPatch) error
	RunByID(ctx context.Context, id string) (model.RunEntity, bool, error)
	ListRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error)
	RunningRuns(ctx context.Context, sessionID string) ([]model.RunEntity, error)

	// ——— 消息读模型 ———
	ReplaceSessionMessages(ctx context.Context, sessionID string, msgs []model.MessageEntity) error
	SearchMessages(ctx context.Context, q string, limit int) ([]model.MessageHit, error)
	SearchUserMessages(ctx context.Context, sessionID, q string) ([]model.MessageTextHit, error)

	// ——— 排队输入 ———
	CreateInput(ctx context.Context, in model.InputEntity) error
	ClaimQueuedInput(ctx context.Context, id string, at time.Time) (bool, error)
	DeleteQueuedInput(ctx context.Context, sessionID, id string) (bool, error)
	HeadQueuedInput(ctx context.Context, sessionID string) (*model.InputEntity, error)
	ListQueuedInputs(ctx context.Context, sessionID string) ([]model.InputEntity, error)
	ListAllQueuedInputs(ctx context.Context) ([]model.InputEntity, error)
	ReorderQueuedInputs(ctx context.Context, sessionID string, ids []string) error
	StaleQueuedInput(ctx context.Context, id string) (bool, error)
	StaleAllQueuedInputs(ctx context.Context) (int64, error)

	// ——— 权限记忆 ———
	AddMemory(ctx context.Context, m model.PermissionMemoryEntity) error
	DeleteMemory(ctx context.Context, sessionID, title, kind string) (bool, error)
	DeleteSessionMemories(ctx context.Context, sessionID string) error
	ListAllMemories(ctx context.Context) ([]model.PermissionMemoryEntity, error)

	// ——— 模型清单（会话要认模型是否在册）———
	ListModelDefinitions(ctx context.Context) (model.ModelEntityList, error)

	// ——— 元数据（读模型重建版本号等）———
	GetMeta(ctx context.Context, key string) (string, bool, error)
	DeleteMeta(ctx context.Context, key string) error

	// ——— 用量聚合（会话用量快照的读面）———
	UsageSummary(ctx context.Context, q model.UsageQuery) (*model.UsageSummaryResult, error)
}
