package maintenance

import (
	"context"
	"time"
)

// ParentPollInterval 父进程存活检测的轮询间隔（桌面壳被强杀时的响应时间上界）。
const ParentPollInterval = time.Second

// WatchParentExit 轮询父进程 id，**父进程消失即回调**（默认行为：取消 serve 的 ctx → 优雅停机）。
//
// 为什么需要它（设计 §7.4）：桌面壳被 `kill -9` 时来不及发 SIGTERM，核心会变成孤儿——
// 它自身还握着一堆 runtime 子进程（每个已建会话一个）。POSIX 下父进程一死，子进程会被
// reparent 给 init/launchd，**`os.Getppid()` 随之变化**，据此即可自保退出。
//
// 平台口径：这条在 POSIX（macOS / Linux）成立；Windows 没有 reparent 语义，
// `Getppid` 不会变——那边要靠 Job Object（M4-3 的 Windows 安装器一并处理）。
//
// getppid / interval 可注入：父进程死亡无法在单测里真的制造，只能断言"观察到变化即回调"。
func WatchParentExit(ctx context.Context, getppid func() int, interval time.Duration, onParentGone func()) {
	if interval <= 0 {
		interval = ParentPollInterval
	}
	original := getppid()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if getppid() != original {
				onParentGone()
				return
			}
		}
	}
}
