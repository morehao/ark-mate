package session

import "testing"

// TestLocalPlacementSingleWorker 证明单 worker 落点的不变式（M2-3）：
//   - endpointID 为空（= 本进程，无需跨 worker 路由）；
//   - cwd 原样透传（下发给 worker 的就是它自己本地卷上的路径，不做任何重写）。
//
// 「路径不下发远端绝对路径」这条验收闸在单 worker 下退化为「透传」，本测试把这条基线钉住——
// 云端实现若把 cwd 改写成别的形态，就与这里的基线语义分叉，必须显式设计（并改文档）。
func TestLocalPlacementSingleWorker(t *testing.T) {
	p := NewLocalPlacement()
	for _, cwd := range []string{"/Users/x/workspaces/t/u/slug", "C:\\work\\a", ""} {
		ep, local := p.Place("dsh", cwd)
		if ep != "" {
			t.Errorf("单 worker 的 endpointID 应为空（本进程），got %q", ep)
		}
		if local != cwd {
			t.Errorf("cwd 应原样透传：in=%q out=%q", cwd, local)
		}
	}
}
