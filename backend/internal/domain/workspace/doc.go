// Package workspace 是**工作空间**这个域：哪些目录可用、当前在哪个目录、以及"宿主能不能抬起
// 系统目录面板"这条宿主事实的判定与命令配方。
//
// 它**负责**（今天已落地的部分）：原生目录选择器的宿主判定（绑回环 / 非 SSH / 有可服务的显示
// 会话）与命令配方（macOS `osascript`、Linux zenity/kdialog），以及真的把宿主命令 spawn 起来取回
// 路径。判定是**纯函数**、事实是**注入**的，所以这一半不依赖任何会话状态。
//
// 它**负责**（P2b 起又多了两半）：**起始点偏好策略**（代建根 + 生效名单，见 `policy.go`）、
// 以及工作空间路径的三条件校验与解析（`ValidateWorkspace` / `ResolveWorkspace` /
// `DiscardWorkspace`）——它们此前是 `manager` 的包级函数，与选择器同域（同一个"工作空间"概念）。
//
// 它**不负责**：工作空间登记表的持久化（已在 `adapters/outbound/workspaces`）、会话与
// 工作空间的归属（那要读会话读面，属 `domain/session`）、对外 DTO 与错误码出口
// （用例层 `app/*` 与 `contract/dto*`）。
//
// 为什么这一半能先搬：它对 `*Service` 的依赖只有"本进程实际监听的地址"一个字段（装配期注入、
// 之后只读），**零会话依赖、零库依赖**——与 `domain/interaction` 同形的自足孤岛。剩下那一半
// （登记表 / 白名单 / 视图）的临界区与 `sessions` 共用 `Service.mu`，要先做并发审查，见计划 §3.1.32。
package workspace
