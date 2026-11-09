# dsh 资源初始化：桌面端 vs ark-mate

> 从仓库根 `AGENTS.md` 拆出的分册（2026-11-08）。本册回答一个问题：**dsh 官方桌面端在初始化时会准备、注册、注入哪些资源，ark-mate 用 `mate-acp` 组合补了哪些、刻意不补哪些**——并给一份「升 dsh 版本时要同步的锚点清单」，让 dsh 滚动升级时可长期跟踪。
>
> **事实源是代码，不是本文**：dsh 侧事实在 dsh 源码 checkout（`dsh-v0.2.2-rc.1`，下文路径以该 tag 为准，根为 `deepseek-harness`）；ark-mate 侧事实在本仓 `backend/internal/infra/dsh/` 与 `frontend/apps/desktop/scripts/primary-runtime/`。本文只写「当前磁盘/源码上真实存在什么」+ 来源路径；与代码冲突时以代码为准。
>
> **不做的事**：本文不描述 ACP 适配面本身（启动形态、鉴权、会话结算、凭据纪律等）——那是 [`dsh-acp-adaptation.md`](../dsh-acp-adaptation.md) 的主题；本文只补「资源初始化」这一横向切面，二者交叉引用。

## 0. 一句话结论

dsh 官方把「能力」切在 **profile 模板 × bundle 的 patch** 上：`acp` 模板只挂 `dsh-base + dsh-acp-app`（**只有 ACP 传输，没有 office、没有任何文件交付插件**），而要「生成 office 文件 + 交付给用户」的全部资源都在 **`sdk-app` / `web-app`** 这两个 bundle 里。ark-mate 跑的是 ACP 传输，所以它**自建 `mate-acp` profile**（基座 = acp 模板），再用 `cordis.patch.yml` 覆写层把 office 三件套 + `present` + `deliverable-file-references` + `time-context` + `tool-ask-user` 声明式补回来；office 所需的 Python/Node/pnpm 运行时**不在 dsh 载荷目录内**（dsh 官方同样把它放在独立的 `primary-runtime` 载荷里，并非只有 ark-mate 要），ark-mate 只是**自随一份逐字节相同**的 `primary-runtime`，经 4 个 `DSH_BUNDLED_OFFICE_*`/`DSH_PRIMARY_RUNTIME` env 注入。**「要不要 Python」两边一样，「Python 从哪来、怎么送到运行时前面」两边不同。**

## 1. 两套初始化范式总览

| 维度 | DSH 桌面端（官方） | ark-mate（`mate-acp`） |
| --- | --- | --- |
| 组合单元 | **profile 模板** `acp` / `web` / `headless` / `sdk` / `sdk-minimal`（`packages/util/official-bundles/src/bundles.ts:10-26`） | 自建持久 profile **`mate-acp`**（`backend/internal/infra/dsh/profile.go:28`），bundles 取 acp 模板同款 `[dsh-base, dsh-acp-app]`（`profile.go:36-44`） |
| 能力面来源 | bundle 的 `cordis.patch.yml`（`dsh-base` 535 行核心管线 + 各 app bundle 追加） | dsh-base（acp 已含）+ 自己的 `cordis.patch.yml` 覆写层（`profile.go:78-108`） |
| office 能力 | 在 `sdk-app` / `web-app` 里（见 §2） | 覆写层逐字搬 office block（`profile.go:78-108`） |
| office 运行时（Python/Node/libreoffice） | 同样来自 `primary-runtime` 载荷（含 Python），由 runtime-bootstrap 在进程启动前 set `DSH_PRIMARY_RUNTIME`/`DSH_BUNDLED_OFFICE_*`（或改走不带 Python 的 `DSH_OFFICE_SIDECAR`）（`python/sdk-runtime/runtime-bootstrap.mjs:60-110`） | **自随一份逐字节相同的 `primary-runtime` 载荷**，`BundledEnv` 派生 4 个 env 经 `reg.SetLaunchEnv` 注入（`profile.go:202-227`、`backend/internal/bootstrap/assembly/prepare_runtime.go:41-59`） |
| 资源随包布局 | `<Resources>/runtime/` = `primary-runtime/` + `office-skills/` + `bin/node` + `cli/` + `versions.json`（`apps/desktop/scripts/prepare-runtime.ts:36-76`）；dsh 载荷在 `payloads/dsh/<ver>/` | dsh 载荷随包（`resources/payloads/dsh/0.2.2-rc.1/`，`release.json:10-13` pin）；`primary-runtime` 由 `make desktop/payloads` 装配（设计口径见 §4） |
| 交付声明（present） | `sdk-app:70-71`（`web-app` 亦挂） | 已补入覆写层（`profile.go:102-103`） |
| 交付提示词 | `deliverable-file-references`（`sdk-app:76-77`、`web-app:351-352`） | 已补入覆写层（`profile.go:105-106`）；另经身份 overlay 的 `personaSuffix` 追加一条「成品全部 present」的部署级口径（见 §2.1） |
| skill 发现根 | `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents`（`packages/util/home-paths/src/index.ts:142-193`） | 投影到 `${HOME}/.agents/skills`，**刻意不注入 `DSH_AGENTS_HOME`**（`backend/internal/infra/dsh/adapter.go:51-58`；理由见 `dsh-acp-adaptation.md` §3） |

## 2. 能力面逐项对照（profile / bundle 层）

**判据**：`dsh-base` 不含以下任何一个文件交付插件（`base/cordis.patch.yml` 里 `present`/`workspace`/`deliverable-file-references`/`file-reference-local`/`connection` 的 `id:` 命中数均为 0，实测）；它们只在 `sdk-app` / `web-app` 的 patch 里被 `insert`。

| 资源 / 插件 | DSH 桌面端官方落点 | ark-mate 现状 | 说明 |
| --- | --- | --- | --- |
| `workspace-dependencies` | `sdk-app:31-36` | ✅ 覆写层 `profile.go:84-88` | office 三件套之一；`disabled: !!js` 门控 `DSH_PRIMARY_RUNTIME` |
| `skill-office` | `sdk-app:38-44` | ✅ 覆写层 `profile.go:90-96` | office 七件套资产根 + libreoffice-kit cli |
| `office-to-pdf` | **仅 `web-app:283-284`**（sdk-app 无） | ✅ 覆写层 `profile.go:98-99` | ⚠️ 见 §5 F6：它借自 web-app，非 sdk-app |
| `present`（`dsh-tool-present`） | `sdk-app:70-71`、`web-app` | ✅ 覆写层 `profile.go:102-103` | 模型产出文件后声明交付物 |
| `deliverable-file-references` | `sdk-app:76-77`、`web-app:351-352` | ✅ 覆写层 `profile.go:105-106` | 交付行为系统提示词 |
| `workspace` | `sdk-app:54-55`、`web-app:87-88` | ❌ 未补 | P1：present 的 cwd 路径基准（见 §5） |
| `file-reference-local` | `sdk-app:73-74`、`web-app:93-94` | ❌ 未补 | P1/P2：`@file` 补全；取决于 ACP 是否暴露该交互面 |
| `connection` / `file-upload` | `sdk-app:48-49` / `51-52` | ❌ 未补 | P2/不相关：ACP 已有传输；上传走平台 attachmentstore |
| `time-context` | `web-app/presets/standard.patch.yml`（sdk-app:61） | ✅ 覆写层 `profile.go`（WP-A） | 时间上下文（当前时间/时区/经过时长）；inject `['agents','sessionProjections']` 均由 base 满足 |
| `tool-ask-user`（`ask_user_question`） | `web-app/presets/standard.patch.yml`（sdk-app:64） | ✅ 覆写层 `profile.go`（WP-A） | 模型侧结构化提问；平台 elicitation 卡片通道入口；inject `['tools','userQuestions']` 均由 base 满足（`ctx.userQuestions` = base 的 `dsh-user-questions`） |
| `schedule` / `tool-schedule` | `sdk-app:57,67` | ❌ 未补 | 不相关：平台有自建 automation（两套事实源冲突，见缺口笔记 §6，故刻意不补） |

> 说明：`web-app` 里的 `dsh-client-ui-*`（`web-app/cordis.patch.yml` 大量）是 Web 前端渲染层，ark-mate 桌面 ACP 不渲染 dsh Web 前端，**全部不相关**，不列。

### 2.1 交付口径：全部 present + 交付目录清理（personaSuffix 覆盖）

**问题一（声明不全）**：`present` 工具自身的描述写死了「Usually the 1-2 most important deliverables; at most 4 per call.」（`@deepseek-ai/dsh-tool-present/lib/index.js`）。这句在**插件内部硬编码**，没有 config 面能改（只有 `maxFiles`，默认 8，且只影响硬上限的报错阈值，不影响模型读到的文案）。于是模型一次生成多格式成品时，常只挑 4 个 present，其余只写盘不入库——平台侧 `workartifact` 又**只认 present**（见 [`resolve.go`](../../backend/internal/infra/workartifact/resolve.go)），结果卡片与「产物」都只覆盖被 present 的那几个。

**问题二（过程产物污染工作区）**：office 各子 skill（`office-docx` 等，逐字与上游一致、不可改）明确要求「**Keep source scripts, intermediate files, and final documents in the task workspace**」——构建脚本、中间产物会和成品一起留在交付目录。同一 `default` 工作区被后续会话复用时，模型会把上一次残留的 `build/*.py` 当现成捷径直接改数重跑，**绕过 office 子 skill 的校验链**（`office-recalculate`/`office-render`）。复盘见会话 `ses_01M6ZKC9ZKN5X5427HZPTDEZB1`：只加载了 `office` 路由与 `office-export-pdf`，没走 `office-docx/pptx/xlsx`，正是捡了残留 `build/`。

**做法**：`deliverable-file-references`（一方指导）里没有这两条约束，且排在系统提示较前位置。平台用 `system-prompt` 的 **`personaSuffix`** 键（部署级**后置段**，排在一方指导之后，能压过前面的默认口径）追加统一约束：**① 本轮所有面向用户的成品都要 present（一次可到 8 个），脚本/中间文件不要 present；② 成品 present 完毕后清理交付目录里的临时脚本、缓存与中间渲染目录，只留成品**（避免下次会话复用陈旧脚本跳过校验）。文案唯一事实源在 [`internal/domain/identity/identity.go`](../../backend/internal/domain/identity/identity.go) 的 `personaSuffix`，经 [`dsh/identity.go`](../../backend/internal/infra/dsh/identity.go) 的 `buildIdentityPatch` 写成 `personaSuffix: |-`（为空时**不写该键**）。这样**不动 dsh 插件源码、不改 `present` config**，只借 dsh 官方 `system-prompt` 配置面，载荷仍逐字节与上游一致。

**升级锚点**：`personaSuffix` 是 dsh `@deepseek-ai/dsh-system-prompt` 的 `Config` 正式字段（`lib/types/index.d.ts` 的 `Config.personaSuffix`）。升级 dsh 时确认该键仍在 schema；`upgrade_probe` 的 `--dump-config-schema` 会因未知键而报错，从而在升级时暴露。

## 3. 逐资源初始化动作 × 落点 × env × 来源

### 3.1 dsh 载荷（不含 Python；Python 另在 primary-runtime，见 §3.2）

- **是什么**：`@deepseek-ai/*` 插件集 + 随包 `npm` + `libreoffice-kit(+platform)` + office 七件套资产，**不含 Python**。
- **ark-mate 落点**：`frontend/apps/desktop/resources/payloads/dsh/0.2.2-rc.1/`（`payload.json` 记 `version=0.2.2-rc.1`、`files=25746`、`bytes=453274083`）；由 `release.json:10-13` 的 `payloads[]` pin 版本。
- **入口**：`node_modules/@deepseek-ai/dsh/lib/bin.js`（`payload.json` 的 `entry`）；平台侧经 `MATE_DSH_ENTRY` 指到它（`profile.go:228-266` 上溯推导 office-skills / primary-runtime）。
- **来源**：dsh `python/sdk-runtime/runtime-bootstrap.mjs`、`apps/desktop/scripts/prepare-runtime.ts`。ark-mate 侧 `frontend/apps/desktop/scripts/prepare-primary-runtime.mjs`（装配入口）。

### 3.2 primary-runtime（Python + Node + pnpm + office-skills）——ark-mate 自随，内容与官方相同

> **先澄清「谁需要 Python」**：Python 是 **office 生成能力**的依赖，不是 dsh 底座的依赖。DSH 官方要 office 的那套 bundle（`sdk-app`/`web-app`）**同样需要 Python**，它的 `primary-runtime` 载荷里就带 Python 3.12.14；ark-mate 因为主动给 `acp` 基座补了 office 能力，所以同样继承这条依赖。**两边「要不要 Python」一致，差异只在「从哪来、怎么送进运行时」**：dsh 走 runtime-bootstrap 包壳在启动前 set env，ark-mate 在本进程里用 `BundledEnv` 派生注入。**不需要 Python 的例外**：`DSH_OFFICE_SIDECAR` 布局只带 standalone node + libreoffice-kit（供只渲染/转换、不跑 Python 脚本的场景）。

- **是什么**：python-build-standalone **Python 3.12.14**（`pythonRelease=20260901`）+ 独立 **Node 24.21.0** + pnpm + 13 个随包 Python 库，以及从 `dsh-skill-office/assets` 拷来的 `office-skills/`。
- **13 个库**（`frontend/apps/desktop/scripts/primary-runtime/lock.json` `pythonPackages`）：numpy 2.3.5、pandas 3.0.1、python-dateutil 2.9.0.post0、six 1.17.0、tzdata 2025.2、python-docx 1.2.0、python-pptx 1.0.2、openpyxl 3.1.5、Pillow 12.3.0、lxml 6.1.3、XlsxWriter 3.2.9、typing_extensions 4.16.0、et_xmlfile 2.0.0。
- **与官方一致性**：`lock.json` 与 dsh checkout 的 `scripts/primary-runtime/lock.json` **字节相同**（md5 一致，逐项 `nodeVersion`/`pythonVersion`/`pythonRelease`/`pythonPackages`/`targets`/`wheels` 全同）。`prepare.ts` 相对官方只有 2 处本地改动（import 路径相对化、wheel 解包改用系统 `unzip` 规避 Node 26 的 `extract-zip` promise 不 resolve bug），**不改变产物**；装配入口 `prepare-primary-runtime.mjs` 因 `import.meta.main` 是 Bun/Deno 专有而自写（Node 直跑 prepare.ts CLI 分支会静默 exit 13）。
- **落盘状态（两个主体、两个阶段，别混读）**：
  - *当前这份 checkout 的磁盘*：`resources/payloads/` 只有 `dsh/`，**`primary-runtime/` 未落盘**（需 `make desktop/payloads` 装配）。
  - *装配好 primary-runtime 的真机*：`dsh --profile mate-acp --dump-config` 已验证三件套 + present/deliverable + ACP 全在场。
  - 两者不矛盾：前者是「还没装配」，后者是「装配后」。
- **降级语义**：无 primary-runtime 时 `BundledEnv` 返回 nil → office env 不注入 → 覆写层门控如实关闭 office（agent 退回自带工具链，不误开、不拒启）。
- **来源**：`frontend/apps/desktop/scripts/primary-runtime/{lock.json,prepare.ts,smoke.py,prune-python-tests.ts,npm-platform.ts}`、`frontend/apps/desktop/scripts/prepare-primary-runtime.mjs`。

### 3.3 env 注入（4 个随包 office runtime env）

| Env Key | 指向 | ark-mate 派生点 |
| --- | --- | --- |
| `DSH_PRIMARY_RUNTIME` | primary-runtime 载荷目录（含 `runtime.json` + `dependencies/`） | `BundledEnv` `profile.go:210`；目录由 `ResolveBundledDirs` 推导（`profile.go:228-266`） |
| `DSH_BUNDLED_OFFICE_SKILLS` | `<dsh 载荷>/@deepseek-ai/dsh-skill-office/assets` | `profile.go:211` |
| `DSH_BUNDLED_OFFICE_CLI` | 随包 `libreoffice-kit/lib/cli.js` **绝对路径（必须是文件）** | `resolveOfficeCLI` `profile.go:180-188`（`DSH_BUNDLED_OFFICE_CLI` 值见 `profile.go:212`）（`statSync().isFile()` 校验，否则置空串关闭 CLI） |
| `DSH_BUNDLED_OFFICE_NODE` | `<primaryRuntime>/dependencies/node/bin/node` | `profile.go:213` |

- **派生与注入链路**：`ResolveBundledDirs()`（`profile.go:228-266`，优先 `MATE_DSH_BUNDLED_RUNTIME`/`MATE_DSH_BUNDLED_OFFICE_SKILLS` 显式指定，否则由 `MATE_DSH_ENTRY` 推导，且校验 `runtime.json` / `office-docx/SKILL.md` 存在） → `BundledEnv(...)`（`profile.go:202-227`，任一为空即返回 nil） → `PrepareRuntimeEnv`（`prepare_runtime.go:28-61`）在**每一处真正拉起底座前**调用，经 `reg.SetLaunchEnv` 写入 dsh 的 `Launch.Env`。
- **不相关的官方 env**（记录以免误补）：`DSH_OFFICE_SIDECAR`（另一种 sidecar 资源布局，ark-mate 走 primary-runtime 一种即可）、`DSH_RESOURCE_CACHE`/`DSH_PTC_RUNTIME_NODE`/`DSH_SUBPROCESS_RUNNER`（runtime-bootstrap 打包壳内部机制，ark-mate 直接 `node bin.js` 不经过）、`DSH_AGENTS_HOME`（**刻意不注入**，理由见 `dsh-acp-adaptation.md` §3）。

### 3.4 profile 落盘（mate-acp 四份文件）

- **落点**：`<DSH_HOME>/profiles/mate-acp/`（`ProfileDir` `profile.go:123`；`DSH_HOME = <MATE_HOME>/runtime/dsh`）。写入函数 `ProvisionProfile` `profile.go:135-156`（幂等 `writeIfChanged`），调用点 `prepare_runtime.go:36-40`。
- **四份文件**（`profile.go:110-121` 装配）：
  - `package.json`：`dsh.profile.bundles = [dsh-base, dsh-acp-app]`（`profile.go:32-44`）。
  - `cordis.yml`：空 entry 列表 `[]`（树靠 bundles + patch 组合）。
  - `cordis.patch.yml`：**覆写层**（`profile.go:78-108`）—— office 三件套 + present + deliverable-file-references + time-context + tool-ask-user。
  - `pnpm-workspace.yaml`：`packages: [.]`、`nodeLinker: hoisted`、`autoInstallPeers: false`。

### 3.5 平台侧其它初始化 / 投影（与 dsh 加载相关的落点）

| 资源 | 落点 | 来源 |
| --- | --- | --- |
| `DSH_HOME` 隔离 | `<MATE_HOME>/runtime/dsh`（`LauncherFallback: true`） | `adapter.go:57-58` |
| skill 投影 | `${HOME}/.agents/skills/<id>/SKILL.md`（最低优先 rank 500） | `adapter.go:176-195`；物化 `backend/internal/core/capability/skill_materialize.go` |
| 原生凭据文档 | `<$DSH_HOME>/.credentials.yaml`（`NativeDoc`，0600） | `adapter.go:96-122`；`credentials.go` |
| 模型清单 overlay | `<$DSH_HOME>/mate-models.patch.yml`（经 `--patch` 递入） | `adapter.go:89-94` |
| 身份 overlay | `<$DSH_HOME>/mate-identity.patch.yml`（经 `--patch` 递入 `system-prompt`） | `adapter.go`、`identity.go:38` |

## 4. 版本跟踪清单：升 dsh 版本时要同步的锚点文件

**触发条件**：升级随包 dsh 载荷版本（`frontend/apps/desktop/release.json` 的 `payloads[].version` + `resources/payloads/dsh/<ver>/`）时，逐一核对下列锚点，确保 `mate-acp` 覆写层与随包运行时仍与上游一致。

| # | 锚点文件（ark-mate 侧） | 对应 dsh 上游来源 | 核对内容 |
| --- | --- | --- | --- |
| A1 | `backend/internal/infra/dsh/profile.go` 的 `profilePatchYML`（`:78-108`） | `packages/bundle/sdk-app/cordis.patch.yml` 的 office block（`:31-44`）+ present/deliverable（`:70-77`）；`office-to-pdf` 来自 `web-app/cordis.patch.yml:283-284`；`time-context` / `tool-ask-user` 来自 `web-app/presets/standard.patch.yml` | insert 的 id / name / `disabled: !!js` 门控 / config getter 是否逐字一致；`time-context` / `tool-ask-user` 的 name 与 inject 依赖是否仍由 base 满足；上游改了字段要同步 |
| A2 | `backend/internal/infra/dsh/profile.go` 的 `BundledRuntimeEnvKeys`（`:158-164`）与 `BundledEnv`（`:202-227`） | `python/sdk-runtime/runtime-bootstrap.mjs:60-110` | 4 个 env key 名是否仍是上游读的那几个；`DSH_BUNDLED_OFFICE_CLI` 是否仍须指 `lib/libreoffice-kit/.../cli.js` 文件 |
| A3 | `frontend/apps/desktop/scripts/primary-runtime/lock.json` | dsh `scripts/primary-runtime/lock.json`（同 tag） | `nodeVersion`/`pythonVersion`/`pythonRelease`/`pythonPackages`/`targets`/`wheels` 逐项，最好重新 md5 比对 |
| A4 | `frontend/apps/desktop/scripts/primary-runtime/prepare.ts` | dsh `scripts/primary-runtime/prepare.ts` | 本地那 2 处改动（import 相对化、系统 unzip）是否仍成立；上游新增字段是否需带上 |
| A5 | `frontend/apps/desktop/scripts/prepare-primary-runtime.mjs` | dsh `apps/desktop/scripts/prepare-primary-runtime.ts` | `skillSource`（指向 dsh 载荷 `dsh-skill-office/assets`）、`dsh-tool-workspace-dependencies` 软链方式是否随上游入口变化 |
| A6 | `backend/internal/infra/dsh/adapter.go` 的 `Isolation`/`Projections`/`ModelConfig`/`Credential` | dsh `packages/skill/skill-filesystem`、`packages/util/home-paths`、凭据/配置面 | `DSH_HOME` 落点、`~/.agents/skills` 默认根、`.credentials.yaml` 文档形态、`--patch` 契约是否变 |
| A7 | `backend/internal/bootstrap/assembly/prepare_runtime.go` | 上游 app-boot 的 profile 初始化时机 | 拉起底座前的准备动作是否仍覆盖所有本机 spawn 路径 |

**验证口径**：改完跑 `cd backend && go test ./internal/archtest/` + 真机 `dsh --profile mate-acp --dump-config`，确认 office 三件套 / present / deliverable-file-references / ACP 全在（`x-cordis.complete=true`、诊断为空）。`smokePrimaryRuntime` 只验 Python 导入与 docx/pptx/xlsx 往返，**不触达 present/交付**，交付链需另做端到端冒烟（生成 .pptx → present → 平台登记为交付物）。

## 5. 已知脆弱点与缺口（供升级时留意）

- **F6 · `office-to-pdf` 是 web-app 的传递依赖**：`office-to-pdf` 只由 **`web-app`** 声明（`web-app/package.json:144` 的 `workspace:*`，patch 在 `web-app/cordis.patch.yml:283-284`），**`sdk-app` 里没有它**。ark-mate 的 `mate-acp` 把这一条搬进覆写层（`profile.go:98-99`）——即它是从 web-app 这条**不同 bundle 链**借来的插件。升版本若 web-app 移除或改变该依赖的挂载前提，ark-mate 的 office→pdf 会静默失效。核对 A1 时对 `office-to-pdf` 单独确认其包仍在 dsh 载荷内且 name 未变。
- **`workspace` 缺失（P1）**：`present` 校验「路径在当前 cwd 内」依赖 dsh `workspace` 服务（`tool-present/README.md`），而 `mate-acp` 未挂 `workspace`（`sdk-app:54-55`）。目前 present 已补、workspace 未补，交付链端到端需实测 cwd 来源是否被满足。
- **`file-reference-local` 缺失（P1/P2）**：取决于 ACP 是否暴露 `@file` 交互面；不影响 office 生成/交付硬链。
- **`primary-runtime` 未随包落盘时**：`BundledEnv` 返回 nil，覆写层门控把 office 关闭（如实降级）；`prepare_runtime.go:44` 会打 WARN「未解析到 ark-mate 自带的 office 随包资产」。这是设计内降级，不是缺陷。
- **`prepare.ts` 走系统 `unzip`**：构建机（mac/linux）须有 `unzip`，否则回落 `extract-zip`（Node 26 上有 bug；运行在 Node 24 场景实际安全）。

## 相关分册

- 适配面（启动形态、鉴权、会话结算、凭据纪律、模型清单交付、权限投影）：[`dsh-acp-adaptation.md`](../dsh-acp-adaptation.md)。
- office 能力排查叙事：[`office-capability.md`](../office-capability.md)。
- 构建形态与运行配置（构建期固定 runtime、随包载荷与 `doctor --verify-payload`）：[`build-and-runtime.md`](build-and-runtime.md)。
- 本机隔离纪律（读写底座 home 的约束）：[`isolation.md`](isolation.md)。
