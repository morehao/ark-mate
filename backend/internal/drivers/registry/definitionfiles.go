package registry

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/drivers/mechanism/definition"
)

// AutomationDefinitionFiles 把定义文件机制适配成 `automation` 域声明的窄端口。
//
// 为什么落在 registry（装配点）而不是一个「适配器包」：它只做"把机制的宽面收窄成域端口"
// 这一件事，而这件事只有同时看得见域端口与机制实现的地方写得了（机制层禁止 import 域）。
// 同包已有 workspaceStore / skillDefs / mcpDefs 三个同形适配体。
//
// 那次断言不是"猜"：草稿只由本适配体的 `Open` 产出，类型就那一个。断言失败说明有人把别处
// 造的草稿喂进来——那本来就该是错的。
type AutomationDefinitionFiles struct{}

var _ automation.DefinitionFiles = AutomationDefinitionFiles{}

// Open 打开定义草稿（不存在 → 可写的空文档；不落盘）。
func (AutomationDefinitionFiles) Open(dir, kind, id string, patch map[string]any) (automation.DefinitionDraft, error) {
	d, err := definition.OpenDefinition(dir, kind, id, patch)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Save 提交草稿（文件载体的 mtime 乐观锁在机制内部）。
func (AutomationDefinitionFiles) Save(d automation.DefinitionDraft) error {
	dd, ok := d.(*definition.DefinitionDraft)
	if !ok {
		return fmt.Errorf("registry: 无法保存 %T 定义草稿（草稿只能由本适配体的 Open 产出）", d)
	}
	return definition.SaveDefinition(dd)
}

// Write 只改几个键的一次性写（覆盖层）。
func (AutomationDefinitionFiles) Write(dir, kind, id string, patch map[string]any) error {
	return definition.WriteDefinition(dir, kind, id, patch)
}

// Remove 删除定义文件（返回是否真的删掉了）。
func (AutomationDefinitionFiles) Remove(dir, kind, id string) (bool, error) {
	return definition.RemoveDefinitionPermanently(dir, kind, id)
}
