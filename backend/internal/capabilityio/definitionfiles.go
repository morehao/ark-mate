package capabilityio

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/mechanism/definition"
)

// DefinitionFiles 把定义文件机制适配成各域声明的窄端口。
//
// 为什么需要它：机制返回的是自己的 `*definition.DefinitionDraft`，而领域声明的是一个
// 三方法的窄接口——Go 的返回类型不能协变，只能在装配点做一次可赋值收窄（`Open`），
// 提交时再断言收回具体类型（`Save`）。
//
// 那次断言不是"猜"：草稿只由本适配体的 `Open` 产出，类型就那一个。断言失败说明有人把别处
// 造的草稿喂进来——那本来就该是错的。
type DefinitionFiles struct{}

var _ automation.DefinitionFiles = DefinitionFiles{}

// Open 打开定义草稿（不存在 → 可写的空文档；不落盘）。
func (DefinitionFiles) Open(dir, kind, id string, patch map[string]any) (automation.DefinitionDraft, error) {
	d, err := definition.OpenDefinition(dir, kind, id, patch)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Save 提交草稿（文件载体的 mtime 乐观锁在机制内部）。
func (DefinitionFiles) Save(d automation.DefinitionDraft) error {
	dd, ok := d.(*definition.DefinitionDraft)
	if !ok {
		return fmt.Errorf("capabilityio: 无法保存 %T 定义草稿（草稿只能由本适配体的 Open 产出）", d)
	}
	return definition.SaveDefinition(dd)
}

// Write 只改几个键的一次性写（覆盖层）。
func (DefinitionFiles) Write(dir, kind, id string, patch map[string]any) error {
	return definition.WriteDefinition(dir, kind, id, patch)
}

// Remove 删除定义文件（返回是否真的删掉了）。
func (DefinitionFiles) Remove(dir, kind, id string) (bool, error) {
	return definition.RemoveDefinitionPermanently(dir, kind, id)
}
