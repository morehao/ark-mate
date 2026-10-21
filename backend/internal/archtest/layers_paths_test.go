package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLayerTablePathsResolve 层表里的每个路径都必须真实存在。
//
// 为什么需要它：v32Layers 的 deny 是精确字符串。搬迁时路径改对、deny 漏改，
// 守卫不会红——原本非法的边会静默变合法。这条守卫把"死路径"变成硬失败。
func TestLayerTablePathsResolve(t *testing.T) {
	check := func(path, where string) {
		t.Helper()
		info, err := os.Stat(filepath.Join(backendRoot(t), path))
		if err != nil || !info.IsDir() {
			t.Errorf("层表路径不存在: %s（来自 %s）", path, where)
		}
	}

	for key, layer := range v32Layers {
		check(key, "v32Layers key")
		for _, deny := range layer.deny {
			check(deny, "v32Layers["+key+"].deny")
		}
	}
	for _, deny := range v32AdapterDeny {
		check(deny, "v32AdapterDeny")
	}
	// C3 4.6：同带边登记表的两个端点也必须真实存在，且确实是"同带、不同 key"。
	for _, e := range v32SameBandEdges {
		check(e.from, "v32SameBandEdges.from")
		check(e.to, "v32SameBandEdges.to")
		if e.why == "" {
			t.Errorf("同带边登记缺理由: %s → %s", e.from, e.to)
		}
		fromKey, fromLayer, okFrom := v32Resolve(e.from)
		toKey, toLayer, okTo := v32Resolve(e.to)
		switch {
		case !okFrom || !okTo:
			t.Errorf("同带边登记映射不到层表: %s → %s", e.from, e.to)
		case fromLayer.layer != toLayer.layer:
			t.Errorf("同带边登记跨了层: %s（L%d）→ %s（L%d）", e.from, fromLayer.layer, e.to, toLayer.layer)
		case fromKey == toKey:
			t.Errorf("同带边登记与源同 key，本不需要登记: %s → %s", e.from, e.to)
		}
	}
}
