# 模型选择功能完善设计（2026-09-09）

> 参考实现：openwork（`apps/app` 的模型选择器与 ModelOption/ModelRef 结构）、
> 平台既有结论：`docs/design-and-roadmap.md` §5.3（三段式 model catalog）与「决策 v6③」、
> `docs/agent-runtime-refactor-design-20260908.md` §5.5/§6.2（模型面与投影 Spec）、
> `docs/deepseekharness-acp-adaptation.md`（dsh `session/set_config_option` 改 model）。
>
> 本文档为**方案存档**，尚未改动任何代码；落地按 §10 分阶段推进。

---

## 1. 现状与差距（以源码核实为准）

**当前实际能力**

- Provider 档案 `providers.json` 只存 `{id, type, model, key}`；`model` 在
  `frontend/apps/panel/src/pages/AgentsPage.tsx` 注释即「仅展示元数据」——前端展示、后端透传，
  **不驱动任何运行时行为**。
- 模型无会话级落点：`handler.ConversationCreateReq` 仅 `agent_id/prompt/cwd`；
  `manager.ConversationRec` 无 model 字段；`store` `conversations` 表无 model 列。
- Runtime 投影 `session_runtime.go` 的 `ApplyModel` **一律返回 `unsupported_model`**
  （`Spec.ModelConfigOptionID` 为空 → 模型走 env/provider 配置）。
- 连接池键 `manager/orchestrator.go:connKey(runtimeID, nativeEnv)` **不含模型**。
- 面板无模型选择器——设计文档「决策 v6③」明示「模型=底座自身默认，面板不设模型选择器」
  （当时实测 opencode 忽略客户端模型选择：issue #13644）。

**参考 openwork 的能力**

- 全量模型 catalog：按 provider 分组、搜索、收藏、推荐星标、`effort/behavior` 二级选择。
- `ModelRef{providerID, modelID}`（会话级模型身份）、`ModelOption{title, description, behaviorTitle/
  Label/Value/Options, isFree, isRecommended, source}`（目录条目）。

**本方案目标（已确认三点决策）**

| 决策 | 取值 |
|---|---|
| 选择粒度 | **会话级**（每次对话可选） |
| 目录来源 | **三段式降级**：live → 磁盘缓存（TTL 24h）→ go:embed 快照 |
| 注入时机与覆盖 | opencode 走 `OPENCODE_MODEL` env、dsh 走 provider 配置 + `session/set_config_option`，**仅拨号时生效**（不做对话中热切） |

---

## 2. 目标与非目标

**目标**

1. 提供可选的模型目录（三段式），面板据此展示模型选择器。
2. 用户按会话选择 `(provider, model)`，会话记录并持久化。
3. 模型在选择处真实生效：opencode 经启动 env、dsh 经会话 config option。
4. 目录失败可降级（绝不阻塞对话）。

**非目标（明确不做）**

- 对话中的模型热切换（opencode 忽略 ACP 客户端模型选择，issue #13644）。
- 不做 openwork 的 org/den/云端供应商权限层（ark-mate 单操作员、无云端模型市场）。
- 不做模型的运行时训练/在线推理栈。

---

## 3. 关键约束与架构结论

| 约束 | 对架构的影响 |
|---|---|
| opencode 忽略 ACP 客户端模型选择（issue #13644） | opencode 模型只能经**启动 env** 注入；改模型 = 换/重建连接，不热切 |
| dsh 支持 `session/set_config_option` 改 model/reasoning_effort（`deepseekharness-acp-adaptation.md:41`；acp-go-sdk 已有 `SetSessionConfigOptionRequest/Response`） | dsh 可经 ACP 在会话层应用模型 |
| `connKey(runtimeID, nativeEnv)` 不含模型 | 会话级模型必须并入连接键，否则不同模型会话会误共享同一子进程 |
| 同 runtime 同配置共享暖进程 | 模型不同 → 独立连接；模型相同 → 可共享（与权限 env 同策略） |

> **核心结论**：模型选择 = **拨号参数**（spawn 期），不是会话内热切换。
> 会话记录 `(provider, model)`；`acquire` 把 `(runtimeID, nativeEnv, modelEnv)` 全部纳入连接键；
> opencode 经模型 env 拨号，dsh 在 `NewSession` 后、首轮 prompt 前调用 `SetConfigOption(model)`
> （必要时连同 effort）。两者向平台归一为「模型是否生效」。

---

## 4. 数据模型与三段式 catalog

### 4.1 上游 provider 类型（ProviderType）

与 `manager/providers.go:envKeyOf` 保持一致：`openai | anthropic | deepseek | openrouter | groq`；
env 注入名 = `strings.ToUpper(type) + "_API_KEY"`（dsh 特例见 §6）。

### 4.2 目录条目归一化（ModelOption）

```go
type Model struct {
    ID          string   `json:"id"`
    Title       string   `json:"title"`
    Description string   `json:"description,omitempty"`
    Behavior    []string `json:"behavior,omitempty"`   // effort/reasoning 可选值（dsh 用）
    Recommended bool     `json:"recommended,omitempty"`
    IsFree      bool     `json:"is_free,omitempty"`
}
```

### 4.3 三段式获取链

```
Resolve(typ, apiKey)
  → liveFetch(typ, apiKey)                 成功 → 写 <cache>/models/<typ>.json(TTL 24h) → (models, live, nil)
  → 磁盘缓存未过期                          命中 → (models, cache, nil)
  → go:embed 快照 (catalog/snapshots/<typ>.json) → (models, snapshot, nil)
```
- live 段仅当 `apiKey` 非空才发起，否则直接走快照。
- 全链失败返回空切片 + 明确错误（上层按「无模型」处理，不抛 5xx）。

---

## 5. REST 契约（新增路由；须同步 README + `TestReadmeRoutesMatchRouter`）

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/v1/forge/models?provider=:type` | 平台侧模型目录（三段式；响应带 `source`） |
| GET | `/v1/forge/providers/:id/models` | 某 provider 档案的可选模型（live 优先，兜底快照） |
| PATCH | `/v1/forge/providers/:id` | `model` 受目录约束（不在目录 → 40001 友好文案） |
| POST | `/v1/forge/conversations` | 新增 `model`（会话级） |
| GET/PATCH | `/v1/forge/conversations/:id` | 详情/PATCH 增 `model` |

> 每新增/改名路由都需同步 `README.md`「控制面接口摘要」反引号 `/v1/forge` 路径，
> 保持 `internal/server/router/router_test.go:TestReadmeRoutesMatchRouter` 绿。

---

## 6. 会话级模型 → 生效链路（Phase B 概览）

```
用户选 (provider, model) → ConversationRec.{provider, model} 落库/内存
  → StartConversation(agent, model)
      ① acquire：modelEnv 并入 connKey；opencode 经 <modelEnv> 拨号
      ② dsh：NewSession 后调用 SetConfigOption(model[, effort]) 再开始首轮
      ③ 其他 runtime（echo）：明确 unsupported（「此底座不支持选模型」）
  → 模型只影响拨号/会话参数；不同模型会话各自独立连接
```

**spawn env 映射（Manager 侧）**：`providerModelEnvFor(type, model)`
- opencode → `OPENCODE_MODEL=<model>`（配合用户 `opencode.json` 的 `"model":"{env:OPENCODE_MODEL}"`）。
- dsh → 经 `SetConfigOption`（同时保留 provider 配置作基线）；若需进程级，`DEEPSEEK_MODEL`/`DEEPSEEK_ACP_MODEL`。
- echo → 无（返回明确不支持）。

---

## 7. 前端交互（参考 openwork，不含云权限层）

| 面 | 改动 |
|---|---|
| `packages/types` | `ModelOption`、`ModelRef{providerID,modelID}`；`Agent/Conversation` 增 `model`；`Provider` 增 `models` |
| `packages/api` | `listModels`、`listProviderModels`；conversation create/patch 带 `model` |
| `apps/panel/src/console` | 新增 `modelPicker.ts`（搜索、按 provider 分组、推荐星标、收藏、effort 二级；dsh 启用 effort，opencode 隐藏） |
| `components` | 新增 `ModelSelect.tsx`（对照 openwork `model-select.tsx`）；`StartPage.tsx`/`ChatPage.tsx` 输入卡挂模型 pill；`AgentsPage.tsx` provider 表单 `model` 由 `Input` 改 `Select`（目录约束） |
| 样式 | 交互态 CSS 收口 `console.css.ts`；颜色沿用 `theme.ts` tokens，不硬编码色值 |

---

## 8. 边界与错误处理

- catalog live 失败：静默回退缓存 → 快照，响应带 `source` 标注（live/cache/snapshot）。
- 选中模型不在目录：40001 + 友好文案；目录为空时允许「手动输入（不校验）」兜底。
- provider 无 key：模型可展示但禁用（无法 live 校准；选取不触发注入）。
- opencode：模型改动标记关联会话 **stale**，前端提示「重启/重连后生效」。
- 会话 resume：`ensureConversation` 重建时携带该会话 model，保障恢复后模型一致。

---

## 9. 一致性守卫

- 路由新增 → 同步 README + `TestReadmeRoutesMatchRouter` 保持绿。
- 事件类型 / 存储变更 → 同步 `manager/events.go` 与 `store/schema.go` 迁移（v4 幂等，`forge migrate` 可导入）。
- 前端 `typecheck` + `go test ./...` 保持通过；相关单测走真实装配路径（`manager.NewService` + `router.New`）。

---

## 10. 分阶段落地

| 阶段 | 内容 | 验收 |
|---|---|---|
| **A** | `internal/modelcatalog`（go:embed 快照 + live + TTL 缓存）+ `GET /models`、`GET /providers/:id/models` + README + 测试 | 目录可查、带 source 降级；UI 尚无感 |
| **B** | store 迁移 v4、`ConversationRec`/req 增 model、`connKey` 并入模型、opencode model env、dsh `SetConfigOption`、`ApplyModel` 落地 | 会话级模型真实生效 |
| **C** | 前端 `ModelSelect` + `StartPage/ChatPage` 输入卡 + provider 表单 catalog 化 + types/api | 面板可选模型 |
| **D** | stale 提示、effort/behavior（dsh 先做）、边界文案、docs/README 终稿 | 收尾打磨 |

---

## 11. Phase A 细化：`internal/modelcatalog` 包

> 目标：提供三段式模型目录能力，供 `/models` 与 `/providers/:id/models` 消费。
> 本包**不关心运行时注入**（那属 Phase B），只负责「拿来一份可选的模型列表」。

### 11.1 包与文件布局

```
backend/internal/modelcatalog/
├── catalog.go        // ProviderType / Model / Source / Resolve / liveFetch / cachePath / snapshotModels
├── catalog/
│   └── snapshots/    // go:embed 快照：openai.json / anthropic.json / deepseek.json / openrouter.json / groq.json
│       ...
├── live.go           // 各 provider type 的 /models 拉取与解析（openai 兼容协议）
├── cache.go          // 磁盘缓存读写 + TTL
└── modelcatalog_test.go
```

### 11.2 核心类型

```go
package modelcatalog

// Source 标定目录数据来源（排障与降级提示用）。
type Source string

const (
    SourceLive     Source = "live"
    SourceCache    Source = "cache"
    SourceSnapshot Source = "snapshot"
)

// ProviderType 上游 provider 类型（与 manager/providers.go:envKeyOf 保持一致）。
type ProviderType string

const (
    ProviderOpenAI     ProviderType = "openai"
    ProviderAnthropic  ProviderType = "anthropic"
    ProviderDeepSeek   ProviderType = "deepseek"
    ProviderOpenRouter ProviderType = "openrouter"
    ProviderGroq       ProviderType = "groq"
)

// Model 目录中的一个模型条目（归一化表示）。
type Model struct {
    ID          string   `json:"id"`
    Title       string   `json:"title"`                // 展示名（上游 name；缺省 = ID）
    Description string   `json:"description,omitempty"` // 归属 provider 展示
    Behavior    []string `json:"behavior,omitempty"`   // effort/reasoning 可选值（dsh 用）
    Recommended bool     `json:"recommended,omitempty"` // 推荐（星标；go:embed 快照标注）
    IsFree      bool     `json:"is_free,omitempty"`
}

// Valid 校验 ProviderType 是否被支持。
func Valid(typ ProviderType) bool

// Resolve 三段式取模型目录。
//   live 成功 → 写缓存(TTL 24h) → (models, SourceLive, nil)
//   未过期缓存 → (models, SourceCache, nil)
//   否则 go:embed 快照 → (models, SourceSnapshot, nil)
// apiKey 为空：跳过 live，直接快照。
// cacheDir：测试可注入临时目录；空 = DefaultCacheDir()（$FORGE_MODEL_CACHE_DIR，否则 ~/.forge/cache/models）。
func Resolve(ctx context.Context, typ ProviderType, apiKey, cacheDir string) ([]Model, Source, error)
```

> 实现注（2026-09-09）：`Resolve` 实际签名比上文多了 `cacheDir`（默认 `DefaultCacheDir()`，便于测试
> 指向临时目录，避免污染宿主 `~/.forge`）；另暴露 `SupportedTypes() []ProviderType` 与
> `ErrUnknownProvider`。`manager` 层新增 `Service.ListModels`/`ListProviderModels`，
> `restErr` 把 `provider_not_found` 映射到 40401。

### 11.3 内部函数

```go
// liveFetch 拉上游 /models 并归一；仅当 typ 被支持且 apiKey 非空可调用。
func liveFetch(ctx context.Context, typ ProviderType, apiKey string) ([]Model, error)

// snapshotModels 读 go:embed 快照 catalog/snapshots/<typ>.json。
func snapshotModels(typ ProviderType) ([]Model, error)

// cachePath 磁盘缓存路径 ~/.forge/cache/models/<typ>.json。
func cachePath(typ ProviderType) (string, error)
```

### 11.4 go:embed 快照格式

`catalog/snapshots/<typ>.json`：

```json
{
  "source": "snapshot",
  "models": [
    { "id": "deepseek-v4-flash", "title": "DeepSeek V4 Flash", "recommended": true },
    { "id": "deepseek-v4", "title": "DeepSeek V4", "behavior": ["low", "medium", "high"] }
  ]
}
```

- 快照为**编译期静态**（`//go:embed`），随 `-tags release` 进二进制。
- 后续可用脚本从上游刷新后提交，但运行期永远以 live → cache → snapshot 为优先级。

### 11.5 上游 live 端点（openai 兼容协议）

| ProviderType | 端点 |
|---|---|
| openai | `GET https://api.openai.com/v1/models` |
| anthropic | `GET https://api.anthropic.com/v1/models`（前缀 `anthropic-version` 头） |
| deepseek | `GET https://api.deepseek.com/models` |
| openrouter | `GET https://openrouter.ai/api/v1/models` |
| groq | `GET https://api.groq.com/openai/v1/models` |

- 鉴权：`Authorization: Bearer <apiKey>`；读取失败/超时/4xx → 不抛，交由降级链。
- 归一化：取 `data[]`（openai/deepseek/groq 的 `{id}` 或 Anthropic/OpenRouter 的模型对象），
  `Title` 取上游 `name`，缺省回落 `ID`；`Behavior/Recommended/IsFree` 由上游字段或快照标注补全。

### 11.6 进程内 memo（可选，降低重复 live 拉取）

`Resolve` 首层可加进程内 `sync.Map`/LRU 按 `(typ, apiKey)` 短时缓存（几十秒），避免并发请求
反复 live。磁盘缓存是跨进程持久层，进程内 memo 是请求级加速层，二者不冲突。

### 11.7 消费方与测试

- 消费方：`handler` 新增 `ModelsHandler.List`、`ProviderHandler.Models`（见 §5）。
- 测试：三类用例——live 成功回落（mock 上游）、live 失败回缓存、双失败回快照；
  校验返回 `Source` 与排序稳定性；`Valid(typ)` 边界。
- 一致性：`/models` 与 `/providers/:id/models` 路由新增后同步 README（§9）。

### 11.8 Phase A 新增/修改文件清单

| 文件 | 动作 |
|---|---|
| `backend/internal/modelcatalog/catalog.go` | 新增（类型 + Resolve + 降级链） |
| `backend/internal/modelcatalog/live.go` | 新增（各 provider live 拉取/归一化） |
| `backend/internal/modelcatalog/cache.go` | 新增（磁盘缓存 + TTL） |
| `backend/internal/modelcatalog/catalog/snapshots/*.json` | 新增（go:embed 快照） |
| `backend/internal/server/handler/models.go` | 新增（`ModelsHandler`） |
| `backend/internal/server/handler/provider.go` | 修改（增 `Models` 方法） |
| `backend/internal/server/router/router.go` | 修改（注册 `/models`、`/providers/:id/models`） |
| `backend/internal/manager/service.go` + `service_test.go` | 修改（实现 `ListModels`/`ListProviderModels`；仓库级目录 API） |
| `README.md` | 修改（接口摘要同步 `TestReadmeRoutesMatchRouter`） |

---

## 12. Phase B 细化：会话级模型生效

> 目标：把「用户选的 (provider, model)」落到真实运行时——会话记录模型、拨号时注入、dsh 经 ACP
> 会话配置选项生效、opencode 经启动 env 生效。
> **关键结论（源码 + ACP 契约核实）**：模型作用域分两种——
>   - **opencode**：模型是**进程级**（启动 env `OPENCODE_MODEL`）→ 必须并入连接键，改模型 = 换连接；
>   - **dsh**：模型是**会话级**（`session/set_config_option`，请求携带 `SessionId`）→ 无需并入连接键，
>     同一连接可承载不同模型的会话（每会话各调一次 `set_config_option`）；
>   - **echo**：无模型，明确 unsupported。

> **实现注（2026-09-09）**：本 Phase 已落地。与上文推导一致，但补三处实现细节：
>   - `Spec` 增 `EffortConfigOptionID`（dsh="reasoning_effort"）与 `ModelEnvKey`（opencode="OPENCODE_MODEL"），
>     用于区分「模型经 env 注入（进程级）」与「经 set_config_option（会话级）」；`ModelConfig` 增 `Effort`。
>   - `connKey` 改为变长 env 组签名 `connKey(runtimeID, nativeEnv, keyEnv, modelEnv)`：**provider 密钥 env
>     并入连接键**（B5 修复项——避免同 runtime+权限不同 provider 误共享子进程），模型 env 仅当
>     `ModelConfigOptionID==""`（opencode 进程级）时并入。
>   - `orchestrator.startConversationOn` 在 `NewSession` 后调 `proj.ApplyModel`：opencode/echo 返回
>     `unsupported_*`（忽略，模型已由拨号 env 注入）；dsh `set_config_option` 失败仅告警、不阻断会话。
>   - store 迁移 v4：`conversations` 增 `provider`/`model` 列；`ConversationMetaPatch` 增 `Model`。
>   - 会话记录在 `ConversationRec` 增 `Provider/Model/Effort`，经 `POST /conversations`（`model`/`effort`）
>     与 `PATCH /conversations/:id`（`model`）写入。
>   - 未做（属 Phase C/D 前端）：UI 上的「opencode 模型创建时锁定、dsh 会话内可改」。后端已按
>     §14.1 语义实现（opencode 模型经拨号 env，无会话内切换原语）。

### 12.1 acl：暴露 `session/set_config_option`

`internal/acl/session.go` 新增（acp-go-sdk `ClientSideConnection.SetSessionConfigOption` 已存在，
`SetSessionConfigOptionRequest` 用 `ValueId{ConfigId, SessionId, Value}` 表达 string 配置项）：

```go
// SetConfigOption 对本会话设置一个配置选项（ACP session/set_config_option）。
// configID 如 "model" / "reasoning_effort"；value 为 id（如模型 id / effort 档位）。
func (s *Session) SetConfigOption(ctx context.Context, configID, value string) error {
    resp, err := s.client.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
        ValueId: &acpsdk.SetSessionConfigOptionValueId{
            SessionId: s.id,
            ConfigId:  acpsdk.SessionConfigId(configID),
            Value:     acpsdk.SessionConfigValueId(value),
        },
    })
    if err != nil {
        return Classify(err)
    }
    _ = resp
    return nil
}
```

### 12.2 runtime：回填 `Spec` 与落地 `ApplyModel`

- `runtime/contracts.go` `Spec.ModelConfigOptionID` 回填：
  opencode = `""`（env 注入）、dsh = `"model"`（会话配置项）、echo = `""`。
- `runtime/session_runtime.go` `ApplyModel` 改为按 `spec.ModelConfigOptionID` 分派：
  - 非空（dsh）→ `sess.SetConfigOption(ctx, spec.ModelConfigOptionID, m.Model)`（错误透传）；
  - 空（opencode/echo）→ 继续返回 `unsupported_model`（opencode 走启动 env，见 12.4；echo 无模型）。
- `ModelConfig{Provider, Model}` 保持；`Provider` 为 provider 档案 id（仅注入与目录定位用）。

### 12.3 store：迁移 v4

`internal/store/schema.go` 追加迁移（幂等，`forge migrate` 可导入）：

```go
{
    version: 4,
    name:    "conversations.provider/model（会话级模型选择）",
    statements: []string{
        `ALTER TABLE conversations ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
        `ALTER TABLE conversations ADD COLUMN model    TEXT NOT NULL DEFAULT ''`,
    },
},
```

`ConversationRec`（`manager/service.go`）与 `conversations` 读写同步增 `Provider`/`Model`；
`events.go` 不变（model 附随 `bus created` 事件的 payload，或新建会话详情字段，不新增事件 type）。

### 12.4 manager：拨号与连接键（核心）

- `providerModelEnvFor(providerID, model) []string`（替换 `providerEnvFor` 语义）：返回某 provider
  档案的密钥 env（`<TYPE>_API_KEY`）+ 模型 env（opencode → `OPENCODE_MODEL=<model>`）。
- **连接键运行时感知**：`connKey(runtimeID, nativeEnv, modelEnv)`，但仅当 `Spec.ModelConfigOptionID`
  为空（opencode）才把 `modelEnv` 并入键（模型进程级）；dsh（ModelConfigOptionID 非空）不并入
  （模型会话级，可共享连接）。这同时修复一个潜在问题：此前 provider 密钥 env **没有**进连接键，
  同 runtime + 同权限配置但不同 provider 的 agent 会误共享子进程——Phase B 把「全量影响拨号的 env
  签名」统一纳入键。
- `acquire(ctx, agent, spec, model runtime.ModelConfig)`：`modelEnv`（含密钥 + 模型 env）叠加
  `nativeEnv` 后作为 `env` 传给 `runtime.Connect`；opencode 经此生效。
- `startConversationOn`：`NewSession` 后，若 `model.Model != ""` 且 `spec.ModelConfigOptionID != ""`
  （dsh）→ 调 `sess.SetConfigOption(ctx, spec.ModelConfigOptionID, model.Model)`，再回填
  `ActiveConversation.model`。
- `ConversationCreateReq.Model`（`handler`）→ 校验（可选模型在目录中/或空）→ `StartConversation`
  传入 `model`。会话 **resume**（`ensureConversation`）读取会话持久化的 model 保证一致。

调用点：`orchestrator.go:253/552` 两处 `acquire` 签名同步；`service.go CreateConversation` 传 model。

### 12.5 handler + router

- `ConversationCreateReq` 增 `Model string`（`json:"model"`）；`ConversationResp`/`ConversationCreateResp`
  增 `Model`（与 `Provider`）；GET/PATCH `/conversations/:id` 支持 `model` 子集更新。
- 新增路由按 §5（`/models`、`/providers/:id/models` 已在 Phase A）；**本次无新增路由**，仅 PATCH 字段
  扩展 → 不一定触发 `TestReadmeRoutesMatchRouter`（但若加路由须同步 README）。
- README「控制面接口摘要」补充 `POST /conversations` 的 `model` 及 `PATCH /conversations/:id` 语义。

### 12.6 边界

- opencode 改模型：因模型进程级，改模型只对**新拨号**会话生效；已存在会话记 **stale**（沿用 §8）。
- dsh 改模型：`set_config_option` 即时生效（会话级），无需重连。
- 选中的模型对 echo 返回 40501 + 友好文案（「此底座不支持选模型」）。
- provider 无 key 时选取模型不触发密钥注入（无 key 则不加 `<TYPE>_API_KEY`），仍可仅注入 model env。

### 12.7 测试与文件清单

- acl：`Session.SetConfigOption` 单测（集成测试走真实 echo 底座不支持，验证 unsupported 分支 + wire 帧）。
- manager：`connKey` 运行时感知（opencode 含 modelEnv / dsh 不含）、`providerModelEnvFor` 映射、
  `StartConversation` 带 model 落库、dsh `SetConfigOption` 调用桩（mock client）。
- store：迁移 v4 幂等（重复 apply 不报错）。
- 文件改动：`internal/acl/session.go`、`internal/runtime/{contracts,adapters,session_runtime}.go`、
  `internal/manager/{orchestrator,service,providers}.go`、`internal/store/schema.go`、
  `internal/server/handler/handler.go`、`README.md`、`internal/acl/rest_test.go` 等。

---

## 13. Phase C 细化：前端模型选择器

> 目标：把「会话级模型」做成可交互选择器。参考 openwork 的 `ModelSelect` / `ModelPickerModal`
> 交互（搜索、按 provider 分组、推荐星标、effort 二级），但**不引入**其 org/den/云端供应商权限层
> （ark-mate 单操作员、无云端模型市场）。面板基于 antd + `@ark/ui`，模型 pill 复用 `Pill`/`tokens`。

### 13.1 类型与 API（`packages/types` / `packages/api`）

`packages/types/src/agent.ts`：

```ts
export interface ModelRef { providerID: string; modelID: string }

export interface ModelOption {
  providerID: string   // provider 档案 id（或类型）
  modelID: string
  title: string        // 展示名
  description?: string // 归属 provider 展示（如 dsh / openai）
  behaviorTitle?: string        // 如 "推理档位"（dsh reasoning_effort 才有）
  behaviorLabel?: string        // 当前档位
  behaviorOptions?: { value: string|null; label: string }[]  // 如 low/medium/high
  isRecommended?: boolean
  isFree?: boolean
  source?: 'live' | 'cache' | 'snapshot'
}

export interface Provider { id; type; model?; has_key; models?: ModelOption[] }  // models 供表单目录用
```

`packages/types/src/conversation.ts`：`ConversationSummary`/`ConversationDetail` 增 `provider?`、`model?`；
`CreateConversationReq` 增 `model?: string`；`ConversationPatch` 增 `model?: string`。

`packages/api/src/resources/forge.ts`：

```ts
export const listModels = (agentId?: string): Promise<ModelOption[]> =>
  http.get<ModelOption[]>(`/forge/models${agentId ? `?agent=${agentId}` : ''}`)
export const listProviderModels = (id: string): Promise<ModelOption[]> =>
  http.get<ModelOption[]>(`/forge/providers/${encodeURIComponent(id)}/models`)
```
`createConversation`/`updateConversation` 已在 `req`/`patch` 上透传 `model`（Phase B 后端字段）。

### 13.2 组件（`apps/panel/src/components`）

- **`ModelSelect.tsx`**：模型 pill（可点击打开）。基于 antd `Select` 封装：
  - `showSearch` + `filterOption`（按 `title/modelID/description` 子串匹配）；
  - 用 `Select.OptGroup` 按 `providerID` 分组，选项渲染为 `title`+`description`，推荐模型加星标
    （`★`，配色 `tokens`，不硬编码）；
  - 选中项 `behaviorOptions` 非空时，从 pill 进入 **effort 二级面板**（同 openwork `pane=effort`），
    仅 dsh 且模型带 `behavior` 才显示；opencode/echo 无 behavior → 隐藏。
  - `value: ModelRef | null`；`onChange(model: ModelRef, behavior?: string|null)`；`disabled`。
- **`ModelPickerModal.tsx`**（可选，等价 openwork 全览）：`Dialog` + 搜索框 + provider 手风琴 +
  推荐/全量分组 + 收藏（star 切换）+ effort 子选择；`target: 'default' | 'session'`。
- 展示态复用 `@ark/ui` 的 `Pill`（自带 `title` tooltip + 截断），触发态包一层 `Select`/Popover。
- 交互态 CSS 收口 `console.css.ts`；颜色/圆角走 `theme.ts` `tokens`。

### 13.3 页面接线

- **`StartPage.tsx`**（新建会话，默认 agent）：
  - `useState<ModelRef|null>` 初值 = `localStorage['forge.panel.lastModel']`（对照既有的
    `forge.panel.lastAgentId` 记忆）；进入时拉当前 agent/provider 的模型目录。
  - 现只读 pill「模型 · Agent 默认」（`StartPage.tsx:163`）→ 换成 `ModelSelect`。
  - `createAndRun(prompt, model)` 传递 `model`；发送后记忆 `lastModel`。
- **`ChatPage.tsx`**：
  - 聊天头（`renderChatHead`）或 Composer 常驻行挂 `ModelSelect`，初值 = `activeSum.model`；
  - 选择变化 → `updateConversation(id, { model })`（会话级；dsh 即时生效，opencode 标记 stale
    提示「重连后生效」）。
  - Composer「单飞 turn 门控」提示旁可加当前模型名（可选）。
- **`AgentsPage.tsx`**：provider 表单 `model` 由 `Input`（`AgentsPage.tsx:528`）改 `Select`，
  `options` 来自 `listProviderModels(id)`（Phase A/B 后端目录），带「可手动输入（不校验）」兜底。

### 13.4 数据流与状态（`store.ts`）

- `ForgeState` 增 `modelCatalog: ModelOption[]`、`modelSource: Source | ''`；`ForgeActions` 增
  `loadModelCatalog(agentId?: string)`（回调 `listModels`，失败 toast + 记 snapshot 兜底）。
- `createAndRun(prompt: string, model?: ModelRef)`：`createConversation({ prompt, model: model?.modelID })`。
- `sendPrompt` 不变；`updateConversation` 复用现有 `rename/togglePin` 同一封装。

### 13.5 边界

- 目录为空/live 失败仍可手动输入（不校验），避免死锁（对齐 §8）。
- provider 无 key：模型可展示但禁用（Phase B 注入不触发）。
- opencode 改模型：选中后提示「重连后生效」（stale 语义）；dsh 即时。
- effort（dsh）：仅当所选 model 带 `behaviorOptions` 才出现；默认档为 `behaviorOptions[0]` 或
  `null`（交给后端默认）。

### 13.6 文件清单

`packages/types/src/{agent,conversation,model}.ts`、`packages/api/src/resources/forge.ts`、
`apps/panel/src/components/{ModelSelect,ModelPickerModal}.tsx`、
`apps/panel/src/pages/{StartPage,ChatPage,AgentsPage}.tsx`、`apps/panel/src/store.ts`、
`apps/panel/src/console.css.ts`（新增交互态）。

> **实现注（2026-09-09）**：本 Phase 已落地核心子集。与上文推导的偏差：
>   - `GET /agents` 增 `runtimes`（agent 候选 runtime 顺序，ResolveRuntime 按此试探），供前端判定
>     模型作用域（`opencode`=创建时锁定 / `deepseekharness`=会话内可改 / `echo`=无模型）。
>   - 类型以**后端 DTO 为准**收敛：`ModelOption{provider,id,title,description?,behavior?,recommended?,is_free?}`，
>     目录响应为 `ModelCatalogResp{source,models,note?}`（`listModels(agentId?)`/`listProviderModels(id)` 均返回
>     `ModelCatalogResp`，非 §13.1 的裸数组）；`ModelRef{providerID?,modelID}` 中 `providerID` 目前仅作展示分组。
>   - `createConversation`/`updateConversation` 经 `@ark/api` 透传 `model`（`CreateConversationReq.model/effort`、
>     `ConversationPatch.model`、`ConversationSummary.provider/model`）。
>   - UI：新建会话（StartPage）用 `ModelSelect`（antd Select + provider 分组 + 推荐星标 + 搜索，
>     允许清空回「Agent 默认」；记住 `forge.panel.lastModel`）；会话详情抽屉「模型」行——`deepseekharness`
>     显示可改 `ModelSelect`（选后 `updateConversation({model})`，dsh 即时），`opencode` 只读（label 提示仅创建时），
>     `echo` 显示「无模型」。
>   - **未做**（留待打磨）：`ModelPickerModal` 全览弹窗、AgentsPage provider 表单单模型改 Select、
>     ChatPage 头像行内 `ModelSelect`（已在会话详情抽屉等价覆盖）、effort 二级面板（dsh
>     `behavior` 目前只作标签展示；后端已支持 `effort` 字段）。

---

## 14. Phase D 细化：收尾打磨

> 目标：把 A/B/C 三阶段的边界语义、文案、测试与文档落地，达到「可选、可解释、可回退」。

### 14.1 stale 判定（opencode 模型变更 → 明确「仅随新建会话生效」）

有据约束：opencode 忽略 ACP 客户端模型选择（issue #13644），模型 = 进程级启动 env；会话绑定连接的
子进程，跨连接迁移/热切在该底座不成立。**Phase D 定稿语义**：

| runtime | 会话内改模型 | 处理 |
|---|---|---|
| dsh | 允许 | 即时 `set_config_option(model)`，无需重连 |
| opencode | **不允许** | UI 禁用该会话模型变更（`Select` `disabled` + tooltip「模型仅随会话创建时选定；opencode 将忽略会话内变更」） |
| echo | 不允许 | 隐藏模型 pill / tooltip「此底座不支持选模型」 |

- 因此**不做**「改模型→stale→重连」链路（开放性高、底座不支持、易误导），改为在创建时锁定。
  这同时收敛 Phase B §12.4 的「opencode modelEnv 并入连接键」——模型只在拨号时确定，会话内不变。
- **定稿语义覆盖**：§8 边界与 §13.5 此前写的「opencode 改模型 → 标记 stale / 重连后生效」，
  以本节「会话内锁定 + 创建时选定」为准（更诚实，不假装会话内切换有效）。

### 14.2 effort/behavior（dsh reasoning_effort）

- acl 复用 `Session.SetConfigOption(ctx, "reasoning_effort", value)`（同 §12.1 通道）。
- `runtime` 增 `Spec.EffortConfigOptionID`（dsh = `"reasoning_effort"`；opencode/echo = `""`）；
  `ModelConfig` 增 `Effort string`（空 = 不设置，交后端默认），随 `set_config_option` 一并下发。
- dsh 会话创建时若带 `behavior`（effort 档位）即写入；`ModelOption.behaviorOptions` 来自目录。
- opencode/echo 无 effort → 前端隐藏该面板（§13.2）。

### 14.3 边界文案（前端 toast / tooltip / 空态）

| 场景 | 文案 |
|---|---|
| 目录为空 / live 失败 | 「暂无可选模型，可手动输入；将为默认模型启动」 |
| 选中模型不在目录 | 40001：`所选模型不在可用目录中，请重新选择`（目录兜底允许手动输入） |
| provider 无 key | 模型可展示但 `disabled`，tooltip「该 Provider 未配置密钥，选择不会触发注入」 |
| opencode 改模型 | 见 §14.1 tooltip（会话锁模型） |
| echo 模型 | tooltip「此底座不支持选模型（free 演示）」 |
| dsh effort 变更 | 即时生效，无提示；仅当模型带 `behaviorOptions` 才可交互 |

### 14.4 测试矩阵

**后端（`go test ./...`）**
- `modelcatalog`：live 成功→写缓存并 `SourceLive`；live 失败→缓存 `SourceCache`；双失败→快照
  `SourceSnapshot`；`Valid(typ)` 边界；mock 上游响应 / JSON 非法。
- `store`：迁移 v4 幂等（重复 `migrate` 不报错）；`conversations.provider/model` 读写回环。
- `manager`：`connKey(runtimeID, nativeEnv, modelEnv)`（opencode 含模型 / dsh 不含）；
  `providerModelEnvFor` 映射（`OPENCODE_MODEL` 等）；`StartConversation(model)` 落库；
  dsh `SetConfigOption` 调用桩（mock client）；`resume` 读会话 model 一致。
- `runtime`：`ApplyModel` 按 `ModelConfigOptionID` 分派（dsh 调 `set_config_option` / opencode 返回
  unsupported / echo unsupported）；`EffortConfigOptionID` 回填。
- `handler/router`：`GET /models`、`GET /providers/:id/models` 真实装配路径；
  `TestReadmeRoutesMatchRouter` 保持绿（README 同步）。

**前端（`pnpm typecheck` + 测试）**
- `ModelSelect`：渲染/搜索过滤/分组/推荐星标；effort 面板仅当 `behaviorOptions` 非空出现。
- `StartPage`：`createAndRun(prompt, model)` 传递模型并记忆 `lastModel`。
- `AgentsPage`：provider 表单 `model` 由 `Input` 改 `Select`（目录 + 手动兜底）。
- `ChatPage`：会话模型变更对 opencode 禁用（`disabled`+tooltip）；dsh 允许。

### 14.5 文档与 README 终稿

- `README.md`「控制面接口摘要」：补 `GET /models`、`GET /providers/:id/models`、`POST/PATCH
  /conversations` 的 `model` 字段；删除/修订「面板不设模型选择器」（决策 v6③ 时代）表述为
  「模型随会话创建选定；opencode 忽略会话内变更」。
- `docs/design-and-roadmap.md` 决策 v6③ 与 `agent-runtime-refactor-design` §5.5 标注「模型面已由
  本方案落地（2026-09-09），仅 dsh 支持会话内切换」。`events.go` 无新增事件 type（model 附随会话
  详情/创建事件 payload）。

### 14.6 Commit 约定（AGENTS.md：`feat/fix/refactor` + 中文 scope）

- Phase A：`feat(model): 三段式模型目录与 /models 路由`；
- Phase B：`feat(model): 会话级模型选择（dsh set_config_option / opencode env）`；
- Phase C：`feat(model): 面板模型选择器（StartPage/ChatPage/Provider 表单）`；
- Phase D：`fix(model): opencode 会话内模型锁定 + effort 通道 + 文案与文档`。

### 14.7 分阶段 DoD / 验收

| 阶段 | DoD |
|---|---|
| A | `GET /models`/`GET /providers/:id/models` 可查、带 `source` 降级；单测 + README 守卫绿 |
| B | 会话创建带 model 落库；dsh 会话内切模型即时生效；opencode 会话锁模型；`connKey` 含模型 |
| C | 面板可选模型；effort（dsh）可交互；provider 表单目录化；typecheck 通过 |
| D | 边界文案齐；测试矩阵绿；README/设计文档定稿 |

> **实现注（2026-09-09）**：Phase D 落地子集。与上文推导的偏差：
>   - **effort 二级面板**：以模型的 `behavior` 字段为档位来源（deepseek 快照里
>     `deepseek-v4` 带 `["low","medium","high"]`），仅当默认 agent runtime 为 `deepseekharness`
>     且选中模型带 `behavior` 时，在新建会话（StartPage）展示「推理档位」`Select`；选值经
>     `createAndRun` 的 `ModelRef.effort` 传 `CreateConversationReq.effort`（后端落
>     `set_config_option(reasoning_effort)`）。会话内改 effort 未接入（后端 `ConversationPatch`
>     无 effort 字段，留待后续）。
>   - **ModelPickerModal**：全览弹窗（搜索 + provider 分组 + 推荐星标 + 恢复默认），从 StartPage
>     「浏览全部」进入，选择模型并关闭（effort 由内联 Select 承担）；未做收藏（star 切换，单操作员场景省略）。
>   - **AgentsPage provider 表单**：`Model` 字段由 `Input` 改 `AutoComplete`（可手动输入 / 从目录选，
>     不校验），编辑已有 provider 时经 `listProviderModels(id)` 拉取候选（live 优先，兜底快照）。
>   - **opencode 会话内锁定**：会话详情抽屉「模型」行对 opencode 显示只读（`Select` `disabled` +
>     placeholder「仅随创建时选定」）；dsh 可改；echo 显示「无模型」（对齐 §14.1 语义表）。
>   - 设计文档此前的「opencode stale/重连后生效」措辞按 §14.1 收敛为「**仅随新建会话生效**」。

---

## 15. 开放项

1. catalog live 段首期是否只做「单一 provider 类型 + 已知 key」的 `/models`，还是连 dsh profile 模型一并聚合（建议先做前者）。
2. dsh `effort/reasoning` 是否随本次一并暴露（`set_config_option` 同通道，成本低；建议做）。
3. 会话级模型是否需要**跨 agent 的全局最近模型记忆**（openwork 的 favorites/recent），还是仅当前会话。
