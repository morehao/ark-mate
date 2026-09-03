# deepseek-harness（dsh）ACP 适配要点

> 依据源码核实：`/Users/morehao/Documents/study/ai-agent/deepseek-harness`
> （2026-08/09 快照，v0.1.x，`@deepseek-ai/dsh`）。开发者预览期，接口可能 breaking，
> 以该仓库最新源码与 `packages/acp/acp/README.md` 为准。

## 关键结论（推翻旧假设）

| 旧假设（M0 文档初稿） | 现状（源码核实） |
|---|---|
| 独立 bin `dsh-acp-demo --config <cordis.yml>` | **已不存在**。2026-08-11 改名台账把 `@deepseek-ai/dsh-acp-demo` 改名为 `@deepseek-ai/dsh-acp-app`，并归入统一启动器 `dsh` 的 profile 组合（bundle，无独立 bin） |
| 仅 fresh session、断连即失效 | **会话可持久化**：`sessionCapabilities { close, list, resume }`；list/resume 跨进程（同一 `$DSH_HOME` profile 持久化根）；disconnect 走 quiescent teardown，持久会话仍可 resume |
| 需注入子进程 `DEEPSEEK_API_KEY` | 仍可透传（provider `llm-deepseek` 默认从进程环境读 `DEEPSEEK_API_KEY`），但**无需注入到子进程内部某配置**；凭据引用支持 `apiKeyEnv` 覆盖 |

## 启动形态（adapter 已按此更新）

```text
命令：dsh
参数：--profile acp
环境：无必填（DEEPSEEK_API_KEY 随子进程环境透传即可；profile provider/model 由
      profile 行配置，默认 deepseek-official / deepseek-v4-flash）
```

- 安装/运行：`npx @deepseek-ai/dsh --profile acp`；`web/headless/sdk/sdk-minimal/acp`
  五个 profile 首次使用时从内置模板自动初始化到 `$DSH_HOME/profiles/`。
- stdout 只承载 NDJSON ACP 帧（诊断走 stderr）；stdin EOF、ACP 连接关闭、SIGINT/SIGTERM
  均触发 bounded shutdown（先 drain agent 再退出）。
- `dsh --profile acp --help` 打印帮助即退出，不占用 stdio。

## 能力面（initialize，`packages/acp/acp/src/index.ts`）

- `authMethods: []` → **authenticate 立即成功，无需鉴权**（ark-forge acl 不触发 agent 鉴权分支）。
- `sessionCapabilities: { close: {}, list: {}, resume: {} }`；无 `loadSession`/fork → 对应门控报 unsupported 正确。
- `promptCapabilities`: `image` 仅当持久附件仓 + 精确 provider/model 支持才 true；`audio:false`、`embeddedContext:false`。
- 同连接可多会话并行（list 非空 → `MultiSessionConcurrent` true）。
- 会话语义：`session/new` 需**绝对路径 cwd**（先校验工作区）；可声明 stdio/HTTP MCP（stdin EOF 场景不建议 stdio MCP）；
  `session/resume` 恢复持久化会话但不回放历史（reconnect 由请求携带的 MCP 声明重建）；
  `session/set_config_option` 改 model/reasoning_effort；prompt 每会话单飞；
  `session/close` 只关目标会话；无 load/删除/fork/附加目录/终端/客户端 FS/SSE 与传输级 MCP。
- stop reason 映射（`codec.ts`）：正常/被接管 quiescence → `end_turn`；超 token → `max_tokens`；
  `session/cancel` → `cancelled`（另有 refusal 位）。

## ark-forge 侧已落地/需注意

1. `internal/runtime/deepseekharness`：Command=`dsh`、Args=`["--profile","acp"]`、Version=`dsh --profile acp`；
   Caps：`SessionsPersistent=true`、`MultiSessionConcurrent=true`、`Images=false`、`EmbeddedContext=false`。
2. 断连语义文案更新：harness 断连 ≠ 会话销毁，进行中 turn 被取消，持久会话可 resume；
   面板/M1 的 resume 入口按 `Client.Capabilities()` 裁剪（opencode 多 load/resume/fork，harness 无 load/fork）。
3. `--config <cordis.yml>` 形态废弃：config 归属 `$DSH_HOME/profiles/acp`（或 `--patch` 覆盖），
   不再由 ark-forge 传路径；若需自定义 profile 行，走 `dsh plugin --profile acp add/...` 或追加 `--patch`。
4. harness 冒烟（本机验证）：确保 `dsh` 在 PATH（或 `npx @deepseek-ai/dsh`），导出 `DEEPSEEK_API_KEY`，
   运行 `forge demo --runtime deepseekharness --prompt "..." --allow-all`（cwd 建议绝对路径）；
   预期 initialize 无鉴权直通 → session/new → prompt 流 → turn.ended。

## 引用文件（外部仓库）

- `packages/acp/acp/README.md` — protocol contract（方法矩阵/能力/MCP 信任模型/stop reasons）
- `packages/bundle/acp-app/README.md` 与 `cordis.patch.yml` — profile 组合（persona、provider/model 行）
- `packages/bundle/acp-app/src/index.ts` — cmdline 提供者（help/EOF 生命周期）
- `packages/acp/acp/src/index.ts`（initialize 能力面）、`src/codec.ts`（stopReason 映射）
- `apps/cli/README.md`、`apps/cli/src/{bin,args,profile-boot}.ts` — `dsh` 启动器与 profile 引导
- `apps/cli/README.md` 注记：profile 首用自动初始化、`$DSH_HOME/cordis.patch.yml` 覆盖层
