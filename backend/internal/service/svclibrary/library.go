// Package svclibrary 定义能力 Library 的读模型端口（M2-4）。
//
// 独立成包而不是挂进 svccapability：Library 是**跨领域**的聚合视图（skill/mcp/agent/tool），
// 挂在任何单一领域端口上都说不通。它不拥有数据、也不写任何东西——只有一个查询方法。
//
// 本包**不 import `manager`/`store`**：端口只声明"要什么、给什么"，取值类型下沉在域里（R4）。
package svclibrary

import (
	"github.com/morehao/ark-mate/internal/domain/skill"
	"github.com/morehao/ark-mate/internal/dto/dtolibrary"
)

// Query 一次 Library 查询。
//
// 它**是** `domain/skill.Query`（别名，不是平行类型）：类型下沉之后，实现包（`manager`）不必再
// 反向 import 本包就能写出 `ListLibrary` 的方法签名——那正是 R4 要断掉的依赖（"实现包拿端口
// 类型当签名"）。别名让既有调用点与守卫的错误文案保持可读。
type Query = skill.Query

// LibrarySvc Library 读模型端口（实现方本包的 `Library`，由控制器构造）。
//
// 断言**不写在实现包里**：装配点交的是依赖事实（三张能力表 / 技能投影 / 运行期事实），
// 控制器在自己的构造函数里造 `NewLibrary(...)`。
type LibrarySvc interface {
	// ListLibrary 返回统一能力清单（按 kind、id 排序）+ 逐 runtime 可达性。
	ListLibrary(q Query) ([]dtolibrary.LibraryItem, error)
}
