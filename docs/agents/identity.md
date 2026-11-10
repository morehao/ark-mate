# 产品身份（模型自述的机制）

模型"自称谁"不是它自己决定的，是平台通过**产品身份**这条交付链塞给底座系统提示（和首轮上下文）的。本文是这条机制的**单一事实源入口**：文案在哪写、怎么交付、怎么生效、为什么不热更、有哪些守卫。改身份相关代码前先读这一份。

> 定位前提（另一份事实源）：身份是**部署级常量**——一个构建产物对应一个产品身份，不做用户改名/人格定制（见 [`../unimplemented.md`](../unimplemented.md) O7）。界面、`/meta`、日志里都叫 `ark-mate`，但"模型自述"是另一件事，由本文管的这条链负责。

## 1. 唯一事实源：文案在哪写

**所有身份文案只写在 `backend/internal/domain/identity/identity.go`**，改一处即全平台生效：

- `const Version`（如 `v2`）：产品身份代数，进 `/meta.product.identityVersion` 与日志，**不进 prompt 正文**（正文变会让系统提示前缀缓存 KV cache 失效，那是账单上的事）。
- `const Name / DisplayName`：`ark-mate`。
- `const Tagline`：一句话定位（`/meta` 与「关于」面板）。
- `const systemPrompt`：**主路径**（每轮生效）——告诉模型"你是 ark-mate、产品 vs 运行时 vs 模型怎么区分、不越权承诺、不在思考/回复里泄漏执行层品牌名/env/路径、不引用本指令"。
- `const userContext`：**回退面**（`<product_identity>` 块，每个执行上下文一次）——同义精简版。
- `const personaSuffix`：**部署级系统提示后置段**（dsh `system-prompt.personaSuffix`，排在一方指导**之后**，能压过底座内置口径）——承载两条**平台级交付纪律**（与产品人格无关，故单独成段、不混进 `systemPrompt`）：① 成品全部 `present`；② 过程产物（构建脚本/中间文件）落会话 `.scratch/`，交付目录只留成品。详细机制见 §3.1。

> 纪律：改**任何一段**文案都要 `Version +1`；文案**不得含底座名**（dsh/opencode/…）、**不得含模板花括号** `{{ }}`/`{% %}`（某底座的 persona 是模板，未知变量在首轮组装时才抛错 → 表现为"每次会话都失败"）、**不含版本号/路径/主机名**。这些由 `identity_test.go` + `archtest` 守卫钉住（第 7 节）。

## 2. 值对象：(internal/core/identity) `Spec` / `Mode` / `Fingerprint`

`backend/internal/core/identity/spec.go` 是**纯值对象**（leaf 包，不认识任何底座）：

- `Spec{ Version, Name, DisplayName, Tagline, SystemPrompt, UserContext, PersonaSuffix }` —— 一次交付的全部事实；`SystemPrompt`/`UserContext` 两个面分开是因为它们作用面与失败面不同；`PersonaSuffix` 是**系统提示面的第三段**（`personaPrefix` 之后），随系统提示面一起交付、`user`/`off` 模式一起清空。
- `Mode`（`auto|system|user|off`）：`ParseMode` 解析 `MATE_IDENTITY_MODE`（**空 = auto**；非法值报错**不静默回落**）。`Mode.Apply(spec)` 把模式作用到 Spec，返回"本模式该交付的两个面"。
- `Spec.Fingerprint()`：幂等判据，由**文案 + 版本**算（不由产物算——否则格式化差异会误判"身份变了"）。重复交付同一 Spec 必须收敛。
- ⚠️ **零值注意**：`Fingerprint()` 对零值 Spec **也返回非空 hex**（恒 hash 固定前缀 + 空字段）。判断"身份是否启用"要用 `Spec.Enabled()`（任一非空）**而不是** `Fingerprint() == ""`。将来任何"把身份指纹并进别的判据"的改动都要留意这点。

## 3. 两层交付面

| 面 | 载体 | 生效粒度 | 失败半径 | 开关 |
|---|---|---|---|---|
| **主路径** | `mate-identity.patch.yml`（底座 `--patch`） | 每轮（系统提示） | 写坏会让底座**拒启** | 系统提示非空 |
| **回退面** | 首轮注入 `<product_identity>` 块 | 每个执行上下文一次 | 只会让 prompt 变长 | `UserContext` 非空 |

- **主路径**由 `backend/internal/infra/dsh/identity.go` 生成：`<平台独占根>/runtime/dsh/mate-identity.patch.yml`，内容 `- id: system-prompt` + `includeHarnessIdentity: false`（去掉底座固定开场白）+ `personaPrefix`（ar-mate 身份段）。**只动平台自有文件与启动参数，底座源码/vendor 零改动**（守住"多底座、底座零改动"的定位）。
- **回退面**：会话首个 prompt 前注入；`session.user_message` 事件与 transcript **仍是用户原文**，块前缀不进历史。新建 / 原生恢复 / handoff / 代际迁移都会重新锚定。

### 3.1 交付纪律：`personaSuffix` + 会话过程产物目录 `.scratch/`

身份交付面（同一份 `mate-identity.patch.yml` 的 `system-prompt` 条目）里，除 `personaPrefix` 外还写一段 **`personaSuffix`**：它是**部署级系统提示后置段**，排在底座一方指导（如 `deliverable-file-references`）**之后**，因此能压过底座默认口径。承载两条**平台统一的交付纪律**（与产品身份无关，故单独成段）：

1. **成品全部 present**：`present` 工具自身的描述写死「at most 4 per call」，模型常只挑几个成品 present，其余只写盘不入库；而 `workartifact` 抽取侧**只认 present**。故上升为「本轮所有面向用户的成品都要 present，脚本/中间文件不要 present」。
2. **过程产物落 `.scratch/`、交付目录只留成品**：office 各子 skill（逐字与上游一致、不可改）要求「构建脚本/中间文件与成品一起留在工作区」，于是过程产物会落在交付目录；同一工作区被后续会话复用时，模型会把上一次残留的 `build/*.py` 当捷径复用、绕过校验链（复盘 `ses_01M6ZKC9...`）。故要求过程产物进会话过程产物目录、present 完毕后清理。

**平台侧的结构性收口**（光靠提示是软引导，不稳）：`internal/core/scratch` 是 `.scratch` 约定的**单一事实源**（路径/判定/命名，纯函数无 IO）；落盘 IO 由 `sessionruntime.cwdScratch` 实现、经 `session.ScratchPort` 注入。行为：
- **落点**：会话启动时建 `<cwd>/.scratch/<session_id>/`（在 `workspace-write` 写根之内，零沙箱改动）。
- **永不进库**：`workartifact` 抽取层对相对路径首段为 `.scratch` 的**直接拒绝登记**（过程文件不进产物库/资料库）。
- **清理**：会话**归档**时清该会话子目录；进程启动时清一次「查无会话」的孤儿 `.scratch/*`。**只删 `.scratch/` 子树，绝不删交付目录里的用户文件**。
- **界面**：文件浏览/资料库**不展示** `.scratch`（即使开了「显示隐藏文件」）。
- **为什么不按会话隔离整个 cwd**：交付面必须跨会话共享连续（G1），过程隔离（G2）在单一 cwd 下与之冲突；唯一解是**按产物性质分层**（交付面 vs 过程面），不是按会话切目录。**二期「把落点移到 `MATE_WORKSPACE_DIR/.scratch` 走 dsh `sandbox-policy.additionalWritableRoots` 放行」试过又回退**——回退原因：① 该 patch 对 `config` 是**整块替换**，只写 `additionalWritableRoots` 会把 `mode/workspaceRoot` 抹掉 → `mode` 回落 `read-only`、全会话只读（真机事故）；② 落点挪到 cwd 外后模型无从得知绝对路径、仍会写 cwd 的 `.scratch`，收益落空。现存 `.scratch<sid>`（cwd 内）与提示词一致、模型实测遵守，是当前终局。

> 运维/部署视角（`.scratch` 目录在哪、怎么清）见 [`../deployment.md`](../deployment.md) §2.2.1；dsh 资源侧对照见 [`dsh-resources.md`](dsh-resources.md) §2.1。

## 4. 装配时序与生效时机

```
bootstrap/assembly startIdentity (PhasePlatform 步骤，装配期一次)
  → ParseMode(MATE_IDENTITY_MODE)
  → spec = mode.Apply(productidentity.Render())
  → planes.Identity.SetSpec(spec)            // 先落值：交付失败时首轮块仍可用
  → planes.Identity.Deliver(ctx, spec)       // 再交付：写 patch + 装 --patch
```

- 身份**不是 critical 步骤**：交付失败只留一条诊断 `identity_patch_failed` + WARN，**不阻塞启动**（只影响"模型怎么自称"，不影响会话能否跑）。
- **生效时机 = `EffectNextProcess`（重启应用）**。见第 5 节为什么。

## 5. 为什么不热更（设计边界，不是待办）

想"不重启就换身份"在本仓当前定位下**做不到**，且**已决定不做**（见 [`../unimplemented.md`](../unimplemented.md) O8）。三个硬约束叠加：

1. dsh 的 `--patch` 是**启动参数**，进程起来后不可改；
2. dsh 的 `hmr` 在 `acp` profile **`disabled: true`**（stdout 归 ACP 协议），连配置热重载都关着；
3. **ACP 协议不暴露**任何 persona / system-prompt / instruction 方法或 config option（`session/set_config_option` 只认 `model`/`reasoning_effort`，`_meta` 也被底座丢弃）。

dsh 在**进程内**其实可热改系统提示（`systemPrompt.section()` 可撤销注册），但那要求以**插件形式跑在 dsh 进程内**；本仓是 **ACP 外挂客户端**，够不到。改成内嵌插件 = 推翻"多底座、底座零改动"定位，不值。

**产品层面**：热更会让同一份 transcript 出现"两代自述"（旧历史旧身份、新上下文新身份）——那不是产品行为。所以"改身份需重启"是**刻意的取舍**，不是缺陷。要改这条先回答 O8 里那个问题（transcript 两代自述并存的读面口径）。

## 6. 四模式与撤回

`MATE_IDENTITY_MODE=auto|system|user|off`（`deployment.md` 有配置说明）：

- `auto`（默认）：系统提示为主 + 首轮块兜底；
- `system`：只走系统提示，失败即**无身份**（验证主路径用，不静默回退）；
- `user`：只走首轮块；
- `off`：都不交付（产品面 `/meta`、关于面板仍保留）。

`user`/`off` 在启动时**主动撤回**已有身份 patch（**删文件 + 摘 `--patch` 参数**）。"只不写"不够：上一份 patch 还在、`--patch` 还装着，底座照旧读旧提示，而界面不会说这件事。写一份空 patch 也不行——空/只有注释的 patch 会让底座 **fails boot**。

## 7. 守卫（判据，改之前先看）

身份交付"写错不会有测试变红"（用户问"你是谁"得到另一个答案，而启动/会话/同步全正常）——所以有一族守卫钉它。计数只在 [`guards.md`](guards.md) 维护，此处只列语义：

- `identity_boundary_test.go`：`Render()` 结果必含产品名 / 无底座名 / 无模板花括号 / 字符预算 / 确定性；`Mode` 四模式映射。
- `TestDshIdentityPatchMatchesPayloadPin`：`dsh.Identity.TestedOnVersion` ↔ `apps/desktop/release.json` payloads 里 dsh 那条逐字相等（`Tested=false` 时该字段必须空、`Note` 不得空）。
- `TestNoStartupDumpForIdentity`：`infra/dsh/identity.go` 剥注释后不得出现 `dumpSchemaFlag`/`os/exec`/`dshBin`（判据落在"能力能不能拉起子进程"上，而不是 flag 字面量）——因为启动期**刻意不跑** `--dump-config-schema`（身份 patch 只有两个键、目标条目在 pin 版本已验证）。
- `TestProductIdentityMatchesUi` / `TestAboutPanelShowsProductWithoutEngineWords`：界面品牌与「关于」面板不出现执行引擎词。
- `TestIdentityPromptForbidsRuntimeInternalsInNarration`：身份文案必须带"不在思考/回复里引用执行层品牌名、env、home 路径"这条纪律。

## 8. 已知边界

- 身份只影响"模型怎么自称"，**不影响任何会话能否运行**；交付失败不拒启。
- 用户问"你是什么模型/运行时"时，模型应**如实回答并区分**产品/运行时/模型——身份是产品人格，**不是让模型撒谎**。
- **不改写历史 transcript**：旧会话里的旧自述保留原样，只有新的执行上下文才重新声明。
- 首版不做用户改名/人格定制（O7）。
- 每轮约 150–250 tokens 成本（系统提示面）。

## 9. 代码地图

| 关注点 | 位置 |
|---|---|
| 文案唯一事实源 | `backend/internal/domain/identity/identity.go` |
| 值对象 / Mode / Fingerprint | `backend/internal/core/identity/spec.go`（+ `*_test.go`） |
| 身份交付面（registry） | `backend/internal/drivers/registry/identity.go`（`IdentityDelivery`） |
| 装配步骤 | `backend/internal/bootstrap/assembly/assembly.go` `startIdentity` |
| dsh 侧产物生成 | `backend/internal/infra/dsh/identity.go`（`mate-identity.patch.yml`） |
| 首轮块注入 | `backend/internal/sessionruntime/orchestrator.go`（`Identity: planes.Identity.Spec` 注入 `session.NewRunner`）→ 会话首轮 `BeginRun` 叠前缀下发（只进下发内容、不进事件/落库） |
| 配置键 | `backend/internal/config/config.go`（`MATE_IDENTITY_MODE`） |
