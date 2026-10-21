package run

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"
)

// ——— P2b：工作空间域的会话事实适配器（`workspace.SessionSource` 的实现） ———
//
// 工作空间域不拥有会话（同层零 import 守卫），所以它只声明"我需要哪些事实"，这里如实交出。
//
// **本文件里的每一次调用都不得持 manager 的任何锁**：那是 P2b 前置（计划 §3.1.49）清掉的那类
// 锁序反转的另一面——workspace 域会在自己的锁下回调这里，再叠一层 manager 锁就是三条边互指。
// 守卫 `TestManagerDoesNotHoldMutexWhileReadingSessionSnapshot` 钉的是同族的前一半。

// Sessions 实现 `workspace.SessionSource`（全量会话快照 → 域侧最小值对象）。
func (s *Service) Sessions() []workspace.SessionView {
	snap := s.sessionPlane.Snapshot()
	out := make([]workspace.SessionView, 0, len(snap))
	for i := range snap {
		r := &snap[i]
		out = append(out, workspace.SessionView{
			ID: r.ID, WorkspaceID: r.WorkspaceID, Cwd: r.Cwd,
			WorkspaceTemporary: r.WorkspaceTemporary, UpdatedAt: r.UpdatedAt,
		})
	}
	return out
}

// ClearWorkspaceAssignment 实现 `workspace.SessionSource`：只清归属、不动 cwd。
//
// 用 `MutateAll`（会话域**自己取锁**）而不是"先 Snapshot 再逐条改"：后者改的是副本，
// 表现是"库里那一列清空了、内存里还挂着已删除的分组"——而它要等重启才自愈（静默）。
func (s *Service) ClearWorkspaceAssignment(workspaceID string) int {
	if workspaceID == "" {
		return 0
	}
	return s.sessionPlane.MutateAll(func(rec *session.SessionRec) bool {
		if rec.WorkspaceID != workspaceID {
			return false
		}
		rec.WorkspaceID = ""
		return true
	})
}

var _ = context.Background
