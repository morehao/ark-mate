package definitions

import (
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/adapters/outbound/mechanism/definition"
)

// skillDefs 把定义机制**收窄**成技能域声明的端口（`skill.Definitions`）。
//
// 为什么必须有这一层：`Mechanism.DefinitionRepoFor` 返回的是机制层自己的 `DefinitionRepo`
// 接口，而 Go 的方法返回类型不能协变——领域层声明一个方法集更小的接口，**不能**被
// `*definition.Mechanism` 直接满足，只能在装配点做一次可赋值转换。
//
// 它不是"第二装配根"，也不是被禁的那种转发层：这里不构造任何东西、不持有状态、不解释语义，
// 只是把已经造好的同一个机制暴露成领域层看得到的那两个方法（`Dir` / `RepoFor`）。
// 每个域一份，因为每个域声明的方法集不同——收得越窄，换载体时被牵连的地方越少。
type skillDefs struct{ m *definition.Mechanism }

// 编译期确认适配体确实满足端口（端口一旦加方法，这里先红）。
var _ skill.Definitions = skillDefs{}

// Dir 能力定义根目录；空串 = 本进程没装载能力数据面。
func (s skillDefs) Dir() string { return s.m.Dir() }

// RepoFor 取技能定义载体（返回值从机制的宽接口收窄成领域窄接口）。
func (s skillDefs) RepoFor(sub string) (skill.DefinitionRepo, error) {
	r, err := s.m.DefinitionRepoFor(sub)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// NewSkillPlane 用定义机制造技能平面（**唯一构造点**：适配体不外泄，装配面只拿到域平面）。
//
// 顺序敏感的一处：`caps` 必须与 MCP 平面**共用同一份**能力注册表（两份注册表 = 两个事实源）。
func NewSkillPlane(caps *capability.Registry, m *definition.Mechanism) *skill.Plane {
	return skill.NewPlane(caps, skillDefs{m: m})
}
