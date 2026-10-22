package sessionruntime

import (
	"context"
	"github.com/morehao/ark-mate/internal/core/failure"
	"time"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 会话**元数据写面**的三份依赖（`svcsession.MetaDeps` 的实现，P4a 正文第四十四刀）———
//
// 适配器留在 `manager` 的理由与交互动作面同形：端口的三个签名只吃**域/pkg 类型**
// （`runtime.ModelConfig` / `model.SessionMetaPatch`），所以它能在不认识用例层的地方实现
// （层守卫不许 manager import `service/svc*`）。

// ResolveModel 把请求里的模型引用解析成 (平台行 id, 运行时值)。
//
// 实现是 manager 既有的那一段（默认行 / 按 id 取行 / 行 → 运行时值换算，见 `model_list.go`）：
// 这条链要读模型清单（store）与供应商面，属模型面的判据，用例层只认端口。
func (s *Service) ResolveModel(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error) {
	return s.planes.Providers.ResolveSessionModel(ctx, runtimeID, modelID)
}

// ApplyModel 把模型投给活跃会话（不活跃 → false，不报错）。
//
// `orch == nil` 这里是**显式的错误**而不是让它 panic：搬过来之前那一支是 nil 解引用
// （`s.orch.ApplySessionModel` 会炸），而"服务尚未装配编排器"是这一层能说清的事实。
func (s *Service) ApplyModel(ctx context.Context, sessionID string, cfg runtime.ModelConfig) (bool, error) {
	if s.orch == nil {
		return false, failure.NewError(failure.ClassService, "orchestrator_unavailable", "服务尚未装配编排器", nil)
	}
	return s.orch.ApplySessionModel(ctx, sessionID, cfg)
}

// UpdateMeta 落库元数据：**失败只告警、不阻断响应**（所以端口签名没有返回值）。
//
// 两个细节原样保留：脱离请求取消（`WithoutCancel`——请求断了不该让这次已经生效的元数据变更丢在
// 半路）与 3 秒上限（写库卡住不能拖住 HTTP 响应）。
func (s *Service) UpdateMeta(ctx context.Context, id string, meta model.SessionMetaPatch) {
	if s.planes.Resources.Store() == nil {
		return
	}
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.planes.Resources.Store().UpdateSessionMeta(ctx2, id, meta); err != nil {
		glog.Warnw(ctx, "store: update session meta failed", "session", id, "err", err)
	}
}
