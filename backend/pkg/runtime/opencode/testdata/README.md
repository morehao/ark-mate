# opencode golden（A4 / ADR-9 / ADR-12）

本目录的 golden **不是**给人读的文档，而是**逐字节契约**，所以文件本身就是 adapter 的原始产出
（JSON 里写不了注释）。改了什么、为什么、在哪个版本上验的，记在这里；测试文件
`../golden_test.go` 里也有同一份理由（代码就近可读）。

## 两个文件

| 文件 | 是什么 | 谁产出 |
| --- | --- | --- |
| `opencode-v11.json` | **冻结基线**：v11 生成器对固定夹具的产出 | `../v11_frozen_test.go` 的 `buildV11OpencodeJSON`（逐字抄自 `git show 358ab27:backend/internal/manager/runtime_config.go`） |
| `opencode-v12.json` | 新 adapter（`../config.go` 的 `buildConfig`）对**同一夹具**的产出 | `buildConfig(resource.Set)` |

## 夹具

3 个供应商（`alpha-gw` / `beta.gw` / `zeta`）× 每个 2 条模型，其中 1 条设默认（`m-beta-1`）；
含多字节 `name`、双引号、反斜杠、`#`、`:`、`/`、制表符。两个供应商的 v11 `type` 都是 `openai`
——**刻意**，用来复现 ADR-16 记的那个缺陷（`envKeyOf(type)` 让它们共用一把密钥）。

## A / B 的判据

- **A 部分（未点名的一切）必须逐字节相同**。证明方式不是直接比两份字节（B 部分必然不同），
  而是把 `opencode-v11.json` 按**仅 B 部分**改写一遍，再与 `opencode-v12.json` 逐字节比对
  （`rewriteOpencodeV11ToV12`）。改写函数只允许做下面三件事，多做一件比对就红。
- **B 部分（本方案点名的偏差）以新形态为期望**：

| # | 改了什么 | 为什么 | 依据 |
| --- | --- | --- | --- |
| ① | 不再生成 `options` 块（`api` / `compat` 随之没有出口） | `options` 是 opencode **V1** 的成员名；官方迁移指南逐字说 provider 的 `options` 按请求角色拆成 `settings`/`headers`/`body`，且禁止在一个 V2 条目里混入 V1 形态 | ADR-11、设计 §官方约定合规性核对 ① |
| ② | `package` 由 `@ai-sdk/openai-compatible` 换成 `@opencode/ai/providers/openai-compatible` | 裸 npm 名只是被 2.0.26 二进制的别名表接住，"今天恰好被兼容"不是契约；官方 V2 原生写法是内置包路径。协议本期只有 openai，所以该字符串由 adapter 内置 | ADR-11、设计 §官方约定合规性核对 ② |
| ③ | `env` 由 `["<TYPE>_API_KEY"]` 换成逐供应商引用名 `["MATE_<ID规范化>_<哈希8>_API_KEY"]` | v11 的 `envKeyOf(type)` 在逐 route 循环里求值，而 `providers` 表对 `type` 无唯一约束 → 两个同 type 的供应商共用一把密钥 → 至少一条 route 用错密钥。同时引用名走 `MATE_` 命名空间，避免被用户 shell 里可能已导出的"知名"名（`OPENAI_API_KEY`）静默遮蔽 | ADR-16、设计 §实测事实 附带发现 2/3 |

**`_arkMate.generator_version` 保持 1**（A 部分）：模板改了要不要 +1 这个判断会把它变成 A 部分差异，
而该值只影响归属标记本身（代际键由编排层按内容摘要驱动）。要动它请走"新增 B 条目"的流程。

**未迁移字段（刻意不在夹具里）**：v11 的 `models.<id>.extra` 会原样并进配置，而 v12 的模型没有这个
字段（Q3 删除，设计 §载体差异把它登记进「未迁移字段」清单）。夹具不带它，是有意的；`golden_test.go`
的 `TestGoldenOpencodeExtraHasNoV12Exit` 把这件事**机器登记**下来，避免差异悄悄混进 A 部分。

## 版本与重跑义务

B 部分与端到端行为都**绑版本**：本 golden 的实测依据是随包 **opencode 2.0.26**
（`.dsh/docs/notes/probes/gate1-opencode-probe.mjs`，三条 PASS + 两条反证）。按设计
§行为基线与等价性验证 的规则 2，**改 B 部分必须同时重跑端到端闸门 A6**；版本升级后同理。
