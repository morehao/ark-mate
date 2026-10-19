package workspace

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/model"
)

// ——— P2b：装配侧要交下来的两样家当（端口） ———
//
// 工作空间域不拥有会话，也不拥有登记表：前者属 `domain/session`（同层零 import 守卫——两个域
// 互相 import 会让"这份数据归谁"没有答案），后者是持久层的事。所以本域只声明"我需要哪些事实"，
// 由装配侧一次接上。

// SessionView 会话在"工作空间视角"下的**最小事实**。
//
// 刻意不是 `session.SessionRec`：那个类型有 18 个字段，而本域只用到 5 个。收窄成值对象之后，
// 会话域加字段不会让这里跟着"顺手也多读两个"——那种顺手读正是把两个域的耦合悄悄拉紧的方式。
type SessionView struct {
	ID string
	// WorkspaceID 归属的工作空间登记 id（'' = 未登记/登记已删 → 读面归默认工作空间）。
	WorkspaceID string
	// Cwd 执行事实（ACP 的 cwd）。与 WorkspaceID 是两件事（设计 ADR-2）。
	Cwd string
	// WorkspaceTemporary 该目录是否由**平台**给出（默认工作空间）：它不是用户的目录，
	// 因此不作为"最近使用"的候选（复用无意义）。
	WorkspaceTemporary bool
	UpdatedAt          time.Time
}

// SessionSource 会话事实的来源（装配期登记）。
//
// 两个方法都**只读或只改会话侧**：本域拿到的是副本（`Sessions`）或一次受控的写动作
// （`ClearWorkspaceAssignment`），绝不持有会话域的锁去做判断——那正是 P2b 前置
// （计划 §3.1.49）清掉的那类锁序反转。
type SessionSource interface {
	// Sessions 全量会话快照（**副本**：调用方改不动会话域的状态）。
	Sessions() []SessionView
	// ClearWorkspaceAssignment 把"归属该工作空间"的会话的归属清空，返回改动条数。
	//
	// 它服务的是"登记被删"那条路：读面按 `workspace_id` 分组，而登记删掉之后那些行仍指着一个
	// 不存在的对象——不清理的表现是**重启前它们挂在已删除的分组下**，而库里那一列还是旧值。
	//
	// 为什么是"一个动作"而不是"把会话表交出来"：后者等于让本域有能力改会话的任何字段，
	// 而这里真正需要的只有这一处、且它的语义（"只清归属，不动 cwd"）必须由会话侧定义。
	ClearWorkspaceAssignment(workspaceID string) int
}

// SetStore 装配期登记库句柄（登记表读写用）。
//
// **必须在装配期调用**：构造时按值传一个当时的 `st` 会永远停在 nil，而表现是"该走库的走了内存"
// ——静默（与 `session.Plane.SetStore` 同一条教训）。
func (pl *Plane) SetStore(st WorkspaceStore) { pl.st = st }

// SetSessionSource 装配期登记会话事实来源（nil = 无会话面：`RecentWorkspaces` 返回空）。
func (pl *Plane) SetSessionSource(src SessionSource) { pl.sessions = src }

// sessionViews 取会话快照（未登记来源 → 空：离线命令没有会话面）。
func (pl *Plane) sessionViews() []SessionView {
	src := pl.sessions
	if src == nil {
		return nil
	}
	return src.Sessions()
}

// ——— 登记表载体的窄端口（v3.2 刀 6）———
//
// 工作空间域要读写自己的登记表（`workspaces` 表），但"表怎么存"不属于领域知识。
// 端口由消费方声明，装配根注入 `*store.Store` 的适配体。
//
// 为什么有 16 个方法而不是 1–3 个：这是**聚合仓储**，不是"把库交出来"。十三条读写都是
// 工作空间这一件事的操作（列表/按 id/按路径/建/改/删/排序/挪位/默认/最后使用/计数），
// 没有任何一条能去动别的聚合；三条 `Is*` 是**错误分类**，把载体的哨兵错误翻译成领域判决，
// 免得领域层认识 `store.Err*`。
//
// 方法签名与 `*store.Store` 逐字相同（除三条 `Is*`），所以适配体只需嵌一层再加三个方法——
// 那三个方法不是转发，是把"载体说这是什么错"翻成"领域该判什么"。
// WorkspaceTx 一段原子写里的三个窄操作（v3.2 刀 3）。
//
// 事务的**开合**在仓储（它才有库句柄），**顺序与判定**在这里——"默认工作空间不可删"是域的
// 策略，不该住在仓储里被所有调用方继承。
type WorkspaceTx interface {
	WorkspaceRow(ctx context.Context, id string) (*model.WorkspaceEntity, bool, error)
	ReleaseSessions(ctx context.Context, id string) (int64, error)
	DeleteRow(ctx context.Context, id string) (bool, error)
}

type WorkspaceStore interface {
	// Atomically 开一个事务执行 fn（fn 返回错误即整体回滚）。
	Atomically(ctx context.Context, fn func(WorkspaceTx) error) error
	BootstrapDefaultWorkspace(ctx context.Context, dir string) (*model.WorkspaceEntity, error)
	DefaultWorkspace(ctx context.Context) (model.WorkspaceEntity, bool, error)
	ListWorkspaces(ctx context.Context) ([]model.WorkspaceEntity, error)
	GetWorkspace(ctx context.Context, id string) (model.WorkspaceEntity, bool, error)
	WorkspaceByPath(ctx context.Context, path string) (model.WorkspaceEntity, bool, error)
	CreateWorkspace(ctx context.Context, path, title string) (model.WorkspaceEntity, error)
	RenameWorkspace(ctx context.Context, id, title string) error
	RelocateWorkspace(ctx context.Context, id, canonicalPath string) (bool, error)
	MoveWorkspace(ctx context.Context, id, beforeID string) ([]string, error)
	CountWorkspacesByID(ctx context.Context) (map[string]int64, error)
	LastUsedByWorkspaceID(ctx context.Context) (map[string]string, error)
	SetWorkspaceDefaults(ctx context.Context, id, capabilityJSON, permissionMode string) (bool, error)

	// IsPathTaken 目标路径已被别的登记项占用（→ workspace_exists）。
	IsPathTaken(err error) bool
	// IsDefaultImmutable 该登记项是默认工作空间、不可改路径/不可删。
	IsDefaultImmutable(err error) bool
	// IsNotFound 该登记项不存在。
	IsNotFound(err error) bool
}
