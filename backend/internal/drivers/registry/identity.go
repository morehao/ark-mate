package registry

import (
	"context"
	"sync"

	pkgidentity "github.com/morehao/ark-mate/internal/core/identity"
	"github.com/morehao/ark-mate/internal/core/resource"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 产品身份的交付面（部署级常量） ———
//
// 身份不是资源：不落库、没有清单、没有凭据，一个二进制只对应一个产品身份。所以它不进
// `Resources`（那一层的每一件事都是"用户声明的资源向底座交付"），而是单独一小块：
//
//	Spec      —— 本进程**生效**的身份（模式作用之后的两个面）；
//	Outcome   —— 最后一次交付的结论（诊断/日志用；不进 `/meta`，关于面板不显示执行引擎）；
//	Deliver   —— 把 Spec 交给构建期选定的那个底座（失败**不阻塞启动**：首轮块是兜底）。
//
// 为什么 Spec 要有读取口而不是把值直接塞给会话域：装配顺序是"先造编排器、后跑装配步骤"，
// 而身份的最终形态要等模式算完才知道。读取口让"会话在第一个 prompt 时才问身份"成为可能，
// 也保证 `/meta`、日志与首轮块读的是**同一份**事实。
type IdentityDelivery struct {
	reg *runtime.Registry

	mu      sync.RWMutex
	spec    pkgidentity.Spec
	outcome resource.Outcome
}

// NewIdentityDelivery 构造身份交付面（不读盘、不写盘；交付发生在装配步骤里）。
func NewIdentityDelivery(reg *runtime.Registry) *IdentityDelivery {
	return &IdentityDelivery{reg: reg}
}

// SetSpec 记下本进程生效的身份（装配期一次；交付步骤也经它落值）。
func (d *IdentityDelivery) SetSpec(s pkgidentity.Spec) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.spec = s
}

// Spec 本进程生效的身份（未装配 = 零值，两个面都为空 ⇒ 会话不注入）。
func (d *IdentityDelivery) Spec() pkgidentity.Spec {
	if d == nil {
		return pkgidentity.Spec{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.spec
}

// Outcome 最后一次交付的结论（诊断/日志用）。
func (d *IdentityDelivery) Outcome() resource.Outcome {
	if d == nil {
		return resource.Outcome{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.outcome
}

// Deliver 把身份交给**构建期选定的那个底座**，并记下本进程生效的身份。
//
// 三条语义：
//   - Spec 先落值、再交付：交付失败时首轮块仍要能用（`auto` 的兜底正是这么工作的）；
//   - 该底座没有身份面（未注册 / 未实现端口）→ 如实 `unsupported`，**不编造成功**；
//   - 交付失败 → 调用方（装配步骤）记 WARN + 诊断，启动继续（身份不是关键路径）。
func (d *IdentityDelivery) Deliver(ctx context.Context, spec pkgidentity.Spec) (resource.Outcome, error) {
	d.SetSpec(spec)
	if d == nil || d.reg == nil {
		return resource.Outcome{Status: resource.StatusUnsupported, Problems: []string{"未装配 runtime 注册表"}}, nil
	}
	id := runtime.SelectedRuntime()
	// 先确认该 id 在册：不在册时 `Identity` 也会返回 false，但两种 false 的语义不同
	// （"没有这个底座"不是"这个底座不承载身份"），诊断面要说得清。
	if _, ok := d.reg.Get(id); !ok {
		out := resource.Outcome{
			Status:   resource.StatusUnsupported,
			Problems: []string{"构建期选定的执行面未注册：身份不会被交付（首轮块仍可用）"},
		}
		d.record(out)
		return out, nil
	}
	adapter, ok := d.reg.Identity(id)
	if !ok {
		out := resource.Outcome{
			Status: resource.StatusUnsupported,
			Where:  "该执行面未声明产品身份交付面",
			Problems: []string{"这个底座不承载系统提示面：身份只走首轮块" +
				"（要确认是产品设计还是装配漏了登记）"},
		}
		d.record(out)
		return out, nil
	}
	out, err := adapter.DeliverIdentity(ctx, spec)
	if err != nil {
		out = resource.Outcome{
			Status:   resource.StatusFailed,
			Where:    out.Where,
			Effect:   resource.EffectNextProcess,
			Problems: append(out.Problems, "身份交付失败："+err.Error()),
		}
	}
	d.record(out)
	return out, err
}

// record 记下结论（失败也记：诊断面要能回答"上次为什么没成"）。
func (d *IdentityDelivery) record(out resource.Outcome) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.outcome = out
}
