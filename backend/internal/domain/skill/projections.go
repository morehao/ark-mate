package skill

import (
	"sync"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/runtime"
)

// Projection 一个 runtime 上**已解析**的技能投影。
//
// 为什么把结果登记下来而不是各处现算：启停、重校、审计三处都要用它，三处各自解析一遍必然
// 出现"某处不展开占位符"这类漂移。**解析归编排层（app），这里只存结果**。
type Projection struct {
	Runtime    string
	Projection runtime.Projection
	// Err 解析该投影时的失败原因（非空 = 这个 runtime 声明了投影但根解不开：占位符变量未设置、
	// 落点模板非法…）。**仍然登记**它，是为了让审计视图能报"没配置"而不是让这个 runtime
	// 从列表里彻底消失——「我明明声明了，怎么哪儿都看不到」是最难排查的一种沉默。
	// 解析失败的投影不参与重校（Err 非空时 ReconcileSkillProjection 直接返回）。
	Err string
}

// MaterializeSpec 把已解析的投影翻译成物化层要的平凡形态（P-1：capability 不 import runtime）。
func (p Projection) MaterializeSpec() capability.MaterializeSpec {
	return capability.MaterializeSpec{
		Root:   p.Projection.Root,
		Layout: p.Projection.Layout,
		Format: p.Projection.Format,
		Owner:  p.Runtime,
		// 该投影方的历史归属名：物化层据此**认领**旧副本并在写回时把旧名字换成新名字。
		// 漏了它的后果很具体——`.mate-managed` 里那条旧名字永远留在集合里，于是
		// "集合空了才删目录"永不成立，**禁用/删除技能时副本删不掉**，底座照样读到已禁用的技能。
		OwnerAliases: runtime.LegacyAliasesOf(p.Runtime),
	}
}

// Projections 投影目标的**状态**（自持一把只保护自己这把列表的锁）。
//
// 并发语义与拆分前一致：登记在装配期与重校时发生、读取在请求路径上发生，所以需要锁；
// 但这把锁**只保护这一个字段**——它原先借用的是 `Service.mu`，而后者同时保护会话表，
// 那是历史巧合而不是语义要求。
type Projections struct {
	mu sync.Mutex
	ps []Projection
}

// NewProjections 造一个空的投影登记处。
func NewProjections() *Projections { return &Projections{} }

// Set 登记物化目标（幂等覆盖；由编排层在启动解析后调用）。
func (m *Projections) Set(ps []Projection) {
	m.mu.Lock()
	m.ps = append([]Projection(nil), ps...)
	m.mu.Unlock()
}

// Snapshot 取投影目标的副本。
func (m *Projections) Snapshot() []Projection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Projection(nil), m.ps...)
}

// For 取某 runtime 的已解析投影（ok=false = 本进程没登记它）。
func (m *Projections) For(runtimeID string) (Projection, bool) {
	for _, p := range m.Snapshot() {
		if p.Runtime == runtimeID {
			return p, true
		}
	}
	return Projection{}, false
}
