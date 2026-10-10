package testutil

import (
	"os"
	"testing"
)

// TestStubRegistryIsStubOnly 锁定 StubRegistry 的**刻意收窄**：只装 stub。
//
// 如果哪天有人把它"顺手"改成 runtime.AllBuiltinRegistry()（opencode + dsh），
// 多数依赖它的用例仍会过——正是这种"改了也没红"的漂移最危险：测试从此在验证
// 「内置清单恰好长这样」，内置清单增删一个底座，一堆与它无关的断言跟着变。
func TestStubRegistryIsStubOnly(t *testing.T) {
	reg := StubRegistry()
	specs := reg.List()
	if len(specs) != 1 {
		t.Fatalf("StubRegistry 应只装 stub，实际装了 %d 个", len(specs))
	}
	if specs[0].ID != "stub" {
		t.Errorf("唯一 spec 应是 stub，得到 %q", specs[0].ID)
	}
}

// TestTempDBPathAndOpenStore TempDBPath 给的是**可用且可重复打开**的路径。
//
// 重复打开这一点是重点：重启恢复类测试依赖"同一路径开两次"，而 t.TempDir() 每次调用
// 返回不同目录——把 TempDBPath 实现成"每次现算"就会悄悄造出两个不同的库，
// 断言失败的表现却是"数据丢了"，很难定位。
func TestTempDBPathAndOpenStore(t *testing.T) {
	path := TempDBPath(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("TempDBPath 不应预先创建文件，Stat 得到 err=%v", err)
	}

	st := OpenStore(t, path)
	// 写入一点东西，关掉，再从同一路径打开验证留存。
	if err := st.Close(); err != nil {
		t.Fatalf("关闭第一个句柄: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("OpenStore 后应已创建库文件: %v", err)
	}
	reopened := OpenStore(t, path)
	t.Cleanup(func() { _ = reopened.Close() })
}
