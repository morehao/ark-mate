package sessionruntime

import (
	"context"

	"github.com/morehao/ark-mate/internal/contract/dtomodel"
)

// ——— 执行面的**接库端口**（阶段 0.1）———
//
// 改造前 `Service.EnableStore(*store.Store)` 同时接两半：会话域（本层拥有）与领域平面
// （`registry.Planes` 拥有）。那个签名让本层直接 import 了 `persistence/store` 与
// `drivers/registry` 两个具体适配器——层表允许，但"执行面不认具体适配器"这条不变量没有落到
// 类型上：谁把 `*store.Store` 换成另一份存储实现，本层得跟着改。
//
// 现在拆成两个端口，**都由装配层实现**（它同时持有库句柄与平面集合）：
//   - 会话域端口：`domain/session.Store`。**不在这里另起别名**——一个类型只有一处定义
//     （`TestWorkspaceImplementationLivesInDomain` 的别名归零判据），调用点直接用域类型；
//   - `PlaneStore`：把**同一份**库句柄接到全部领域平面的动作。
//
// 为什么 PlaneStore 是函数型端口而不是接口：它要的不是句柄，而是"接库这件事"本身。
// 装配层把 `*registry.Planes` 与 `*store.Store` 一起封进来，本层只负责在正确的时机调用它，
// 于是既不需要 import registry，也不需要 import store。
//
// **typed-nil 判定在装配层**（`bootstrap/assembly/ports.go`）：`nil` 是"未接库"的合法取值
// （memory 模式），不能把一个装了 nil 具体指针的接口当成有值传进来。

// PlaneStore 领域平面接库动作（装配层实现；nil = 本次装配没有领域平面或未接库）。
//
// 调用点只有一个（`Service.EnableStore`），与会话域端口**同一次**传入：
// 漏接任一半的表现都是静默的（该走库的走了文件、或反过来——定义只写进内存、凭据读不到库）。
type PlaneStore func()

// ——— 模型读面/同步面的端口（阶段 0.3）———
//
// 改造前 `ModelFace` 直接内嵌 `*registry.ModelViews`：执行面因此认识 registry 的组合实现，
// 而 `ModelFace` 的方法集由"嵌了谁"决定——换一份读面实现就得改这里。
//
// 现在它只认识三条方法（`model.ModelSvc` / `Host` 真正消费的那几条）：读面、同步、同步触发。
// 装配层把 `registry.NewModelViews(planes)` 注进来（见 `bootstrap/assembly/services.go`）。
type ModelViews interface {
	// ModelsView 组装 `GET /models` 读面（平台清单 + 聚合生效结论 + 逐 runtime 明细）。
	ModelsView(ctx context.Context, runtimeID string, refresh bool) (*dtomodel.ModelsResp, error)
	// SyncModels 重新同步清单（空 = 全部已注册 runtime）。
	SyncModels(ctx context.Context, runtimeID string) (*dtomodel.ModelSyncResp, error)
	// TrySyncModels 提交后立刻同步，返回一句给用户看的话（空 = 无需说明）。
	TrySyncModels(ctx context.Context) string
}
