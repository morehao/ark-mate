# 控制面契约（对外接口摘要 · 错误码 · WS 帧）

> **这份文档是什么**：ark-mate 控制面的**对外契约权威摘要**——REST 路由表、业务错误码、WS 事件帧格式。
> 契约的**设计背景与分层**见 [`architecture.md`](./architecture.md) §6；本文只列"对外长什么样"。
>
> **它是机器判据的对象**：路由由 `TestReadmeRoutesMatchRouter` 与真实路由表**双向**比对，错误码由
> `TestReadmeCodesMatchRegistry` 与 `internal/core/code` 比对，WS 事件类型由守卫冻结。
> **改路由 / 事件 type / 错误码必须同一次提交同步这里**——否则守卫当场变红。
>
> 历史：本节原为 `README.md` 的「控制面接口摘要」整段，2026-11 迁出到本文，内容逐字保留、只搬位置不改判据。

## 控制面接口摘要

REST 前缀 `/v1/mate`；响应统一信封 `{code, requestID, msg, data}`，`code=0` 成功，非 0 为业务错误（40401 未知会话/供应商/MCP server、40901 会话正忙、40501 能力不支持、50201 runtime/连接故障等）。

**时间字段**：所有返回的瞬时（`created_at` / `updated_at` / `started_at` / `ended_at` / `timeout_at` / `next_due_at` / `last_fired_at` / `probed_at` / `checked_at` / `since` / `at` …）一律 **Unix 毫秒 `int64`**，前端按需格式化（`new Date(ms)`）。落库 / transcript / 导出正文 / `automation.schedule.at` / `usage` 的 `from`-`to` 保持 RFC3339 或日期串；口径与例外见 [docs/agents/time.md](agents/time.md)。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 健康检查（`{code:0,msg:"ok"}`） |
| GET | `/meta` | **诊断面（M1-7）**：`{coreVersion, webBuildId, apiContract, webPath, webDigest, home, runtime{id,name,version}, payloads[]}`。**`runtime` 是 v19 新增的装配事实出口**（构建期固定的底座身份）：底座由构建期固定、用户面零选择入口，所以「现在跑的是哪个底座」只能由事实面回答；加一个字段比加一条路由便宜，也不给用户新的选择面。`webPath` 是核心实际读的那份资产目录的绝对路径（没接资产层时为 null）、`webDigest`/`webBuildId` 由资产层与版本闸门（M2-3/M2-4）接真值，未接时为 null/空串（`webSource` 字段已于 2026-10-10 删除：核心只有"调用方给的目录"一种来源）。`payloads[]` 是**已解析**的载荷命中清单（`{name,version,digest,source}`，恒为数组），`source` 是命中级别：`bundled`（随包）/`runtime-env`（显式覆盖）/`path`（PATH 上找到）。桌面态由壳交（只有它做三级解析，M3-4）；`make dev` **也交一份**——dev 的核心同样按写死的名字走 PATH，所以它报 `source: path`（或覆盖生效时的 `runtime-env`），版本在**能从路径证明**时给真值、否则 `unknown`（核心与本仓都不探测本机安装：底座连 `--version` 都写自己的 home）。真·非桌面形态（`make server` / 仅 API）仍为空数组。令牌不进这里（M1-1 起令牌只经 stdin） |
| GET | `/capabilities` | **能力声明面（M1-7，设计 §7.3）**：`{entries:{browser,desktop}, capabilities:[{id,available,browserFallback}]}`。两个入口**同构**（id 集合与顺序一致），只有 `available` 随入口不同——壳的反代带 `X-Mate-Carrier: desktop`，浏览器入口的请求没有它。`browserFallback` 是 V3 单向一致性的机器判据（`tray` 是唯一允许 `none` 的纯增强能力）。**当前 id 集合 = `tray` + `native-dialog`**：2026-10-11 收窄（P3/D5）——`open-path` / `diagnostics-export` / `deep-link` / `update` 四条在两个入口都没有消费者，已删；差集与每项的靶见设计 §7.3，判据由 `internal/archtest` 的 `TestDeclaredCapabilitiesHaveConsumers` 钉住 |
| GET | `/runtime/diagnostics` | **底座诊断快照（v19）**：`{state,runtime,runtime_name?,command?,connected,pid?,stderr_source,stderr_tail[],last_error?,remediation[],checked_at}`。它原为「按 agent 取诊断」的那条路由（v19 已删）——底座由构建期固定，路径里再带一个 id 只会暗示「底座可以选」，所以路由与端口都不带参数；身份由响应里的 `runtime` 回答，展示名只在 `/meta` 的 `runtime.name` 一处（诊断面不重复第二份），也不再回带已下线的 agent 身份键。`stderr_source` 标来源 live/last_error/none；`remediation` = 按事实推导的可执行修复步骤（底座未装配 / 启动命令不可执行 / 要注入的供应商悬空或无密钥 / 底座自报鉴权方式 / 底座声明的安装步骤）。**只读**：不主动拨号，只读此刻已建立的事实 |
| GET | `/providers` | 供应商列表：`{id,name?,protocol,base_url,has_key,revision,updated_at?,note?,model_count,problems[],applicability[],refs{models[],runtimes[]}}`（密钥**只写不读回**，仅 `has_key`）。**v12 起供应商 = 一个 OpenAI 兼容端点 + 一把密钥**：`type`（自由串）与 `projections`（逐底座运行时参数）都删掉了（`protocol` 是枚举、本期只有 `openai`；`base_url` 是资源字段），`problems[]` 是「开会话之前就能看见」的本地段问题清单（缺端点 / 密钥缺失 / 密文解不开）。`applicability[]` 是逐**已注册** runtime 的可达性（只读派生）：缺 `base_url` → `not_configured` 并给可执行出路；否则报该底座走哪条凭据通道（原生凭据文档 / 子进程 env）。**载体按持久层模式分叉**：`MATE_STORE=sqlite` 时存在 `providers` 表（**v13 起是资源表**：平台字段全成列 = `id`/`name`/`protocol`/`base_url`/`note`/`revision`/`created_at`/`updated_at`，**没有 `body`、也没有 `key_ref`**；写面单事务 = 行 CAS + 同一张审计表追一条 `kind=provider` 的变更），`<ProviderDir>/providers.json` 降级为导入/导出载体；memory 模式仍是该文件。**密钥在库（v20）**：明文落在本表的 `key` 列（v20 决策：密钥在库，理由见 `docs/deployment.md` §2.1）；`has_key` = `key` 非空——**只有一个判据**，不再有"库里有引用、盘上解不开"这种中间态。盘上另有一份 `<MATE_HOME>/credentials.json`（0600）的**严格引用映射**，它是**可重建的双写镜像**：写失败只 WARN + 进 `problems[]`，删掉不丢任何东西 |
| POST | `/providers` | 新建供应商 `{id,name?,protocol?,base_url?,api_key?,note?}`：`id` 过词法校验（仅 `a-zA-Z0-9._-`，≤64，会成为凭据引用段与镜像文件的键）；`protocol` 缺省 `openai`，非 `openai` → `40501` `protocol_unsupported`（原生 anthropic/gemini 协议尚未取证）；已存在 → `40901` `provider_exists`（**绝不覆盖**，改它用 PATCH）。**写面顺序固定**：① 校验 → ② 载体提交（行 CAS + 审计，**密钥与档案同一笔写**）→ ③ 更新内存 → ④ 双写镜像（失败只 WARN：镜像可重建，**不需要补偿**——v20 删掉了整段两阶段写与失败补偿，见设计 §8.1） |
| PATCH | `/providers/:id` | **只更新**（不再 upsert；不存在 → `40401` `provider_not_found`）：`{name?,protocol?,base_url?,api_key?,note?}` 指针语义（`nil` = 不改）。`api_key` 给空串 = 清除（删密文；v13 起没有 `key_ref` 列可清）。响应带 `effect{channel,effect,applies_to_models,affected_sessions,deferred,note}`：只有 `protocol`/`base_url`/`api_key` 变了才说「下一个轮次边界生效」（`effect=next_run`），只改 `name`/`note` 如实回 `effect=none`；`deferred` 是其中「有在途轮次或有未应答权限/提问、将顺延」的会话数，判据与代际裁决**同源** |
| POST | `/providers/:id/probe` | 连接自检两段：`{upstream?:bool}` → `{ok,checks:[{name,status,detail,remedy}]}`，`status` ∈ pass/fail/warn/skip/unknown。**本地段无条件且零出网**（exists/protocol/base_url/secret/refs/delivery）；**上游段仅当 `upstream:true`** —— 发**一次** `GET <base_url>/models` 带 `Authorization: Bearer <key>`（超时 5s、不重试、**不落清单不写缓存不进 `/models`**、错误文本先过 redact），只判「连得上/鉴权过/几条模型」。自建网关因此也能自检（v11 那张「type → 厂商 URL」硬编码表已删）。`ok` = 「没有 fail」（warn/skip/unknown 都不算失败）。**`GET` 与页面加载永不触发上游段** |
| DELETE | `/providers/:id` | 删除：幂等（不存在 → `deleted:false` 且 code=0）；被模型引用 → `40901` `provider_in_use` + `referenced_by_models[]`（**不做级联清空**：清空引用等于静默换掉凭据来源，删掉挂靠模型等于静默删用户数据）。顺序**先在 store 的同一事务内查引用 → 删行（密钥就在这一行上，随行一起消失）+ 审计 → 摘内存 → 尽力清掉镜像里的那条引用**（v13 起：引用检查与删行同事务，消除「删供应商」与「给它加模型」并发时的 TOCTOU 悬挂窗口）；v20 之后**没有「密文残留」这个失败态**——镜像清不掉只 WARN，下一次重建镜像时它自然消失 |
| POST | `/models` | 新建一条**应用层模型清单**条目：`{provider_id,model,name?,note?,input_modalities?,max_input_tokens?,max_output_tokens?,is_default?}`（`model` = **发给上游的模型名**，v24 从 `model_id` 改名：`_id` 一律指「指向某表 `id`」；两个容量字段 = 平台声明的输入/输出上限，v25 加，取值必须 ≥ 1） → 条目（`input_modalities` ∈ `["text"]`（默认）/`["text","image"]`，非法取值 `40501 modality_unsupported`）。**`runtime` 不是资源字段**：请求体里出现它 → `40001` `invalid_param`（v12 起一份清单投给所有已注册底座）。`(provider_id, model)` 已存在 → `40901` `model_exists`（库级唯一索引）；`provider_id` 不存在 → `40401` `provider_not_found`；memory 模式（无清单载体）→ `40501` `model_list_unavailable`。`is_default:true` 写的是 `meta.default_model` 这个**单一值**（单一默认值 `meta.default_model`） |
| PATCH | `/models/:id` | 指针更新 `{name?,note?,input_modalities?,max_input_tokens?,max_output_tokens?,is_default?}`（不存在 → `40401` `model_not_found`；带 `runtime` → `40001`）。两个容量字段是**三态**：省略 = 不改、≥1 = 声明、**`0` = 清除声明**（回到「由环境兜底」，落库 NULL）；负数 → `40001`。**`is_default` 有两个方向**：`true` = 设为**唯一**默认（覆盖 meta 的单一值）、`false` = 取消默认（仅当它当前正是默认；幂等）。`is_default` 只是**写面的触发字段**：它不落进模型行、不出现在读面的行上（读面只有 `default_model` 一个字段） |
| DELETE | `/models/:id` | 幂等（不存在 → `deleted:false` 且 code=0）。**行为变更（B4）**：v11 里「它是默认模型」会被 `40501` `model_is_default` 拒绝，v12 **允许删**并**连带清空默认**——响应回 `default_cleared:true` 把它说出来（默认是**偏好**，偏好跟着资源消失比拦住用户更合理）。删掉一条**不会弄坏既有会话**（dsh 逐字：a saved selection can still submit requests after its catalog entry disappears），所以不扫会话改绑。**删行与清默认在 store 的同一事务内**（v13：消除「默认指向一条已删除的行」的悬挂窗口） |
| POST | `/models/sync` | 重新同步（`{runtime?}` 空 = 全部已注册）：`{applied:[{runtime,status,where,effect,pending,credential{channel,status,effect,tested,where},problems[]}]}`。**不绕过任何一道门**：清单为空走**撤回**路径（清注入、摘启动参数、删/清空文件），adapter 的自校验失败一个字节都不写，dsh 侧还有**底座级校验门**（候选先在 staging `DSH_HOME` 上跑一次 `--patch <候选> --dump-config-schema`，只有「退出码 0 + `x-cordis.complete=true` + 诊断为空」三条同时成立才落盘）。任一 runtime 失败 → `50001` `resource_sync_failed`（**资源不回滚**，只是没交付到那些底座）。成功路径**没有 `advertised`**——那要新进程才拿得到，此刻报它就是假装已生效，只如实回 `pending:true` |
| GET | `/models` | **应用层清单 + 聚合生效结论**（`?runtime=<id>` 只看那一个底座的机器面明细）：`{models[], default_model, effective, effective_at, sync[], note?}`。`models[]` 一条 = `{id, provider_id, model, name?, note?, input_modalities, max_input_tokens, max_output_tokens, revision, updated_at?}`（`model` = 发给上游的模型名，**不是** `id` 那一族；两个容量字段**总是出现**，`null` = 平台未声明——那时生效的是底座自己的兜底值，界面上必须如实标出，v25），**顺序固定为 `(provider_id, model)` 升序**（v13 修掉了 v12 的「平台 UUID 字典序」——那是用户可见的缺陷：每加一条模型它出现在任意位置）。**用户面只有前四个字段**（设置 · 模型 整节零底座词汇）——`default_model` 是**唯一**默认（平台模型 id），`effective` ∈ applied/pending/failed 是聚合结论，`effective_at` ∈ now/next_session/next_launch 取**所有底座里最晚**的那个（保守而非欺骗：改密钥在 dsh 上一轮就生效，页面会说「下次启动生效」；精确结论留在 `sync[]`）。`sync[]` 是**机器面/诊断面**（逐 runtime 的 `status`/`where`/`effect`/`credential`/`problems`），前端**不得直接渲染**其中的 runtime/where/problems 值。**读面无副作用**：逐 runtime 结论读的是上一次同步的持久记录，不在读路径上重跑同步 |
| GET | `/models/catalog` | **运行时目录** → `{runtime,source,fetched_at?,image_input?,options[],auth_guidance?,note?}`（v19 去掉了 `agent` 字段：底座构建期固定，目录属于**那一个**底座）（`image_input` 是**连接期事实**——ACP initialize 的 `promptCapabilities.image`；取不到即缺席，**不是 false**）：**由运行时自报**的会话配置项（ACP session config options），`category=model` 为主，其余选项（`mode`/`thought_level`）原样透出但只作展示。取样来源 `source` ∈ session/cache/resume/none。取值一律是**不透明字符串**（dsh 为 `JSON.stringify([mate-<provider>, model])`；具体形态由该底座的 adapter 声明），前端只按 `category` 渲染，**绝不解析或拼接**。**v26 起每个候选多一个 `model_id` 注解**：它指向平台清单里的哪一行（`mdl_…`；空 = 平台未声明 ⇒ 该候选**不可选**）——会话的 `model` 收的是这个平台行 id，而选择发生在目录上，所以注解跟着候选走（一次取数即可「选运行时值、提交平台 id」）。算法是**派生值对派生值**（对每条平台行算运行时值再与候选逐字比），所以平台**不解析**底座词表；多命中按 fail-closed（不注解）。**绝不新建探测会话**（dsh 的 `session/new` 会落持久记录且不广告 `session/delete`） |
| GET | `/composer/skills` | composer `/` 技能选择器的**内置技能目录**（v4）→ `{items:[{id,name,desc}]}`（顺序即展示顺序）。与「能力数据面」的 `GET /skills`（技能定义目录）是**两回事**：这里是平台内置、一次性注入的协作技能（如 `plan-first` 先给方案），无定义文件、无物化、不进能力指纹 |
| GET | `/sessions` | 会话列表（`?archived=true` 过滤归档）。两个工作空间过滤参数口径不同：`?cwd=<绝对路径>` = 工作空间**精确等于**该路径的会话（历史行为）；`?workspace_id=<登记 id>` = **归属**该工作空间的会话（v14；左栏分组用它，可索引。按**默认**工作空间过滤时**同时包含** `workspace_id=''` 的历史会话——那些行读面归默认工作空间）。每条会话回读 `cwd`、`workspace_id`、`workspace_title`、`workspace_temporary` 与 `origin`（**来源，非用户输入**：缺省 = 人工建；`automation:<id>` = 该定时任务触发——面板据此打「定时」标，也是「这条会话是谁开的」的唯一权威判据）。`workspace_id`/`workspace_title` 是**读面解析后**的值（历史行下发默认工作空间的 id 与展示名），前端不必自己按 cwd 去匹配登记表（那会因规范化差异匹配不上） |
| GET | `/workspaces/browse` | 目录浏览（`?path=<绝对路径>`（缺省 = 偏好名录第一项，未配则= 服务进程家目录）、`?hidden=true` 含隐藏目录）：**只列目录、全系统** → `{path, parent, home, roots, crumbs[], truncated, entries:[{name, path, hidden}]}`；`entries[].path` 与建会话 cwd 同形态（已符号链接规范化），可直接提交。`crumbs` 是**从文件系统根到当前目录（含自身）**的祖先链（服务端算，前端不拼路径——盘符 / UNC / 符号链接三处都会拼错），逐级可跳；`truncated` = 单层超过 1000 条时被截断；`hidden` = 宿主约定的隐藏名（POSIX 点前缀），服务端在 `hidden=false` 时不下发、下发时带标记供面板做「显示隐藏文件」开关。**`roots` 是起始点偏好**（`MATE_WORKSPACE_ROOTS`；空 = 无偏好），**不是允许范围**：任何已存在的目录都能浏览、都能当工作空间（v14.5 起「选工作区是全系统」），`parent` 一直给到文件系统根） |
| POST | `/workspaces/pick-directory` | **抬一次宿主原生目录选择器**（v14.5，对齐参照实现的 `directory-picker-native`）：无请求体 → `{path}` 或 `{cancelled:true}`。面板由**核心进程**抬起（macOS `osascript` 的 `choose folder`、Linux zenity/kdialog），所以**本机浏览器入口与桌面窗口都能用到它**——判定看宿主事实（绑回环 / 非 SSH / 有显示会话），不看客户端形态；宿主抬不起来时回 `40501 native_pick_unsupported`（前端据此开应用内浏览，`GET /capabilities` 的 `native-dialog` 就是这条声明）。**取消不是错误**：`{cancelled:true}` 让调用方静默收手。Windows 上**明确不支持**（本仓没有那条 COM 路径），同样回 40501 而不是假装成功 |
| POST | `/workspaces/dirs` | 在某个**已存在**的目录下建一个子目录（目录浏览面的「新建文件夹」）：`{parent, name}` → `{path}`（新建目录的规范化绝对路径，可直接当 cwd）。`parent` 走**与建会话同一条校验**（绝对 / 存在 / 是目录 + 符号链接规范化；名录**不参与**，v14.5 起它只是浏览起点偏好）；`name` 必须是**单段**（不含 `/`、`\`，不是 `.` / `..`，否则 `40001 workspace_invalid`）；同名目录已存在 → `40901 workspace_exists`（**不覆盖、不静默复用**——"我建的那个"与"原来就有那个"是两件事）。**不落库**：只动磁盘，登记仍走 `POST /workspaces` |
| GET | `/workspaces/files` | 目录**内容**清单（`?path=<绝对目录>` 必填、`?hidden=true` 含隐藏项）：`{path, roots, entries:[{name, path, is_dir, size, modified_at}]}`（目录在前、文件在后）。与 `browse` 分工：**browse 用于选工作空间**（只列目录，条目可直接当 cwd），**files 用于看资料**（列文件，条目**不可**当 cwd）。**全系统可看**（v14.5 起不再按名录过滤），跟随符号链接取真身属性 |
| GET | `/workspaces/recent` | 最近使用的工作空间（`?limit=` 默认 10 上限 50）：**从既有会话派生**（`{path, name, last_used_at, sessions}`，按最近使用倒序、同目录聚合计数、排除平台代建的临时空间与默认工作空间），不另存状态。**v14.3 起面板已无消费者**（选择器只剩登记项 + 「添加工作空间…」），端点保留是因为它仍是可用的只读派生视图（诊断/脚本/将来的"最近"入口），删它要连带改 README 与文档——遗留原因记录在工作空间设计文档 |
| GET | `/workspaces` | **工作空间登记表**（v14）：按展示顺序列出用户登记的工作空间 → `{items:[{id, path, title, is_default, list_order, dir_status, sessions, default_permission_mode?, default_capability?, last_used_at?, created_at, updated_at}], default_id?, writable, provision_dir?}`。**v33**：每条新增 `default_permission_mode`（该工作空间新建会话时的默认权限档，空=未设）与 `default_capability`（默认能力选定原始 JSON，空=未设）——二者只服务前端「新建会话预选初值」与「默认设置」页回显，服务端**不在建会话时回落它们**（`permission_mode` 必传，见 `POST /sessions`）。它是选择器与左栏分组的**唯一事实源**（`browse`/`libraries`/`recent` 都是派生视图）。`dir_status` ∈ `ok\|missing\|not_dir\|unreadable`，**读时现算不缓存**：外部删/移目录只在下一次读时反映；非 `ok` 的行**仍然返回**（登记不会因为目录消失就凭空不见，用户得能看见并清理它）。历史会话（`workspace_id=''`）在**读面**归到默认工作空间那一行（数量与最近使用都并进去）。memory 持久层 → `writable:false` + **只列那一条默认工作空间**（用户登记表在 memory 模式下无处可存，所以不可写——那必须让前端看见，否则它会渲染一个必然失败的创建入口；而默认工作空间是**平台代建**的东西、不是用户登记项，装配期照常建目录并列出，否则"未指定 cwd 建会话"这条最普通的路在 memory 形态下会报 `workspace_required`。代建显式关闭时 items 为空） |
| POST | `/workspaces` | 登记一个目录为工作空间：`{path, title?, strict?}` → 单条登记。`path` 走**与建会话同一条校验**（绝对 / 存在 / 是目录 + 符号链接规范化），所以不存在"能登记、建不了会话"这条路；拒绝码与建会话**逐字一致**（`40001 workspace_invalid`）。**名录不参与**（v14.5：任何可用目录都能登记）。**默认 get-or-create**（同路径已登记 → 返回既有那一行，重复点击不报错）；`strict:true` 时重复报 `40901 workspace_exists`。一个目录至多一条登记由**库级唯一索引**保证（并发两次创建时输家重读既有行）。默认工作空间由装配期建立（见 `POST /sessions` 的工作空间段），本端点不创建它 |
| PATCH | `/workspaces/:id` | **补丁语义**，按请求体里出现的键**只改那些格**：`{title?, path?, default_permission_mode?, default_capability?}`。`title` 空串 = 清空（界面按末级目录名渲染），**改名不改身份**（`id` 与 `path` 都不动，已有会话归属不受影响）；`path` = 改登记路径；**v33 新增** `default_permission_mode`（取值 ∈ `read-only`/`workspace-write`/`danger-full-access` 或空串=清掉；非法 → `40001 invalid_permission_mode`）与 `default_capability`（能力选定原始 JSON，空串=清掉）——它们只影响**之后**在该工作空间新建会话时前端预选哪个档/能力，**不改任何已有会话**（会话的 `native_mode`/`capability_json` 是快照）。未知 id → `40401 workspace_not_found` |
| DELETE | `/workspaces/:id` | 删除一条登记**而保留数据**：目录、文件、会话历史一概不动，该工作空间下会话的 `workspace_id` 清空（读面归默认工作空间）→ `{id, released_sessions}`。`released_sessions` 是给界面说清"这 N 个会话回到了默认工作空间"的事实。**默认工作空间拒绝**（`40001 workspace_default_immutable`：它是未指定 cwd 的落点）。memory 持久层 → `40501 workspace_schema_read_only` |
| POST | `/workspaces/:id/move` | 重排（DOM `insertBefore` 语义）：`{before_id?}`（空 = 追加到尾部；锚点是自身 = 空操作）→ `{ids:[...]}` **完整**顺序。重排在同一事务内把 `list_order` 重编号为 `0..n-1` 并**逐行对账**（行数 / 位置 / id 任一不符即回滚）——顺序有洞的库下次重排会自己漂移，而没有任何界面能显示它。未知目标或锚点 → `40401 workspace_not_found` |
| POST | `/files` | **上传文件（全局唯一收字节端点，产物溯源 P0′）**：由 golib `ginupload.Register` 提供，`multipart/form-data`（`file` 必填 + `content_hash` SHA256 必填，用于内容寻址去重）→ `{file_id, name, mime_type, status}`。`file_id` = `core_file_upload.id`。附带的 `POST /files/check-exist`、`GET/DELETE /files/:id`、`POST /files/:id/presign-url`、`GET /files/:id/redirect`、`GET /files/:id/serve`、`POST /files/multipart`（分片上传整套）均由 ginupload 提供。**业务模块不再各自收字节**：附件/资料库/产物都只建索引、引用 `file_id`。**`content_hash` 由上传方算**（小文件前端直算，大文件走分片）。memory 模式（无持久层）不挂本组路由。**前端预览/下载走 `POST /files/:id/presign-url` 换签名 URL 再直取 `/objects/...`（本地驱动 `BaseURL` 指向的公共前缀），不用 `GET /files/:id/serve`**——`serve` 仅保留为调试/兼容，产品路径不经它。**签名字节通路只挂在核心 origin**（即 `/v1/mate/files/:id/presign-url`）：前端若把请求打到别的宿主（丢了 `/mate` 段，路径退化成 `/v1/files/.../presign-url`、`/files/.../presign-url` 等），核心会回 **HTTP 405 + 信封 `code=40401`**（msg 提示改用核心 origin），而不是一句误导人的"接口不存在"404——据此一眼可判"打错了后端" |
| POST | `/files/:id/convert-pdf` | **office 预览转 PDF**（docx/pptx 预览）：把已落 filestore 的 `file_id`（= `core_file_upload.id`）文档在**服务端**用随包 `@deepseek-ai/libreoffice-kit` CLI 转成 PDF，**直接以 `application/pdf` 字节流返回**（不是 JSON 信封——前端拿去喂现有 `PdfRenderer`；docx/pptx 是 ZIP 二进制，前端按文本渲染必乱码）。可选 `?name=` 给原始文件名（推导输出后缀）。**失败走信封**（HTTP 200 + code≠0）：随包 payload（node / libreoffice-kit）缺失 → `40501` `office_preview_unsupported`（明确降级）；`file_id` 不存在/不可读 → `40401` `attachment_not_found`；转换进程失败（非合法 PDF）→ `50001` `office_preview_failed`。**只读已有 blob 转格式，不破坏「全局唯一收字节面」**（收字节仍在 `POST /files`）。memory 模式无字节存储 → 40501。 |
| POST | `/attachments` | **建附件业务索引**（v32；P0′ 收口后只收 JSON：`{file_id, name?}`）→ `{id, file_id, name, mime_type, size_bytes, sha256, kind, state, created_at}`（`file_id` = 附件字节的 filestore 句柄，前端凭它换 presign-url 预览/下载）。字节已由 `POST /files` 落 filestore，本端点**只建索引**：按已落字节**探测真实类型**（png/jpeg/webp/gif → `kind=image`；否则 `kind=document`；**伪装图**按文件头拒 → `40001 attachment_unsupported_type`），`state` 初始 `pending`，挂到某一轮时置 `linked`；`file_id` 不存在 → `40401 attachment_not_found` |
| GET | `/attachments/:id` | 回读附件元数据（不回字节）→ `AttachmentResp`（含 `file_id`）。未知 id → `40401 attachment_not_found` |
| DELETE | `/attachments/:id` | 撤下**待挂**附件 → `{deleted}`。`state=linked`（已挂进某一轮）→ `40901 attachment_linked`（与「已投递的队项不可撤」同一取舍）；不存在 → `deleted:false` 且 `code=0`（**幂等**）。删行 + `filestore.DeleteFile`（物理 blob 是否真删由内容去重决定） |
| GET | `/materials/nodes` | **列资料库目录**（v33；`?parent=<id>&limit=&offset=`，空 parent = 空间根；一层子节点，`updated_at` 降序）→ `{nodes[], has_more, offset, limit}`。**本期只支持默认个人空间** |
| GET | `/materials/nodes/:id` | 资料库节点详情 → `{id, parent_id, node_kind, title, body?, table_id?, schema?, file_id?, source_url?, size_bytes, category?, provenance_id?, created_at, updated_at}`（按 kind 裁剪载体；`file_id?` 仅 drive 种类：附件链路的 `core_file_upload.id`，前端忘签名通路预览/下载；v34 起带 category/provenance_id）。未知 id → `40401 material_node_not_found` |
| POST | `/materials/nodes` | **建节点**（v33；`{parent_id?, node_kind, title, body?, schema?, source_url?, space_id?, file_id?}`）：`node_kind ∈ folder/doc/table/link/page`（`drive` 走「先 `POST /files` 拿 file_id → 传 `file_id`」两步式，**P0′ 起不再有 materials 上传端点**）。种类与载体不匹配 → `40001 material_node_kind_mismatch`；非默认 `space_id` → `40501 space_not_supported`；未知种类 → `40000 invalid_node_kind` |
| PATCH | `/materials/nodes/:id` | 改名/移动/改正文（`{title?, parent_id?, body?}` 指针语义）。移到自身或子孙下 → `40001 material_cycle`；目标父不存在 → `40401 material_node_not_found`；非 doc/link/page 改 body → `40001 material_node_kind_mismatch` |
| DELETE | `/materials/nodes/:id` | 删节点 → `{deleted}`（folder **级联删子树**；不存在 → `deleted:false` 且 `code=0`，**幂等**）。字节按 filestore 去重回收（附件链路），本期不主动删 blob |
| POST | `/materials/import-artifact` | **收录产物进资料库**（v34；`{artifact_id, parent_id?}`）→ 节点详情。按产物 kind 映射 node_kind（file→drive/text→doc/table→table/link→link），建 `category='artifact'` 且 `provenance_id=artifact_id` 的节点（**引用而非复制**产物）。幂等：同一 artifact 已收录 → 返回既有节点。产物不存在 → `40401 artifact_not_found` |
| POST | `/artifacts` | **登记产物**（v34；JSON：`{file_id?, content_text?, session_id, run_id?, tool_call_id?, origin, kind, title, path?, mime_type?, size_bytes?}`）→ 产物详情。**只建索引、不收流**（字节已在 `POST /files` 落 filestore）：file 类传 `file_id`（从 filestore 补 sha256/size）、内联类传 `content_text`。载体与 kind 不匹配 → `40001 artifact_kind_unsupported`；memory 模式 → `40501 artifact_unavailable` |
| GET | `/artifacts/:id` | 产物详情 → `{id, session_id, run_id?, tool_call_id?, origin, kind, title, path?, mime_type?, size_bytes, file_id?, content_text?, content_sha256?, dirty, created_at, updated_at}`。未知 id → `40401 artifact_not_found`。`dirty` **惰性计算**：本端点按 `path`（工作区相对路径，仅工具写工作区文件的产物有）重算工作区哈希比对 `content_sha256` 并回写；工作区原文件被删/改动 → `dirty=true`；列表端点 `GET /sessions/:id/artifacts` **不算** dirty（返回存量） |
| GET | `/sessions/:id/artifacts` | **列某会话的全部产物**（v34）→ `{artifacts[], total}`（`created_at` 降序）。含未被收录进资料库的产物 |
| GET | `/materials/search` | **关键词检索**（v33；`?q=` 必填、`?limit=` 默认 50 上限 200、`?offset=`）→ `{hits:[{node_id,title,node_kind,snippet}], total, has_more, note?}`。对 title/body 做**大小写不敏感子串匹配**——**不是语义检索、不是问答**；空结果 `total:0` + `note` 提示换词或浏览 |
| GET | `/materials/nodes/:id/rows` | **取表格行**（v33 P3；仅 `node_kind=table`）→ `{node_id, schema, rows}`（`rows` 为 JSON 数组字符串）。非 table → `40001 material_node_kind_mismatch` |
| PUT | `/materials/nodes/:id/rows` | **整表替换行**（v33 P3；`{rows}`，`rows` 必须是 JSON 数组字符串）→ `{node_id, schema, rows}`。非法 JSON/非数组 → `40000`；非 table → `40001 material_node_kind_mismatch` |
| POST | `/materials/nodes/:id/publish` | **发布 page**（v33 P3；仅 `node_kind=page`）→ `{node_id, view_path, file_path}`：把 body HTML 落盘到 `<MATE_HOME>/material_pages/<id>/index.html`（本机可打开）。空 body → `40000`；非 page → `40001 material_node_kind_mismatch`。**不做公网分享** |
| GET | `/materials/nodes/:id/view` | **查看 page**（v33 P3）：回 body 的 HTML（`Content-Type: text/html; charset=utf-8`），仅本机回环。非 page → `40001 material_node_kind_mismatch` |
| GET | `/sessions/search` | 会话搜索（`?q=` 必填、`?limit=` 默认 30 上限 50；含归档、`updated` 降序） |
| GET | `/search/messages` | 跨会话消息检索（`?q=` 必填、`?limit=` 默认 30 上限 200；返回 `seq` 锚点用于跳转定位，`source` 标数据面） |
| POST | `/sessions/export` | 批量导出 `{ids[], format?: markdown\|text\|json}`（ids 上限 50）→ `{files:[{filename, content, bytes, events, truncated?}], skipped[], total_bytes}` |
| POST | `/sessions` | `{prompt?, cwd?, model?, workspace_id?, permission_mode, capabilities?}`（v19：已下线的 agent 选择键**不再被读取**；带上它 → 40001 `agent_selection_removed`，**显式拒绝**而不是静默忽略） → `{id, event_seq, cwd, workspace_temporary, …}`。**权限档（v33，必传）**：`permission_mode` ∈ `read-only`/`workspace-write`/`danger-full-access`；缺键、空串或非法值 → `40001 invalid_permission_mode`（**零副作用**：不建会话/不拉进程/不写库）。用户面没有「跟随默认」——每次新建都提交一个具体档；工作空间默认档只做前端预选、不参与服务端回落。档位决定会话跑在哪个暖进程（`DSH_PERMISSION_MODE` 进 `ConnKey`，不同档各起进程），并作为快照落 `sessions.native_mode`（读面回显）。（带 prompt 则后台起首轮；`model` 是 **`GET /models/catalog` 的 `options[category=model].values[].model_id`**，即**平台模型行 id**（`mdl_…`）——**v26 的破坏性 wire 变更**：此前它是目录里的不透明 `value`。形状不对或查不到行 → `40401 model_not_found` 且**零副作用**（不建会话/不拉进程/不写库）；解析成功后由该底座的 adapter 现算运行时值并经 `session/set_config_option` 会话级应用，运行时拒绝即建会话失败）。**工作空间**：`cwd` 非空时校验「绝对 / 存在 / 是目录」并按符号链接规范化（v14.5 起**不再**要求落在 `MATE_WORKSPACE_ROOTS` 之内——那份名单只是浏览起点偏好），不满足则 `workspace_invalid`（40001）；为空时落在**默认工作空间**（v14：`MATE_WORKSPACE_DIR`（默认 `~/.ark-mate/workspaces`）下的 `default/`，由装配期登记成一条工作空间，登记表为空时才建立）并回 `workspace_temporary=true`；没有默认工作空间（`workspace_dir` 为空，或默认工作空间已被移除）且未指定则报 `workspace_required`——**不回落 serve 进程目录**（那会让所有会话共用一个沙箱边界）。**v14 起不再按会话 id 代建独占目录**：两个未指定 cwd 的会话共享同一个默认工作空间（代价与理由见工作空间设计文档）。可选 `workspace_id`（`GET /workspaces` 的 id）直接选中一个登记项，此时 cwd 可省（平台按登记路径定）；`workspace_id` 与 `cwd` 同时给出且指向不同目录 → `40001 workspace_conflict`（**拒绝而不猜**：猜错的后果是会话跑在没打算用的目录里，而那是沙箱边界）。显式 cwd 落在**已登记**目录上时会带上该登记的归属；落在未登记目录上则归属留空（读面归默认工作空间）——**不自动登记**用户没主动选过的目录。工作空间建成即不可变（它是底座 ACP session 的 cwd，也是原生权限 `workspace-write` 的边界）；**改名/删登记不改它**。**能力选定（M3）**：`capabilities: {expert?, skills?, mcps?, apps?}` 把这条会话钉在一个专家（0..1）与技能/MCP/应用的子集上，逐 kind **三态**——键缺失 = 未声明（回落平台默认：全量）；`[]` = 明确一个都不要；非空 = 白名单。`expert` 同构三态：键缺失 = 不选专家（只有整个 `capabilities` 缺省时，工作空间默认才可能带入专家）；`""` = 明确不用；非空 = 专家 id。**专家只填空缺**（显式 > 专家推荐 > 平台默认），推荐里不可达的项**跳过并在响应 `warnings` 列出**（不阻断创建）；专家不存在/禁用 → `40401 expert_not_found` / `40001 expert_disabled`。带专家的会话在创建时把有效技能/MCP **物化**成具体 id 列表落库（改专家定义不漂移；专家 id 不进指纹）。传了键就按白名单**覆盖**（不是求交，否则"这条会话想放宽"永远表达不出来）。选定与它解析出的**能力指纹**一起落库，重启后不漂移。不可达（id 不存在 / 该 runtime 不适用，两者**同码不同文案**）→ `40001 capability_not_applicable` 且**零副作用**（不建会话、不拉进程、不写库、不留代建目录）。生效边界见「能力的生效边界」一节 |
| GET | `/sessions/:id` | 会话详情（元数据 + 挂起权限 + 权限记忆） |
| PATCH | `/sessions/:id` | 元数据更新 `{title?, pinned?, archived?, model?}`（`model` 与 `POST /sessions` **同一个词**：平台模型行 id `mdl_…`；解析失败 → `40401 model_not_found` 且**一个字节都不落库**，绝不静默换模型。能不能改、什么时候生效由该底座的能力声明决定；dsh 实测为会话内即时生效）。**v26 起会话响应里没有 `provider` 字段**（它恒空——v19 删 agent 面之后就没有写入点了；供应商由模型行给出） |
| PATCH | `/sessions/:id/capabilities` | 会话内改能力（M3-8）：`{expert?, skills?, mcps?, apps?}`，逐 kind **三态补丁**——键缺失 = 不改这一格、`null` = 回「未声明」（技能/MCP 回默认全量、专家回跟随默认、**apps 回无 app 工具**）、`[]` = 明确一个都不要、非空 = 白名单；专家 `""` = 明确不用。**`apps` 与会话 app 业务工具面同源**（`CapabilitySelection.Apps`，设计 §5.5）：它是**唯一来源**、口径为「注入这些 app 绑的工具域」，未知/停用 app 在注入侧**自然收窄不报错**（不新增可达性校验、与 `POST /sessions` 同口径），前端据此只列 `listApps()` 结果并把未启用项灰掉；它**进代际指纹**（`--apps=` 进 `plan.Direct`）。语义是**补丁不是整体替换**（前端只提交改动的那一格）。服务端在同一请求里做**专家物化**（推荐只填空缺、不可达推荐跳过并进 `warnings`）与**可达性校验**（技能/MCP/专家）：不过 → `40001 capability_not_applicable`（原因逐条列）/ `40401 expert_not_found` / `40001 expert_disabled`，且**一个字节都不落库**。写的是 `sessions.capability_json`（**不新增 pending 列**——"待生效"是派生量）：生效仍是「下一个轮次边界」，`maybeMigrateGeneration` 比对「记录算出的指纹」与「句柄此刻挂着的指纹」，忙则顺延、切不动回退旧代际。清空能力写成 `{}` 而不是空串（空串 = 未声明，代际迁移会把目标指纹回落成句柄当前值 → 判「没变化」→ 清空永远不生效）。响应是 `SessionResp`：`capabilities` 为**物化后**的选定（含 `apps`），`warnings` 带本次编辑的边界提示（换专家不重注入人格、专家推荐模型不切换） |
| GET | `/sessions/:id/history` | 事件回放（`?after=` 向后（`seq > after`）/ `?before=` 向前（`seq < before`，加载更早）互斥 + `limit`，默认 200/上限 1000；**两个游标都可缺席**：都不给 = 从第 0 条起的第一页，`after=0`/`before=0` 分别是「第 0 条之后」「第 0 条之前（空页）」。seq 是**会话内**位次、0 起；sqlite 模式读 events 表可翻到会话第一条） |
| GET | `/sessions/:id/runs` | 轮次明细（runs 读模型；一轮 = 用户发一次 prompt → 终态，开轮即落行。进行中的轮 `status=running`、`last_seq=-1`（未收口）、`ended_at` 为空；失败轮输入/统计回溯按此序） |
| POST | `/sessions/:id/prompt` | `{prompt, delivery?, attachments?, skill?}` → `{id, status, event_seq, queued?, input_id?}`（多轮）；`delivery` 见「busy input 队列」；`skill` 为内置技能 id（v4，见 `GET /composer/skills`），隐式前缀注入本轮、一次性消费，未知 id → `40001 invalid_skill`，忙时排队携带技能 → `40001 skill_queue_unsupported` |
| POST | `/sessions/:id/cancel` | 取消进行中的 run（幂等） |
| GET | `/sessions/:id/inputs` | 列出**待投递**的排队输入（§6 当前态：`input_id`/`text`/`delivery`/`target_run_id`/`generation`/`created_at`）。事件负责过程、这里负责当前态——WS 冷挂载只发 snapshot 不回放历史，没有这个入口队列会在刷新后凭空消失 |
| DELETE | `/sessions/:id/inputs/:input_id` | 撤下一条**尚未投递**的排队输入（§6）；已投递/已作废是台账行不可撤，不存在的 id 幂等 `deleted:false` |
| PATCH | `/sessions/:id/inputs/:input_id` | **行内编辑**（v4）：`{text?, attachments?}` 改一条尚未投递的队项（只改 `queued` 行），发 `session.input_updated` 事件让各端队列就地替换文本/附件（不出队、不改顺序）；不存在/已投递/已作废 → 40401（与撤下的幂等 no-op 不同：编辑要求队项确实还在） |
| POST | `/sessions/:id/inputs/:input_id/move` | **重排**（队列动作①；DOM insertBefore 语义，body `{before_id?}`，缺省 = 追加到末尾）。回**重排后的完整队列**；打不到 / 锚点不在队列里 → 40001，队项不在 → 40401（与撤下的幂等 no-op 刻意不同：假的"移动成功"会让界面真的去改本地顺序） |
| POST | `/sessions/:id/inputs/:input_id/send-now` | **立即发送**（动作②）：让它成为**下一轮**——排到队首；会话空闲时服务端立刻投递（`input_delivered`），忙时等当前轮收尾先投它 |
| POST | `/sessions/:id/inputs/:input_id/pop` | **取出编辑**（动作③）：取回正文并从队列作废，回 `{text}` 给前端回填输入框；发 `input_cancelled{reason:"user_edit"}` |
| GET | `/sessions/:id/permissions` | 挂起权限列表 |
| POST | `/sessions/:id/permission` | 权限决策 `{call_id, allowed, option_id?, always?}`（拒绝也透传 option_id/always） |
| GET | `/sessions/:id/permissions/memories` | 会话级权限记忆（始终允许+始终拒绝，`{title,kind,decision,since}`；旧 `/permissions/always` 为兼容别名） |
| POST | `/sessions/:id/permissions/memories/revoke` | 撤销权限记忆 `{title, kind}`；两者均空 = 一键撤销全部（旧 `/permissions/always/revoke` 为兼容别名） |
| GET | `/sessions/:id/permissions/always` | **兼容别名**，等价 `/sessions/:id/permissions/memories`（历史客户端/脚本在用，响应逐字段一致） |
| POST | `/sessions/:id/permissions/always/revoke` | **兼容别名**，等价 `/sessions/:id/permissions/memories/revoke`（请求体与行为一致） |
| GET | `/sessions/:id/elicitations` | 挂起结构化提问列表（表单 schema / URL） |
| POST | `/sessions/:id/elicitation` | 结构化提问决策 `{elicitation_id, action, content?}` |
| POST | `/sessions/:id/elicitations/ask` | **外部 source 结构化提问（answerer 桥接）**：`{mode, message, schema?, url?, timed?, runtime_session_id?}`；**阻塞**到用户决策/超时/取消，返回 `{action: accept\|decline\|cancel, content?}`。归属由路径会话显式给出（scope_source=wire，不走 D1 推断）；前端弹卡与决策复用 `/elicitations` + `/elicitation` 两条链路。`timed=true`（dsh `askTimed` 限时模式）时，**桥接连接断开**按「超时转 pending」处置：卡片保留可答（`session.elicitation_resolved` action=`pending`），用户随后作答经 `/elicitation` `late=true` 作为**新一轮**进入（超时不等于取消）；非 timed（缺省，阻塞式）断开即取消。**服务端强校验 `X-Mate-Answerer-Token`**（answerer 插件随每次请求携带；缺头/错值 → 401）。供 dsh `ask_user_question` 的 answerer 插件回填用户答案使用 |
| GET | `/library` | **统一能力清单（M2）**：skill/mcp/tool/expert 四类同构（v19 起 agent 不再是一种定义）（`kind`/`name`/`description`/`origin`/`scope`/`enabled`/`source_file`），`?kind=` 过滤、`?q=` 对 id/name/description 做大小写不敏感子串（未知 `kind` → 40000）。每条回带**逐 runtime 的 `applicability[]`**（`projected`/`untested`/`not_configured`/`not_applicable`/`disabled` + 人话 `reason`；expert 无逐 runtime 维度，给空数组）——**刻不给"整体可用吗"的合并结论**：对一个底座可用、对另一个不可用是有用信息，压成一个 bool 就没了。只读派生视图，**不拥有任何数据** |
| GET | `/experts` | **专家定义（P0）**列表：`id`/`name`/`description`/`avatar`/`model`/`skills`/`mcps`/`enabled`/`tags`/`has_instructions`（**不返回 instructions 正文**）；按 `updated_at` 倒序，含禁用（UI 标灰）。专家是**会话级能力预设**（推荐技能/MCP/模型 + P1 角色设定），不拥有技能、不新增 agent 身份层 |
| GET | `/experts/:id` | 专家详情：返回 `instructions` 全文（管理页/选择面板预览用）；不存在 → 40401 `expert_not_found` |
| POST | `/experts` | 新建专家定义（body = 专家定义，`id` 必填；`^[a-z0-9]+(-[a-z0-9]+)*$`、≤64）：过闸门（name 1–40、description ≤120、avatar 枚举、skills/mcps 去重升序每类 ≤50、instructions ≤4096 bytes 且拒绝 `</system-reminder>`/`<!--`/`-->`/`{{`/`}}`/NUL）→ 事务插行 + 变更审计 → 入注册表；id 已存在 → 40901 `expert_exists`，闸门拒绝 → 40001 `definition_invalid` 且零写入 |
| PUT | `/experts/:id` | 覆盖写专家定义：只改 body 里出现的键（未出现的保留、`null` 删键、未知键保留），路径 id 与 body id 不一致 → 40000；行 `revision` CAS 冲突 → 40901 `definition_changed` |
| DELETE | `/experts/:id` | 删专家定义（幂等：不存在 → `deleted:false` 且 code=0）；已建会话按创建时物化的技能/MCP 快照继续生效，不受影响 |
| PATCH | `/experts/:id` | `{enabled}` 启停专家：禁用只影响新选择，不影响已建会话 |
| GET | `/apps` | **应用定义（一期 A+Slot）**列表：`id`/`name`/`description`/`avatar`/`tags`/`members`/`entities`/`tool_domains`/`slots`/`page`/`api_version`/`enabled`/`has_page`（派生）/`source`/`revision`/`created_at`/`updated_at`（后两者 Unix 毫秒）；`source` 为应用来源（`self`=自建、含对话创建 / `market`=市场下载 / 装包，v41 落库）；按 `id` 升序，含禁用（UI 标灰）；**不返回 settings_schema 正文** |
| GET | `/apps/:id` | 应用详情：额外返回 `settings_schema` 全量；`source` 同列表（`self`=自建 / `market`=市场下载）；不存在 → 40401 `app_not_found` |
| POST | `/apps` | 新建应用定义（body = 应用定义，`id` 必填，`^[a-z0-9]+(-[a-z0-9]+)*$`、≤64）：先 `capability.ValidateApp`（name 1–40、description ≤120、tags ≤8×≤20、avatar 枚举、members 去重升序每类 ≤50、entities/slots/tool_domains 词法与上限、api_version caret、page 三字段齐备）→ 再**白名单在册判断**（`entities[].kind ∈ appslot.KnownEntities`、`slots[].impl_id ∈ appslot.KnownImpls`）→ 事务写 `apps` 行 + 重建 `app_members`（members 四类 + entity）+ 一条 `definition_revisions`(kind=app)；id 已存在 → 40901 `app_exists`，校验/白名单拒绝 → 40001 `app_invalid` 且零写入 |
| PUT | `/apps/:id` | 覆盖写应用定义：只改 body 里出现的键（未出现的保留、`null` 删键），路径 id 与 body id 不一致 → 40000；以 `definition_revisions.seq` 为当前版本做 CAS（`apps` 无 revision 列），不中 → 40901 `definition_changed`（绝不 last-write-wins）；不存在 → 40401 `app_not_found` |
| DELETE | `/apps/:id` | 删应用定义（幂等：不存在 → `deleted:false` 且 code=0）：解绑 `app_members`（按设计 §5.2，默认不动成员定义、只解除归属） |
| PATCH | `/apps/:id` | `{enabled}` 启停应用：禁用只影响新选择，不影响已建会话；不存在 → 40401 `app_not_found` |
| POST | `/apps/install` | **安装应用包（二期 C WP-C6）**：body `{dir, enable}`，`dir` = 本地来源包目录（含 `manifest.json`）。复用 `apppkg` 分发校验：清单合法性 + 完整性 sha256 + min core version 门槛，全过才原子拷到 `<MATE_APPS_DIR>/<id>/`（覆盖安装）；`enable=true` 时装完即启用（起隔离 host + apply slot）。未接运行期能力 → 40001 `app_runtime_unavailable`；包不合法/被篡改/版本不兼容 → 40001 `app_invalid` 且不落地 |
| DELETE | `/apps/:id/uninstall` | **卸载应用（WP-C6）**：先停运行期（disposeSlot RPC 精确撤销 + 停 host，回退「无此 app」默认态）→ 清本地包目录 → 删定义行；幂等（不存在 → `uninstalled:false`）。返回 `{uninstalled, pack_removed, definition_deleted}` |
| GET | `/market` | **应用市场清单（M-A3）**：`data` 为顶层数组，每条 = 清单条目 + 派生装态 `installed`（本地有包或定义）/`enabled`（定义开关）/`updatable`（本地包版本 < 清单 version）/`installable`（`source.type==dir` 且来源根下目录存在且非「已装同版本」）/`state`（已装时 off/pending/active/failed/disposed）。清单来源：内置 `market.json`（go:embed）/远端 registry（`MATE_APP_MARKET`）/磁盘缓存三级兜底；`source.dir` 在可解析时回填为安装器可直接透传的**绝对路径** |
| POST | `/market/refresh` | 强制刷新市场清单缓存（清空内存缓存，下次 `GET /market` 强制重拉） |
| POST | `/apps/:id/upgrade` | **更新已装应用（M-A3）**：本期更新走 C8 版本治理链路**未接线**，明确返回 40501 `app_not_supported`「更新能力接线中」，**不假装成功**（应用包更新需签名/版本治理，属 C8） |
| GET | `/skills` · `/mcps` · `/tools` | 能力数据面目录（Skill/MCP/Tool；MCP 含探测状态与 `tool_count`、`enabled`/`scope`；skill 含 `enabled` 与正文扫描 `warnings`） |
| POST | `/skills` | 新建 skill 定义（body = skill 定义，`id` 必填）：**先过注册闸门**（正文扫描、`runtimes` 去重、**id 词法**）→ 通过才提交给载体（memory = 原子替换文件、sqlite = 事务插行 + 变更审计，未知键一律保留）→ 最后才更新内存；id 已存在 → 40901 `skill_exists`，闸门拒绝 → 40001 `definition_invalid` 且**零写入**（草稿从未提交，无需回滚）。**id 词法是底座契约，不是平台审美**：须匹配 `^[a-z0-9]+(-[a-z0-9]+)*$` 且 ≤64 位——dsh 的 `dsh-skill` 对 skill 文件 frontmatter 的 `name` 强制这一条，不合规只打一条 warn 就**静默丢弃整条**，而平台物化正是把 id 原样写成 `name`，于是放行不合规 id 等于造出「物化体检报 `in_sync`、底座永远读不到」的 skill（2026-10-08 实测确认）；物化层同规则再拦一道（导出 API 被直接调用也不放行）。MCP/tool 的 id 规则不受此波及 |
| POST | `/skills/import` | **技能包导入（D1）**：`multipart/form-data`，字段 `file=<id>.zip`（≤20 MiB）。包内**只允许 `SKILL.md`**（根目录或唯一一层包装目录；`__MACOSX/`、`.DS_Store` 这类打包元数据忽略，额外的脚本/资源直接拒绝——「导入成功但附件没落盘」是静默丢失）。`SKILL.md` 必须有 YAML front matter：`name`（规范化后就是定义 id，须能折成 `^[a-z0-9]+(-[a-z0-9]+)*$`）、`description`（必填），可选 `title` 作展示名。解析/校验失败 → 40001 `definition_invalid` 且**零字节落盘**；同名 id → 40901 `skill_exists`（**不覆盖**）；正文 critical → 40001 且零写入。**写面不新开一条路**：解析结果折成与 `POST /skills` 相同的请求体后调用同一条 `WriteSkill`，所以「上传」与「手填」经过的闸门逐条相同 |
| PUT | `/skills/:id` | 覆盖写 skill 定义：**只改 body 里出现过的键**（未出现的保留原值；`null` 删键、`""`/`[]` 清空），路径 id 与 body id 不一致 → 40000；提交前用载体的乐观锁复核（文件 = mtime+size、DB = 行 `revision`），读写窗口内被外部改动 → 40901 `definition_changed`（**绝不 last-write-wins**） |
| DELETE | `/skills/:id` | 删定义（文件 / DB 行）+ 撤回已物化的 `.mate-managed` 副本 + 摘内存条目；幂等（本来就不存在 → `deleted:false` 且 code=0，不报 404） |
| PATCH | `/skills/:id` | `{enabled}` 启停一个 skill（§13.4 S4）：写回定义载体的 `enabled`（**只改这一个键、未知键原样保留**）+ 更新注册表 + 立刻重校投影；禁用 = overlay，已物化副本被**撤回**，随时可恢复。载体里没有它（代码注入/内存注册）→ 40001 `skill_not_persistable`，**不顺手新建** |
| GET | `/skills/:id/materialization` | 物化体检（§13.4 S2，只读）：每处物化目标的期望摘要 `hash` vs 实测 `actual_hash` + `status`（`in_sync`/`missing`/`tampered`/`stale`/`disabled`/`user_owned`/`not_applicable`/`untested`/`not_configured`）+ `owners`（该落点上共同投放的 runtime 集合）；回答「agent 实际读到的与注册表一致吗」 |
| POST | `/mcps` | 新建 MCP server 定义：闸门比 skill 多（transport 枚举、stdio **绝对路径**、`store:`/`env:` 引用词法、明文与密钥键不重复），写面**一个都不新开也不放行**——定义怎么进注册表就走同一条路；id 已存在 → 40901 `mcp_exists`；载体同 skill 写面（memory 文件 / sqlite `mcp_servers` 行 + 变更审计） |
| PUT | `/mcps/:id` | 覆盖写 MCP server 定义（键的出现与否有语义，同 skill 写面）；响应回带 `unresolved_secret_keys`（绑了引用但当前解析不出来的键，只给键名） |
| DELETE | `/mcps/:id` | 删定义（文件 / DB 行）+ 摘内存条目 + **一并摘除归属该 server 的 `mcp` 来源工具**（留着等于让用户看见一个调不通的能力）；幂等 |
| POST | `/mcps/:id/probe` | 对 MCP server 做一次 live `tools/list` 探测并写 Tool 目录（显式触发；stdio/http/streamable_http/sse 四种传输均支持） |
| POST | `/mcps/:id/tools/:tool/call` | 调用 MCP 工具（`tools/call`；body `{arguments,timeout_ms}`；工具级失败 → `is_error=true` 且 code=0；超时 40901） |
| GET | `/mcps/:id/resources` | 实时拉取 MCP 资源列表（`resources/list` + `resources/templates/list`；模板不支持 → `templates_error` 降级） |
| POST | `/mcps/:id/resources/read` | 读取 MCP 资源（`resources/read`；body `{uri}`；text/blob 原样保真） |
| GET | `/mcps/:id/prompts` | 实时拉取 MCP 提示词模板（`prompts/list`） |
| POST | `/mcps/:id/prompts/:name/get` | 取用 MCP 提示词（`prompts/get`；body `{arguments}`，值为字符串） |
| GET | `/automations` | **定时任务（M4）** 列表：定义 + 运行态（`state`/`next_due_at`/`last_fired_at`/`last_status`/`last_reason`/`last_reason_kind`/`consecutive_failures`/`recent_runs`）。**装载失败的定义也在列表里**（带 `error` 字段）——写得不对却「列表里没有、调度也不跑、日志一行 WARN」是最难排查的一类投诉 |
| POST | `/automations` | 新建定义（body = 定义，`id` 必填 = 定义键）：先按**合并结果**过闸门（schedule 语义、`timezone` 必须 IANA、`target.cwd` 必须非空绝对路径；**带 `target.runtime_hint` → 40001 `runtime_selection_removed`**，老定义里的残留键在保存时自愈清除）→ 通过才落载体（memory 模式原子替换、sqlite 模式行 CAS，未知键一律保留）→ 失败**回滚内存**；id 已存在 → 40901 `automation_exists`（**绝不覆盖**），闸门拒绝 → 40001 `definition_invalid` 且**零字节落盘** |
<!-- schedule 六态（v36 引入受限 rrule，不引 cron）：`schedule.kind ∈ once \| daily \| weekly \| interval \| monthly \| rrule`。前五态用 `schedule.timezone`（IANA 名）；`rrule` 用 `schedule.rrule`（两行文本 `DTSTART…\nRRULE:…`，RFC 5545 受限子集），**时区由 DTSTART 自带**（UTC `Z` 或 `TZID=IANA`），**不读也不要求 `schedule.timezone`**。`rrule` 存权威表达式**不规范化**，拒绝而非降级（不支持宏/`SECONDLY`/多 `BYSECOND`/`COUNT` 与 `UNTIL` 共存/单周期候选 >10080 等一律 `40001 invalid_schedule`，消息可读）；不存在的本地 DTSTART 拒绝、后续不存在的本地发生时点跳过、重叠取较早。认领/追赶沿用既有 `NextDue`→`LatestAtOrBefore` 路径（`rrule` 走"只补最近一次"），不发新事件类型。 -->

| POST | `/automations/preview` | 只算未来若干次到期时刻（body `{schedule,count?,from?}`，count 默认 5 上限 20；**不落盘、不建会话、也不要求定义已存在**）：每项回带 UTC `at` 与本地墙钟 `wall` + `timezone`，跨 DST 空档时给 `warnings`——时区 + DST + weekly 星期集合叠在一起，没人能从 JSON 里读出「到底什么时候跑」 || GET | `/automations/:id` | 取一条（不存在 → 40401 `automation_not_found`） |
| PUT | `/automations/:id` | 覆盖写：**只改 body 里出现过的键**（未出现的保留原值；`null` 删键），路径 id 与 body id 不一致 → 40000；**平台独占的运行态键**（`last_fired_at`/`last_status`/`last_reason`/`last_session_id`/`consecutive_failures`/`recent_runs`）不可写 → 40001（`last_fired_at` 就是认领判据，能写它就能让同一次触发重放或永久卡住不跑）；读写窗口内被外部改动 → 40901 `definition_changed`（memory 模式判 mtime、sqlite 模式判行 `revision`） |
| DELETE | `/automations/:id` | 删定义 + 摘内存条目；幂等（本来就不存在 → `deleted:false` 且 code=0） |
| GET | `/automations/:id/runs` | 运行史（最新 20 条；读载体的 `recent_runs`，有界）：**含 `overlap`/`duplicate`/`skipped`**——「没跑成的那些」才是最需要被看见的；`skipped` 记录可带 `reason_kind`（如 `target_session_missing`，机器读的失效原因码）；定义装载失败 → 40001（不假装没有运行史） |
| POST | `/automations/:id/run` | 手动触发一次：走**同一认领判据**（上一次还在跑 → `overlap`，手动不等于可以并发），但**不推进认领游标**（手动试跑不该静默吃掉当晚的排期）；失败既报错又留痕（`skipped` + `needs_attention`） |
| GET | `/usage/summary` | 用量统计（`?from=&to=&group_by=day&tz_offset=`；区间半开；日期参数与 day 桶按 `tz_offset` 的**客户端时区**解释，缺省 0/UTC；无 store → 40501）。**v19 去掉了「按 agent 选择」这个维度（`group_by=agent` 与那个查询键）**（会话身份只剩 `runtime_id`，而底座构建期固定 ⇒ 按它聚合只有一桶），带上它 → 40001 `agent_selection_removed`（显式拒绝；静默忽略会让调用方以为筛过了）。**v37 新增 `llm` 块**：内部模型调用流水（`llm_calls`）的 token 明细汇总——`{calls, tokens{input/output/cached_read/cached_write/thought/total}, by_purpose[]}`，**流水口径**（可跨调用相加、**含旁路无会话调用**）；与既有 `totals/buckets` 的**会话快照**口径（`context_*`）分开，二者不可混用 |
| GET | `/ws/events` | 实时事件通道（见下） |

### 业务错误码

信封 `code` 字段的**唯一事实源是 `backend/internal/core/code`**（本表由 `TestReadmeCodesMatchRegistry`、
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
| `40401` | 资源不存在（会话/runtime/provider/MCP server） |
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
| 专家不存在（`GET/PUT/DELETE /experts/:id`、建会话引用未知 expert） | `40401` | `expert_not_found` | 刷新专家列表后重选 |
| 专家已被禁用（建会话引用） | `40001` | `expert_disabled` | 到「扩展 · 专家」启用，或换一个 |
| id 已被占用 / 读写窗口内被外部改动 | `40901` | `*_exists` · `definition_changed` | 换 id 或刷新后重试（**平台绝不 last-write-wins**） |
| 未配置能力目录（**仅 memory 模式**） | `40001` | `capability_dir_unset` | 设 `MATE_CAPABILITIES`；不静默"内存生效"（sqlite 模式的定义在 DB 里，写面不需要目录） |
| **应用定义**（`/apps`）id 已被占用 | `40901` | `app_exists` | 换 id，或改它用 `PUT /apps/:id`（**绝不覆盖**） |
| **应用定义**（`/apps`）定义被拒（词法/上限不过，或 `entities[].kind`/`slots[].impl_id` 不在白名单在册） | `40001` | `app_invalid` | 改定义内容重试；**被拒的定义零写入** |
| **应用定义**（`GET/PUT/PATCH/DELETE /apps/:id`）目标不存在 | `40401` | `app_not_found` | 刷新应用列表后重选 |
| **应用包安装**（`POST /apps/install`）未接运行期能力 | `40001` | `app_runtime_unavailable` | 本进程未启用运行期应用能力（需 C 期装配）；纯 A+Slot 部署不支持装包 |
| **应用包卸载**（`DELETE /apps/:id/uninstall`）清理包目录失败 | `50201` | `app_uninstall_failed` | 检查本地应用目录权限/占用后重试（先停运行期已尽量保证） |

**写面的载体按持久层模式分叉（M6-3）**：`MATE_STORE=sqlite` 时能力定义的事实源是 DB——
`skills`/`mcp_servers` 两张表**就是定义本身**（`id` + 完整 `body` + `revision`），写面走
**单事务：行 CAS + 同事务追一条变更审计**（`definition_revisions`：kind / definition_id / action /
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

**agent 域已随 v19 整体删除**：agent 不再是"一种定义"，所以它没有搬家路径、也没有
`mate agent import|export`、`meta.imported_agents`/`meta.default_agent` 与 `<AgentDir>/*.json` 的装载面。
撤掉的依据是"这个域没有独立身份"（`docs/decisions.md`「决策 v19」）：底座由**构建期固定**，
它的每个字段要么是派生事实（`is_default`/`session_count`/`default_model`）、要么是平台事实
（`permission_policy`/`provider`），撤掉写面之后没有剩余字段可写。会话与能力的归属因此只剩
`runtime_id` 一个身份列；per-agent 的 `mcps` 白名单随定义载体一起消失，能力的表达位收回到
**会话级 `capabilities`**（`POST /sessions`，逐 kind 三态）。被撤掉的标识符由 `TestNoAgentVocabulary`
钉住（白名单只放历史迁移 DDL、停在旧版本的测试夹具、ACP 原文与显式拒绝面）。

**定时任务定义走同一条搬家路径（M6-7）**：装配期在 sqlite 模式下自动导入一次
`<AutomationDir>/*.json`，指纹进 `meta.imported_automations`（**三个域各自记指纹**），
`mate automation import|export` 是对应的运维口子。它与前两个域**唯一的结构性差别**是"一个文件
装了两样东西"：导入按 `automation.ReservedKeys` 把文件切成**定义正文 + 运行态列**并**同一事务**
写进去（不出现"定义进来了、运行史没进来"的半截状态），导出反向合成同一个 `<id>.json`
（所以"导出 → 空库导入 → 再导出"逐字节相同）。时间归一成 UTC + RFC3339Nano——它是认领 CAS 的
比较值，两种写法会让同一个 occurrence 比对不上；`recent_runs` 进列时按上限截断（文件可以被人
手工塞进几百条，"有界"是列的契约）；保留键类型写坏（如 `last_fired_at: 123`）**逐条报错**而不是
静默丢弃。

**provider 域的事实源与搬家路径（v11 列化 → v19 目录改名 → v20 密钥进库）**：装配期在 sqlite 模式下
自动导入一次 `<ProviderDir>/providers.json`（整个 provider 域只有**一个文件**，所以幂等判据取
**文件内容指纹**而不是"名字+大小"——这条域最常见的编辑就是"把 `type` 改一下"），指纹进
`meta.imported_providers`（**第四个域，各自记指纹**），`mate provider import|export` 是对应的运维口子。
导入**逐条过同一道闸门**（id 要能当引用路径段用），迁移前**一次性**备份成
`<ProviderDir>/providers.json.bak-v11`（已存在则不覆盖），导入**绝不覆盖**已有行；落库物是**列载荷**
（`name`/`protocol`/`base_url`/`note` 进列，未知键不再有寄存处——键名可在 `meta.migration_notes_v13` 查到）。
它与前三个域有两处结构性差别，都是 provider 侧的事实：① `providers.json` 降级为**导入/导出载体**
（写面一个字节都不回写它）；② **密钥的落点在库**——v13 之前是"文件里明文"，v13–v19 是"文件里只留引用
`key_ref: "vault:providers/<id>"`、密文落 `vault/providers/<id>.enc`"，**v20 起是 `providers.key` 一列**
（明文进库，决策 v20）。于是 v20 的迁移是一个**不可逆动作**（关闭回退窗口）：把 `vault/providers/*.enc`
解密后写进 `providers.key` 与 `<MATE_HOME>/credentials.json` 镜像。搬运**只读不删**：`vault/` 与 `vault.key` 原样留着，`mate doctor` 报「已迁移，可删」——删除是不可逆落笔，由用户在确认 `has_key` 都对得上之后自己做；回退靠 `backups/` 里那份迁移前快照（"停在新版本 → `mate provider export`"的步骤见
[部署文档 §6](deployment.md)）。目录改名（v19）是另一件更轻的事：新目录没有 `providers.json`
而旧 `agents/` 目录里的 `providers.json` 存在时**导入一次** + WARN 指向新路径，**不删旧文件**。
导入对账（`meta.providers_import_mismatch`）**只告警不阻断**启动，坏行逐条报出。

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

WS 帧契约（详见设计文档 §4.5）：客户端 `{action: attach|detach|ping}`；**每次连接的第一条帧必须是通道级 `attach`**（`{action:"attach", token:"…"}`，无 `agg_id`）——它交付 stream 面凭据，服务端 5s 内未收到即关闭（1008），令牌因此不进 URL；其后的 `attach` 才带 `agg_id` 与 `since_seq`（**省略或负数** → 从该会话第 0 条起全量回放 + live；`>= 0` → 从该位次之后增量续传 + live；只有请求起点早于该聚合的缓冲淘汰水位时才回 `snapshot`）。服务端 `{type: snapshot|event|detached|pong}`，event 载荷 `{id?, type, seq, agg_id, version, payload}`（`id` = 事件行身份 `evt_<26 位 ULID>`，**只有 durable 事件才有**——`automation.run` 这类只走实时通道的事件不带，于是「帧上有 `id` ⟺ 它查得到」成立；`seq` 是**会话内**位次（0 起）与唯一游标），type 域见 `backend/internal/manager/events.go`（`session.message/thought/tool_call/tool_update/user_message`、`session.permission_request/permission_resolved`、`session.elicitation_request/elicitation_resolved`、`session.input_queued/input_delivered/input_cancelled`（§6 排队输入）、`session.config_options`（运行时会话配置项全量更新，如模型）、`session.commands`（底座自报的会话可用斜杠命令**全量**列表——**发现面，不是平台声明的能力**：当前内置的 `dsh` 不通告（其 ACP 面不含命令，故永不产生该事件），底座是否通告由各 adapter 自己的行为决定；空数组表示"通告了当前没有命令"，执行只是把命令行当普通提示词发出去，平台不投影也不写面）、`run.started`（一轮开始，载荷带该轮身份 `run_id`）、`run.ended/error`、`run.cancelled`、`automation.run`（一次 automation 触发的结果，含没产生会话的 duplicate/overlap/skipped——**持久运行史在定义文件的 `recent_runs`，这条事件只走实时通道**）等）。会话动作的 POST 响应返回当前 `event_seq` 水位，作为 attach 续传起点。**文本增量在服务端攒批**（③ 合并，25ms / 4KB / 64 条）：一帧 `session.message`/`thought` 可能覆盖多个 delta，客户端按 `msg_id` 追加同一条消息的渲染逻辑不变；工具卡/权限卡/终态等事件不合并，顺序不被文本增量越过。


