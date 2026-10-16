package workspace

import (
	"time"

	"github.com/morehao/ark-mate/internal/store"
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
func (pl *Plane) SetStore(st *store.Store) { pl.st = st }

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
