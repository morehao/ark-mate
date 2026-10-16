package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/skill"
)

// ——— §13.4 S4/S5：技能启停（overlay）与激活（正交） ———
//
// 两个概念分开，且都必须能对用户解释：
//
//	Enabled    平台要不要**投放**这个技能（定义文件里的 `enabled`；false = 不物化、不进 agent 能力目录）。
//	Activation 当前这个 agent 的环境**适不适用**（候选 runtime 里有没有可物化的 source）。
//	           —— 与 Enabled 正交：启用着的技能在某个 agent 上完全可能不适用，反之亦然。
//
// 禁用是 **overlay**：定义文件、正文、sources 一律保留，只把 `enabled` 写成 false。因此
// 「关掉一个技能」随时可逆（改回 true），不需要重新导入正文——这是与「删掉定义文件」的本质区别。
//
// 落盘 = 编辑用户文件（M10 纪律）：读原文件为 map → 只改 `enabled` 一个键 → 原子写回。
// **未知键原样保留**——用户在自己文件里加的字段不该被平台抹掉。
//
// 本文件现在只剩**转发层**（P4b 删）：技能平面的实体（定义读写、物化与审计、启停、投影登记）
// 全在 `domain/skill.Plane`；`Service` 侧只保留调用点用得着的方法名。

// capabilityDirSnapshot 能力定义目录（memory 模式下是启停写回位置；sqlite 模式下是导入/导出目录）。
func (s *Service) capabilityDirSnapshot() string {
	// 刻意**不**加外层锁：`defs.Dir()` 自己带锁（`definition.Mechanism.mu`），在这里再取
	// `s.mu` 只会制造一处 `s.mu → defs.mu` 的固定嵌套，而反向取锁的那天就是死锁。
	return s.defs.Dir()
}

// ——— P3d 过渡期转发层（纪律：一行 + TODO）———

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
func (s *Service) SetSkillProjections(ps []skill.Projection) {
	s.skillPlane.Projections().Set(ps)
}

// TODO(P4b): 删转发（调用点直接依赖 skill.Projections）
func (s *Service) skillProjectionsSnapshot() []skill.Projection { return s.skillPlane.Snapshot() }

// TODO(P4b): 删转发（调用点直接依赖 skill.Projections）
func (s *Service) skillProjectionFor(runtimeID string) (skill.Projection, bool) {
	return s.skillPlane.Projections().For(runtimeID)
}

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
func (s *Service) ReconcileSkillProjection(p skill.Projection) (int, int, error) {
	return s.skillPlane.ReconcileSkillProjection(p)
}

// TODO(P4b): 删转发（调用点直接依赖 skill.Plane）
func (s *Service) writeSkillEnabled(ctx context.Context, id string, enabled bool) (string, error) {
	return s.skillPlane.WriteSkillEnabled(ctx, id, enabled)
}
