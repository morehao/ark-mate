package manager

import (
	"context"
)

// ——— P2b：工作空间域（策略 / 校验 / 解析）——已清空 ———
//
// 这个文件曾经装着三样东西：策略读写转发、三个纯函数转发（`ValidateWorkspace` /
// `ResolveWorkspace` / `DiscardWorkspace`）、以及两个**类型别名**（`WorkspaceResolution` /
// `DefaultWorkspaceRef`）。前两批按"调用点直接依赖域"清掉（P4a 正文第二十一/二十二刀），
// 别名在第二十三刀清掉——**别名让"两份定义"这种危险状态不会出现，但它同时让"谁是这个类型的
// 主人"变得不可判定**：接错线时两边都编译过（第三条硬规矩禁别名正是为此）。
//
// 现在 `manager` 一侧只剩 `planner` 这一个持有者（`WorkspacePlane()` 是它的事实出口）。

// planner 工作空间域（装配期构造；策略状态在它自己的锁下）。
//
// 它是 `Service` 上**唯一**的工作空间策略持有者：字段不再复刻一份，否则"域写一份、manager 读
// 另一份"就是第二个事实源（而表现是面板改了名单、校验却按旧的走——静默）。
var _ = context.Background
