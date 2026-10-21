package automation

import "github.com/morehao/ark-mate/internal/core/capability"

// ——— 定义文件载体的窄端口（v3.2 刀 7 收尾）———
//
// 自动化域有**两个载体**：sqlite 行（`AutomationStore`）与定义文件（内存/开发态）。
// 文件侧原先直接调 `mechanism` 的包级函数，于是领域层认识了机制层。
//
// 这里把文件载体收成四个动作 + 一个草稿接口：
//
//	Open   打开草稿（不存在 → 可写的空文档；**不落盘**）
//	Save   提交草稿（文件载体的 mtime 乐观锁在它内部）
//	Write  只改几个键的一次性写（覆盖层）
//	Remove 删除定义文件
//
// `DefinitionDraft` 只暴露领域真正会用的三个观察：文档、路径、打开时是否已存在。
// 提交动作留在端口上（而不是草稿的方法），是因为它的实现要知道"这份草稿属于哪个载体"——
// 那是机制的知识。
type DefinitionDraft interface {
	Doc() *capability.DefinitionFile
	Path() string
	Existed() bool
}

// DefinitionFiles 定义文件载体。
type DefinitionFiles interface {
	Open(dir, kind, id string, patch map[string]any) (DefinitionDraft, error)
	Save(d DefinitionDraft) error
	Write(dir, kind, id string, patch map[string]any) error
	Remove(dir, kind, id string) (bool, error)
}

// SetDefinitionFiles 装配期登记文件载体（nil = 只用 DB 载体；memory 模式下必须有）。
//
// **必须在装配期调用**：与 `SetStore` 同一条教训——构造时按值传会让它永远停在 nil，
// 而表现是"该走文件的路径直接空指针"。
func (pl *Plane) SetDefinitionFiles(files DefinitionFiles) { pl.files = files }
