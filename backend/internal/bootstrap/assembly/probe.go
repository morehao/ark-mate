package assembly

import (
	"context"

	"github.com/morehao/ark-mate/internal/contract/dtomodel"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/provider"
	"github.com/morehao/ark-mate/internal/domain/skill"
)

// DiagnosticPlanes 诊断面允许读的**只读**事实（装配断言与 doctor 用）。
//
// 为什么不把 `*planes.Planes` 直接交出去（C3 §3g 第 3 条）：容器有 17 个平面与一批装配/装载
// 方法，交出去等于把"装配内部结构"变成公开面——任何一条测试都能顺手摸到本不该外露的平面，而
// 这种泄漏不会让任何守卫变红（测试从返回值拿类型，不需要 import）。这里只列今天真正被读的
// 4 条，且全是只读；要加一条 = 显式加一行，评审时看得见。
//
// 生产行为路径**不得**从这里取事实：行为面只认用例接口与端口（`Probe` 的注释同款约束）。
type DiagnosticPlanes interface {
	ListSkills() ([]skill.SkillView, error)
	AutomationByID(id string) (automation.AutomationView, bool)
	ListProviders(ctx context.Context) ([]provider.ProviderItem, error)
	RuntimeConfigStates(ctx context.Context) map[string]dtomodel.RuntimeSyncResp
}
