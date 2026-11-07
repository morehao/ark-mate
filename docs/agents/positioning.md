# 外部形态的参照实现（DSH）

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 定位口径的唯一事实源 = `docs/decisions.md`（定位陈述见 `docs/positioning.md`）。

**DSH（`deepseek-harness`，本仓内置底座 `dsh` 的上游）是本仓"外部形态"的重要参考**：交付形态、桌面与 web 两个入口的关系、宿主能力的边界、目录/工作区选择这类**产品交互**，取口径时先读它，再回来对齐。它在这些题上比我们想得全，而且**两个入口都实现了**，所以它的选择本身就是证据。

**最该记住的一条**（2026-10-11 连错两次换来的）：**"客户端能不能看见宿主屏幕"这类判定属于宿主，不属于客户端。** DSH 的做法是宿主侧解析 + 能力声明：

- `packages/host/directory-picker-auto` 在启动时采样**宿主事实**（`webServer.host` 是否回环、是否 SSH 启动、有没有可服务的显示会话），用纯函数在 `native` / `browse` 两套实现里**互斥挂载**一套；
- 原生那半是**宿主进程**抬的（macOS `osascript`、Linux zenity/kdialog、Windows COM），所以 **`dsh web` 的本机浏览器入口也用系统面板**；只有远程/SSH/无显示会话才退化成应用内浏览；
- 两个实现是**一对可替换的 occupant**，不是"两套并存"。

本仓的对应物（照抄过来的形状）：判定与命令配方在 `backend/internal/domain/workspace/picker.go`（纯函数；P4b 起装配点直接持 `workspace.Picker`，转发已删）、声明走 `GET /capabilities` 的 `native-dialog`、执行面是 `POST /workspaces/pick-directory`。**别放错边**：判据一旦落到客户端（UA、`window.electron`、"有没有 Electron 桥"），本机浏览器就会被静默降级——这正是我们踩过的那个坑，现在有守卫 `TestDirectoryPickerIsTwoStateHostResolved` 钉着。

**其余同样值得先查再定的题**：目录选择器自身的交互（面包屑/双栏/新建文件夹/隐藏项开关，见 `packages/client/ui-directory-picker-browse`）、工作区作为持久实体的形态（`packages/workspace`）、会话与工作区的归属关系、以及"桌面只能多给、多给的那半必须有服务端等价路径"这条一致性纪律（它的 web bundle 与 desktop bundle 各有一份 cordis 配置，可逐项对照）。**取舍照旧**：DSH 没有的概念（例如我们的工作区起始点偏好）不要硬套，写清偏离理由即可。
