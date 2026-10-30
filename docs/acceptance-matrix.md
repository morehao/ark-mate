# 桌面与浏览器的一致性验收矩阵

> **这份文档是什么**：「桌面只能多给」是一条口径，而口径必须能被判定。本文件收集**桌面与浏览器入口
> 之间的一致性判据**——差异清单（每一项都给出浏览器侧的等价路径与两侧靶）与靶命令总表。
>
> **谁在读它**：`backend/internal/archtest/diff_list_test.go` 的 `TestDifferenceListHasDesktopTargets`
> **逐字读** §1 的表——表头逐字匹配、行数 ≥ 4、每行「桌面靶」列的每个反引号断言标题必须**逐字**出现在
> `frontend/scripts/desktop-ui-acceptance.mjs` 里。**改表就要改那条守卫看的东西**：表头被重写、行被删除、
> 断言标题改了字，都会当场红（读不到该表即 `fatal`，fail-closed）。
>
> **沿革**：本节原在 `docs/desktop-architecture-design-20260928.md` §7.3（那篇文档于 2026-10-16 文档
> 重建时归档删除）。表的内容**原样迁入**，只搬位置、不改判据。

## 1. 桌面与浏览器的差异清单

**每项都要给出浏览器入口的等价路径与靶——没有靶的项必须写明为什么不需要。**

| # | 桌面独有的东西 | 桌面侧落地 | 浏览器入口怎么完成 | 桌面靶（`pnpm ui:accept:desktop` 断言） | 浏览器靶（`pnpm ui:accept` 断言） |
| --- | --- | --- | --- | --- | --- |
| D1 | 宿主原生目录选择器 | 声明面 `native-dialog`；执行面 `native.pickDirectory`（壳抬 `dialog`） | **同一套**：`POST /workspaces/pick-directory`（核心 spawn `osascript`/zenity）；宿主抬不起来时退化成应用内浏览 | `native 键与契约一致（只剩 pickDirectory）`；`顶层键 = boot/native（无 capabilities / 无 platform）` | `能力声明里 native-dialog 存在且有 browserFallback`；`wsdir`（应用内浏览那一态） |
| D2 | 事件流的基址（WS 直连回环） | `boot()` 下发 `streamBaseUrl`；WS **直连回环**（不经营反代，见 [`architecture.md`](./architecture.md) §6.2） | 浏览器入口连**同源** `/v1/mate/ws`，首帧 attach（无凭据） | 无靶：事件流基址由 boot() 下发，平台不做鉴权、无凭据可断言 | `smoke:e2e` / `e2e-live-turn` 的同源连流 |
| D4 | 托盘图标与菜单 | `apps/desktop/src/tray.ts`（壳进程，**不进契约**） | **无等价物，也不需要**：纯增强、不阻塞任何流程，声明面里 `browserFallback: none` | 无靶：纯增强、UI 不可观测（壳进程行为，验收脚本够不到）——由 archtest 的 `declaredWithoutConsumer` 登记表兜住「声明了但界面不读」 | 不适用 |
| D5 | 窗口控制（`setBounds`/`close`）、拖入/粘贴文件真实路径、用系统程序打开 | **已删**（2026-10-11，P3/D5；零调用方） | 不适用（功能本身不存在） | 反过来断言**没有**：`顶层键 = boot/native（无 capabilities / 无 platform）`、`native 键与契约一致（只剩 pickDirectory）` | 不适用 |

**怎么读这张表**：

- 「桌面侧落地」列写的是**机制落在哪**（声明面 / 执行面 / 壳进程），不是操作步骤。
- 「浏览器入口怎么完成」列是**等价路径**：桌面能做的事，浏览器入口必须有一条走得通的路；反过来，
  **纯增强**的项（不阻塞任何流程）才有资格写「无等价物，也不需要」。
- 「靶」两列是**断言标题**，逐字对应两侧验收脚本里的断言名。桌面靶写 `无靶：<理由>` 的，理由必须
  说清**为什么这个问题在该侧不可观测**（例如「壳进程行为，验收脚本够不到」），不能只写「不需要」。

## 2. 靶命令总表

| 靶 | 命令 | 前置 | 覆盖 |
| --- | --- | --- | --- |
| 浏览器入口（开发态） | `cd frontend && pnpm ui:accept [cap\|cmd\|auto\|model\|wsdir]` | `make dev` 在跑（靶 = `127.0.0.1:10001`） | 声明面能力、命令发现面、自动化、模型选择、目录浏览对话框 |
| 桌面（真 Electron + 真核心） | `cd frontend && pnpm ui:accept:desktop` | 无（脚本自起壳并自造自清夹具；**跑之前别让 `make dev` 的核同时活着**——两个核共用同一个 `mate.db`） | 桌面独有能力、契约形状、事件流基址与凭据 |
| 桌面 · skill 投影 | `pnpm ui:accept:desktop-skills` | 同上 | skill 真的投放到底座读根并被消费 |
| 桌面 · 不依赖宿主机 | `pnpm verify:no-host` | 同上 | 最小 `PATH` + 私有 `HOME` 下真跑一轮 → 退出 → 重开续聊，断言诱饵零命中 |
| 桌面 · 打包态 | `pnpm ui:accept:desktop-nohost:packaged` | `make desktop/dist` | 上面两条在**安装包形态**上的复验 |
| 资产一致性（壳 vs 核心） | `pnpm asset-parity` | 桌面态 | 逐路径比状态码 / Content-Type / Cache-Control / sha256，钉住五类划分与 SPA 深链归属 |
| 桌面 DoD 汇总 | `make verify/desktop-dod` | 源码态 | archtest + typecheck + 契约 + 资产一致性 + 引擎 ABI + 合规门禁 |
| 浏览器产物靶汇总 | `make verify/ui-product` | `make dev` 在跑 | 上表第一条 |

**两条靶互斥的原因**：它们都要独占 `~/.ark-mate/mate.db`（`make dev` 的核与桌面验收脚本自起的核是
两个进程）。同时跑会互相污染会话与能力定义，验收结果不可读。

## 3. 维护约定

1. **新增一项桌面独有能力**时，先回答「浏览器入口怎么完成同一件事」——答不出来就不该新增（除非它
   是纯增强，且要以 `browserFallback: none` 声明）。
2. **给某项加/改断言**时，同步本表的靶列——那是守卫唯一读的地方。
3. **删一项**时把整行删掉（不要留空行），并在提交信息里说明被删的是能力本身还是仅验收面。
4. **可证伪性优先**：本表的价值在于「不一致会红」。若某项的靶只是"跑得通"而不是"这条差异被钉住"，
   那它不该出现在这张表里。
