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
}
