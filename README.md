# ark-mate

**底座 + 其上的个人 Agent 助手**：底座是 `mate` 内核（多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约），其上是面向用户的**个人 Agent 助手**，**聚焦桌面端**；单机、单操作员、开箱即用，执行能力来自 DeepSeek Harness（`dsh`）等 ACP 兼容的开源运行时。

> A personal agent assistant on top of a reusable agent core — DeepSeek Harness and other ACP-compatible executors behind one control plane. Desktop-first.

`ark-mate` 只做**数据管理、控制面与交付形态**——skill 执行、MCP 工具调用、推理循环等运行时能力一律复用底座原生实现；平台不重造运行时。

---

## 这个产品是什么

ark-mate 是装上就能用的**个人 Agent 助手**：建会话、跑起来、看见过程与结果。它由两层构成，**两层职责并列成立**（不是「先做产品、顺带提供底座」，也不是「平台为主、控制台只是演示」）：

| 层 | 是什么 | 面向谁 |
| --- | --- | --- |
| **底座层**（`mate` 内核） | 多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约 + 交付形态 | 「把控制面接到自己的 runtime / 自己的前端上」 |
| **产品层** | 底座之上的个人 Agent 助手：会话问答、权限/提问决策、能力管理、专家、定时任务、资料库…… | 「我自己的助手」 |

**交付形态**：

| 形态 | 状态 |
| --- | --- |
| **桌面**（macOS / Windows，随包运行时——用户机器不需要 Go / Node / 预装底座） | **主线交付**，主线排期只推进它 |
| **浏览器入口**（`make dev`） | **开发形态**，不是交付面；服务的是与交付态**同一份产物** |
| **容器 / 自建二进制 + 外挂 dist** | **维护态**：保留可用、继续被测试、可修缺陷；不加新功能、不进主线排期 |

**边界（明确不做）**：不做运行时能力本身；不做多用户 / 团队 / 登录 RBAC；不做跨 agent 协作（一次会话只绑一个 runtime）；不做远程会话通道；不做 IDE 外壳。底座由构建期固定、用户不能选 runtime；内置适配只有 `dsh`，但 adapter 层完整——接新底座 = 新建 `internal/infra/<id>/` + 装配根加一行，不改框架。完整边界与代价见 [`docs/positioning.md`](docs/positioning.md) 与 [`docs/decisions.md`](docs/decisions.md)。

## 产品功能（概览）

> 每条只一句话；**功能细节见 [`docs/features.md`](docs/features.md)**，技术机制见 [`docs/architecture.md`](docs/architecture.md)。

- **会话问答**：多轮流式会话（正文 / 思考 / 工具卡），失败轮就地重试，中断续聊，搜索、导出、断线重连。
- **交互决策**：权限确认（含会话级「始终允许 / 始终拒绝」记忆）与结构化提问（按 JSON Schema 渲染表单）。
- **排队与插话**：会话忙时可排队或「打断并发送」，已排队项可撤下 / 编辑 / 重排 / 立即发送。
- **能力数据面**：Skill / MCP / Tool 的定义、启停、探测与投放；远程 MCP 经平台网关代连；会话可钉住能力子集。
- **专家**：会话级能力预设（推荐模型 + 推荐技能/MCP + 角色设定），建会话时可选。
- **定时任务**：一次性 / 每日 / 每周 / 间隔 / 每月，显式 IANA 时区；同一时刻至多触发一次，运行史如实留痕。
- **工作空间**：会话运行在可登记的工作空间目录里（也是原生权限边界），支持全系统浏览与新建目录。
- **附件 / 产物 / 资料库**：上传图片/文档、产物溯源与自动抽取、资料库目录树（结构浏览 + 关键词匹配，不含语义检索）。
- **模型与供应商**：一端点 + 一把密钥的供应商档案、应用层模型清单、由底座自报的运行时模型目录。
- **命令门禁 / 用量统计**：简单命令自动裁决、复杂命令询问；按日期聚合的轮次 / 时长 / 上下文 / 成本统计。
- **桌面形态与产品身份**：随包运行时的桌面应用；模型自述身份来自底座系统提示（产品与执行层如实区分）。

## 三分钟跑起来

**交付态（用户）**：装桌面应用即可，随包运行时——机器上不需要 Go / Node / 底座：

```bash
make desktop/dist   # 出安装包（当前只出 macOS；Windows 安装器见 docs/roadmap.md）
```

**开发态（改代码）**：自备 Go ≥ 1.26、Node 26.x、pnpm 11.x，以及至少一个 ACP 兼容底座（当前内置 `dsh`）与模型 API key。数据默认落 `~/.ark-mate`（`MATE_HOME` 覆盖）。

```bash
make dev            # 浏览器开发态：后端 :10001 直接服务构建产物（无 HMR，改完刷新）
make desktop/dev    # 桌面态（Electron 壳 + 真核心）：验壳 / native / 凭据 / 打包前行为
```

> **改 UI 为什么走 `make dev`**：它服务的就是 `frontend/apps/web/dist`——**和交付态同一份产物、同一套 `/v1/mate` 契约**，只是没有 Fast Refresh（改完等一次增量重建、刷新浏览器）。**别同时跑** `make dev` 与 `make desktop/dev`（两个核心共用 `~/.ark-mate/mate.db`）。
>
> 详细前置、`RUNTIME_BIN`（指定用哪一份底座）、前后端分离开发、CLI 子命令、容器等，见下方「完整的前置与开发」。

## 完整的前置与开发

<details>
<summary>点开：前置依赖、dsh 版本、构建 / 启动、开发循环、验收、容器（细节见 docs/agents/commands.md 与 docs/deployment.md）</summary>

### 前置

| 依赖 | 说明 |
| --- | --- |
| Go ≥ 1.26 | `go.mod` 声明 `go 1.26.1`；Go 命令一律在 `backend/` 下跑 |
| Node 26.x（LTS） | 见 `frontend/.nvmrc` 与 `frontend/.node-version`（精确版本，唯一事实源） |
| pnpm 11.x | 见 `frontend/package.json` 的 `packageManager` |
| dsh（deepseek-harness） | npm 包 `@deepseek-ai/dsh`，需 `@next`（`latest` 缺 `acp` profile 模板） |

```bash
npm i -g @deepseek-ai/dsh@next     # 或 npx --yes @deepseek-ai/dsh@next --profile acp ...
dsh --profile acp --help           # 验证可用
```

模型凭证：harness 读取进程环境 `DEEPSEEK_API_KEY`（若本机 `dsh` 已配好 profile/home，无环境变量也能跑）。

### 构建并启动带面板的服务（维护态，可选）

> 主线交付是桌面。核心**不自带**面板资产：资产只有 `frontend/apps/web/dist` 一份，起服务时用 `--web-dir` 指过去，没给就拒绝启动（退出码 3）。**构建不依赖 CGO**（纯 Go SQLite）。

```bash
make web                                                   # 构建前端产物 → frontend/apps/web/dist
cd backend && CGO_ENABLED=0 go build -o ../mate ./cmd/mate # 产物在仓库根 ./mate
./mate serve --addr 127.0.0.1:10001 --web-dir "$PWD/frontend/apps/web/dist"
```

默认监听 `127.0.0.1:10001`（仅回环，平台**不做鉴权**）。对外部署请在**反向代理层**自行加鉴权。完整环境变量、配置目录（`.ark-mate/config`）、Host/Origin 矩阵、桌面态反向代理等见 [`docs/deployment.md`](docs/deployment.md)。

### 前后端分离开发

```bash
make dev          # 一条命令同时起前后端（Ctrl-C 一起收掉）
# 或分开：make server + cd frontend && pnpm build:watch（pnpm dev 现会直接报错，见 docs/agents/commands.md）
# 指定用哪一份底座：make dev RUNTIME_BIN=<可执行文件>（详见 docs/agents/commands.md）
```

> 用量/会话统计需要 sqlite 持久层（`make dev` / `make server` 默认 `MATE_STORE=sqlite`）；纯内存用 `MATE_STORE=memory make dev`。

### 验收与容器

```bash
make verify/ui-product       # 浏览器靶（开发态即交付件）：REST/WS 冒烟 + 界面验收
make verify/desktop-dod      # 桌面 DoD 汇总
make image                   # 最小镜像（只含 mate 本体，不含底座）
docker compose up -d --build # 等价 compose 路径（变量见 .env.example）
```

命令配方与 `RUNTIME_BIN` 细节见 [`docs/agents/commands.md`](docs/agents/commands.md)，容器与部署全表见 [`docs/deployment.md`](docs/deployment.md)。

</details>

## 文档索引

> `docs/` 下是**当前有效的对外 / 产品面口径**。外部产品的对照与逆向分析不进 `docs/`，一律在 `.dsh/docs/`（gitignore）。对外契约（路由、错误码、WS 帧）见本文末尾「对外契约参考」，由守卫与代码双向比对。

| 文档 | 讲什么 | 什么时候读 |
| --- | --- | --- |
| [`docs/features.md`](docs/features.md) | 产品功能说明（每项能做什么、边界在哪） | 想了解「这个助手能干什么」 |
| [`docs/contract.md`](docs/contract.md) | 控制面契约（REST 路由表、业务错误码、WS 帧格式）权威摘要 | 接 API / 改路由 / 改错误码 |
| [`docs/positioning.md`](docs/positioning.md) | 定位与边界（做什么 / 不做什么 / 一致性取向） | 判断某项需求是否在定位内 |
| [`docs/architecture.md`](docs/architecture.md) | 架构总览（分层、进程拓扑、数据模型、契约面、能力面、前端桌面、目录地图） | 第一天接手本仓 / 深挖技术实现 |
| [`docs/decisions.md`](docs/decisions.md) | 决策登记（定位口径与关键决策的**唯一事实源**，含每条代价） | 想知道「为什么变成这样」 |
| [`docs/roadmap.md`](docs/roadmap.md) | 排期（L 段承诺 + B/C 段方向） | 看接下来做什么 |
| [`docs/unimplemented.md`](docs/unimplemented.md) | 未实现能力与降级清单（**唯一汇总口径**） | 判断能力边界 / 降级事实 |
| [`docs/acceptance-matrix.md`](docs/acceptance-matrix.md) | 桌面与浏览器的一致性验收矩阵（差异清单 + 靶） | 加桌面能力 / 改验收 |
| [`docs/dsh-acp-adaptation.md`](docs/dsh-acp-adaptation.md) | 内置底座 dsh 的适配事实（启动形态、能力基线、隔离） | 改 dsh 适配 / 排查握手 |
| [`docs/deployment.md`](docs/deployment.md) | 部署与运维（二进制 + 外挂 dist、systemd、容器、环境变量全表、备份、排障） | 部署 / 上线 / 排障 |
| [`docs/expert-management.md`](docs/expert-management.md) | 专家管理页的产品与交互设计 | 改专家页 |
| [`docs/platform-capability-protocol.md`](docs/platform-capability-protocol.md) | 平台能力 MCP 工具面契约（automation / expert / library 三域） | 接平台内置 MCP 工具 |
| [`docs/agents/README.md`](docs/agents/README.md) | 开发纪律与机器判据的分册索引（`AGENTS.md` 的细化） | 改代码前读对应分册 |

## 仓库结构

```
backend/                单 Go module（github.com/morehao/ark-mate）
  cmd/mate              进程入口：只做子命令分发（→ internal/bootstrap/cli）
  internal/             单 module 的全部代码
    bootstrap/          组合根与生命周期：app（启停）/ assembly（唯一装配根）/ cli / cliops / maintenance
    config              进程配置（flag + MATE_* 环境变量 + 目录化配置）
    core/               跨域借用的纯词汇（code 错误码、failure、capability、identity、redact、resource…）
    domain/             业务规则与状态（session / interaction / workspace / provider / skill / mcp / automation / identity…）
    dto/dto*            请求/响应 DTO（控制器、用例层、CLI、WS 共用）
    service/svc*        用例层（控制器依赖的窄接口 + 实现；包之间零互相 import）
    persistence/        model（表形状）/ dao（列词汇 + 泛型原语）/ store（读面与事务）
    infra/              跟外面说话的实现（protocol/acp、protocol/mcp、dsh adapter、credentials、dbclient…）
    runtime             运行时注册表 + Spec 契约 + Adapter 接口 + 隔离投影
    drivers/            接线：域端口 ↔ 适配体（mechanism / registry）
    sessionruntime      运行期编排（会话执行 / 权限 / 断连语义）
    controller/         只收 service 接口 + DTO + gin
    router              唯一路由装配点 + 前端资产交付
    archtest            架构守卫（机器判据的唯一集中地）
frontend/               pnpm workspace（apps/* + packages/*）
  apps/web/             入口 + 组装；产物 dist 是唯一一份 UI
  apps/desktop/         桌面产品（Electron 壳 + 打包）
  packages/console      界面本体（React 19 + antd 6 + zustand）
  packages/{types,api,ui,desktop-contract,tsconfig}
docs/                   对外 / 产品面文档（见上「文档索引」）
```

更细的分层、依赖方向与目录地图见 [`docs/architecture.md`](docs/architecture.md) §4、§9；目录边界纪律见 [`docs/agents/layout.md`](docs/agents/layout.md)。

---

## 对外契约

> 控制面契约（REST 路由表、业务错误码、WS 事件帧格式）的权威摘要已独立成 [`docs/contract.md`](docs/contract.md)。它是本项目对外契约的事实面，由守卫与代码**双向**比对（路由 `TestReadmeRoutesMatchRouter`、错误码 `TestReadmeCodesMatchRegistry`、WS 事件类型冻结）——**改路由 / 事件 type / 错误码必须同一次提交同步该文档**。契约的设计背景见 [`docs/architecture.md`](docs/architecture.md) §6。

---

## 协议 / License

[MIT](./LICENSE)
