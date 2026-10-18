package credential

import (
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/resource"
)

// 候选环境变量名的推导。放在凭据机制里，因为它是**四层回落**的一部分：继承环境与两份
// dotenv 都按它们取值。

// ProviderEnvKeyNames 该供应商在**已注册 runtime**上的候选环境变量名（四层回落的"继承环境"
// 与两份 dotenv 都按它们取值）。
//
// 为什么用这个名字而不是底座自家的什么 `DEEPSEEK_API_KEY`：平台注入子进程 env 用的就是它
// （`resource.RefFor(spec.Credential.RefPattern, id)`，逐供应商唯一，ADR-16），而参照实现 dsh
// 自己的查找优先级就是"继承环境 > 存储文档"、且存储文档的 refs **按环境变量名**存放。
// 于是这里查的键与底座真正会读的键是**同一个**——分层顺序对齐不是巧合，是同一份命名。
func (m *Store) ProviderEnvKeyNames(providerID string) []string {
	if m.reg == nil || strings.TrimSpace(providerID) == "" {
		return nil
	}
	specs := m.reg.List()
	seen := make(map[string]bool, len(specs))
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		name := resource.RefFor(spec.Credential.RefPattern, providerID)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
