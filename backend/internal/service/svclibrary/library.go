// Package svclibrary 定义能力 Library 的读模型端口（M2-4）。
//
// 独立成包而不是挂进 svccapability：Library 是**跨领域**的聚合视图（skill/mcp/agent/tool），
// 挂在任何单一领域端口上都说不通。它不拥有数据、也不写任何东西——只有一个查询方法。
package svclibrary

import "github.com/morehao/ark-forge/internal/dto/dtolibrary"

// Query 一次 Library 查询。
type Query struct {
	// Kind 空 = 全部；否则须是 dtolibrary.ValidKinds 之一（未知值报 40000，不静默返回空表）。
	Kind string
	// Q 对 id/name/description 做大小写不敏感子串匹配。
	Q string
}

// LibrarySvc Library 读模型端口（实现方 internal/manager，编译期断言在 manager/ports.go）。
type LibrarySvc interface {
	// ListLibrary 返回统一能力清单（按 kind、id 排序）+ 逐 runtime 可达性。
	ListLibrary(q Query) ([]dtolibrary.LibraryItem, error)
}
