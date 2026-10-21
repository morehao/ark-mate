package workspaces

import (
	"context"
	"errors"

	"github.com/morehao/ark-mate/internal/domain/workspace"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
)

// workspaceStore 把登记表载体适配成工作空间域声明的端口（`workspace.WorkspaceStore`）。
//
// 十三读写法全部由内嵌的 `*store.Store` **提升**而来（签名逐字相同，不做转发），
// 这里只补三条 `Is*`——它们不是转发，是把载体的哨兵错误翻译成领域判决：
// 领域层因此不必认识 `store.ErrWorkspace*`（那三个错误的身份只属于持久层）。
//
// 为什么翻译放在适配体里而不是让 store 直接返回领域错误：`store` 不认识 `internal/domain`
// （v3.1 层表把这条边钉成非法），而让领域认识 `store` 同样不行——两边各退一步，
// 翻译就只能落在同时看得见两侧的地方。
type workspaceStore struct{ *store.Store }

// NewStore 把登记表载体收窄成工作空间域声明的端口（装配点注入用；未接库时调用方传 nil，
// 本函数不替它判——typed-nil 的判定只落在装配根一处）。
func NewStore(st *store.Store) workspace.WorkspaceStore { return workspaceStore{Store: st} }

// IsPathTaken 目标路径已被别的登记项占用。
func (w workspaceStore) IsPathTaken(err error) bool {
	return errors.Is(err, store.ErrWorkspacePathTaken)
}

// IsDefaultImmutable 该登记项是默认工作空间，路径与存在性都不可改。
func (w workspaceStore) IsDefaultImmutable(err error) bool {
	return errors.Is(err, store.ErrWorkspaceDefaultImmutable)
}

// Atomically 一段跨聚合原子写（v3.2 刀 3）。
//
// 一次**可赋值收窄**：仓储的句柄是 `*store.WorkspaceTx`，领域声明的是
// `workspace.WorkspaceTx`（三个方法的窄接口）。Go 的返回类型不协变，所以闭包的参数类型
// 只能在这里改一次形——句柄本身结构上已经满足领域接口。
func (w workspaceStore) Atomically(ctx context.Context, fn func(workspace.WorkspaceTx) error) error {
	return w.Store.Atomically(ctx, func(tx *store.WorkspaceTx) error { return fn(tx) })
}

// IsNotFound 该登记项不存在。
func (w workspaceStore) IsNotFound(err error) bool { return errors.Is(err, store.ErrWorkspaceNotFound) }

// 编译期确认适配体确实满足端口（端口一旦加方法，这里先红）。
var _ workspace.WorkspaceStore = workspaceStore{}
