package assembly

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/sessionruntime"
)

// ——— 接库端口的适配体（阶段 0.1）———
//
// `sessionruntime.EnableStore` 只收端口，具体句柄留在装配层。本文件是**唯一**把
// `*store.Store` / `*registry.Planes` 折成那两个端口的地方，因此也是 typed-nil 判定的唯一落点：
// `var st *store.Store = nil` 装进接口**不是 nil**，会话域里那句 `if pl.st == nil` 会失效，
// memory 模式会变成一次对 nil 的方法调用。
//
// 两个适配体都遵守同一条约定：**未接库就返回 nil**（而不是返回一个包着 nil 的接口）。

// sessionRuntimeStore 把装配根持有的库句柄折成会话域端口。
func (as *assembly) sessionRuntimeStore() session.Store {
	if as.st == nil {
		return nil
	}
	return as.st
}

// planeStore 领域平面的接库动作：把**同一份**库句柄接到全部领域平面。
//
// 它是函数型端口（`sessionruntime.PlaneStore`）的实现，句柄与平面都取自装配根——
// 执行面因此不需要认识 `*registry.Planes`。
type planeStore struct {
	planes *registry.Planes
	st     *store.Store
}

// EnableStore 实现 `sessionruntime.PlaneStore`。
func (p planeStore) EnableStore() {
	if p.planes == nil || p.st == nil {
		return
	}
	p.planes.EnableStore(p.st)
}

// planeStore 折出领域平面的接库动作（未接库时返回 nil）。
func (as *assembly) planeStore() sessionruntime.PlaneStore {
	if as.st == nil || as.planes == nil {
		return nil
	}
	return planeStore{planes: as.planes, st: as.st}.EnableStore
}
