package session

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/workspace"
	wkrow "github.com/morehao/ark-mate/internal/domain/workspace/row"
)

// ——— 会话用例的**依赖端口**（P5：写面从 sessionruntime 搬进本包）———
//
// 搬过来的四条用例（CreateSession / PromptSession / CancelRun / DeliverQueuedNow）此前长在
// `run.Service` 上，于是"会话用例"有两个中心：读面在本包、写面在执行面。
// 端口把两边的**语言**对齐：用例层只声明"我要什么能力"，执行面（`Orchestrator`）实现它。
//
// 每个端口都窄到"这一条用例真正用到的那几样"，不把整个执行面交进来。

// WorkspaceFacts 工作空间面（解析目标、按路径查归属、读默认值）。
type WorkspaceFacts interface {
	ResolveTargetWorkspace(reqWorkspaceID string) (target *workspace.DefaultWorkspaceRef, assignment string, err error)
	WorkspaceIDByPath(ctx context.Context, path string) string
	WorkspaceDefaultsOf(id string) (workspace.WorkspaceDefaults, bool)
}

// WorkspaceStore 工作空间行的窄库口（只读，用于 `workspace_id` 与 `cwd` 的一致性校验）。
type WorkspaceStore interface {
	GetWorkspace(ctx context.Context, id string) (wkrow.Item, bool, error)
}

// SessionStarter 建会话（执行面：拨号/恢复/落库由它做）。
type SessionStarter interface {
	StartSession(ctx context.Context, opt session.StartOptions) (*session.CreateResult, error)
}

// PromptDelivery 投递一轮 prompt（含 delivery 三值）。
type PromptDelivery interface {
	Prompt(ctx context.Context, sessionID, text string) (int64, error)
	PromptWithDelivery(ctx context.Context, sessionID, text, delivery string) (session.PromptOutcome, error)
}

// RunCanceller 取消在途轮次（幂等）。
type RunCanceller interface {
	CancelRun(ctx context.Context, sessionID string) error
}
