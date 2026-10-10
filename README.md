# ark-mate

**底座 + 其上的个人 Agent 助手**：底座是 `mate` 内核（多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约），其上是面向用户的**个人 Agent 助手**，**聚焦桌面端**；单机、单操作员、开箱即用，执行能力来自 OpenCode、DeepSeek Harness 等 ACP 兼容的开源运行时。

> A personal agent assistant on top of a reusable agent core — OpenCode, DeepSeek Harness, and other ACP-compatible executors behind one control plane. Desktop-first.

**定位（两层，详见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)「决策变更 v8」）**：
1. **底座层** = `mate` 内核：把「多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约 + 交付形态」做成可复用的一层，面向「把控制面接到自己的 runtime / 自己的前端上」。**runtime 接边有界**：当前 `dsh` / `opencode` **两个一等适配**，**不追求数量、不承诺接入任意 ACP 生态**——接入成本在维护面（隔离表三份 / 投影实测 `Tested` / 能力粒度声明 / 随包载荷与 ABI 断言），不在 adapter。
2. **产品层** = 底座之上的**个人 Agent 助手**：面向「我自己的助手」——建会话、跑起来、看见过程与结果，**聚焦桌面端**（macOS / Windows，随包运行时，用户机器不需要 Go / Node / 预装底座）。
3. **一份 UI 产物，两种加载方式**：开发态（vite，含 HMR）与交付态（Electron 壳）。**浏览器入口只是开发过程使用的形态**，不是交付面。
4. **三条不变的红线**（沿用决策 v3）：**能力不重造**（skills / MCP / 推理等执行能力直接复用底层 runtime 原生实现，本项目不做运行时功能）；**数据管理是平台职责**（统一管理会话数据（wire transcript / 历史回放）、skill 数据、MCP 服务器数据）；**单机单操作员**（不做多人协作 / 多用户鉴权 / 远程聊天通道，跨 agent 协作亦不在定位内）。

## 三分钟跑起来

**交付态（用户）**：装桌面应用即可，随包运行时——机器上不需要 Go / Node / 底座：

```bash
make desktop/dist   # 出安装包（当前只出 macOS，ad-hoc 签名、未公证；Windows 安装器见计划 M4-3）
```

**开发态（改代码）**：本机自备 Go ≥ 1.26、Node 24.x、pnpm 11.x（见「快速开始」前置表），以及至少一个
ACP 兼容底座（`opencode` 或 `dsh`）与模型 API key。数据默认落 `~/.ark-mate`（`MATE_HOME` 覆盖）。

> **`opencode` 可以不装在本机**：`make dev` / `make server` 默认从 `PATH` 找 `opencode`，用
> `OPENCODE_BIN=<可执行文件>` 就能改成指定哪一份（仓库里那份随包载荷就是现成的候选；桌面态一直用
> 随包载荷、**不看** `PATH`）。写进仓库根的 `.env` 一次配好，或命令行临时给；详见「前后端分离开发」
> 一节的 `OPENCODE_BIN`。

```bash
make dev           # 浏览器开发态：vite :5173（**HMR**）+ 后端 :10001，改 UI 走这条最快
make desktop/dev   # 桌面态（Electron 壳 + 真核心）：验壳 / native / 凭据 / 打包前行为
```

> **服务端形态（维护态）不自带面板资产**（2026-10-10 起 `go:embed` 已删除）：核心要面板就得
> 用 `--web-dir` / `MATE_WEB_DIR` 指一份 `frontend/apps/web/dist`（加 `make web` 出这份产物），
> 没给就**拒绝启动**（退出码 3）；容器镜像里已经带好那份并设了 `MATE_WEB_DIR`。

> **改 UI 为什么走 `make dev`**：桌面态**没有 HMR**（壳加载的是构建产物），浏览器开发态才是唯一的热更新
> 循环；两者跑的是**同一份** `apps/web/dist` 与**同一套** `/v1/mate` 契约。桌面态的调试路径（CDP 附渲染
> 进程、`KEEP_SHELL=1`、主进程 `--inspect`）见 [docs/desktop-architecture-design-20260928.md](docs/desktop-architecture-design-20260928.md)。
> **别同时跑** `make dev` 与 `make desktop/dev`——两个核心共用 `~/.ark-mate/mate.db`。

## 底座边界（支持谁、不承诺什么）

底座层是上面两层结构中的下层，面向两类用法：

- **换自己的 runtime**：`pkg/runtime` 是 adapter 注册表 + 投影/指纹声明；远程 ACP 端点经
  `.ark-mate/config/runtimes.json` 声明即可接入（见下文「远程 runtime（A5）」）——**属工程能力，不属定位承诺**。
- **接自己的前端 / 自动化**：对外契约只有 `REST + WS`（本 README 的「控制面接口摘要」就是路由表），
  内核不绑定自带面板——`apps/web` 是**产物包**，桌面壳与核心读同一份 `dist`。

**不承诺**：不做运行时功能（决策 v3①）、不做多用户（v3③）、不做跨 agent 协作（v2②）、
**不承诺「接入任意 ACP 生态」与 runtime 数量**（决策 v8②）。

## 特性（当前形态）

> 本节是**能力清单**，不是承诺面：哪些是定位承诺、哪些只是「工程上具备」，见上文「定位」与「底座边界」
> （决策 v8②：**runtime 数量与「接入任意 ACP 生态」都不在承诺面**）。

- **ACP 直连运行时**：内置 `opencode`（`opencode acp`）与 `deepseekharness`（`dsh --profile acp`）两个 runtime adapter；子进程 stdio 拉起、能力探测、权限请求归一。
- **远程 runtime（A5）**：除本地子进程外，同一套 ACP 客户端还可接**远程端点**——`acl.HTTPTransport`（Streamable HTTP：POST 每条消息 + SSE 响应流承载通知/权限请求，另开 GET 长流收无在途请求时的服务端消息，响应头 `Acp-Session-Id` 回带）与 `acl.WSTransport`（一条 WebSocket，一帧一条消息）。远程 runtime 在 `.ark-mate/config/runtimes.json` 声明（`id/name/transport/endpoint/headers`），启动即注册进同一 runtime 注册表，agent 的 `runtimes` 字段直接引用；传输层不新增依赖、不加第二套客户端（把网络连接伪装成 io 流，握手/权限/elicitation 逻辑与 stdio 完全同构）。远程传输的**服务端半场**在 `pkg/acl`（`ServeWSSession` / `NewRemoteHandler`）；历史上的 `mate acp-serve` 演示子命令已随 echo 底座删除（远程**功能**不受影响，失去的只是开箱演示）。远程 runtime 无 pid（进程注册表/孤儿回收不适用），连接级诊断（建连失败、HTTP 状态、被丢弃的非 JSON 消息）进 `/agents/:id/diagnostics` 的 `stderr_tail`。**统一脱敏闸门**：stderr 尾巴（`pkg/acl.TailLines`）、对外错误文案（`pkg/code.Error`）与全部事件载荷（`manager.publish`，即 events 表/transcript/WS 三处同一份）在离开进程前统一过 `pkg/redact`——命中凭证形状（`sk-*`/`AKIA*`/`ghp_*`/`Bearer`/JWT/PEM 私钥块/`api_key=` 形态）、已登记的真实 provider key（注入时 `RegisterDynamicSecret`）或高熵随机串时替换为 `***`；正常内容（sha256/git 摘要、UUID、长标识符）不被改写。**底座起不来时，REST 信封的 `msg` 会带底座 stderr 的尾巴**（在 `pkg/code` 的 acl→信封投影点并入，最多 5 行、每行按字符截断、走同一个脱敏闸门）：ACP SDK 对传输失败只会回一句通用文案（JSON-RPC `-32603` 的固定 message `Internal error`，且会被换成"底座未完成 ACP 握手就断开"），真因在子进程 stderr 里——不并的话用户只看到 `50001 Internal error`，完整原因却躺在核心日志里（2026-10-09 实测：从含保留键 `.env` 的目录起核心、再建 dsh 会话，就是这个面目）。量纲与选行都刻意与诊断面板不同：面板/事件的 `stderr_tail` 保留 200 行，信封 `msg` 只带 5 行（它进错误气泡与截图）；且**先因后果**——Node（dsh）把原因打在栈**之前**，单纯"取最后 5 行"会只剩一串 `at …` 与 `Node.js v24.x`（这正是第一版实现踩到的坑），所以先认原因行（`Error:`/`panic:`/`Traceback`/`fatal:`…）再从它开始取，Python 这类"原因在最后一行"则由窗口回退覆盖。
- **结构化提问通道（ACP `elicitation/*`，unstable 契约）**：agent 发起 `elicitation/create`——form 模式携带 JSON Schema 表单（单选/多选/文本/数字/布尔等字段 + required/default/枚举约束）、url 模式携带引导链接——服务端发布 `session.elicitation_request` 事件 → UI 依据 schema 渲染提问卡 → 用户提交（content）/拒绝/取消经 `POST /conversations/:id/elicitation` 决策回流给 agent；超时自动取消（默认 30min，`MATE_ELICITATION_TIMEOUT` 可覆盖）；宿主在 Initialize 中声明 `ClientCapabilities.Elicitation{Form,Url}`。**会话归属（两段式）**：Go SDK（coder/acp-go-sdk v0.13.5）未建模规范 scope 字段、顶层 `sessionId` 在解码时被丢弃，但 elicitation 变体里的 **`_meta` 会被完整保留** → 宿主**先读 `_meta`**（`sessionId`/`session_id`/`conversationId`/`conversation_id`，只认非空字符串，见 `pkg/acl/scope.go`）置 `scope_source=wire`；未命中才按「连接上唯一在途 turn」归属（`orchestrator.busyConn`）置 `inferred_inflight`；两者都不成立时置 `ambiguous`——不猜测、按取消回应，并把事实与建议写进该 agent 诊断的 `remediation`（`GET /agents/:id/diagnostics`），不再无痕消失。归属来源随载荷回带（`scope_source`: `wire` | `inferred_inflight` | `ambiguous`），REST/WS 均可观测。**决策带 scope 校验**：只有登记该提问的会话能决策（`POST /conversations/:id/elicitation` 的 `:id` 与归属会话不符时，与「未知 elicitation_id」同码同文案 40001，不泄露他会话挂起项，挂起项保持不动）。**URL 模式收尾**：agent 发 `elicitation/complete`（unstable 通知，只带 ACP 侧 `elicitationId`）时，宿主按该 id 反查 URL 模式挂起项并以 `accept` 收敛（收敛事件 `action=accept`、`reason=complete`，前端标注「链接流程已完成」，与「用户提交」可区分），不再把卡片挂到超时；已决/重放/非 URL 模式一律无副作用（仅记日志）。规范 scope 解析列为触发式能力位（首个真实 elicitation wire 出现时启用，预案见 `docs/agent-interactions-acp-v1-design-20260907.md` §0/§2.4A）。
- **权限确认通道（ACP 一致）**：agent 发起 `session/request_permission` → 服务端发布 `session.permission_request` 事件（含 options：allow/reject × once/always）→ 用户经 `POST /conversations/:id/permission` 决策 → 决策回流给 agent（**显式拒绝带 deny 选项时回 Selected{该选项}，agent 可区分拒绝/取消/超时**；无可选 deny 选项时兜底 Cancelled）。**决策回执如实描述实际投递结果**（`allowed/rejected/always` + 请求 `option_id` + ACP 形态 `outcome`/`selected_option_id`）——`option_id` 缺省或无效时回执给出兜底选中的第一个选项，显式拒绝无 deny 选项时回执 `outcome=cancelled`，不回显请求参数、不猜测。**决策带 scope 校验（C3）**：只有登记该请求的会话能决策——`POST /conversations/:id/permission` 的 `:id` 不存在时 40401（`conversation_not_found`），与挂起项归属不符时与「未知 call_id」同码同文案 40001，不泄露他会话挂起项且挂起项保持不动（与 elicitation 决策同一口径）；支持会话级权限记忆——「始终允许」与「始终拒绝」**双侧对称、同键互斥覆盖**（决策 `{title, kind, decision: allow|reject}`，可见可撤销：`GET /conversations/:id/permissions/memories` 列出、`POST …/memories/revoke` 单条或一键撤销，旧 `/permissions/always` 路径兼容别名）；挂起超时自动拒绝/取消（默认 30min，`MATE_PERMISSION_TIMEOUT`/`MATE_ELICITATION_TIMEOUT` 可覆盖）；`session/cancel` 按 ACP 规范回 Cancelled 全部挂起。**任何挂起（权限/提问）一旦终局即发布收敛事件**（`session.permission_resolved` / `session.elicitation_resolved`），前端以服务端事件为唯一事实收敛卡片（`timeout_at` 仅作展示，无前端自设时钟）。
- **免安装免 key 的演示底座已移除（M0）**：内置 `echo` runtime（daemon 自派生 `mate acp-demo` 子进程充当 ACP server，每轮 prompt 发起真实 `session/request_permission`，并可触发结构化提问与失败轮）已**彻底删除**，内置 runtime 收敛为 `opencode` / `deepseekharness`。**失去的产品能力如实记录在此**：① 不装 opencode/dsh、没有模型 key 也能在面板点通权限确认卡与结构化提问的**首次体验路径消失**；② `mate acp-serve` 与 `mate acp-demo` 子命令一并删除——远程传输**功能不受影响**（`.ark-mate/config/runtimes.json` 声明远程端点照常工作），失去的只是服务端半场的开箱演示；③ 最小容器镜像失去免 key 自检手段，容器内自检须挂真实底座（见 `docs/deployment.md`）。权限/提问通道本身的实现与覆盖**不变**（`pkg/acl/remote_test.go` 的 ws/http 全链路含权限往返；`pkg/acl/elicitation_e2e_test.go` 与 `permission_e2e_test.go` 用自建最小 ACP responder 覆盖 elicitation 四场景与权限 reject/失败轮；台账见 `.dsh/docs/notes/2026-09-22-echo-removal-coverage-ledger.md`）。
- **能力数据面（Skill/MCP/Tool）**：`<CapabilityDir>/skills|mcp|tools/*.json` 是唯一事实源，坏定义告警跳过（不拖垮整表）。**Skill 正文装载期扫描（§13.2）**：入表前对 `body` 跑 11 条模式（9 条 critical：改写/无视/遗忘指令、角色劫持、插入新指令、system prompt override、`rm -rf`、删除全部文件、套取凭据；2 条 warning：敏感路径引用、可疑命令串联）——**critical 直接拒载**（`RegisterSkill` 报错 → 目录装载告警跳过，绝不进注册表、更不会被物化进 agent 技能目录），物化层再拦一道（导出 API 直接调用也不放行）；warning 放行但记在条目上，经 `GET /skills` 的 `warnings` 暴露（severity/pattern/message），超 5 万字符记 info。规则是**字面匹配**（英文正则）：语序改写/中文改写可绕过，这条边界写在测试里，别当成语义检测。面板「能力」页在 Skill 行显示告警标签（`N 条告警`，hover 逐条列 severity/pattern/说明）。MCP stdio 子进程进**独立进程组**（`pkg/mcp/proc_unix.go`，与 agent 侧 `Setpgid` 同款）：close 连孙进程一起回收——`npx -y …` 会 fork 出 node 层，只杀直接子进程既留孤儿、又让 `Wait()` 卡到孙进程退出。
- **Skill 启停与激活（§13.4 S4/S5）**：`PATCH /skills/:id {enabled}` 写回定义**载体**（memory 模式 = 定义文件、sqlite 模式 = `skills` 表那一行，见下文「写面的载体」）——**禁用是 overlay**：定义、正文、runtimes 一律保留，只把 `enabled` 置 false，随时可改回（与「删掉定义文件」的本质区别）；写盘按 M10 纪律（读原文件为 map → 只改一个键 → 临时文件 + rename 原子替换），**未知键原样保留**，不砸用户自己的字段；载体里没有它（代码注入/内存注册）时**显式报错**`skill_not_persistable` 而不是假装生效。落盘后立刻对登记过的物化目标重校：**禁用把已物化副本从 runtime 目录撤回**、启用重新写出（重校失败不回滚定义变更，失败原因随响应回带 `materialize_errors`）。**启用（Enabled）与适用（Applicable）正交**：`GET /agents/:id/capabilities` 的 skills 不再按候选 runtime 静默过滤，而是全量列出 + 每条给 `enabled`/`applicable`/`reasons`（人话原因：已被禁用 / 候选 runtime 都没在 `runtimes` 里声明（并列出声明了哪些）/ 声明了但该 runtime 没有可用的投影落点）——不适用是环境事实，不是让人看不见。面板 Skill 行的开关即调此接口（禁用后行标「已禁用」，Agent 视角对不适用项标「不适用」并 hover 出原因）。
- **能力的生效边界（M3 / ADR-13）**：一条会话可以钉住技能/MCP 的**子集**（`POST /conversations` 的 `capabilities`，逐 kind 三态：键缺失 = 未声明 → 回落 agent 声明；`[]` = 一个都不要；非空 = 白名单，且**覆盖**而非求交）。选定解析出的**能力指纹**决定这条会话用哪个**能力代际**（能力集是进程级的：ACP 没有"给活会话换能力"的通道，所以每套能力各自一个 runtime 子进程，同指纹共享）。生效边界只有一条规则——**下一个轮次**——但四个方向的处置不同：① **扩权/改内容**在下一个轮次边界迁移过去（走既有 resume/handoff 语义）；② **缩权**（收回原生权限、换 provider 档案，即进程级能力面变化）**立刻生效**：迁移失败**不回退**，宁可这一轮带码拒发（`40901 generation_switch_required`），也不用已被收回的权限面继续跑；③ **忙则顺延**（有在途轮次、或有未应答的权限/提问都不切），探针不可读时保守当忙；④ 纯内容变化迁移失败**回退旧代际继续跑**（只落 `generation_switch_failed` 诊断，不产生"会话不可用"）。**代际上限**：同一配置（runtime + 原生权限 + provider）下默认 4 个并存代际（`MATE_GENERATION_LIMIT`，0 = 不限制），达上限时先淘汰「无会话引用且空闲」的代际，仍超限则 `40901 generation_limit_reached` 并要求复用现有能力集——**绝不静默复用错误的能力集**（猜细了是安全 bug，猜粗了只是多花内存）。迁移可观测：`session.resumed` 带 `reason=generation`。
- **MCP 明文 ⟂ 密钥（§13.3 M1/M6）**：`McpServer` 四个字典各司其职——`env`/`headers` 是**明文**（可读回键名、可 diff），`secret_env`/`secret_headers` 的**值只能是引用**（`vault:<ns>/<name>` 走 §5 的 AES-256-GCM 密文，或 `env:<ENV_NAME>` 走进程环境变量），**明文密钥一律拒收**（写进定义文件就是等着落盘）。引用只在**注入前一刻**由 manager 解析成明文（`KindMcpSecret` 参与 AAD，与 provider 密钥不通用），定义文件与日志里永远没有密钥本体；解析失败（密文缺失 / env 未设 / vault 不可用 / kind 不符）**不注入该 server** 并逐条 WARN（键名 + 引用 + 出路，不含值）——不静默塞空凭证让 server 带着 401 在很远的地方报错。**同一套解析在探测面与调用面同样生效**（`POST /mcps/:id/probe`、工具/资源/提示词调用共用 `resolveMcpSecrets`，明文 env 与 `secret_headers` 经 `mcp.Server.Env`/`Headers` 下发到 stdio 子进程）：这两条路径在 2026-10-09 前**不下发** env，表现为"靠 env 拿凭据的 stdio server 会话里能用、`GET /mcps` 却长期报探测失败"（同一个 server 两处 spawn 的进程环境实测相反）；解析失败时它们也不拿空凭证去试，而是当场 `40001 [mcp_secret_unresolved]` 并留 `last_probe_error`（只有键名与引用）。读模型**只回键名**（`env_keys`/`secret_env_keys`/`secret_header_keys`，连引用都不回），并给 `unresolved_secret_keys` 把"绑了但解析不了"的半配置摆到列表上。明文 header 命中像凭据的键名（`Authorization`/`X-Api-Key`/`*-token`…）时注册期**只告警**建议改放 `secret_headers`——值层识别属 §7 的凭据卫生，猜错会挡掉正常配置。面板 MCP 行展示 `密钥 N` 与 `密钥未就绪 N` 两个标签（hover 列出键名与排查出路），值在任何界面都不出现。
- **远程 MCP 经平台侧网关（M7，`MATE_MCP_GATEWAY=auto|off`）**：远程传输（`sse`/`http`/`streamable_http`）的 MCP 由平台**代连**——底座侧只注入**一条恒定**的 stdio 条目 `mate-gateway`（`<平台可执行文件> mcp bridge --agent <id> --runtime <id> [--mcps <会话钉住的白名单>]`），网关进程自己读平台的库与 vault、解开引用、按当下定义连远端并把工具面拼给底座。**收益是结构性的**：底座的配置面不再随"有几台远程 server / URL 改没改 / 密钥换没换"变化，于是**增删改远程 MCP 不再换代际**（下一次 `tools/list`/`tools/call` 就生效，P-4 的"可执行性即时、可见性下一轮"）；密钥本体也不再进入底座可见面——"MCP 明文 ⟂ 密钥"从约定变成结构事实（底座的 `mcpServers` 里只有我们那条命令与 scope 参数）。**工具命名**是 `<serverID>__<toolName>`（网关列举面与 `POST /mcps/:id/probe` 的 Tool 目录**同一实现**，面板上的名字就是模型能调的名字）；前后缀都可能带下划线，故跨 server 撞名（`a`+`b__x` vs `a__b`+`x`）按 **fail-closed 整组丢弃**并 ERROR 留痕——保留"第一个"会让调用静默落到另一台 server 上。**每次调用**在网关 stderr 留一条 `(server, tool, outcome=ok|error|deny, latency_ms)`：这是"底座何时重列工具面、收窄是否即时生效"唯一的一手证据（网关无状态、不落库）。**边界与回退**（如实记）：① stdio server 两种模式下都由底座**直连**（不经网关），② 另一台机器上的**远程 ACP 端点**拿不到这条条目（它是本机子进程配方，对端起不来），按自己声明的传输直连注入 ⇒ 那两类形态与 M7 之前逐字一致；③ `off` 时四处读面/校验（注入、`AgentCapabilities` 承载矩阵、`Library` 适用性、建会话校验）**一起**回到老口径，读数面与注入面永不同向漂移；④ 缺省路径（未声明 `capabilities`）的指纹恒 `""`、按 R11 不迁移——所以"从零到一加一台远程 server"**必须**靠恒定注入才能被看见，这也是条目无条件的理由（代价：没有远程 server 的会话也会多一个空闲的 `mate mcp bridge` 子进程，不接受就用 `off`）。⑤ 生命周期：stdin EOF 即退出（首要），另有 ppid 轮询兜底（底座被强杀）与 SIGPIPE 忽略（否则一条日志就能把停机路径打死、连连接池都来不及关）；⑥ S2（把 stdio server 也交给网关代理）**暂不做**：那会把"平台多一跳"引入到本来最快的路径上，收益要等 E4/E6（底座是否按进程缓存工具面）实测后才有定论。
- **物化摘要与审计（§13.4 S2）+ 原子物化（S3）**：物化产物带**目录级 SHA-256 摘要** sidecar（`.mate-provenance.json`：算法逐条对照 compozy `internal/skills/provenance.go`——相对路径排序后入哈希、普通文件写入 `file:<path>\nmode:<perm>\n`+内容+`\0`、**软链不 follow**（字面目标入哈希，逃逸即拒 `ErrSymlinkEscape`）、未知文件类型报错、**排除平台记账文件**（`.mate-managed` 与 sidecar 本身））。**投影由 runtime 声明（M1）**：skill 落到哪、叫什么文件名、写成哪种格式，不再由配置里的一个"物化根"决定，而是每个 runtime 自带**投影声明**（`Projection{Kind,Scope,Root,Layout,Format}`）——两个底座的落点与格式本就不同，把这件事塞进一个配置键只会让"设了根但没生效"变成猜谜。当前两个 adapter 都声明 `~/.agents/skills` + `<id>/SKILL.md`，且**两个底座的这条链都已端到端实测通过**（`Projection.Tested: true`）：opencode v2.0.26（2026-10-09 复验：真模型请求体的 skill 目录块里逐字出现平台刚物化的 `<id>probe-desktop</id>`；**反证**是同 env、同 provider 配置下把该文件拿掉后同一位置不再出现。v2 换了两处口径——`debug` 子命令只剩 agents/config/paths、`OPENCODE_DISABLE_EXTERNAL_SKILLS=1` 已不生效，v1 那套「列 `location` / 关外部 skill 看命中 0」的取证法在 v2 上不可用；v1 取证见 `.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md`，v2 全量见 `.dsh/docs/notes/2026-10-09-opencode-v2-payload-verification-notes.md`）、dsh v0.2.0-rc.2（`dsh-base` 常态挂 `skill-filesystem`，其 rank 500 根按 `agentsHome` 默认解析到同一路径；运行级证据是**只存在于该根且非内置**的 skill 被真实会话投给模型；2026-10-09 再补一次**平台路径**实测：真核心 → 内置 `dsh` agent → 会话请求体里 `<available_skills>` 逐字出现平台刚物化的那条 skill——前提是隔离表**不注入** `DSH_AGENTS_HOME`，见下条边界）。**dsh 侧两条必须进承诺措辞的边界**：该根 rank 500 是**最低优先**，项目级 `<cwd>/.agents/skills`(200) 与 `~/.dsh/skills`(400) 会**遮蔽**它——所以"平台投放的 skill 一定生效"这种措辞不成立；底座按 `$DSH_AGENTS_HOME ?? ~/.agents` 解析该根，所以**任何**注入 `DSH_AGENTS_HOME` 的地方都会让读根与写根**悄悄分叉**、skill 静默不生效（平台侧还照旧报 `in_sync`/`Tested: true`）；平台的配置隔离**刻意不注入它**（2026-10-09 修：曾有版本按"agents 面也要隔离"注入过，E5 实测 dsh 会话的 `<available_skills>` 里看不到投影根下的 skill；同一张隔离表在仓库里还有第二份——桌面态随包载荷的**私有启动器**（`frontend/apps/desktop/scripts/gen-runtime-bin.mjs`）也在兜底它，同批修掉，护栏见 `pkg/runtime/isolation_test.go` 的 `TestIsolationKeepsSkillRootAlignedWithProjection` 与 `internal/archtest` 的 `TestDesktopLauncherIsolationMatchesCore`（启动器隔离表 ⊆ 核心隔离表）；桌面态真机验收：`pnpm ui:accept:desktop-skills`），但用户若在平台进程环境里设了它仍会分叉。另外 **skill id 有底座契约的词法**（`^[a-z0-9]+(-[a-z0-9]+)*$`、≤64），写面不合规即 `40001`——见 `POST /skills` 行。部署要换根用 `MATE_PROJECTION_DIRS` 覆盖（见下），**只换根、不改形态**。摘要带来三件事：**审计**（`GET /skills/:id/materialization` 给出期望 vs 实测）、**复现**（换机器/换时间重算，摘要一致即产物一致）、**篡改可见**（改内容/加文件/改权限/换软链都判 `tampered`，重校即修复）。物化本身改为**staging → 写全 → 自校验摘要 → 原子 rename**：agent 永远看不到半成品，中途失败旧产物原样保留；**拒覆盖非自有目录**（目标存在但不带 `.mate-managed` 标记 / 是软链 / 不是目录 → 显式报错，不改别人一个字节——此前是裸 `MkdirAll` + `WriteFile`，同名即覆盖用户目录）。投影根**不存在就建出来**（`MkdirAll`，幂等、不覆盖任何东西）：staging 目录建在根里面（同文件系统才能原子 rename），所以根不在就是硬失败；而默认根 `~/.agents/skills` 在从未投放过 skill 的机器上并不存在——2026-10-09 平台端到端实测：新机器上"第一条 skill"只留一条 WARN（`创建 staging 目录失败 … no such file or directory`），定义进表而 skill 静默不投放。老产物（无 sidecar）在下一次重校被**就地升级**，之后保持幂等。**体检状态集**：`in_sync`/`missing`/`tampered`/`stale`/`disabled`/`user_owned`/`not_applicable`，另加两个"不是故障但要说清楚"的：`untested`（该 runtime 的投影声明尚未经实测——**照常投放**，只是不敢替用户担保底座开箱就读到它）与 `not_configured`（声明了但本进程没解析出该 runtime 的落点，`MATE_PROJECTION_DIRS` 里那项要配、或者 adapter 没声明）。定义文件里出现退役的 `sources` 键时不再静默：装载期给该 skill 挂一条 `legacy_sources_key` 告警（`GET /skills` 的 `warnings` 可见），文案直接给出 `mate capability migrate`。面板 Skill 行的「体检」按钮即调此接口：弹窗逐个物化目标给状态/期望与实测摘要/归属/写入时间，并写明**只报不修**（修复走启用或重校）。
- **定时任务（M4 / automation，载体 M6-7 起分叉）**：定义与运行事实的载体**按持久层模式分派**——`MATE_STORE=sqlite` 时是 `automations` 表：**定义列**（`body` + `revision`/`updated_at`，写面单事务 = 行 CAS + 同一张审计表追一条 `kind=automation`）与**运行态列**（`last_fired_at`/`last_status`/`last_reason`/`last_conversation_id`/`consecutive_failures`/`recent_runs`）分列，于是"用户往定义里塞运行态键"**结构上不可能**（拒写名单仍保留 → 40001，免得出现"我写了它为什么不生效"）；`state` **刻意留在定义里**（启用/停用/归档本来就是用户操作，把 `needs_attention` 改回 `active` 是"我修好了"的正常表达）。`MATE_AUTOMATIONS` 目录在 sqlite 模式下降级为**导入/导出目录**，memory 模式仍是**唯一**载体（`<AutomationDir>/<id>.json`，默认 `~/.ark-mate/automations`）。定义每种写法都可能被手工编辑，所以里面既有 `schedule`（`once` 带 `at` / `daily` / `weekly` + 显式 IANA `timezone`，没有 `hourly`/`monthly`；`weekly` 的 `days_of_week` 是 **`0=周日` 的整数集合**（与 `time.Weekday` 对齐），写面容忍重复/乱序，装载时归一为去重升序**并回写文件**——否则「文件里写的」与「调度实际用的」会是两个值）也有平台写回的运行态（`last_fired_at`/`last_status`/`last_reason`/`last_conversation_id`/`consecutive_failures`/`recent_runs` 环形留痕，写面拒收这几个键）。**认领的落点随载体不同**：memory 模式"跑完再写回"（文件 mtime 乐观锁给不了跨进程认领，崩溃窗口内可能重复跑一次），sqlite 模式**在建会话之前先认领**（`last_fired_at` 的行 CAS，0 行 = 别人先认领了 → 判 `duplicate` 且不建会话），因此同一 occurrence **至多触发一次**；运行态写回失败时 sqlite 模式**不推进内存游标**（下一个 tick 按已认领的游标判 `duplicate`，不会重复触发，丢的是这一轮留痕，日志是 ERROR 级），而 memory 模式维持历史语义（写失败不阻断调度）。**一次触发只有一个事实源**：会话状态就是运行状态（不另建运行态状态机）；**认领三态**按 `duplicate → overlap → claimed` 的顺序判——"这次触发过没有"必须先于"上一次还在不在跑"，否则一条「上次会话卡住不结束」的任务会在重启后把同一 occurrence 再跑一遍；`overlap` 记跳过而**不排队**（排队等于让有副作用的 prompt 堆积后一起冲出去）。**停机恢复（ADR-9）**：启动时收敛一次——过期的 `once`（从未跑过）置 `inactive` 并留一条 `skipped` 痕迹而**不补跑**（它是闹钟不是补偿任务），`daily`/`weekly` 补**最近一次**；**认领游标无论成败都推进（ADR-10）**，失败落 `needs_attention` + `consecutive_failures`（不自动重试），否则同一次错过会每 30s 重报一次、把真正的问题淹掉。触发时以 `origin=automation:<id>` 建会话（可在会话列表按来源筛出）并可按 `target.runtime_hint` **显式指定该 agent 候选里的 runtime**（不在候选里 → 40401，绝不静默回落路由）。运行史与事件**各有分工**：`GET /automations/:id/runs` 读载体里的 `recent_runs`（sqlite 模式是 `recent_runs` 列，有界 20 条）（duplicate/overlap/失败**没有会话可挂**，落 events 表会被 `conversation_id NOT NULL REFERENCES conversations(id)` 挡下），`automation.run` 事件只走实时通道（WS 推送）。调度是单 goroutine ticker（`MATE_AUTOMATION_TICK`，默认 30s；0 = 关闭），触发本身串行化（认领判定是纯函数，纯函数不提供互斥）。`POST /automations/preview` 把"到底什么时候跑"变成可见事实（时区 + DST + weekly 星期集合叠加后没人能从 JSON 里读出来）。
- **控制面 REST**（`/v1/mate`）：agent/runtime/会话列表 + 会话动作——`POST /conversations`（指定 agent + prompt 后台起真实会话，可带 `model` 会话级模型）、`GET /conversations/:id`（详情 + 挂起权限）、`GET /conversations/:id/history`（事件回放分页：`after` 向后 / `before` 向前翻页）、`GET /conversations/:id/runs`（轮次明细：失败轮输入/统计回溯）、会话状态含 `interrupted`（重启前中断残留，可续聊重发）、`POST /conversations/:id/prompt`（同会话多轮；`delivery` 三值见「busy input 队列」）、`POST /conversations/:id/cancel`、`GET /conversations/:id/inputs`（待投递队列当前态）、`DELETE /conversations/:id/inputs/:input_id`（撤下排队输入）、`PATCH /conversations/:id`（title/pinned/archived/model 元数据）、`GET /conversations/:id/permissions`、`POST /conversations/:id/permission`（决策）、`GET /conversations/:id/elicitations`、`POST /conversations/:id/elicitation`（结构化提问决策）。
- **busy input 队列（§6：排队 / 插话）**：会话正忙时 `POST /conversations/:id/prompt` 不再只能报错——body 的 `delivery` 三值决定：缺省/`reject` = 报 `turn_in_progress`（历史行为）；`after_turn` = **入队**，当前轮结束自动投递；`interrupt_then_prompt` = 入队 + **取消当前轮**随后投递（服务端原子化：`CancelTurn` 有界等待、取消失败即撤下该队项，不留「谁也不等」的孤儿）。入队响应带 `queued:true` + `input_id`，`GET /conversations/:id/inputs` 给**待投递项的当前态**（`input_id`/`text`/`delivery`/`target_turn_id`/`generation`/`created_at`），`DELETE /conversations/:id/inputs/:input_id` 可撤下**尚未投递**的项。顺序严格 FIFO（`created_at, id`）——插话不插队（否则连按三次「插话」会逆序执行）。**投递判据是会话执行代际**：入队时记下 `generation`（连接断开重连 / 会话重开都会自增），投递前不等即**作废**（`stale` 台账行 + WARN），旧队列项绝不灌进用户没见过的上下文。**失败轮不自动续投**（避免把队列变成「连续失败重放」）、**进程停机不投**——重启时全库 `queued` 行统一置作废（等的那一轮已随进程消失），不替用户按下发送。事实源：sqlite = `inputs` 表（投递状态即审计台账），memory = 进程内队列。事件三件：`session.input_queued`（建项，载荷含文本/投递模式/目标轮）/`session.input_delivered`（已作为新一轮发出，其后紧跟该轮 `session.user_message`）/`session.input_cancelled`（离队但没投：`user_cancel|deliver_failed|stale`）——**撤下与发出是两条不同的事实**，前端队列只认事件、不做本地推断。**每种离队都必须留下事件**：进程重启作废队项时也逐条补发 `input_cancelled(reason=stale)`，否则「只认事件」的前端会在刷新后留下一条永远投不出、也撤不掉的幻影队项。面板侧：会话忙时输入框不再锁死，多出一个投递模式选择（排队 / 打断并发送），输入框上方显示已排队但尚未发出的条目并可逐条撤下（离队原因以系统提示落进流里，如「投递失败——请重发」）；冷挂载（刷新/直达）时 `GET /inputs` 把当前态查回来补齐队列。
- **轻量会话管理面**：会话元数据（title 由首条用户消息自动推导、pinned/archived、created_at/updated_at）经列表/详情暴露；runtime 能力基线（capabilities）只读暴露；前端侧栏按「置顶 / 今天 / 昨天 / 更早」分组并支持置顶。
- **实时事件流 WS**（`/v1/mate/ws/events`）：attach/detach 订阅（按会话聚合过滤），`since_seq` 游标续传/全量回放，历史缓冲超限自动淘汰并「快照回退」，心跳与断线重连；**慢订阅丢帧发 `gap` 帧**（客户端按水位立即重挂续传）；传输层 `github.com/coder/websocket`。
- **Agent 管理面（P2：注册 + 启停 + 热更新 + 切换当前）**：REST `POST /agents`（注册，校验 runtime 已注册即「可启动预检」）、`PATCH /agents/:id`（name/provider/runtime/`mcps` 白名单热更新——改白名单会作废该 agent 的**空闲**会话句柄，下一轮按新白名单注入，见接口摘要该行）、`POST /agents/:id/default`（切换**当前 Agent**：全局默认单例，新建会话与 `GET /models` 缺省都取它）、`DELETE /agents/:id`、`POST /agents/:id/start|stop`（真实进程级启停；有在途 turn 时 stop 报 `agent_busy`）；定义载体**按持久层模式分叉（M6-6）**：`MATE_STORE=sqlite` 时定义存在 `agents` 表里（`id` + 完整 `body` + `revision`，写面单事务 = 行 CAS + 同一张审计表追一条 `kind=agent` 的变更），`MATE_AGENTS` 目录降级为**导入/导出目录**；memory 模式仍是 `MATE_AGENTS`（默认 `~/.ark-mate/agents`）下的 `<id>.json`，serve 启动时装载——两种模式都**不写代码即可注册第三个 ACP agent**（复用既有 runtime + provider 档案）。**MCP 白名单的写面与生效（M6-8 / P-13）**：`mcps` 是**唯一的 per-agent 能力字段**（skill 的适用面由 skill 自己的 `runtimes` 声明，与 agent 定义无关）。它在 M6-8 之前只有"定义文件 / DB 行"这一条路——写面 DTO 里根本没有这个字段，而 `mate agent import` 又刻意不覆盖已有行 ⇒ sqlite 模式下事实上改不了；现在写面收下它（三态见接口摘要）。入 DB 只解决"读到的是新的"：会话的能力面是在**开会话那一刻**注入的，此后一条活着的句柄一直用它出生时那一套；钉住能力选择的会话由代际机制在轮次边界自愈，而**缺省路径**（未声明能力选择）的指纹恒为空、按 R11 不迁移。所以写面改完白名单会**立刻作废该 agent 的空闲会话句柄**（下一轮 `ensureConversation` 按当前定义重建并投新面）。三处刻意的边界：① 只作废**空闲**会话——有在途轮次或有未应答权限/提问的顺延（与 ADR-13 纪律③逐条同判据，绝不打断跑着的一轮）；② **不动代际键、不关进程**（句柄作废 ≠ 连接关闭，缺省路径的键与 R11 逐字不变，同配置仍共享一个暖进程）；③ 语义相同的改动（同集合换顺序/重复项）**不算变化**，不产生任何重建——白名单是过滤器，重排不改变注入结果。**这条不等于"skill/MCP 定义改动对缺省路径下一轮生效"**：那件事仍归 `docs/capability-change-apply-design-20261008.md` §3（缺省路径 rev）。**种子与行是两层**：内置 `dsh`/`opencode` 永远在代码里（不落行），行只存"对种子的覆盖"，装载时先铺种子再用行里的**非零字段**覆盖（所以一行只写 `name` 不会把种子的 `runtimes` 抹空）；`providers.json`（provider 档案）与 vault 锚点仍留在 `MATE_AGENTS` 目录侧——**换事实源不等于可以砍掉这个目录**；内置播种 agent `dsh`/`opencode` 与保留前缀 `agent-*` 只读（runtime 不可改、不可删；`AgentResp.seed` 为服务端权威标记，历史上前端按 id 前缀猜测，播种 id 改名后失效）。「当前 Agent」不再是硬编码常量（历史实现恒为 `dsh`）：显式选择在 memory 模式落 `<AgentDir>/default.json`（`{"default_agent":"<id>"}`，与 `providers.json` 同属**目录级配置**，不是 agent 定义）、sqlite 模式落 `meta.default_agent`（同一个值的另一个载体），回落链「显式选择（agent 仍存在）> 内置种子 `dsh` > id 升序首个」，被切换的 agent 被删除时自动清除选择；`AgentResp.is_default` 即由此计算。修复一个潜伏缺陷：runtime 子进程此前以 `exec.CommandContext(请求 ctx)` 启动，请求返回即被杀（StartAgent 暖启动断连）——现拨号用脱离请求的上下文，连接生命周期归编排器（watchConn/Stop/Close）。**删除面与错误分类（P2 收口）**：`DELETE /agents/:id` 回带影响面（绑定会话数 / 摘除的活跃会话数 / 是否当前 Agent），有在途 turn 报 `agent_busy`；**归属不可变**——存量会话的 agent 归属不被改写、也不静默改绑（goose 在 provider 不可用时会兜底全局默认并写回会话行，本平台不学这条：我们的 agent 携带权限策略/MCP 白名单/原生上下文，静默改写等于静默换掉权限边界），但内存里仍活跃的会话在删除那一刻即被摘除，修掉此前「删了还能跑、重启后才失效」的半死态。会话侧错误按层拆开：`agent_not_found`（会话绑定的 agent 不存在，文案给出「同 id 重新注册即可复活绑定 / 导出历史另开新会话」两条出路）、`runtime_not_found` / `runtime_unavailable`（底座未注册 / 命令不在 PATH，文案指向 diagnostics 的 `remediation`）、`credential_missing`（agent 绑定的 provider 档案悬空或缺 key → 拨号前显式失败，不静默以「无密钥」姿态起进程）。诊断面把「怎么修」一并外露：`remediation` 由事实推导（runtime 未注册 / 启动命令不可执行 / 档案悬空 / 底座自报的鉴权方式 `caps.AuthGuidance` / runtime 声明的 `SetupSteps`——opencode 与 dsh 各自带安装与登录步骤），健康快照不掺安装说明。
- **P2 页面（Agents 管理页）**：原只读列表升级为管理面——注册向导（ID/名称/Runtime/Provider 档案，`agent-` 保留前缀与格式校验）、**「设为当前」切换当前 Agent**（`is_default` 行标出当前，切换后新建会话面板的 Agent 徽标即时跟随；当前行不再显示该按钮）、启动/停止按钮、编辑（热更新 name/provider；种子 runtime 只读）、删除（仅自定义，Popconfirm 里如实列出影响面：多少条会话将失去执行者、是否当前 Agent；删除回执再报实际摘除数）；下方 Provider 档案区（新建/编辑：type/model/密钥输入，`has_key` 标签只写不读回、留空不变更）；诊断弹窗把 `remediation` 作为「建议处理步骤」列出（怎么装底座、怎么补密钥）。Settings 页文案同步指向该页。
- **P2 provider 注入面**：`GET /providers` + `PATCH /providers/:id`（档案 upsert；密钥**只写不读回**——GET 只给 `has_key`；`key` 空串=清除）；agent 引用 provider 档案时启动拨号注入 `<TYPE>_API_KEY`（type 未知取大写 type + `_API_KEY`），档案落 `<AgentDir>/providers.json`。**密钥加密落盘（§5 vault）**：文件里只留引用 `key_ref: "vault:providers/<id>"`，密文落 `~/.ark-mate/vault/providers/<id>.enc`（AES-256-GCM，AAD 绑定 `{kind, ref}`——密文挪到别的引用/kind 上必然解不开），明文只在内存与子进程 env；主密钥 `~/.ark-mate/vault.key`（0600，拒符号链接与过宽权限；可用 `MATE_VAULT_KEY` 覆盖）**必须纳入备份**，丢了密文不可恢复；旧版明文档案在启动时幂等迁移（加密写回 + 清明文），密文解不开时报 `credential_missing` 并带出「确认 vault.key 未丢失/未被更换」的出路。
- **模型目录（运行时派生，无内置清单）**：`GET /models`（`?agent=<id>`，缺省取默认 agent）返回**由运行时自报**的会话配置项（ACP session config options）——模型目录（`category=model`）为主，运行时给出的其它选项（如 `mode`/`thought_level`）一并原样透出但只作展示，平台**不提供思考档位选择面**（少一个「取值随模型变化」的耦合点），也**不内置任何模型清单**（原 `internal/modelcatalog` 的 `go:embed` 快照 + 上游 live + 24h 磁盘缓存三段式已整体删除；provider 档案维度的模型目录路由（`GET /models` 外的旧路径）也已下线——provider 档案只承担密钥/启动 env 注入）。取样来源由响应 `source` 标注：`session`（直接读该 agent 的活跃会话）→ `cache`（内存 TTL 5min）→ `resume`（恢复该 agent 最近一条历史会话读取，读完即 `session/close`）→ `none`（空目录 + `note` + 运行时声明的鉴权引导 `auth_guidance`，面板据此提示登录）。**绝不新建探测会话**：dsh 的 `session/new` 经 `persistence.ensureMaterialized` 落持久记录、opencode 同样落库，而两家都不广告 `session/delete`，探测会留下永久垃圾。取值一律是**不透明字符串**（dsh 为 `JSON.stringify([provider, model])`、opencode 为 `provider/model`），前端只按 `category` 渲染并原样回传，绝不解析或拼接；可用性以运行时 `session/set_config_option` 的成败为唯一判据（平台不做白名单预判，设置失败即建会话失败、绝不静默回落默认模型），被运行时拒绝过的取值在目录里带 `unavailable`/`unavailable_reason` 标注。dsh 在 provider 拓扑变化时主动推送 `session/config_option_update`，平台据此刷新缓存并向 WS 转发 `session.config_options` 事件，模型下拉与「当前模型」无需轮询保持一致。
- **命令发现面（不做投影/写面）**：底座经 ACP `available_commands_update` 自报的斜杠命令由平台**原样接收并回显**——WS 转发 `session.commands`（全量列表 `{name, description?, hint?}`，顺序由底座给出），面板在会话输入框里输入 `/` 时按该列表给候选提示。**平台不拥有这些命令**：ACP 规范里命令只有通告、没有独立执行方法，执行就是把命令行当普通提示词发出去（平台本来就支持），所以没有投影、没有写面、也没有 REST 读端点（会话重开靠事件重放，见 ADR-17）。**逐底座如实**：opencode 会通告（v2.0.26 实测只通告内置的 `init`/`review`/`compact`——**skill 不再作为命令通告**，v2 把 skill 目录块直接放进模型请求体，取证见 `pkg/runtime/adapters.go` 的 `opencodeSkillProjection`），**dsh 不通告**（命令是插件进程内注册，ACP 面明确不含）——面板对不提供发现面的底座不显示候选，且**不会说成"你还没配命令"**。
- **进程注册表 + 孤儿回收（M3 v4③）**：daemon 把拉起的 runtime 子进程（pid/pgid/命令/runtime/agent/实例 id）登记到 `<数据目录>/processes.json`（`MATE_PROC_REGISTRY` 覆盖，`off` 关闭），正常停止/断连时摘除；启动时回收**上一实例遗留**的孤儿进程（SIGTERM → 宽限期 → SIGKILL 整个进程组），并以「pgid 与登记值一致」防 PID 复用误杀（不一致只清账并留日志）。实测：opencode 子进程在 daemon 被 `kill -9` 后随 stdin EOF 自行退出，故孤儿多出现在「runtime 忽略 EOF / 派生孙进程」场景，注册表为兜底防线。
- **P2 空闲回收 + 按需拉起**：reapLoop（`MATE_REAP_INTERVAL`，默认 5m）扫「无在途 turn、无会话引用、闲置 > `MATE_IDLE_TIMEOUT`（默认 5m；0=关闭）」的暖进程 → 原子摘除 + 停进程；之后会话/StartAgent 自动按需重拨；旧连接收尾不覆盖新连接 running（ownerBefore 保护）。
- **会话 wire transcript（重启存活）**：每 session 一个 JSONL（header + prompt/update 原语，schema v1，append-only 不重写；**文本增量按 ③ 合并后落行**——`session.message`/`thought` 攒批 25ms / 4KB / 64 条，故一行可能覆盖多个 delta、长回复不再产生几百行，而工具卡/权限卡/提问卡/用户消息/终态仍一条一行）；daemon **启动时回读 transcript 重建会话列表与元数据（标题/状态/时间），并把历史事件注入 eventbus**——管理面在重启后仍有会话可见、旧会话可经 WS/history 回看，会话列表不随进程失联（轻量「重启存活」，不引入 DB）；目录 `MATE_TRANSCRIPTS` 或默认 `~/.ark-mate/transcripts`；P1 起提供 `mate migrate` 把既有 transcript 幂等导入 SQLite、`mate transcripts prune --older-than <duration>` 显式清理（旁路停写/只读保留/不设自动删除见设计文档 ADR-5）。
- **Web 问答控制台**（浅色 modern-minimal 应用壳，视觉基准 ark-mate-console.html；frontend 为 `apps/*` + `packages/*` 多包 workspace，React 19 + antd 6 + zustand + axios，结构与脚本对齐 ark-iam/frontend）：主视图即会话问答——会话侧栏（**会话/归档双视图** · 按日分组 · 置顶 · 行内更多操作=重命名/归档/恢复 · 新建会话 · 选 Agent 底座）、流式正文（思考 / 工具卡 / 权限确认卡 / **结构化提问卡**——按 schema 渲染单选/多选/文本/数字/布尔控件并本地校验必填与范围 / meta 行（终态轮后附本轮用量行：上下文占用 · 较上轮 Δ · 成本；hover 弹出 goose 风格明细——上下文占用(快照)/首令牌/总耗时/成本，composer 下方常驻最近一轮汇总行） / **失败轮就地「重试该轮」**；消息可一键复制全文，正文内 ``` 围栏代码段渲染为带语言/行数、可复制、长块可折叠的代码卡片）、composer 追加、停止（**忙时可继续输入**：投递模式选「排队 / 打断并发送」，输入框上方实时列出已排队尚未发出的条目并可逐条撤下——队列只认事件、刷新后按当前态补齐）、「待确认」角标（权限 + 提问合并计数，含超时/取消自动收敛）、**会话详情面板**（元数据 + **运行统计**：轮次/失败/取消/工具数 + 失败轮列表一键重试 + 挂起权限就地决策 + 会话级权限记忆——始终允许/始终拒绝——逐条/一键撤销）、断线自动重连 + reconnecting 横幅（重连按水位续传，浏览器原生 WebSocket）；会话头按绑定 agent 如实标注，agent 已不存在时就地给出路（用同一 id 重新注册 / 导出历史另开新会话），不再只显示一个悬空 id；`/conversations/:id` 直达激活，刷新/冷启动即续看；`/agents`、`/runtimes`、**`/capabilities`（Agent 视角 + 全局目录：skill 行内启停开关与物化体检、MCP 密钥键名与未就绪标记、装载告警标签）**、**用量页（顶栏「用量」，数据面 `GET /usage/summary`：按 agent/日期聚合轮次·时长·上下文·成本，支持近 7/30 天与自定义区间；响应回显口径——上下文/成本为区间末快照、day 桶不可加、多币种不混加，上下文与费用上报率分列）**、`/settings` 为管理次要页；Agents 页每行展示 `permission_policy` 摘要（默认动作 + 工具项数 + 原生模式，hover 展开明细），编辑弹窗可直接改 `default`/`native_mode`/工具项 JSON。经真实 Chrome 驱动的 DoD 验收（新建→多轮→刷新续看→cancel 全过）。
- **服务端形态（维护态）**：核心**不自带**面板资产——资产只有一份 `apps/web/dist`，跑时用 `--web-dir`（或 `MATE_WEB_DIR`）指过去，没给就拒启（退出码 3）；容器镜像内已带一份并设好该变量。2026-10-10 起 `go:embed` 与"单二进制"形态整体删除（理由与代价见 `.dsh/docs/notes/2026-10-10-embed-removal-notes.md`）。
- **P3 收口**：传输保险丝/resync、hydration 四态均完成；会话侧栏（置顶 + 按日分组 + 近底跟随滚动）与工具/思考内联卡片为 M1 既有覆盖项，无需改动。
- **P3 加载四态（前端）**：会话视图加 hydration 态——冷挂载/切回置 `hydrating`，服务端 snapshot 或首个事件置回就绪，空转 12s（保险丝重连前）自动回空态不再永久 loading；聊天页在 hydration 中显示「连接会话…」而非空态问候。
- **P3 传输可靠性（前端）**：WS 传输增 **stalled 保险丝**（OPEN 后 70s 无任何服务端帧——半开/黑洞连接——主动断开并重连，健康连接每 25s ping→pong 计入活跃度不会误熔断）与**重连 resync 裁决**（重连后按各 agg 已确认水位重新 attach，服务端 EventsAfter 开区间 → 增量续传不重复、权威收尾）。
- **无外部依赖进程**：实时会话/事件为进程内内存态；**P1 起持久化落 SQLite**（events 事实源 + sessions/runs/messages/permission_memories 读模型投影 + v8 起的**能力定义** `skills`/`mcp_servers` 及其变更审计 `capability_revisions` + `schema_migrations` 自管迁移），`mate migrate` 把现有 transcript 幂等导入（单事务 + 数量校验 + 失败整体回滚，原 transcript 目录不动）；serve 运行态接线已完成（发布持久化、重启恢复、`MATE_STORE=sqlite|memory|auto`）；messages 逐轮物化（user 原文 + assistant 文本片段按 msg_id 拼接 + 交错时间线 + 工具最终态），会话搜索在 sqlite 模式改走 messages 表（无行时回扫 transcript）；跨会话**消息级检索**返回 `seq` 锚点（前端点击命中即跳转定位到该条消息），会话**批量导出**（markdown/text/json）以事件序列为事实源，两种存储模式产物一致；重启后对既有会话 prompt 自动**按需重建**——按记录的 agent/runtime/cwd 重建 ACP 会话（dsh/opencode 走原生 resume，未声明原生恢复能力的底座走 handoff memo），事件/权限/提问继续聚合到**同一个平台会话**；会话可续聊性只取决于会话是否存在，**任何已存在的会话（含上一轮 `completed`/`failed`）重启后都可继续提问**，停机等主动拆除不改写会话状态。

里程碑与后续规划见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)；**未实现能力与降级清单（唯一汇总口径）**见 [docs/agent-runtime-refactor-design-20260908.md](docs/agent-runtime-refactor-design-20260908.md) §20；harness adapter 的源码级适配核实记录见 [docs/deepseekharness-acp-adaptation.md](docs/deepseekharness-acp-adaptation.md)。

## 快速开始

### 1. 前置

| 依赖 | 说明 |
| --- | --- |
| Go ≥ 1.26 | `go.mod` 声明 `go 1.26.1` |
| Node 24.x（LTS） | 见 `frontend/.nvmrc`（精确版本，唯一事实源） |
| pnpm 11.x | 见 `frontend/package.json` 的 `packageManager` 字段 |
| opencode | 本机安装且支持 `opencode acp` 子命令 |
| dsh（deepseek-harness） | npm 包 `@deepseek-ai/dsh`，见下方版本注意 |

**前端工具链约定（Node / pnpm）**：版本声明全部收在 `frontend/` 内、仓库根不持有——
Node 看 `frontend/.nvmrc`（现为 `24.20.0`），pnpm 看 `frontend/package.json` 的 `packageManager`（现为 `pnpm@11.24.0`）。

- 团队统一用 **fnm** 管理 Node（`fnm env --use-on-cd` 后进目录自动按 `.nvmrc` 切换）；用其它管理器（nvm/Volta/mise）不破坏仓库，只要 node 落在 24 主版本即可。
- 选 Node 24 线的原因：Node 24 是 **LTS** 线（2025-10 起），团队/生产默认的稳定选择。注意 Node 24 仍捆绑 corepack（Node ≥ 25 才移除）：fnm + Node 24 下 `pnpm` 由 corepack shim 提供，首次运行会打印一次 `! Corepack is about to download …` 并把 `packageManager` 锁定的 pnpm 下载入缓存，之后静默；想彻底消除这行可机器级二选一：`corepack disable`（改走独立安装的 pnpm）或 `corepack prepare pnpm@11.24.0 --activate`（预热）。pnpm ≥ 11 也会在本机版本与 `packageManager` 不一致时自动按其下载运行。
- 兜底两层：`engines.node`（`>=24 <25`）保证 node 不符时 `pnpm install` 直接失败；`make dev` / `make web` 在启动前端前校验 node 主版本并给出切换提示。CI（`setup-node` 的 `node-version-file: frontend/.nvmrc`）与本地读同一份声明，杜绝漂移。

**dsh 版本注意**：统一启动器 `dsh` 的 npm `latest`（如 0.1.1-rc.2）**缺 `acp` profile 模板**，需 `@next`（≥ 0.1.2-rc.1）。安装：

```bash
npm i -g @deepseek-ai/dsh@next     # 或不经安装：npx --yes @deepseek-ai/dsh@next --profile acp ...
dsh --profile acp --help           # 验证可用（首用会自动初始化 profile）
```

**版本混装排查**：若 mate 日志里 dsh 子进程启动即崩、ACP 连接随即断开（`connection closed … cause="peer connection closed"`），且 stderr 出现
`stdio app: the launcher must provide ctx.appExit and ctx.appReady before the tree mounts`
（堆栈同时含 `…/installation/lib/node_modules/@deepseek-ai/dsh` 与 `~/.npm/_npx/…/@deepseek-ai/dsh-acp-app`），说明 **dsh 启动器与 acp 插件来自两棵不同版本树**（典型：全局 `latest` 0.1.1-rc.2 启动器 + npx 缓存里 @next 的 acp 插件）。修法：`npm i -g @deepseek-ai/dsh@next` 让全局树自洽后 `dsh --version` 应为 ≥ 0.1.2-rc.1（mate 按 PATH spawn dsh，升级后无需重启 serve）；仍报错则备份重建 `~/.dsh/profiles/acp`（曾被混装写坏时）。

模型凭证：harness 读取进程环境 `DEEPSEEK_API_KEY`；若你本机 `dsh` 已配好 profile/home 设置，无 shell 环境变量也能跑。

### 2. 构建服务端形态（可选，维护态）

> **主线交付是桌面**（`make desktop/dist`）；这一节是"自己起一个带面板的守护进程"的形态，属维护态。
> 核心**不自带**面板资产（2026-10-10 起 `go:embed` 已删除）：面板资产只有 `frontend/apps/web/dist`
> 一份，起服务时用 `--web-dir` / `MATE_WEB_DIR` 指过去，没给就**拒绝启动**（退出码 3）。

```bash
make web                                                      # 构建前端产物 → frontend/apps/web/dist
cd backend && CGO_ENABLED=0 go build -o ../mate ./cmd/mate     # 产物在仓库根 ./mate
```

### 3. 启动并打开面板

```bash
./mate serve --addr 127.0.0.1:10001 --web-dir "$PWD/frontend/apps/web/dist"
# 等价写法：MATE_WEB_DIR=$PWD/frontend/apps/web/dist ./mate serve --addr 127.0.0.1:10001
```

> **默认监听地址（安全相关）**：`mate serve` 不带 `--addr` 时读 `MATE_ADDR`，默认
> `127.0.0.1:10001`——**仅回环，不对外**（默认无鉴权，故默认收窄；`lsof` 可见
> `127.0.0.1:10001`）。确需跨机访问时**先设 `MATE_TOKEN`**（A6 单操作员令牌，见下）再显式改
> `--addr 0.0.0.0:10001`，并置于反向代理/防火墙之后，见 `docs/deployment.md`「安全边界」。
> 生产建议同时 `export GIN_MODE=release`（消除 debug 模式告警）。

> **回环面的 Host 白名单 + Origin 矩阵（M3-5）**：核心在**回环监听**时启用这套判定（设计 §7.2 / §9.1）
> ——`Host` 的主机部分必须是 `127.0.0.1` / `[::1]` / `localhost`，且端口等于**实际监听端口**
> （不匹配 → `421`）；`Origin` 只放行「无 Origin」（壳内反代 / curl / 页面导航）、`mate-app://app`
> （桌面态事件流直连）与自身监听端口上的 http 同源（其余 → `403`，含 `Origin: null`）。
> **跨源开发与自建反代需显式登记**：`MATE_ALLOWED_ORIGINS=http://localhost:5173,https://mate.example.com`
> （逗号分隔，`config.json` 可写 JSON 数组；取值必须是纯 origin `scheme://host[:port]`，带路径/查询
> 或未知 scheme 启动即报错）。这是**显式登记口，不是默认放开**——`make dev` 的 vite :5173 → 核心
> :10001 正是靠它。绑非回环地址（如 `--addr 0.0.0.0:10001`）时这套判定**不启用**（设计 §9.4：
> 对外产品化所需的 Host/Origin 白名单与令牌强制、TLS 终止、多操作员审计同属未完成项），
> 启动日志会打一条 WARN 说明。

**凭据（A6 单操作员令牌 → M1-1 分面凭据）**：设 `MATE_TOKEN=<随机串>`（或 `.ark-mate/config` 的
`token`）后，除 `/health` 外的**全部** REST 路由都要求 `Authorization: Bearer <token>`。
**`?token=` 查询参数已删除**（令牌进 URL 会同时进浏览器历史、反代/网关日志与 Referer）：事件流
那条通道改走**带内首帧 `attach`**——连接建立后必须在 5s 内发出 `{action:"attach", token:"…"}`
（未按时送达或凭据不符 → 服务端以 1008 关闭）。前端顶栏「令牌」按钮可填写/清除（只存本浏览器
localStorage），收到 401 会自动弹出。令牌比较用恒定时间实现，`mate config` 只显示掩码
（`cf…3a（14 字符）`）。未设置凭据 = 历史行为（不鉴权，仅建议回环使用）。

> **桌面态是两个凭据**（设计 §7.2）：壳 spawn 核心时用 `--auth-stdin` 经 stdin 首行下发
> `{"restToken":"…","streamToken":"…"}`（各 32 字节随机 base64url；**不进 argv/env/文件/URL**）。
> 两个面**互斥**：`restToken` 只用于壳的反代（从不进渲染进程），`streamToken` 只在事件流首帧被接受。
> 独立态的 `MATE_TOKEN` 则是同一条令牌同时具备两个面——语义与旧版逐字相同。
>
> 桌面态还用 `--addr 127.0.0.1:0`（内核分配端口，避免与用户已有服务撞端口）与 `--ready-stdout`：
> 监听成功后核心往 **stdout** 打一行 `MATE_READY {"addr":"127.0.0.1:51234","port":51234}`（**不含令牌**），
> 壳按**行首**匹配它（gin 的路由转储等日志同样落在 stdout，按子串匹配会把日志误当就绪行）；
> 拿不到就退回 health 轮询兜底。

浏览器打开 <http://127.0.0.1:10001>：

1. 左侧「新建会话」选择 agent——内置播种 agent 为 `dsh`（默认）与 `opencode`，两者都要求本机
   已装对应底座并在底座内完成 provider 登录（**免安装免 key 的 echo 演示底座已删除**，见上「特性」）；
2. 点击「创建并发送」→ 右侧即时流式渲染 assistant 消息/思考/工具卡；
3. 回复期间可点「⏹ 取消」；结束后可在 composer 输入下一条（多轮同会话）；
4. 断开/刷新后重新点选会话，会按游标全量/增量续传补齐会话视图。

监听地址也可用环境变量覆盖：`MATE_ADDR=:9999 ./mate serve`。会话 wire transcript 默认落
`~/.ark-mate/transcripts/`，可用 `MATE_TRANSCRIPTS=/path/to/dir` 改目录（每会话一个
`<会话ID>.jsonl`，schema v1，可直接查看/留档）；SQLite 库默认 `~/.ark-mate/mate.db`
（`MATE_DB` 覆盖，`MATE_STORE=sqlite|memory|auto` 控制持久层）。完整环境变量与部署形态见
`docs/deployment.md`。

**配置目录（`.ark-mate/config` 目录化，M3 A4）**：不想导 15 个环境变量时，把配置写在
`~/.ark-mate/config/`（`MATE_CONFIG_DIR` 覆盖）：

```
~/.ark-mate/config/config.json   # {"addr":"127.0.0.1:10001","store":"sqlite","db":"/data/mate.db","idle_timeout":"5m"}
~/.ark-mate/config/env           # dotenv：MATE_TRANSCRIPTS=/data/transcripts（# 注释、可 export 前缀）
~/.ark-mate/config/runtimes.json # 远程 runtime 声明（A5）：[{"id":"remote-acp","transport":"ws","endpoint":"ws://10.0.0.9:10020/acp"}]
```

优先级：默认值 < `config.json` < `env` < 进程环境变量 `MATE_*` < CLI flag。目录/文件不存在
= 完全维持历史行为；**键名或取值非法则启动即报错**（不静默回落）。`mate config`（`--json` 可
带来源）打印最终配置与每项来源，上线前可直接核对。

旧键名不会被静默接受：`skills_materialize_dir` / `skills_materialize_dirs` 仍会让启动失败，
但报错会**认出它**并给出新键与等价写法（`skills_materialize_dirs=opencode=/x` ≡
`projection_dirs=opencode:skill=/x`）——升级就是改这一行，而"设了却悄悄没生效"这条路被堵住。

**投影根覆盖（`MATE_PROJECTION_DIRS`）**：skill/MCP 落到哪个原生目录由 runtime 的**投影声明**
决定（见「物化摘要与审计」）；本机默认根不适用时才需要覆盖，写法
`MATE_PROJECTION_DIRS=opencode:skill=/data/agents/skills`（多值逗号分隔，`config.json` 可写 JSON
数组；键是 `<runtime>:<kind>`，值是**绝对**目录）。**只覆盖根，不改形态**——摆放方式与文件格式
仍由 adapter 声明，所以不会出现"路径对了但底座读不到"。

### 4. 命令行冒烟（不经面板）

```bash
# 完整跑一次 fresh 会话并打印事件（超时 5min 默认）
./mate demo --runtime opencode --prompt "你好，请用一句话介绍你自己"
./mate demo --runtime deepseekharness --prompt "你好，请用一句话介绍你自己"


# REST 触发 + WS 收流（可用任意 WS 客户端，如 wscat / 浏览器控制台）
curl -s localhost:10001/v1/mate/conversations \
  -H 'content-type: application/json' \
  -d '{"agent_id":"opencode","prompt":"请只回复两个字：明白"}'
# → {"code":0,...,"data":{"id":"...","event_seq":7,...}}，随后 WS attach 该会话即可收增量
```

> `demo` 默认拒绝一切工具权限请求（仅回复型 prompt 可直通）；`--allow-all` 仅调试放行。控制面 REST
> （serve）的权限语义是「**征询**」：默认 `ask` → 发布 `session.permission_request` 挂起等用户决策，
> 超时自动拒绝；可按 agent 配 `permission_policy`（`allow`/`deny` 直接决策，见「控制面接口摘要」），
> 或由用户在会话内决策并形成会话级记忆。
>
> **原生权限映射（B5）**：`permission_policy` 同时投影到底座**原生**权限开关（双层防护，策略仍是拦截面）——
> dsh 注入 `DSH_PERMISSION_MODE`（`default:deny` → `read-only`；显式 `native_mode: workspace-write|danger-full-access`
> 原样使用；其余不注入以免覆盖环境值），opencode 注入 `OPENCODE_CONFIG_CONTENT` 的 `permission` 表
> （`tools` 的 bash/edit/read/write/fetch 等键按别名映射，全 ask 时不注入）。`native_mode: off`
> 或 `MATE_NATIVE_PERMISSION=off` 可整体关闭。未声明策略的 agent 行为不变。

> **验证权限/提问/失败轮通道（echo 已删除后）**：这三条链路仍由真实底座驱动即可复现——
> 权限：任意 agent 跑一个会触发 `session/request_permission` 的 prompt，面板右侧出权限确认卡
> （允许一次/始终允许/拒绝一次/始终拒绝），点选后授权消息 + 工具卡入流；「始终允许」记为会话级
> 记忆，后续同键自动放行不再弹卡。对应 REST：`GET/POST /conversations/:id/permissions|permission`。
> 失败轮：任何以 `turn.error` 收尾的轮次，聊天流错误条与详情面板「运行统计」都提供「重试该轮」。
> 结构化提问：底座发起 `elicitation/create` 时提问卡按 schema 渲染（单选/多选/复合表单/URL 引导），
> 支持**提交**（本地校验必填/范围/个数）、**拒绝提供**、**取消**，超时自动收敛为「超时自动取消」。
> 对应 REST：`GET /conversations/:id/elicitations`、`POST /conversations/:id/elicitation`
> （body `{elicitation_id, action: accept|decline|cancel, content?}`）。
> 客户端侧的四条 elicitation 场景与权限 reject/失败轮由 `pkg/acl` 的自建 ACP responder 端到端覆盖
> （`elicitation_e2e_test.go` / `permission_e2e_test.go`），不依赖任何外部底座。

### 5. 前后端分离开发

```bash
# 方式 A：一条命令同时起前后端（Ctrl-C 一起收掉）
make dev

# 方式 B：分开两个终端（日志可分别看）
# 终端 A：后端（默认 :10001）
make server            # = cd backend && go run ./cmd/mate serve
# 终端 B：前端（vite dev :5173，/v1 代理到 127.0.0.1:10001，含 ws）
cd frontend && pnpm dev
```

> **用量/会话统计需要 sqlite 持久层**：`make dev` / `make server` 默认 `MATE_STORE=sqlite`
> （自动创建 `~/.ark-mate/mate.db`），这样才有 `runs` 读模型与每轮用量。若想轻量纯内存，
> 用 `MATE_STORE=memory make dev` 覆盖（此时 `/usage/summary` 返回 40501、每轮用量不显示）。
> 已有 transcript（内存模式积累的旧会话）需先一次性 `cd backend && go run ./cmd/mate migrate`
> 导入 SQLite，否则 sqlite 模式下旧会话不在面板列表（serve 会提示你 migrate）。
> 注意：`runs` 在**轮次收口时**物化，**开启 sqlite 之后新增轮次**才有单轮用量；历史轮次只有
> runs 计数、无用量（usage 来自实时 ACP 上报，回放不重算）。

> **dev / server 用哪一份 `opencode`：`OPENCODE_BIN`**。核心拉底座用的是写死的名字
> （`pkg/runtime/opencode/opencode.go` 的 `Command = "opencode"`），由 `exec` 走 `PATH` 解析，
> 所以 `make dev` / `make server` 命中的是**本机装的那份**；桌面态不同——壳在启动期解析随包载荷、
> 把私有 bin 注入子进程 `PATH`，**刻意不回落** `PATH`（设计 §6.5）。于是本机装的是 v1、而仓库的
> 声明与取证以 v2 为准时（2026-10-10 实测：本机 1.18.35 / 随包 2.0.26），显式指定一份即可，
> **不必动本机安装、也不必改全局 `PATH`**。
>
> 两个写法：**写进仓库根的 `.env`**（一次配好；这正是 `docker compose` 用的那个本地配置文件的
> 同一个文件，compose 不读这一项、互不干扰）或**命令行临时给**：
>
> ```bash
> # ① 一次配好：把下面这行加进仓库根的 .env（.env 已 gitignore；模板见 .env.example）
> #    OPENCODE_BIN=frontend/apps/desktop/resources/payloads/opencode/2.0.26/bin/opencode
> #
> # ② 临时盖过 .env（优先级见下）
> make dev OPENCODE_BIN=frontend/apps/desktop/resources/payloads/opencode/2.0.26/bin/opencode
> make server OPENCODE_BIN=/opt/other/opencode
> ```
>
> 版本目录按 `resources/payloads/opencode/<version>/` 里实际那份填（`make desktop/payloads` 的产物，
> 桌面态用的就是它）；也可以给任意绝对路径。**取值优先级：命令行 > 进程环境变量 > `.env` >
> `PATH` 上解析**（dotenv 系工具的通行约定）——`.env` 写好后 `make dev OPENCODE_BIN=…` 仍能临时盖过它。
> `.env` 的解析支持 `KEY=v` / `KEY = v` / `export KEY=v` / 双引号包裹 / 行尾 `# 注释`（单引号不支持）；
> Makefile **只挑 `OPENCODE_BIN` 一个键**，不做整份 `-include`（那个文件是 compose 的 dotenv，
> 引号/多行值/保留键整份交给 make 解析会串味甚至语法错）。
>
> 语义是**显式覆盖、不探测**：都没有 = 与今天逐字相同（`PATH` 上那份；核心侧不做任何 `which` /
> `command -v`）；有值就在**核心子进程**的 `PATH` 前置一个只含 `opencode` 一个名字的转发目录
> （`.cache/dev-bin/`，已 gitignore；**不复用**桌面私有 bin —— 那目录里还有 node/npx/dsh
> 三个启动器，整段前置会让 vite/pnpm 调到的 `node` 因 `MATE_NODE_EXECUTABLE` 未设置以 127 失败），
> 前端（vite）不拿这个 `PATH`。有值但不可执行会**当场失败**——静默忽略的症状是"以为换成了 v2、
> 其实还在跑 v1"。三种来源的症状完全一样，所以两条命令启动时都会回显**实际生效的那一份与来源**：
> `==> 底座  opencode: <路径> [命令行]` / `[环境变量]` / `[.env]` / `PATH 上解析 [未指定 OPENCODE_BIN]`。
> 底座的数据面照旧被隔离：核心按 `pkg/runtime/isolation.go` 注入 `XDG_*` / `OPENCODE_*`，这份
> opencode 的 home 落在 `MATE_HOME` 下，不碰你自己的安装（桌面态用的是同一张表）。

> **`.env` 还有个容易漏掉的读者：`dsh` 底座自己。**（2026-10-10 复核；此前已在
> `.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md` 与
> `.dsh/docs/plans/2026-09-28-desktop-architecture-plan.md` 里记过）dsh 在
> **真实运行**时会读两级 `.env`——**调用目录（cwd）**与 **`$DSH_HOME`**（`dsh --version` 这类快路径
> 不读），优先级是 **继承的进程环境 > `cwd/.env` > `$DSH_HOME/.env`**（`.env` 不覆盖已继承的变量）。
> 任何一级里出现"决定进程怎么启动 / 从哪加载代码与指令 / 怎么联网"的名字，dsh 就**直接拒绝启动**：
> 判据是 `dsh-app-boot` 的 `isBootstrapOnly` = **49 个固定名单**（`PATH`/`HOME`/`SHELL`/`NODE_OPTIONS`/
> `NODE_PATH`/`NODE_EXTRA_CA_CERTS`/`LD_*`/`BASH_ENV`/`SHELLOPTS`/`PERL*`/`PYTHON*`/`RUBY*`/
> `JAVA_TOOL_OPTIONS`/`GIT_*`/`EDITOR`/`VISUAL`/`PAGER`/`BROWSER`/`DEEPSEEK_BASE_URL`/`SSL_CERT_*`/
> `*_CA_BUNDLE`/代理四项…，`0.2.0-rc.2` 逐字读出）∪ 四个**前缀** `DSH_` / `XDG_` / `DYLD_` /
> `BASH_FUNC_`；唯一例外是 **`$DSH_HOME/.env` 可以写代理四项**（`HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY`/
> `NO_PROXY`）——cwd 那份不行，它随 clone 而来。报错原文：
> `dsh: <dir>/.env sets "DSH_VERSION", which only the launching environment may set …; export
> DSH_VERSION instead of putting it in a .env file`。
>
> 本仓对应的坑就是 `.env` 里那条 `DSH_VERSION`（命中 `DSH_` 前缀），而它的**作用面只有"以仓库根为
> cwd 起核心"**：运行时子进程继承核心的 cwd（`pkg/acl/transport.go` 的 `SetDir` 没有生产调用方），
> `make dev` / `make server` 都 `cd backend` 起核心，**不受影响**；同一核心上 `opencode` 底座照常握手
> （**dsh 专有**）。要给 compose 钉底座版本时改用 `export DSH_VERSION=…`（compose 与 dsh 都是"继承
> 环境优先"），或直接删掉那行——`latest` 正是 `Dockerfile` 的 `ARG DSH_VERSION` 默认值。本次新增的
> `OPENCODE_BIN`、以及 `MATE_*` / `WITH_*` / `OPENCODE_VERSION` / 镜像变量都**不在**名单内
> （23 键实测矩阵与上述名单双向印证）。

## 控制面接口摘要

REST 前缀 `/v1/mate`；响应统一信封 `{code, requestID, msg, data}`，`code=0` 成功，非 0 为业务错误（40401 未知 agent/会话、40901 会话正忙、40501 能力不支持、50201 runtime/连接故障等）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 健康检查（`{code:0,msg:"ok"}`） |
| GET | `/meta` | **诊断面（M1-7）**：`{coreVersion, webBuildId, apiContract, webPath, webDigest, home, payloads[]}`。`webPath` 是核心实际读的那份资产目录的绝对路径（没接资产层时为 null）、`webDigest`/`webBuildId` 由资产层与版本闸门（M2-3/M2-4）接真值，未接时为 null/空串（`webSource` 字段已于 2026-10-10 删除：核心只有"调用方给的目录"一种来源）。`payloads[]` 是桌面壳经 `--payloads` 交来的**已解析**随包载荷清单（`{name,version,digest,source}`，恒为数组），`source` 是命中级别：`bundled`（随包）/`runtime-env`（显式覆盖）/`path`（PATH 上找到）——清单真源是壳（只有它做三级解析，M3-4），非桌面态为空数组。令牌不进这里（M1-1 起令牌只经 stdin） |
| GET | `/capabilities` | **能力声明面（M1-7，设计 §7.3）**：`{entries:{browser,desktop}, capabilities:[{id,available,browserFallback}]}`。两个入口**同构**（id 集合与顺序一致），只有 `available` 随入口不同——壳的反代带 `X-Mate-Carrier: desktop`，浏览器入口的请求没有它。`browserFallback` 是 V3 单向一致性的机器判据（`tray` 是唯一允许 `none` 的纯增强能力） |
| GET | `/agents` | Agent 列表（含 `permission_policy` 只读面，含 `native_mode`；`is_default` 标出「当前 Agent」；`conversation_count` = 绑定它的会话数，即删除影响面） |
| POST | `/agents` | 注册 Agent `{id, name?, runtimes[], provider?, mcps?, permission_policy?}`（落定义载体：memory 文件 / sqlite `agents` 行）；`mcps` 缺省 = 未声明（全量），`[]` = 本 agent 禁用 MCP 注入 |
| PATCH | `/agents/:id` | 热更新 `{name?, runtimes?, provider?, mcps?, permission_policy?}`（指针字段，nil 不更新）。**`mcps` 是三态**：键缺失 = 不改、`null` = 清空白名单（回落"未声明"= 全量）、`[]` = 禁用全部、数组 = 白名单；改了白名单**且语义真的变了**（同集合换顺序不算）→ 立刻作废该 agent 的**空闲**会话句柄，下一轮按当前定义重建并按新白名单注入（有在途轮次或有未应答权限/提问的会话**顺延**到下一次重建——与代际迁移的"忙则顺延"同一条判据）。只改 name/provider/runtime 候选不作废任何会话 |
| POST | `/agents/:id/default` | 切换「当前 Agent」`{is_default}`（全局默认单例：新建会话与 `/models` 缺省取它；幂等，落 `<AgentDir>/default.json`，未知 agent 报 40401） |
| DELETE | `/agents/:id` | 删除 Agent（种子 agent 只读，报 `seed_agent_readonly`）：返回影响面 `{deleted, affected_conversations, detached_conversations, was_default}`；有在途 turn 报 `agent_busy`。存量会话**归属不被改写**（历史保留可读，继续提问报 40401 `agent_not_found` 且文案给出「同 id 重新注册 / 导出历史」出路），内存中仍活跃的会话此刻被摘除（不再「删了还能跑」） |
| POST | `/agents/:id/start` · `/agents/:id/stop` | 启停底座进程（无编排器装配时报明确错误） |
| GET | `/agents/:id/capabilities` | 该 Agent 视角的统一能力目录（按候选 runtime 聚合 skill/MCP/tool + runtimes） |
| GET | `/agents/:id/diagnostics` | agent 诊断快照（连接态 + `pid` + `stderr_tail` + 最近失败；`stderr_source` 标来源 live/last_error/none；`remediation` = 按事实推导的可执行修复步骤：runtime 未注册 / 启动命令不可执行 / provider 档案悬空或无密钥 / 底座自报鉴权方式 / 底座安装步骤） |
| GET | `/runtimes` | 底座列表（ACP adapter 注册表） |
| GET | `/providers` | 供应商凭据列表：`{id,type,model?,has_key,revision,updated_at,note?,refs{agents[],runtimes[]},problems[]}`（密钥只写不读回，仅 `has_key`；`problems[]` 是"开会话之前就能看见"的本地段问题清单）。**载体按持久层模式分叉（v11）**：`MATE_STORE=sqlite` 时档案存在 `providers` 表里（`id` + `body` + `revision`，写面单事务 = 行 CAS + 同一张审计表追一条 `kind=provider` 的变更），`<AgentDir>/providers.json` 降级为导入/导出载体（写面**不再回写**它）；memory 模式仍是该文件。**密钥加密落盘（§5 vault）**：载体里只留引用 `key_ref: "vault:providers/<id>"`，密文落 `~/.ark-mate/vault/providers/<id>.enc`（AES-256-GCM，AAD 绑定 `{kind, ref}`——密文挪到别的引用/kind 上必然解不开），明文只在内存与子进程 env |
| POST | `/providers` | 新建档案 `{id,type,model?,note?,key?}`：id 过词法校验（仅 `a-zA-Z0-9._-`，≤64，会成为 vault 文件名）；已存在 → `40901` `provider_exists`（**绝不覆盖**，改它用 PATCH）；`type` 必填（决定注入哪个环境变量名）。**写面顺序固定**：① 校验 → ② vault 写密文 → ③ 载体提交（行 CAS + 审计）→ ④ 更新内存；③ 失败补偿 ②（删掉刚写的密文，不留孤儿） |
| PATCH | `/providers/:id` | **只更新**（v11 起不再 upsert；不存在 → `40401` `provider_not_found`）：`{type?,model?,note?,key?}` 指针更新；`key:""` = 清除（删 ref + 删密文）。响应带 `effect{channel,effect,applies_to_agents,affected_conversations,deferred,note}` —— **生效面回执**：只有 `type`/`key` 变了才说"下一个轮次边界生效"（`effect=next_turn`），只改 `note` 如实回 `effect=none`；`deferred` 是其中"有在途轮次或有未应答权限/提问、将顺延到再下一轮"的会话数，判据与代际裁决**同源** |
| POST | `/providers/:id/probe` | 连接自检两段：`{upstream?:bool}` → `{ok,checks:[{name,status,detail,remedy}]}`，`status ∈ pass\|fail\|warn\|skip\|unknown`。**本地段无条件且零出网**（exists/type/secret/refs/injection/catalog：档案在不在、`type` 能否推出注入变量名、密钥能否解出来、被哪些 Agent 引用、注入变量名、引用它的 Agent 的模型目录可不可读）；**上游段仅当 `{"upstream":true}`** —— 对该 `type` 已登记的上游端点发**一次**最小请求（超时 5s、不重试、**不落清单不写缓存不进 `/models`**、错误文本先过 redact），只判"连得上/鉴权过/几条模型"。`type` 未登记端点 → `unknown`（**无法校验**，不是 fail，与 `mate doctor` 同口径）。`ok` = "没有 fail"（warn/skip/unknown 都不算失败）。**`GET` 与页面加载永不触发上游段**（那条路径会把用户密钥发到第三方） |
| DELETE | `/providers/:id` | 删档案：幂等（不存在 → `deleted:false` 且 code=0）；被 agent 或模型条目引用 → `40901` `provider_in_use` + `referenced_by_agents[]`（**不做级联清空**：清空引用等于静默换掉凭据来源）。删除顺序**先摘内存 → 再删行 + 审计 → 最后删密文**；密文删除失败 → `40001` `vault_delete_failed`，但**如实说明「行已删、密文残留」**（反过来先删密文会留下"行在、密钥没了"的档案，让 `has_key` 撒谎） |
| POST | `/models` | 新建一条**平台模型清单**条目：`{runtime,provider_id,model_id,name?,extra?,hidden?,pinned?,is_default?}` → 条目（含平台按该 runtime 规则**生成**的 `value`）。`runtime` 必须**已声明 `ConfigProjection`**（即该底座有平台清单的兑现通道），否则 `40501` `config_projection_unsupported`；同 `(runtime,provider_id,model_id)` 已存在 → `40901` `model_exists`；`provider_id` 不存在 → `40401` `provider_not_found`；memory 模式（无清单载体）→ `40501` `model_list_unavailable`。**同一条模型在两个底座上就是两行**（两个底座写出的配置 schema 完全不同）。响应带 `effect{channels,effect,…}`：`config_write`（新进程读配置）+ 改了默认时的 `session_config`（新会话创建时下发，**立即**）；`effect` 取最早的那个 |
| PATCH | `/models/:id` | 指针更新 `{name?,extra?,hidden?,pinned?,is_default?}`（不存在 → `40401` `model_not_found`）。`is_default:true` 在**同一个事务**里先清掉同 runtime 的旧默认再置新（每 runtime 至多一条由 store 保证，不由调用方维持）。`hidden`/`pinned` 是**平台侧呈现偏好，不下发**：底座的目录里仍会出现被隐藏的模型（隐藏不是"删掉它"） |
| DELETE | `/models/:id` | 幂等（不存在 → `deleted:false` 且 code=0）；**若它是该 runtime 的默认模型 → `40501` `model_is_default`** 并交出「先指定另一个默认」的出路（**不静默把默认置空**——那会让平台的默认模型在用户看不见的时刻消失）。删掉一条模型**不会弄坏既有会话**（dsh 逐字：a saved selection can still submit requests after its catalog entry disappears），所以不扫会话改绑，UI 文案也不该吓唬用户 |
| POST | `/models/apply` | 强制重新物化某 runtime（或全部已声明落点的 runtime）的模型配置文件（T5 `config_write`）：`{runtime?}` → `{applied:[{runtime,declared,tested,writable,channel,path,digest,generated_at,changed,pending,model_count,note}]}`。**不绕过任何一道门**：未实测仍然只读（生成候选但不落盘）、清单为空仍然什么都不写（空配置会让 opencode 起不了会话、让 dsh 拒启）、自校验失败仍然一个字节不写。成功路径**没有 `advertised`**——那要新进程才拿得到，此刻报它就是假装已生效，只如实回 `pending:true` |
| GET | `/models` | **平台清单 + 运行时事实的对账读面**（`?runtime=<id>` 缺省取该 agent 首选 runtime、`?agent=<id>` 缺省当前 Agent、`?refresh=1` 跳过 5 分钟目录缓存）：`{runtime, agent, declared[], advertised[], diff{declared_not_advertised[],advertised_not_declared[]}, config{…}}`。`declared` = 平台声明的清单（**意图**，含生成的 `value`）；`advertised` = 运行时实际广告的候选（**事实**，不透明 `value`）；`diff` = 两个差集（**差集为空 = 已生效**），每条带 `severity`（`error` 选了就报错 / `degrade` 静默降级 / `info` 只是事实）——**按 route 分**：dsh 的 pi-ai 路由是硬白名单（差集=选了报 UNKNOWN_MODEL）、deepseek 路由只是咨询性目录（差集=按 text-only 估）；opencode 的 `advertised_not_declared` **默认非空**（声明是追加、内置 models.dev 目录一直在），那是常态不是故障。`config` = 该 runtime 的模型配置物化状态（`declared/tested/writable/channel/path/digest/pending/model_count`，**`pending:true` 就是在说"还没生效"**）。**旧形态回落**：仅当该 runtime **未声明 `ConfigProjection`** 时才回带 `options[]`/`source`/`auth_guidance`（第三个底座未来接入时仍可读） |
| GET | `/conversations` | 会话列表（`?archived=true` 过滤归档；`?cwd=<绝对路径>` 只返回工作空间**精确等于**该路径的会话，用于「按工作空间查看/分组」）。每条会话回读 `cwd`、`workspace_temporary` 与 `origin`（**来源，非用户输入**：缺省 = 人工建；`automation:<id>` = 该定时任务触发——面板据此打「定时」标，也是「这条会话是谁开的」的唯一权威判据） |
| GET | `/workspaces/browse` | 目录浏览（`?path=<绝对路径>`（缺省 = 首个工作区根，未配白名单时= 服务进程家目录）、`?hidden=true` 含隐藏目录）：**只列目录** → `{path, parent, home, roots, entries:[{name, path}]}`；`entries[].path` 与建会话 cwd 同形态（已符号链接规范化），可直接提交。配了 `MATE_WORKSPACE_ROOTS` 时被收窄：越界 `path` → `workspace_outside_roots`，`parent` 到根为止（越不过去），根外的符号链接子项不列出，`home` 不在根内则不返回 |
| GET | `/workspaces/libraries` | **资料库**列表 = `MATE_WORKSPACE_ROOTS` 下的一级子目录（`{name, path, root, last_used_at, conversations}`，按名排序）：**零新状态**（不存登记表，目录名即资料库名），未配白名单时为空数组。用途是资料/文档场景"选名字而不是选绝对路径" |
| GET | `/workspaces/files` | 目录**内容**清单（`?path=<绝对目录>` 必填、`?hidden=true` 含隐藏项）：`{path, roots, entries:[{name, path, is_dir, size, modified_at}]}`（目录在前、文件在后）。与 `browse` 分工：**browse 用于选工作空间**（只列目录，条目可直接当 cwd），**files 用于看资料**（列文件，条目**不可**当 cwd）。边界与 browse 一致（越界拒绝、根外真身的符号链接不列） |
| GET | `/workspaces/recent` | 最近使用的工作空间（`?limit=` 默认 10 上限 50）：**从既有会话派生**（`{path, name, last_used_at, conversations}`，按最近使用倒序、同目录聚合计数、排除平台代建的临时空间），不另存状态 |
| GET · PUT | `/settings/workspace` | 工作区根白名单的**设置面**：GET 回 `{configured[], roots[], restricted, provision_dir, provision_included, source, locked_by?, writable}`——`configured` 是面板可编辑的显式名单，`roots` 是**生效**名单（= `configured` ∪ 平台代建根；代建根**无需**手写进配置，它由平台隐式并入）；PUT `{roots[]}` **全量替换**（`{"roots":[]}` 或字段缺省 = 恢复"不限制"），逐项要求绝对路径 / 存在 / 是目录，落盘 `<配置目录>/config.json` 并对**新**会话与浏览面即时生效（已建会话 cwd 不变）。被进程 `MATE_*` / env 文件 / flag 改动过该键时回 `settings_locked`（写入必被覆盖 → 面板转只读），无配置目录的进程回 `settings_readonly` |
| GET | `/conversations/search` | 会话搜索（`?q=` 必填、`?limit=` 默认 30 上限 50；含归档、`updated` 降序） |
| GET | `/search/messages` | 跨会话消息检索（`?q=` 必填、`?limit=` 默认 30 上限 200；返回 `seq` 锚点用于跳转定位，`source` 标数据面） |
| POST | `/conversations/export` | 批量导出 `{ids[], format?: markdown\|text\|json}`（ids 上限 50）→ `{files:[{filename, content, bytes, events, truncated?}], skipped[], total_bytes}` |
| POST | `/conversations` | `{agent_id, prompt?, cwd?, runtime_hint?, model?, capabilities?}` → `{id, event_seq, cwd, workspace_temporary, …}`（带 prompt 则后台起首轮；`model` 为 `GET /models` 的 `options[category=model]` 里的**不透明 `value`**，原样回传、经 `session/set_config_option` 会话级应用，运行时拒绝即建会话失败）。**工作空间**：`cwd` 非空时校验「绝对 / 存在 / 是目录」并按符号链接规范化，且（若配了 `MATE_WORKSPACE_ROOTS`）必须落在某个允许的工作区根之内，否则报 `workspace_outside_roots`；为空时平台在 `MATE_WORKSPACE_DIR`（默认 `~/.ark-mate/workspaces`）下按**会话 id** 代建 `<id>/` 并回 `workspace_temporary=true`；代建关闭（`workspace_dir` 为空）且未指定则报 `workspace_required`——**不回落 serve 进程目录**（那会让所有会话共用一个沙箱边界）。工作空间建成即不可变（它是底座 ACP session 的 cwd，也是原生权限 `workspace-write` 的边界）。**能力选定（M3）**：`capabilities: {skills?, mcps?}` 把这条会话钉在技能/MCP 的一个子集上，逐 kind **三态**——键缺失 = 未声明（回落 agent 声明）；`[]` = 明确一个都不要；非空 = 白名单。传了键就按白名单**覆盖**（不是求交，否则"这条会话想放宽"永远表达不出来）。选定与它解析出的**能力指纹**一起落库，重启后不漂移。不可达（id 不存在 / 该 runtime 不适用，两者**同码不同文案**）→ `40001 capability_not_applicable` 且**零副作用**（不建会话、不拉进程、不写库、不留代建目录）。生效边界见「能力的生效边界」一节 |
| GET | `/conversations/:id` | 会话详情（元数据 + 挂起权限 + 权限记忆） |
| PATCH | `/conversations/:id` | 元数据更新 `{title?, pinned?, archived?, model?}`（`model` 会话级模型：opencode 仅创建时可选，dsh 会话内即时） |
| GET | `/conversations/:id/history` | 事件回放（`?after=` 向后 / `?before=` 向前（加载更早，`before=0`=末尾一页）互斥 + `limit`，默认 200/上限 1000；sqlite 模式读 events 表可翻到会话第一条） |
| GET | `/conversations/:id/runs` | 轮次明细（runs 读模型；失败轮输入/统计回溯） |
| POST | `/conversations/:id/prompt` | `{prompt, delivery?}` → `{id, status, event_seq, queued?, input_id?}`（多轮）；`delivery` 见「busy input 队列」 |
| POST | `/conversations/:id/cancel` | 取消进行中的 turn（幂等） |
| GET | `/conversations/:id/inputs` | 列出**待投递**的排队输入（§6 当前态：`input_id`/`text`/`delivery`/`target_turn_id`/`generation`/`created_at`）。事件负责过程、这里负责当前态——WS 冷挂载只发 snapshot 不回放历史，没有这个入口队列会在刷新后凭空消失 |
| DELETE | `/conversations/:id/inputs/:input_id` | 撤下一条**尚未投递**的排队输入（§6）；已投递/已作废是台账行不可撤，不存在的 id 幂等 `deleted:false` |
| GET | `/conversations/:id/permissions` | 挂起权限列表 |
| POST | `/conversations/:id/permission` | 权限决策 `{call_id, allowed, option_id?, always?}`（拒绝也透传 option_id/always） |
| GET | `/conversations/:id/permissions/memories` | 会话级权限记忆（始终允许+始终拒绝，`{title,kind,decision,since}`；旧 `/permissions/always` 为兼容别名） |
| POST | `/conversations/:id/permissions/memories/revoke` | 撤销权限记忆 `{title, kind}`；两者均空 = 一键撤销全部（旧 `/permissions/always/revoke` 为兼容别名） |
| GET | `/conversations/:id/permissions/always` | **兼容别名**，等价 `/conversations/:id/permissions/memories`（历史客户端/脚本在用，响应逐字段一致） |
| POST | `/conversations/:id/permissions/always/revoke` | **兼容别名**，等价 `/conversations/:id/permissions/memories/revoke`（请求体与行为一致） |
| GET | `/conversations/:id/elicitations` | 挂起结构化提问列表（表单 schema / URL） |
| POST | `/conversations/:id/elicitation` | 结构化提问决策 `{elicitation_id, action, content?}` |
| GET | `/library` | **统一能力清单（M2）**：skill/mcp/agent/tool 四类同构（`kind`/`name`/`description`/`origin`/`scope`/`enabled`/`source_file`），`?kind=` 过滤、`?q=` 对 id/name/description 做大小写不敏感子串（未知 `kind` → 40000）。每条回带**逐 runtime 的 `applicability[]`**（`projected`/`untested`/`not_configured`/`not_applicable`/`disabled` + 人话 `reason`）——**刻不给"整体可用吗"的合并结论**：对 opencode 可用、对 dsh 不可用是有用信息，压成一个 bool 就没了。只读派生视图，**不拥有任何数据** |
| GET | `/skills` · `/mcps` · `/tools` | 能力数据面目录（Skill/MCP/Tool；MCP 含探测状态与 `tool_count`、`enabled`/`scope`；skill 含 `enabled` 与正文扫描 `warnings`） |
| POST | `/skills` | 新建 skill 定义（body = skill 定义，`id` 必填）：**先过注册闸门**（正文扫描、`runtimes` 去重、**id 词法**）→ 通过才提交给载体（memory = 原子替换文件、sqlite = 事务插行 + 变更审计，未知键一律保留）→ 最后才更新内存；id 已存在 → 40901 `skill_exists`，闸门拒绝 → 40001 `definition_invalid` 且**零写入**（草稿从未提交，无需回滚）。**id 词法是底座契约，不是平台审美**：须匹配 `^[a-z0-9]+(-[a-z0-9]+)*$` 且 ≤64 位——dsh 的 `dsh-skill` 对 skill 文件 frontmatter 的 `name` 强制这一条，不合规只打一条 warn 就**静默丢弃整条**，而平台物化正是把 id 原样写成 `name`，于是放行不合规 id 等于造出「物化体检报 `in_sync`、底座永远读不到」的 skill（2026-10-08 实测确认）；物化层同规则再拦一道（导出 API 被直接调用也不放行）。MCP/tool 的 id 规则不受此波及 |
| PUT | `/skills/:id` | 覆盖写 skill 定义：**只改 body 里出现过的键**（未出现的保留原值；`null` 删键、`""`/`[]` 清空），路径 id 与 body id 不一致 → 40000；提交前用载体的乐观锁复核（文件 = mtime+size、DB = 行 `revision`），读写窗口内被外部改动 → 40901 `definition_changed`（**绝不 last-write-wins**） |
| DELETE | `/skills/:id` | 删定义（文件 / DB 行）+ 撤回已物化的 `.mate-managed` 副本 + 摘内存条目；幂等（本来就不存在 → `deleted:false` 且 code=0，不报 404） |
| PATCH | `/skills/:id` | `{enabled}` 启停一个 skill（§13.4 S4）：写回定义载体的 `enabled`（**只改这一个键、未知键原样保留**）+ 更新注册表 + 立刻重校投影；禁用 = overlay，已物化副本被**撤回**，随时可恢复。载体里没有它（代码注入/内存注册）→ 40001 `skill_not_persistable`，**不顺手新建** |
| GET | `/skills/:id/materialization` | 物化体检（§13.4 S2，只读）：每处物化目标的期望摘要 `hash` vs 实测 `actual_hash` + `status`（`in_sync`/`missing`/`tampered`/`stale`/`disabled`/`user_owned`/`not_applicable`/`untested`/`not_configured`）+ `owners`（该落点上共同投放的 runtime 集合）；回答「agent 实际读到的与注册表一致吗」 |
| POST | `/mcps` | 新建 MCP server 定义：闸门比 skill 多（transport 枚举、stdio **绝对路径**、`vault:`/`env:` 引用词法、明文与密钥键不重复），写面**一个都不新开也不放行**——定义怎么进注册表就走同一条路；id 已存在 → 40901 `mcp_exists`；载体同 skill 写面（memory 文件 / sqlite `mcp_servers` 行 + 变更审计） |
| PUT | `/mcps/:id` | 覆盖写 MCP server 定义（键的出现与否有语义，同 skill 写面）；响应回带 `unresolved_secret_keys`（绑了引用但当前解析不出来的键，只给键名） |
| DELETE | `/mcps/:id` | 删定义（文件 / DB 行）+ 摘内存条目 + **一并摘除归属该 server 的 `mcp` 来源工具**（留着等于让用户看见一个调不通的能力）；幂等 |
| POST | `/mcps/:id/probe` | 对 MCP server 做一次 live `tools/list` 探测并写 Tool 目录（显式触发；stdio/http/streamable_http/sse 四种传输均支持） |
| POST | `/mcps/:id/tools/:tool/call` | 调用 MCP 工具（`tools/call`；body `{arguments,timeout_ms}`；工具级失败 → `is_error=true` 且 code=0；超时 40901） |
| GET | `/mcps/:id/resources` | 实时拉取 MCP 资源列表（`resources/list` + `resources/templates/list`；模板不支持 → `templates_error` 降级） |
| POST | `/mcps/:id/resources/read` | 读取 MCP 资源（`resources/read`；body `{uri}`；text/blob 原样保真） |
| GET | `/mcps/:id/prompts` | 实时拉取 MCP 提示词模板（`prompts/list`） |
| POST | `/mcps/:id/prompts/:name/get` | 取用 MCP 提示词（`prompts/get`；body `{arguments}`，值为字符串） |
| GET | `/automations` | **定时任务（M4）** 列表：定义 + 运行态（`state`/`next_due_at`/`last_fired_at`/`last_status`/`last_reason`/`consecutive_failures`/`recent_runs`）。**装载失败的定义也在列表里**（带 `error` 字段）——写得不对却「列表里没有、调度也不跑、日志一行 WARN」是最难排查的一类投诉 |
| POST | `/automations` | 新建定义（body = 定义，`id` 必填 = 定义键）：先按**合并结果**过闸门（schedule 语义、`timezone` 必须 IANA、`target.cwd` 必须非空绝对路径、`runtime_hint` 必须在该 agent 候选里）→ 通过才落载体（memory 模式原子替换、sqlite 模式行 CAS，未知键一律保留）→ 失败**回滚内存**；id 已存在 → 40901 `automation_exists`（**绝不覆盖**），闸门拒绝 → 40001 `definition_invalid` 且**零字节落盘** |
| POST | `/automations/preview` | 只算未来若干次到期时刻（body `{schedule,count?,from?}`，count 默认 5 上限 20；**不落盘、不建会话、也不要求定义已存在**）：每项回带 UTC `at` 与本地墙钟 `wall` + `timezone`，跨 DST 空档时给 `warnings`——时区 + DST + weekly 星期集合叠在一起，没人能从 JSON 里读出「到底什么时候跑」 |
| GET | `/automations/:id` | 取一条（不存在 → 40401 `automation_not_found`） |
| PUT | `/automations/:id` | 覆盖写：**只改 body 里出现过的键**（未出现的保留原值；`null` 删键），路径 id 与 body id 不一致 → 40000；**平台独占的运行态键**（`last_fired_at`/`last_status`/`last_reason`/`last_conversation_id`/`consecutive_failures`/`recent_runs`）不可写 → 40001（`last_fired_at` 就是认领判据，能写它就能让同一次触发重放或永久卡住不跑）；读写窗口内被外部改动 → 40901 `definition_changed`（memory 模式判 mtime、sqlite 模式判行 `revision`） |
| DELETE | `/automations/:id` | 删定义 + 摘内存条目；幂等（本来就不存在 → `deleted:false` 且 code=0） |
| GET | `/automations/:id/runs` | 运行史（最新 20 条；读载体的 `recent_runs`，有界）：**含 `overlap`/`duplicate`/`skipped`**——「没跑成的那些」才是最需要被看见的；定义装载失败 → 40001（不假装没有运行史） |
| POST | `/automations/:id/run` | 手动触发一次：走**同一认领判据**（上一次还在跑 → `overlap`，手动不等于可以并发），但**不推进认领游标**（手动试跑不该静默吃掉当晚的排期）；失败既报错又留痕（`skipped` + `needs_attention`） |
| GET | `/usage/summary` | 用量统计（`?from=&to=&agent_id=&group_by=agent|day`；区间半开、日期按 UTC；无 store → 40501） |
| GET | `/ws/events` | 实时事件通道（见下） |

### 业务错误码

信封 `code` 字段的**唯一事实源是 `backend/pkg/code`**（本表由 `TestReadmeCodesMatchRegistry`、
前端常量由 `TestFrontendBizCodesMatchRegistry` 守卫，改码必须三处同步）。语义错误一律 HTTP 200 +
`code != 0`，只有鉴权失败同时给 HTTP 401。

**HTTP 状态码的第三个取值**：请求**未命中任何路由**时回 HTTP 404 + `code=40401`（标准信封）。
与上一条不冲突——「语义错误走 200」说的是路由命中了、领域层判定失败；未命中路由属于 HTTP 语义
问题，代理与客户端靠状态码识别，不该伪装成"接口存在但拒绝了"。API 路径（`/v1/`）在任何形态下
行为一致；非 API 路径在**给了 `--web-dir`**（拿得到资产）时回落 SPA `index.html`，没有资产时回
信封 404——没有资产就没有可回落的页面（2026-10-10 起资产只有一个来源：调用方给的目录）。

| code | 含义 |
| --- | --- |
| `0` | 成功 |
| `40000` | 参数错误（缺失/类型或取值不合法） |
| `40001` | 请求语义非法（游标互斥、归属不符、状态机不允许） |
| `40101` | 未授权（单操作员令牌缺失或无效；HTTP 401） |
| `40401` | 资源不存在（会话/agent/runtime/provider/MCP server） |
| `40501` | 当前形态明确不支持（典型：memory 模式下的 `/usage/summary`） |
| `40901` | 与当前状态冲突或超出额度（在途轮次、上游超时） |
| `50000` | 服务内部错误（**唯一兜底码**） |
| `50001` | 上游执行失败（runtime / MCP server 报错） |
| `50201` | 上游连接失败（进程拉不起、连接断开、不可达） |

`50000` 是兜底码：任何未归类的 error 经 `code.FromError` 出口都会落成它。
信封里**不会出现 `code: -1`**（那是不带业务码的裸错误落到 gincontext 默认分支的产物）。

**能力定义写面（`POST/PUT/DELETE /skills|/mcps`）按「用户的下一步动作」分派同一个码**——同一个 `40001`
在写面里可能对应三种完全不同的处置，所以 `msg` 会写明是哪一种（`msg` 是给人看的一句话，
**机器判据只有 `code`**；括注里的是服务端内部分类名，不出现在信封的独立字段里）：

| 情形 | code | msg 里的分类名 | 用户该做什么 |
| --- | --- | --- | --- |
| id 缺失/非法、路径 id 与 body id 不一致 | `40000` | `invalid_param` | 改参数 |
| 定义被闸门拒绝（正文 critical / transport 非法 / stdio 非绝对路径 / 引用词法…） | `40001` | `definition_invalid` | 改定义内容重试；**被拒的定义零字节落盘** |
| 目标不存在（`PUT` 到不存在的 id） | `40401` | `skill_not_found` · `mcp_not_found` | 刷新列表（**平台不会"顺便新建"**，打错 id 不会静默建出空定义） |
| id 已被占用 / 读写窗口内被外部改动 | `40901` | `*_exists` · `definition_changed` | 换 id 或刷新后重试（**平台绝不 last-write-wins**） |
| 未配置能力目录（**仅 memory 模式**） | `40001` | `capability_dir_unset` | 设 `MATE_CAPABILITIES`；不静默"内存生效"（sqlite 模式的定义在 DB 里，写面不需要目录） |

**写面的载体按持久层模式分叉（M6-3）**：`MATE_STORE=sqlite` 时能力定义的事实源是 DB——
`skills`/`mcp_servers` 两张表**就是定义本身**（`id` + 完整 `body` + `revision`），写面走
**单事务：行 CAS + 同事务追一条变更审计**（`capability_revisions`：kind / capability_id / action /
actor / at / digest，删定义也留痕且 `digest` 为空）。乐观锁从文件的 mtime+size 换成行 `revision`，
但对外语义**逐字不变**：同一个 `definition_changed`/40901、同一句"刷新后重试、你的改动未写入"、
绝不 last-write-wins；响应里的 `path` 在 sqlite 模式回带定位串 `db:skill/<id>`（`db:mcp/<id>`），
文件模式仍是定义文件路径——**它只是给人看的定位串，不是任何判据**。**memory 模式一切照旧**：
定义是 `MATE_CAPABILITIES/{skills,mcp}/<id>.json`，落盘走 C9/M10 纪律（读 map → 只改目标键 →
未知键保留 → 同目录 staging + 原子 rename + 权限沿用 + mtime 乐观锁）——memory 模式没有持久层，
文件是它唯一的持久形态，所以不跟着换。装载侧同源分派：sqlite 模式从 DB **整体替换**式装载
（先清空再逐行过闸门，库里删掉的行不会以幽灵定义留在内存里），memory 模式仍是目录装载。
错误面同样分叉但**只有一处**：`skill_not_persistable`（启停时载体里没有这条定义）在 memory 模式
是"没配目录/没有定义文件"，在 sqlite 模式是"DB 里没有这一行"（代码注入或内存注册的技能）——
后者在 DB 化之后仍可达，所以这个码**没有**随 M6 消失，只是文案与出路换成了"先用写面或导入落行"。

**存量定义怎么搬到 DB（M6-4，一次性 + 幂等 + 可回退）**：装配期在 sqlite 模式下自动导入一次
`<CapabilityDir>/{skills,mcp}/*.json`（逐条过同一道注册闸门；坏定义 WARN 计数、其余照常导入、
不阻断启动），成功后把目录指纹写进 `meta.imported_capabilities`——**幂等靠指纹而不是"表空不空"**，
所以重启不会重复导入、"我故意删掉的技能"也不会自己回来。导入**绝不覆盖**已有行（只插缺失的，
逐条计入 `existing`）：文件是旧的一版、面板才是新的，按文件回滚等于静默的数据丢失。目录里的
`tools/*.json` **不参与持久化**（工具是探测产物，每轮重建），但导入器会**报数**并在启动日志里
写明（静默丢弃用户放进去的文件是最坏的一种"成功"）。补齐/重跑用 `mate capability import`
（不看指纹、每次按行判重），把 DB 写回目录用 `mate capability export`（此后文件是导出物，
`MATE_CAPABILITIES` 在 sqlite 模式下就是这个**导入/导出目录**）。

**Agent 定义走同一条搬家路径（M6-6）**：装配期在 sqlite 模式下自动导入一次
`<AgentDir>/*.json`，指纹进 `meta.imported_agents`（**两个域各自记指纹**：只改 agent 目录不会让
能力面的导入重跑一遍），`mate agent import|export` 是对应的运维口子，语义与能力面逐条相同
（按行判重、绝不覆盖、坏定义逐条报出、`export` 后文件是导出物且 `meta.default_agent` 一并写成
`default.json`）。三处与能力面**刻意不同**，都是 agent 侧的事实：① 目录里的 `providers.json`
（档案留在文件侧）与 `default.json`（显式选择进 meta）不是 agent 定义，导入导出都跳过它们；
② 导入期**不判 runtime 是否存在**——那是装载期的事，且"暂时没注册"不等于"这条定义该丢"；
③ 保留前缀 `agent-` 与内置种子名（`dsh`/`opencode`）**不能**作为定义行进来（`seedAgent` 只看 id
就判只读，塞一行进去会变成删不掉也改不了 runtime 的幽灵定义）——想覆盖种子就在文件里写同名定义。

**定时任务定义走同一条搬家路径（M6-7）**：装配期在 sqlite 模式下自动导入一次
`<AutomationDir>/*.json`，指纹进 `meta.imported_automations`（**三个域各自记指纹**），
`mate automation import|export` 是对应的运维口子。它与前两个域**唯一的结构性差别**是"一个文件
装了两样东西"：导入按 `automation.ReservedKeys` 把文件切成**定义正文 + 运行态列**并**同一事务**
写进去（不出现"定义进来了、运行史没进来"的半截状态），导出反向合成同一个 `<id>.json`
（所以"导出 → 空库导入 → 再导出"逐字节相同）。时间归一成 UTC + RFC3339Nano——它是认领 CAS 的
比较值，两种写法会让同一个 occurrence 比对不上；`recent_runs` 进列时按上限截断（文件可以被人
手工塞进几百条，"有界"是列的契约）；保留键类型写坏（如 `last_fired_at: 123`）**逐条报错**而不是
静默丢弃。

**定时任务面（`/automations`）沿用同一条分派线**（请求形态 → `40000`；取值语义 → `40001`），
`msg` 写清是哪一种：

| 情形 | code | msg 里的分类名 | 用户该做什么 |
| --- | --- | --- | --- |
| id 缺失/非法、路径 id 与 body id 不一致、`from` 非 RFC3339 | `40000` | `invalid_param` | 改参数 |
| 写了平台独占的运行态键（`last_fired_at`/`recent_runs`…） | `40001` | `invalid_param` | 去掉这些键：它们是平台写回的运行事实，不是输入 |
| 定义不可执行（schedule 语义不合法 / `timezone` 不是 IANA 名 / `target.cwd` 非绝对路径 / `target.prompt` 为空 / 文件不是 JSON 对象） | `40001` | `definition_invalid` | 改定义内容重试；**被拒的定义零字节落盘** |
| 目标不存在 | `40401` | `automation_not_found` | 刷新列表（**平台不会"顺便新建"**） |
| id 已被占用 / 读写窗口内被外部改动 | `40901` | `automation_exists` · `definition_changed` | 换 id 或刷新后重试 |
| 未配置定时任务目录（**仅 memory 模式**） | `40001` | `automation_dir_unset` | 设 `MATE_AUTOMATIONS`；sqlite 模式的定义在 DB 里，写面不需要目录 |
| 定义已停用/归档，却要手动触发 | `40001` | `automation_disabled` | 先 `PUT {"enabled":true}`（或把 `state` 从 `archived` 改回 `active`）再触发 |

**触发失败不算接口错误**：定时触发失败时接口是 `code=0`，失败事实落在定义文件的
`last_status=skipped` + `state=needs_attention` + `last_reason`（**也推 `automation.run` 事件**）——
"这轮没跑成"是运行结果而不是请求被拒；只有**手动** `POST /automations/:id/run` 失败才会同时报错
（用户就站在那里等一个回答），且**照样留痕**。

**能力使用面（建会话 / 轮次边界）**同样按「用户的下一步动作」分派码：

| 情形 | code | msg 里的分类名 | 用户该做什么 |
| --- | --- | --- | --- |
| 建会话时选的能力不可达（id 不存在 / 该 runtime 不适用） | `40001` | `capability_not_applicable` | 按 msg 里逐条列出的原因改选；**零副作用**（不建会话、不拉进程、不写库） |
| 能力代际数达上限（`MATE_GENERATION_LIMIT`，默认 4） | `40901` | `generation_limit_reached` | 复用现有能力集（同一套技能/MCP 组合）、等空闲代际被回收、或调大上限。**平台绝不静默复用一个错误的能力集** |
| 权限面已收窄（原生权限/provider 变化）而新代际建不起来 | `40901` | `generation_switch_required` | 稍后重试（先修配置）；**这一轮不会用已经收回的权限面继续跑**——宁可拒发 |

WS 帧契约（详见设计文档 §4.5）：客户端 `{action: attach|detach|ping}`；**每次连接的第一条帧必须是通道级 `attach`**（`{action:"attach", token:"…"}`，无 `agg_id`）——它交付 stream 面凭据，服务端 5s 内未收到即关闭（1008），令牌因此不进 URL；其后的 `attach` 才带 `agg_id` 与 `since_seq`（省略 → snapshot + live；数字含 0 → 按序重放 + live）。服务端 `{type: snapshot|event|detached|pong}`，event 载荷 `{type, seq, agg_id, version, payload}`，type 域见 `backend/internal/manager/events.go`（`session.message/thought/tool_call/tool_update/user_message`、`session.permission_request/permission_resolved`、`session.elicitation_request/elicitation_resolved`、`session.input_queued/input_delivered/input_cancelled`（§6 排队输入）、`session.config_options`（运行时会话配置项全量更新，如模型）、`session.commands`（底座自报的会话可用斜杠命令**全量**列表——**发现面，不是平台声明的能力**：只有 opencode 会通告，dsh 的 ACP 面不含命令故永不产生该事件；空数组表示"通告了当前没有命令"，执行只是把命令行当普通提示词发出去，平台不投影也不写面）、`turn.ended/error`、`run.cancelled`、`automation.run`（一次 automation 触发的结果，含没产生会话的 duplicate/overlap/skipped——**持久运行史在定义文件的 `recent_runs`，这条事件只走实时通道**）等）。会话动作的 POST 响应返回当前 `event_seq` 水位，作为 attach 续传起点。**文本增量在服务端攒批**（③ 合并，25ms / 4KB / 64 条）：一帧 `session.message`/`thought` 可能覆盖多个 delta，客户端按 `msg_id` 追加同一条消息的渲染逻辑不变；工具卡/权限卡/终态等事件不合并，顺序不被文本增量越过。

## 开发

**桌面是主线**（决策 v8③）——日常用 `make dev` 改 UI（**唯一 HMR 循环**），用 `make desktop/*` 验交付态：

```bash
make dev            # 浏览器开发态：vite :5173（HMR）+ 后端 :10001，改 UI 走这条最快
make desktop/dev    # 桌面态：先出 mate-core + 装配随包载荷，再起 Electron 壳（脚本内的真核心）
make desktop/accept # 真 Electron + 真核心的界面/契约验收（自起自清）
make desktop/dist   # 出安装包（dmg / Windows 安装器）
```

服务端形态（**维护态**：能跑、能修缺陷、不加新功能）：

```bash
make web      # 只构建前端产物（frontend/apps/web/dist，唯一一份）
make build    # 内核编译（全仓只有一个构建配置：不带任何构建标签，不依赖前端产物）
make vet / test   # go vet / go test ./...
# 前端：cd frontend && pnpm typecheck / pnpm build
# 起带面板的服务：cd backend && CGO_ENABLED=0 go build -o ../mate ./cmd/mate && \
#                 ./mate serve --web-dir "$PWD/frontend/apps/web/dist"
```

**桌面态调试**（细节见 [desktop 设计](docs/desktop-architecture-design-20260928.md)）：

```bash
cd frontend
KEEP_SHELL=1 pnpm ui:accept:desktop shell   # 跑完不杀壳：窗口留着，可连 DevTools 慢慢看
pnpm ui:accept:desktop stream               # 也可只跑一条场景（脚本里的 SCENARIOS 列表）
# 附渲染进程调试端口（`pnpm dev` 默认不带）：
cd apps/desktop && node scripts/build.mjs && ../../node_modules/.bin/electron . --remote-debugging-port=9222
#   然后 chrome://inspect 配 127.0.0.1:9222；主进程调试用 --inspect=9229
```

> 起壳前清掉宿主 `ELECTRON_RUN_AS_NODE`（否则 Electron 以纯 Node 跑、没有窗口）；桌面态与 `make dev`
> **别同时跑**（两个核心共用 `~/.ark-mate/mate.db`）。桌面态**没有 HMR**，改 UI 请走 `make dev`。

验收驱动（**浏览器靶 = 开发态**，先 `make dev` 起真服务，验的是**人正在看的那个实例**）：

```bash
cd frontend
pnpm smoke:e2e            # REST/WS 冒烟（不需要真实底座）
pnpm smoke:e2e:live dsh   # 真一轮对话（需要装好底座与凭据）
pnpm ui:accept            # 开发态界面验收（真 Chrome + CDP）：能力勾选面板 / `/` 命令补全 / 自动化页
                          # 可只跑一条：pnpm ui:accept cmd（cap | cmd | auto）
```

桌面入口的靶子反过来——**脚本自己起壳、自己起核**，所以跑之前别让 `make dev` / `make desktop/dev`
的核同时活着（两个核心共用 `~/.ark-mate/mate.db`）；前置是 `make desktop/payloads` 已在场
（壳在启动期就解析随包载荷，缺了直接拒启、不回落 PATH）：

```bash
cd frontend
pnpm smoke:desktop              # V9：真起两份随包载荷 + 隔离环境 + "载荷目录零变化"指纹（不联网、不跑模型）
pnpm ui:accept:desktop          # 真 Electron 壳 + 真核心：契约 / 加固 / 凭据 / 事件流 / 退出六组
pnpm ui:accept:desktop-skills   # 能力投放：平台物化的 skill 到底有没有落进底座真正读的那个根
                                # （用脚本内置的假 LLM 取真请求体，零额度、零凭证）

# 仓库根跑；桌面 DoD 汇总（上三条 + archtest/typecheck/契约/资产一致性/引擎 ABI/合规门禁）
make verify/desktop-dod
```

容器：

```bash
make image                                    # 最小镜像：只含 mate 本体（不含底座；容器能起、health 通，但无免 key 自检）
make image/runtimes WITH_DSH=true WITH_OPENCODE=true   # 额外内置 dsh / opencode（可分别开关）
make image/run                                # 起容器（具名卷 /data，端口只绑回环）
docker compose up -d --build                  # 等价的 compose 路径（变量见 .env.example）
```

数据（SQLite / transcript / agents / capabilities / 配置 / 代建工作空间）全落在 `/data` 一个卷里；
容器内必须 `MATE_ADDR=0.0.0.0:10001` 才映射得出去，对外部署先设 `MATE_TOKEN`。完整参数、
底座开关语义与受限网络（镜像站 / Go 代理）覆盖方式见 `docs/deployment.md` §4。

CLI 子命令：`mate serve`（控制面 daemon）、`mate demo`（单轮真实会话冒烟）、
`mate migrate`（transcript → SQLite 幂等导入）、`mate transcripts prune --older-than <dur>`
（显式清理 transcript；不设自动删除）、`mate config`（打印解析后配置与每项来源）、
`mate capability migrate`（把 skill 定义里退役的 `sources[]` 就地转成 `runtimes[]`；`--dir` 指定
能力目录、`--dry-run` 只打印将要做的改动，可重复执行——已是新形态的定义一律跳过。
**它在 sqlite 模式下只对"导入前/导出后的存量文件"有意义**：文件不再是事实源，跑完还要 `import`
才会进库）、
`mate capability import` / `export`（M6：能力定义换事实源的两个运维口子。`import` 把
`<CapabilityDir>/{skills,mcp}/*.json` 灌进 SQLite，**按行判重、已有行绝不覆盖**，坏定义逐条报出、
其余照常导入；`export` 把 DB 里的定义写回能力目录（此后文件是**导出物**），未知键原样、两次导出
逐字节相同，`--dry-run` 只打印将要写出的内容。装配期在 sqlite 模式下会自动跑一次 `import`
（幂等靠目录指纹 `meta.imported_capabilities`：目录没变就整体跳过，所以"我删掉的技能"不会自己
回来），需要重跑或补文件时用显式命令）、
`mate agent import` / `export`（M6-6：agent 定义换事实源的两个运维口子，形态与 `mate capability
import|export` 一致——`import` 把 `<AgentDir>/*.json` 灌进 SQLite（**按行判重、已有行绝不覆盖**，
`providers.json`/`default.json` 不参与，`default.json` 的显式选择在 meta 未设置时被采纳进
`meta.default_agent`），`export` 把 DB 里的定义写回目录并**同时写出 `default.json`**；装配期在
sqlite 模式下自动跑一次 `import`，幂等靠指纹 `meta.imported_agents`）、
`mate automation import` / `export`（M6-7：定时任务换事实源的两个运维口子，形态与 `mate agent
import|export` 一致——文件里的运行态键被切成**运行态列**导入、导出时再合成同一个文件；装配期在
sqlite 模式下自动跑一次 `import`，幂等靠指纹 `meta.imported_automations`）、
`mate doctor`（**只读体检**：前端资产来源与逐文件摘要、随包载荷清单与入口存在性、载荷
`nodeAbi` 与 `release.json` 的一致性、数据目录可写性、监听端口占用；`--verify-payload` 对载荷树做
**全量复算**（逐个文件 sha256 + 合成摘要 + 文件数/字节数），`--json` 供诊断包/CI 消费。
打包态还会拿 `<Resources>/release.json` 逐项比对 coreVersion / 契约代数 / webBuildId / webDigest /
载荷清单——不符退出码 1；"无法校验"（非打包态没有契约、端口已被自己占用）只记警告，不判失败）、
`mate --version [--json]`（核心版本与 API 契约版本；`--json` 供打包期读取，是桌面发布契约
`release.json` 的 `coreVersion` / `apiContract` 唯一取法）。
（历史上的 `mate acp-serve` / `mate acp-demo` 已随 echo 演示底座删除。）

部署形态（自建二进制 + 外挂 dist / systemd / 容器 / 环境变量全表 / 备份升级 / 排障）见 **`docs/deployment.md`**。

仓库结构：

```
backend/                单 Go module（github.com/morehao/ark-mate）
  cmd/mate             进程入口：只做子命令分发（→ internal/app/command）
  pkg/                  可复用基础设施（不得 import internal/，由 archtest 守卫）
    acl                 ACP 客户端（stdio 传输、能力探测、权限、elicitation（unstable）、错误分类）
    runtime/            运行时注册表 + adapter（opencode/、deepseekharness/，纯数据）
    mcp                 自研最小 MCP 客户端（stdio / http / streamable_http / sse）
    capability          Skill/MCP/Tool 能力定义装载 + skill 物化（投影落点由 runtime 声明）
    routing             路由引擎 FirstAvailable 切片
    config              进程配置（flag + MATE_* 环境变量 + .ark-mate/config 目录化）
    procreg             进程注册表（孤儿 runtime 子进程回收）
    dbclient             gorm 句柄装配 + 自注册纯 Go SQLite dialector（无 CGO）
    object/objusage     跨层共享值对象（会话用量快照）
  internal/             应用专属逻辑
    app                 装配 + 启停生命周期（Init/Run/Close）
    app/command         CLI 子命令（serve / demo / migrate / transcripts / config / acp-*）
    router              唯一路由装配点（按领域分 routes_*.go）
    controller/ctr*     传输层控制器（接口 + 小写实现 + New 构造）
    middleware          HTTP 中间件（单操作员 token 鉴权）
    dto/dto*            请求/响应 DTO（按领域分包，双方共用）
    service/svc*        领域端口（控制器依赖的窄接口，manager 实现）
    manager             领域实现：Service + Orchestrator（会话编排、事件发布、权限策略、用量聚合）
    model               持久层实体 + 列与取值约定（gorm 映射；时间 TEXT/布尔 INTEGER 由 serializer 保证）
    dao                 单表数据访问（gormdao 泛型 Dao + 各领域 Cond）
    store               持久层门面（句柄 + 跨表事务：序号分配、导入 staging、读模型 API）
    eventbus            进程内事件总线（WS 订阅源）
    archtest            架构守卫测试（分层边界 + 事件 type 契约快照）
frontend/               pnpm workspace（apps/* + packages/*）
  apps/web/             Web 问答控制台（React + antd；src/{store,console,components,pages,transport}）
  apps/desktop/         桌面产品（Electron 壳：src/ 主进程与 preload、renderer/ 壳静态页、scripts/ 装配与打包）
  packages/desktop-contract  @ark-mate/desktop-contract（载体契约唯一真源：常量 + 类型 + 纯函数判定；零 Electron 引用）
  packages/tsconfig     @ark-mate/tsconfig（base.json / app.json，同 ark-iam 编译器约定）
  packages/types        @ark-mate/types（REST 信封 / 会话 / WS 帧 / 域事件契约）
  packages/api          @ark-mate/api（axios 客户端 + /v1/mate resources）
  packages/ui           @ark-mate/ui（tokens/themeConfig/AppShell/PageContainer/状态胶囊；DESIGN.md 事实源）
docs/                   设计/路线图 + runtime 适配核实 + 部署运维
  deployment.md         部署/运维（自建二进制 + 外挂 dist、systemd、容器、环境变量、备份、排障）
  design-and-roadmap.md 总体设计与 M0–M3 路线图（历史设计记录）
  agent-runtime-refactor-design-20260908.md  P0–P3 重构设计与逐切片证据（§19.x）；§20 未实现能力/降级清单
```

> `docs/` 下的设计文档是**历史设计记录**（含当时的路径命名——会话路径早期叫 sessions、P0 起为
> conversations）；对外契约以本
> README 的「控制面接口摘要」与实际路由（`backend/internal/router/routes_*.go`）为准，
> 二者由 `TestReadmeRoutesMatchRouter` 自动守卫。

## 协议 / License

[MIT](./LICENSE)
