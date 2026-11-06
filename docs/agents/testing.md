# 测试分层（前端 + 后端全景）

> 从 `commands.md` 拆出的「测试分层」唯一事实源（2026-11-05，刀 0–3 落地后）。
> 这里回答"**某类验证该用哪一层、写在哪、进不进 CI**"，避免重复造轮子或误删某一层。
> 具体命令仍在 `Makefile` / `package.json`，本文件只讲**分层口径与边界**。

## 0. 一句话

**后端已成熟（标准库 + archtest）；前端缺的是「组件渲染层」和「可维护的界面旅程」——本文件划分的五层各司其职，不互相替代。**

## 1. 五层职责（从快到慢、从便宜到贵）

| 层 | 工具 | 入口 | 位置 | 进 CI | 验什么 / 不验什么 |
| --- | --- | --- | --- | --- | --- |
| **L1 后端单测 / 集成** | Go 标准库 `testing`（无第三方框架） | `make test`（全量）、`make test-race` | `backend/internal/**/*_test.go` | ✅ | 业务逻辑、store 迁移、协议（stub runtime，不依赖外部底座） |
| **L1.5 架构守卫** | Go 标准库 | `cd backend && go test ./internal/archtest/` | `backend/internal/archtest/` | ✅ | 分层边界、词汇收口、路由/文档一致性（**守卫计数见 `guards.md`**） |
| **L2 前端纯逻辑单测** | Node 内置 `node --test` | `pnpm test:console` / `test:contract` | `packages/*/tests/*.test.ts` | ✅ | `console/*.ts` 等纯函数、契约形状（**无 DOM、无 React**） |
| **L3 前端组件渲染单测** | Vitest + Testing Library | `pnpm test:component` | `packages/console/tests/components/**/*.test.tsx` | ✅ | 单组件渲染/交互/分支，**依赖全 mock**（`vi.mock` api/store） |
| **L4 前端 e2e（纯 mock）** | Playwright（复用系统 Chrome） | `pnpm e2e:web` | `frontend/e2e/**/*.spec.ts` | ✅ | 跨组件/路由/写面交互的**装配路径**，REST 全 `page.route` mock，静态服务 serve `dist` |
| **L5 真栈验收** | 手写 CDP 脚本 | `pnpm ui:accept` / `ui:accept:desktop` / `smoke:e2e` / `smoke:e2e:live` | `frontend/scripts/*.mjs` | ❌（本地） | **真 Chrome/Electron + 真服务 + 真底座 +（部分）真模型** 的端到端 |

「快/便宜」在上，「慢/贵」在下。**下层能验的别上移到上层**；上层能验的**不删下层**——它们验的是不同东西（见 §3）。

## 2. 新增测试该放哪（决策树）

1. **纯函数 / 数据变换 / DTO 形状**（无 React、无网络）→ **L2** `node --test`（放 `packages/*/tests/`，与现有 `transcriptView.test.ts` 同风格）。
2. **单个 React 组件的渲染 / 交互 / 分支**（点按钮、输入、条件渲染）→ **L3** Vitest（`tests/components/*.test.tsx`）。依赖用 `vi.mock` 注入，**不改组件源码**。
3. **跨组件/路由/弹窗的装配，或"写完真的发出去了"这类写面交互**，但**不需要真后端**→ **L4** Playwright（`frontend/e2e/`，REST 全 `page.route`）。
4. **只能在真浏览器 + 真服务 + 真底座（+ 真模型）下才有意义**的（真 hover tooltip、真能力勾选提交、真 run 流）→ **L5** 手写 CDP / smoke 脚本。
5. **分层边界 / 词汇 / 路由与文档一致** → **L1.5** archtest 加守卫（并在 `guards.md` 登记计数）。

## 3. L5 为什么不删（重要）

`ui:accept` 的断言**全部建立在真服务 + 真数据 + 真交互**之上：它真建供应商/模型、真断言服务端状态、真 hover tooltip、真勾选能力后建会话回显指纹。L3/L4 是 mock 的，**结构性替代不了这些**——删掉 L5 = 直接降低覆盖，与"保证质量"相反。

L3/L4 与 L5 的关系是**互补**：
- L3/L4 把"常见的组件/装配正确性"从"只能真 Chrome 手工跑"里**前移**成 CI 秒级可回归——**减少对 L5 的依赖**，但不消灭 L5。
- L5 只保留"非真栈不可"的那部分。

## 4. 边界纪律

- **前端零业务依赖**：L2/L3 用的测试依赖只作 devDep，**不进运行时代码**（`@ark-mate/console` 的 devDeps）。
- **L3/L4 不改组件运行时行为**：需要注入点时只允许"加可选 props / 抽 hook"这类**不改变默认行为**的改动，且改完必过 `go test ./internal/archtest/`（前端边界守卫）。
- **e2e mock 形状必须与真实 REST 一致**（2026-11-05 踩坑：`/workspaces` 误 mock 成数组而非 `{items,writable}`，`workspaces?.items.length` 崩成白屏，却因 `#root.innerHTML` 仍有 antd 空壳而假象"渲染了"）。用 `page.on('pageerror')` 抓运行时异常，别只看 innerHTML 长度。
- **CI 只跑 L1/L1.5/L2/L3/L4**（纯 headless + mock + stub）；L5 留本地（要真 Chrome/Electron/底座/模型，Linux runner 跑不出有意义结论）。见 `commands.md` 的 CI 段。

## 5. 已知环境约束（本机）

- **跑 L3/L4 必须用 fnm 的 node**（`engines: >=26 <27`），**不能用 dsh 自带 node**：后者带 hardened runtime 签名（Team ID `NAN929V4UM`），加载 rolldown 原生 binding 时被 macOS 拒（`different Team IDs`），Vitest 直接启动失败。CI 用标准 setup-node 不受影响。
- **L4 复用系统 Chrome**（`channel: 'chrome'`），不下载 Playwright 浏览器（避免写 `~/Library/Caches/ms-playwright`）。
- **pnpm store 在工作区内** `.pnpm-store`（沙箱下 `~/Library/pnpm/store` 不可写）；装卸依赖带 `--store-dir <repo>/.pnpm-store`。
- **L3/L4 产物**（`e2e/.artifacts/`、`e2e-report/`）已 gitignore。
