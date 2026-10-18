# dsh golden（A5 / ADR-9 / ADR-12）

本目录的 golden **不是**给人读的文档，而是**逐字节契约**，所以文件本身就是 adapter 的原始产出
（YAML 里加注释会破坏逐字节比对）。改了什么、为什么、在哪个版本上验的，记在这里；测试文件
`../golden_test.go` 里也有同一份理由（代码就近可读）。

## 两个文件

| 文件 | 是什么 | 谁产出 |
| --- | --- | --- |
| `dsh-v11.patch.yml` | **冻结基线**：v11 生成器对固定夹具的产出 | `../v11_frozen_test.go` 的 `buildV11DshPatchYAML`（逐字抄自 `git show 358ab27:backend/internal/manager/runtime_config.go`） |
| `dsh-v12.patch.yml` | 新 adapter（`../config.go` 的 `buildPatch`）对**同一夹具**的产出 | `buildPatch(resource.Set)` |

## 夹具

3 个供应商（`alpha-gw` / `beta.gw` / `zeta`）× 每个 2 条模型，其中 1 条设默认（`m-beta-1`）；
含多字节 `name`、双引号、反斜杠、`#`、`:`、`/`、制表符（覆盖 `yamlQuote` 的转义路径）。
两个供应商的 v11 `type` 都是 `openai`——**刻意**，用来复现 ADR-16 记的那个缺陷。

## A / B 的判据

- **A 部分（未点名的一切）必须逐字节相同**：`- id: acp` 与 `- id: llm-pi-ai` 的条目形态、
  `displayName`、`api`、`baseURL`、`models[]` 的**数组顺序**（= 逐 route 过滤后的 `ModelID` 升序，
  即 v11 `collectRuntimeConfigInput` 的排序）、`name` 的 YAML 转义、缩进与尾换行。
  证明方式是：把 `dsh-v11.patch.yml` 按**仅 B 部分**改写一遍，再与 `dsh-v12.patch.yml` 逐字节比对
  （`rewriteDshV11ToV12`）。改写函数只允许做 B 部分那一件事，多做一件比对就红。
- **B 部分（本方案点名的偏差）以新形态为期望**：

| # | 改了什么 | 为什么 | 依据 |
| --- | --- | --- | --- |
| ① | `apiKeyEnv` 由 `<TYPE>_API_KEY` 换成逐供应商引用名 `MATE_<ID规范化>_<哈希8>_API_KEY` | v11 的 `envKeyOf(type)` 在逐 route 循环里求值，而 `providers` 表对 `type` 无唯一约束 → 两个同 type 的供应商共用一把密钥 → 至少一条 route 用错密钥。`MATE_` 命名空间同时避免被用户 shell 里的同名变量静默遮蔽 | ADR-16、设计 §实测事实 附带发现 2/3 |

**未迁移字段（刻意不在夹具里，由用例单独登记）**：v11 的 `projections.<runtime>.compat` 会写成
`compat: {…}`，而 v12 的 `resource.Provider` 没有这个字段（设计 §载体差异把它登记进「未迁移字段」
清单）。`golden_test.go` 的 `TestGoldenDshCompatHasNoV12Exit` 证明"拿掉那一行之后两份产出逐字节
相同"——于是这是被点名、被机器看见的一处差异，而不是混进 A 部分的静默差异。
`api` 这一项**不进 B**：v11 从 `projections.<runtime>.api` 取值、v12 固定为 `openai-completions`，
夹具两边的值本来就相同（协议本期只有 openai 一种），所以它落在 A 部分的逐字节范围里。

## 版本与重跑义务

实测依据是随包 **dsh 0.2.0-rc.2**（2026-10-18 在 **0.2.1-rc.2** 上复测：清单/凭据/目录三条链均通过）：

- 清单通道：`.dsh/docs/notes/probes/dsh-acp-catalog.mjs`（机制）、
  `.dsh/docs/notes/probes/gate1-dsh-probe.mjs --platform-writes`（平台写盘全链路）；
- 凭据面（与本 golden 无关，但同属 dsh 侧交付）：`.dsh/docs/notes/probes/h6-dsh-credentials-probe.mjs`
  （7/7 PASS）。

按设计 §行为基线与等价性验证 的规则 2，**改 B 部分必须同时重跑端到端闸门 A6**；版本升级后同理。
