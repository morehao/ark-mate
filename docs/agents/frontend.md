# 前端风格约束（frontend/DESIGN.md）

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 设计值的事实源是 `packages/ui/src/theme.ts` 的 `tokens`；交互态样式收口在 `packages/console/src/console.css.ts`。

- 颜色/圆角/间距唯一事实源 = `packages/ui/src/theme.ts` 的 `tokens`；改设计值须同步 DESIGN.md front matter（两处人工一致），业务代码禁止硬编码色值。
- 交互态 CSS 唯一收口在 `packages/console/src/console.css.ts`，不新增 css 文件；聊天正文样式由 `Transcript.tsx` 自绘，勿搬进 antd。
- 「可选中项」（导航行 / 新建会话 / 会话行 / 设置分节 / 分组行 / tab…）的 hover 与选中态**只引用 `console.css.ts` 顶部 `:root` 的 `--fg-menu-*` 变量**（默认灰 / hover 文字加深 + `surfaceActive` 底 / 选中 `accent` + `selectedBg` 底，不加粗）：新增一个可选择面时不要再自定一套色，否则又会变成「好几处各不相同的选中态」（2026-11-06 反馈的根因）。
- **单选 chip 类不走菜单变量**（v2.4）：权限 chip（`.fg-chip`）、投递 pill（`.fg-delivery-pill`）这类"下拉里选一个"的小控件，是 chip 交互态（hover 软底 + 抬边），不是会保持选中底色的列表行，不受上一条约束。
