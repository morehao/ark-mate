package manager

import (
	"github.com/morehao/ark-mate/internal/mechanism/definition"
)

// 本文件是 P1/P3d 过渡期的**转发层**：定义机制（双载体 + CAS + 审计）已经搬进
// `internal/mechanism/definition`，但 `*Service` 的调用点暂时仍按老名字调它。
// 按纪律，转发只允许"一行 + 下面的 TODO"这一种形态——不许在这里夹带校验、日志或错误包装。

// TODO(P4b): 删转发（调用点直接依赖 definition.Mechanism）
func (s *Service) definitionRepoFor(sub string) (definition.DefinitionRepo, error) {
	return s.defs.DefinitionRepoFor(sub)
}

// TODO(P4b): 删转发（调用点直接依赖 definition.Mechanism）
func (s *Service) capabilityDirFor(sub string) (string, error) {
	return s.defs.CapabilityDirFor(sub)
}
