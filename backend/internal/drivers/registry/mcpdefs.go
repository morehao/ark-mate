package registry

import (
	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/drivers/mechanism"
)

// mcpDefs 把定义机制收窄成 MCP 域声明的端口（`mcp.Definitions`）。
//
// 与 `skillDefs` 同一理由：`DefinitionRepoFor` 返回机制层自己的宽接口，而 Go 的返回类型
// 不能协变，领域声明的窄接口只能在装配点做一次可赋值转换。这里不构造、不持状态、不解释语义。
type mcpDefs struct{ m *mechanism.Mechanism }

// Dir 能力定义根目录；空串 = 本进程没装载能力数据面。
func (s mcpDefs) Dir() string { return s.m.Dir() }

// RepoFor 取 MCP 定义载体（返回值从机制的宽接口收窄成领域窄接口）。
func (s mcpDefs) RepoFor(sub string) (mcp.DefinitionRepo, error) {
	r, err := s.m.DefinitionRepoFor(sub)
	if err != nil {
		return nil, err
	}
	return r, nil
}
