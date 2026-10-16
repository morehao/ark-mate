package definition

import (
	"context"

	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/golib/glog"
)

// ——— 定义形状的历史迁移（旧 sources[] / legacy mcp ref → 新形状）———
//
// 放在机制层而不是任何单个域：这两件事都只与**定义文档的形状**有关，与"skill 该怎么投影"
// "mcp 该怎么注入"无关；而 skill / mcp / provider 的导入路径都要用它。

// TranslateLegacyMcpRefs 把一条 MCP 定义文档里的旧前缀引用**就地**翻成 `store:`，
// 返回被翻过的条数（0 = 没有旧前缀，调用方不必重新序列化）。
//
// 它只改**内存里的文档**，不写回用户文件（那条纪律是"导入不删不回写"）；调用方重新
// `Marshal()` 之后，写进库的正文才是翻译后的形态。翻译点本身只有 `pkg/capability` 那一处。
func TranslateLegacyMcpRefs(ctx context.Context, doc *capability.DefinitionFile, where string) int {
	n := 0
	for _, key := range []string{"secret_env", "secret_headers"} {
		var bindings map[string]string
		ok, err := doc.Decode(key, &bindings)
		if !ok || err != nil || len(bindings) == 0 {
			continue
		}
		changed := false
		for k, ref := range bindings {
			if translated, legacy := capability.TranslateLegacySecretRef(ref); legacy {
				WarnLegacySecretRef(ctx, ref, where+" 的 "+key)
				bindings[k] = translated
				changed = true
				n++
			}
		}
		if changed {
			_ = doc.Set(key, bindings)
		}
	}
	return n
}

// WarnLegacySecretRef 旧前缀被显式翻译时的 WARN（**唯一的翻译点在 `pkg/capability`**，
// 文案也由它给：把字面量与判据收在一处，别处只负责说"是在哪儿翻的"）。
func WarnLegacySecretRef(ctx context.Context, ref, where string) {
	glog.Warnw(ctx, capability.LegacySecretRefWarning(ref), "ref", ref, "where", where)
}
