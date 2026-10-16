package svcsession

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// MetaSvc 会话**元数据写面**：`title` / `pinned` / `archived` 的子集更新，以及**会话内改模型**。
//
// 为什么它算真用例：请求语义（nil 字段不更新）、"模型解析失败即上抛且**一个字节都不落库**"、
// 以及"改的是**身份**（平台行 id）而不是运行时值"这三条判据都长在这一层；域平面只提供
// "在会话自己的锁下就地改"这个动作。
type MetaSvc interface {
	// UpdateSession 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
	UpdateSession(ctx context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error)
}

// MetaDeps 元数据写面的三份依赖（窄端口）。
//
// 为什么不直接拿 store/注册表/执行面：
//
//	`ResolveModel` 要读**模型清单**（默认行 / 按 id 取行 / 行 → 运行时值的换算）——那是模型面的事实，
//	               用例层不该认识它的载体形态；
//	`ApplyModel`  是**执行面**的动作（把决定送进正在跑的那一轮），与交互动作面同形；
//	`UpdateMeta`  是持久层动作，且带一个"写库失败只告警、不阻断响应"的口径——那是存储的可靠性
//	               策略，不是这一层的判据（所以它**不返回错误**）。
type MetaDeps interface {
	// ResolveModel 把请求里的模型引用（平台行 id；空串 = 默认行）解析成
	// (平台行 id, 运行时值)。
	ResolveModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error)

	// ApplyModel 把模型投给**活跃会话**（不活跃 → applied=false，只更新元数据）。
	ApplyModel(ctx context.Context, sessionID string, cfg runtime.ModelConfig) (bool, error)

	// UpdateMeta 落库元数据（失败只告警，不阻断——所以它没有返回值）。
	UpdateMeta(ctx context.Context, id string, meta model.SessionMetaPatch)
}

// Meta 元数据写面用例。依赖事实：会话域平面（就地改 + 回读投影）+ 上面那三份。
type Meta struct {
	plane *session.Plane
	deps  MetaDeps
}

// NewMeta 造元数据写面用例（依赖事实由装配点交下来，用例实例在这里造）。
func NewMeta(plane *session.Plane, deps MetaDeps) MetaSvc { return &Meta{plane: plane, deps: deps} }

var _ MetaSvc = (*Meta)(nil)

// UpdateSession 更新会话元数据（title/pinned/archived 子集；nil 字段不更新）。
//
// 两段刻意分开，顺序不能换：
//
//	① 元数据就地改（域锁下）——顺带在**持锁区**里读出会话的 runtime 身份。它在会话建立后不再变，
//	   但"不变"不该靠约定：解锁之后再读字段，读到的可能就是另一个 goroutine 刚写过的值。
//	② 模型变更（可选）——`patch.Model` 是**平台行 id**，必须先解析成"行 + 运行时值"再投递；
//	   解析或投递失败即上抛，且**元数据那一段也不落库**（避免"界面显示新模型、实际仍用旧的"这种
//	   静默偏差）。解析要走库，所以它必须在①的持锁区**之外**（持锁读库会偶发死锁）。
func (m *Meta) UpdateSession(ctx context.Context, id string, patch dtosession.SessionPatch) (*dtosession.SessionResp, error) {
	var sessionRuntime string
	if !m.plane.Mutate(id, func(r *session.SessionRec) {
		if patch.Title != nil {
			r.Title = *patch.Title
		}
		if patch.Pinned != nil {
			r.Pinned = *patch.Pinned
		}
		if patch.Archived != nil {
			r.Archived = *patch.Archived
		}
		sessionRuntime = r.RuntimeID
		r.UpdatedAt = time.Now().UTC()
	}) {
		return nil, sessionNotFoundErr(id)
	}

	appliedModelID := patch.Model
	if patch.Model != nil {
		platformID, cfg, err := m.deps.ResolveModel(ctx, sessionRuntime, *patch.Model)
		if err != nil {
			return nil, code.FromError(err)
		}
		applied, err := m.deps.ApplyModel(ctx, id, cfg)
		if err != nil {
			return nil, code.FromError(err)
		}
		if !applied {
			// 会话不活跃（未启动/已断连）不是错误：只更新元数据，下次恢复时生效。
			glog.Debugw(ctx, "会话不在活跃集合，模型变更仅更新元数据", "session", id)
		}
		m.plane.Mutate(id, func(r *session.SessionRec) {
			// **落库的是身份**（平台行 id），不是运行时值——定案「辰」的核心。
			r.ModelID = platformID
			r.UpdatedAt = time.Now().UTC()
		})
		appliedModelID = &platformID
	}

	m.deps.UpdateMeta(ctx, id, model.SessionMetaPatch{
		Title: patch.Title, Pinned: patch.Pinned, Archived: patch.Archived, ModelID: appliedModelID,
	})
	resp := m.plane.SessionResp(id)
	return &resp, nil
}
