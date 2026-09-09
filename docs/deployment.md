# ark-forge 部署与运维

> 面向单操作员（决策 v3③：无多用户/团队语义、无登录鉴权）。本文只写**可复核**的部署形态与
> 参数；除明确标注**未执行**的两项（systemd 单元、容器镜像）外，命令均在本机实跑过（见文末
> 「验证记录」）。设计背景见 `docs/design-and-roadmap.md`
> §M3 与 `docs/agent-runtime-refactor-design-20260908.md`。

## 1. 交付形态

| 形态 | 命令 | 产物 | 说明 |
| --- | --- | --- | --- |
| 单二进制（推荐） | `make all` | 仓库根 `./forge` | 先 `pnpm build` → 拷入 `backend/internal/web/dist` → `go build -tags release`；前端面板经 `//go:embed` 内嵌，无外部静态文件依赖 |
| 仅后端内核 | `make build` | 无独立产物 | 不带 `release` tag，`web` 包不参与编译（`MountWeb` 为空实现），面板不内嵌 |
| 前后端分离 | `make dev` / 分开起 | `frontend/apps/panel/dist` | 前端可独立部署（需自行把 `/v1` 反代到后端，含 WebSocket 升级） |
| 容器 | `docker build -t ark-forge .` | 镜像 | 多阶段：Node 构建前端 → Go 内嵌编译 → 精简运行时 |

前置：Go（见 `backend/go.mod`）、Node（`frontend/.nvmrc`）+ pnpm（`frontend/package.json` 的
`packageManager`）。`make all` 会校验 Node 主版本与 `.nvmrc` 一致。

## 2. 运行

```bash
# 最小启动（数据默认落 ~/.forge/）
GIN_MODE=release ./forge serve --addr 127.0.0.1:10001
curl -s http://127.0.0.1:10001/v1/forge/health     # {"code":0,"msg":"ok"}
```

浏览器打开 <http://127.0.0.1:10001>（release 产物自带面板）。

### 2.1 监听地址与安全边界（重要）

- `--addr` 缺省时读 `FORGE_ADDR`，其**默认值为 `127.0.0.1:10001`（仅回环）**——`forge serve` 裸跑
  只监听本机（实测 `lsof -nP -iTCP -sTCP:LISTEN` 显示 `127.0.0.1:10001`，非 `*:10001`）。控制面
  **无鉴权**，因此：
  - 单机自用：保持默认即可，无需额外参数；
  - 需跨机访问：显式 `--addr 0.0.0.0:10001`（或 `FORGE_ADDR=0.0.0.0:10001`）并置于反向代理/内网
    之后，用防火墙或安全组限制来源；**不要**直接暴露到公网（M3 可选②「单操作员 token」尚未实现，
    见路线图）。容器场景需显式 `FORGE_ADDR=0.0.0.0:10001`（见 §4）。
- 生产建议 `GIN_MODE=release`（否则每次启动打印 debug 模式告警）。
- 会话 wire transcript（`~/.forge/transcripts/*.jsonl`）与 SQLite 库含**完整对话原文**，按敏感数据
  对待（文件权限、备份加密）。

### 2.2 数据目录与持久层

| 路径 | 默认 | 覆盖 | 内容 |
| --- | --- | --- | --- |
| SQLite 库 | `~/.forge/forge.db` | `FORGE_DB` | 会话/事件/runs/messages/权限记忆（读模型与事实源） |
| transcript | `~/.forge/transcripts/` | `FORGE_TRANSCRIPTS` | 每会话一个 `<id>.jsonl`（schema v1，append-only） |
| Agent 定义 | `~/.forge/agents/<id>.json` | `FORGE_AGENTS` | 自定义 agent（含 `permission_policy`）、`providers.json` |
| 能力定义 | `~/.forge/capabilities/` | `FORGE_CAPABILITIES` | `skills/`、`mcp/`、`tools/` 目录 |
| skill 物化目标 | 空（关闭） | `FORGE_SKILLS_MATERIALIZE_DIR` | 非空才写（可能指向真实 opencode 用户目录，故默认关闭） |

持久层开关：`FORGE_STORE=sqlite|memory|auto`（空 = auto：库文件已存在且非空 → sqlite，否则
memory）。**memory 模式无 runs 读模型**：`GET /usage/summary` 返回 40501
`usage_stats_unsupported`（明确降级，不返回假零值）。

### 2.3 环境变量（全部来自 `backend/internal/infra/config/config.go`）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `FORGE_ADDR` | `:10001` | 监听地址（见 §2.1） |
| `FORGE_STORE` | auto | `sqlite` / `memory` / 空 |
| `FORGE_DB` | `~/.forge/forge.db` | SQLite 路径 |
| `FORGE_TRANSCRIPTS` | `~/.forge/transcripts` | wire transcript 目录 |
| `FORGE_AGENTS` | `~/.forge/agents` | agent definitions 目录 |
| `FORGE_CAPABILITIES` | `~/.forge/capabilities` | 能力数据面目录 |
| `FORGE_SKILLS_MATERIALIZE_DIR` | 空 | skill 物化根（默认关闭） |
| `FORGE_TURN_TIMEOUT` | 0（关闭） | 单轮看门狗（如 `30m`），超时自动 cancel 并发终态事件 |
| `FORGE_PERMISSION_TIMEOUT` | `30m` | 挂起权限自动拒绝超时 |
| `FORGE_ELICITATION_TIMEOUT` | `30m` | 挂起结构化提问自动取消超时 |
| `FORGE_IDLE_TIMEOUT` | `5m` | 暖进程空闲回收（0 = 关闭） |
| `FORGE_REAP_INTERVAL` | `5m` | 空闲回收扫描间隔 |
| `FORGE_HANDOFF_MAX_CHARS` | `6000` | 跨 runtime / 无原生 resume 时的历史摘要预算（0 = 关闭） |
| `FORGE_PROC_REGISTRY` | `<dir(FORGE_DB)>/processes.json` | runtime 子进程注册表（孤儿回收用）；`off`/`disable`/`none` = 关闭登记与回收 |
| `FORGE_REAP_GRACE` | `2s` | 孤儿回收 SIGTERM → SIGKILL 的宽限期 |
| `GIN_MODE` | `debug` | 设 `release` 关闭 gin debug 告警 |

### 2.3.1 配置目录 `.forge/config`（A4）

环境变量多到需要脚本管理时，改用配置目录（目录不存在 = 完全维持环境变量行为）：

```
~/.forge/config/config.json   # 主配置（JSON 对象，键名可用 addr 或 FORGE_ADDR）
~/.forge/config/env           # dotenv 风格（KEY=VALUE；# 注释、空行、可选 export 前缀与成对引号）
```

支持的键（与 §2.3 表一一对应）：`addr` `transcripts` `db` `store` `agents` `capabilities`
`skills_materialize_dir` `idle_timeout` `reap_interval` `turn_timeout` `handoff_max_chars`
`permission_timeout` `elicitation_timeout` `proc_registry` `reap_grace`；另可用 `FORGE_*` 原名、
`-` 写成分隔符，以及少量别名（`transcript`/`db_path`/`agent_dir`/`capability_dir`）。

**优先级**：内置默认值 < `config.json` < `env` < 进程环境变量 `FORGE_*` < CLI flag。所以
「文件里写基线，环境变量做覆盖，flag 做临时调试」三段式可用。目录路径由 `FORGE_CONFIG_DIR`
决定（默认 `~/.forge/config`）。

**严格性**：未知键、非法 JSON、非法 duration/整数都会让 `forge serve` 在启动阶段退出（退出码
2，stderr 带文件路径与合法键清单）——不静默回落，避免「改了配置却没生效」。核对用
`forge config`（文本）或 `forge config --json`（含每项来源 `default|config.json|env|env(process)`）。

systemd 场景可直接用同一目录：`EnvironmentFile=-%h/.forge/config/env`（§3 单元里已注释示例）。

### 2.4 底座前置条件

- `opencode`：需本机可执行 `opencode acp`。
- `deepseekharness`（dsh）：需本机可执行 `dsh --profile acp`（同 `$DSH_HOME` 持久化根）。
- `echo`：daemon 自派生 `forge acp-demo` 子进程，**无需任何外部依赖与模型 key**（用于冒烟与
  权限/提问演示）。内置播种 agent 为 `dsh`（默认）与 `opencode`；`echo` 需自建 agent：

```bash
curl -s http://127.0.0.1:10001/v1/forge/agents -H 'content-type: application/json' \
  -d '{"id":"echo","name":"Echo","runtimes":["echo"]}'
```

> 种子 agent（内置 `dsh`/`opencode` 与保留前缀 `agent-`）**只读**：不可删、runtime 不可改
> （报 40001 `seed_agent_readonly`）；name/provider/`permission_policy` 可改。

## 3. systemd（裸机/VM）

`/etc/systemd/system/ark-forge.service`：

```ini
[Unit]
Description=ark-forge 控制面 daemon（单操作员 ACP 会话底座）
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=forge
Group=forge
WorkingDirectory=/opt/ark-forge
Environment=GIN_MODE=release
Environment=FORGE_ADDR=127.0.0.1:10001
Environment=FORGE_DB=/var/lib/ark-forge/forge.db
Environment=FORGE_TRANSCRIPTS=/var/lib/ark-forge/transcripts
Environment=FORGE_AGENTS=/var/lib/ark-forge/agents
Environment=FORGE_CAPABILITIES=/var/lib/ark-forge/capabilities
ExecStart=/opt/ark-forge/forge serve
Restart=on-failure
RestartSec=3
# 单进程守护：会话/事件在 SQLite 与 transcript 中，重启后自动恢复
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/ark-forge

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --home /var/lib/ark-forge --shell /usr/sbin/nologin forge
sudo install -d -o forge -g forge /var/lib/ark-forge /opt/ark-forge
sudo install -o forge -g forge -m 0755 ./forge /opt/ark-forge/forge
sudo systemctl daemon-reload && sudo systemctl enable --now ark-forge
systemctl status ark-forge --no-pager
```

> 注意：底座子进程（opencode/dsh）由 daemon 拉起，需对 `forge` 用户可见（PATH 与配置）。
> `ProtectHome=read-only` 下默认 `~/.forge` 不可写，故示例把数据目录改到 `/var/lib/ark-forge`。

## 4. 容器

`Dockerfile`（多阶段；仓库已含）：

```bash
docker build -t ark-forge:local .
docker run -d --name ark-forge -p 127.0.0.1:10001:10001 \
  -v ark-forge-data:/data \
  -e FORGE_ADDR=0.0.0.0:10001 \
  -e FORGE_DB=/data/forge.db \
  -e FORGE_TRANSCRIPTS=/data/transcripts \
  -e FORGE_AGENTS=/data/agents \
  -e FORGE_CAPABILITIES=/data/capabilities \
  ark-forge:local
curl -s http://127.0.0.1:10001/v1/forge/health
```

容器内用 `echo` 底座即可自检（无需模型 key）；接 `opencode`/`dsh` 需在镜像或挂载中提供对应
可执行文件与配置。镜像内 `HEALTHCHECK` 用 `curl` 打 `/v1/forge/health`（端口取自 `FORGE_ADDR`）。

> **降级说明（本环境未执行）**：本机 Docker 守护进程可用，但到 `auth.docker.io` 拉取基础镜像
> 元数据持续 `i/o timeout`，因此**镜像构建未在本机跑通**（`docker build` 与 `buildx --check` 均因
> 拉取 `node:24-bookworm`/`golang:1.26-bookworm`/`debian:bookworm-slim` 失败）。Dockerfile 的三个
> 阶段与 `make all` 的步骤一一对应（Node 构建 → 拷 `apps/panel/dist` 到 `backend/internal/web/dist`
> → `go build -tags release`），后者已在本文「验证记录」实跑通过；容器形态请在能访问镜像仓库的
> 机器上按上述命令构建并自检。

## 5. 健康检查与可观测

- 存活探针：`GET /v1/forge/health`（`code:0`）。
- 就绪判据（无独立 readiness 端点，按下列信号组合）：health 通 + `GET /v1/forge/agents` 返回非空
  + `GET /v1/forge/runtimes` 含预期底座。
- 日志：stdout（JSON/text 由 slog 输出）。权限自动决策、MCP 探测、断连回收等关键路径均有日志
  （如 `permission: auto-decided by agent policy`、`mcp: probe ...`）。
- 事件留痕：会话事件同时落 SQLite `events` 表与 transcript 文件，可离线复盘。

## 6. 备份 / 升级 / 回滚

```bash
# 备份（停服或在线 SQLite 备份均可；transcript/agents/capabilities 是纯文件）
sudo systemctl stop ark-forge
tar czf ark-forge-$(date +%F).tgz -C /var/lib ark-forge
sudo systemctl start ark-forge

# 升级：替换二进制后重启（SQLite 迁移在启动时自动执行，幂等）
sudo install -o forge -g forge -m 0755 ./forge /opt/ark-forge/forge
sudo systemctl restart ark-forge

# 回滚：换回旧二进制重启——但若新版本已把库迁移到更高 schema 版本，旧二进制会
# 拒绝启动（store.migrate: schema vN 高于本二进制支持的 vM）。跨版本回滚必须先恢复库备份。
```

重启恢复语义：SQLite 模式从 `conversations`/`events` 恢复会话与历史（`interrupted` 状态表示重启
前中断的轮次，可续聊重发）；transcript 模式（memory）启动时回读 JSONL 重建会话列表。

## 7. 排障

| 现象 | 排查 |
| --- | --- |
| 面板打不开 / `GET /` 404 | 用了 `make build`（未内嵌前端）→ 改用 `make all`；或检查是否访问了后端端口 |
| 启动即 panic「web dist 未内嵌」 | `-tags release` 编译前未跑 `make web`（`make all` 会顺序执行） |
| 会话建不了、`agent_not_found` | agent 被删或 definitions 目录不可写；内置种子 `dsh`/`opencode` 只读不可删 |
| 轮次一直转、无输出 | 底座进程未起（`opencode acp` / `dsh --profile acp` 不在 PATH）；设 `FORGE_TURN_TIMEOUT` 让看门狗兜底 |
| 权限卡不出现 | agent 配了 `permission_policy: allow|deny` → 已自动决策（看 `session.permission_policy` 事件与日志） |
| `GET /usage/summary` 40501 | 跑在 memory 模式（无 runs 读模型）→ `FORGE_STORE=sqlite` |
| 上一 daemon 被 `kill -9` 后疑似残留 runtime 进程 | 启动日志看 `procreg: 启动孤儿回收完成`；注册表在 `<dir(FORGE_DB)>/processes.json`（含 pid/pgid/runtime/agent），可人工核对；`FORGE_PROC_REGISTRY=off` 可关闭 |
| 启动日志 `跳过疑似 PID 复用的登记` | 正常保护：登记条目的 pgid 与当前进程不一致（PID 被复用）→ 只清账不杀进程 |
| 启动报 `schema vN 高于本二进制支持的 vM` | 库被更新版本迁移过 → 恢复对应备份或升级二进制（拒绝启动是刻意保护） |
| 数据库锁/写入失败 | 单实例语义（未实测多实例并发）；勿让两个 daemon 共用同一 `FORGE_DB` |

## 8. 验证记录（本机实跑）

环境：macOS（无 systemd）、Go 1.26、Docker 守护进程可用但到 `auth.docker.io` 的网络受限。

- `make all` → 仓库根 `./forge`（40 MB，含内嵌前端）；`GIN_MODE=release ./forge serve --addr
  127.0.0.1:18140`（`FORGE_STORE=sqlite` + 独立数据目录）→ `GET /v1/forge/health` = `{"code":0,
  "msg":"ok"}`；`GET /` = 200 `text/html`，`<title>ark-forge · Agent 问答控制台</title>`，资产
  `/assets/index-*.js` 可访问；release 模式无 gin debug 告警。
- 按 §2.4 命令建 `echo` agent → `POST /conversations` 跑通一轮（权限卡经 REST 决策）→ 终态
  `completed`；`GET /usage/summary` 返回 `runs=1 completed=1 duration_ms=105 context_used=137
  usage_reported=1 cost_amount=0.00137 USD`。
- 种子只读：`DELETE /agents/dsh` 与 `PATCH /agents/dsh {"runtimes":["echo"]}` 均 `code=40001`
  `seed_agent_readonly`；`PATCH /agents/dsh {"name":…}` 成功且 `seed:true` 保持；`POST
  /agents/nope/start` = `code=40401`。（此前 agent 端点裸错误会漏成 `code=-1`，已修并加断言。）
- 重启恢复：kill daemon 后以同一 `FORGE_DB` 重启，`GET /conversations` 仍列出该会话（标题保留）。
- 裸跑 `forge serve`（不带 `--addr`）→ `lsof -nP -iTCP -sTCP:LISTEN` 实测 `127.0.0.1:10001`
  （仅回环；此前默认 `:8099` 会绑所有网卡，已按安全要求收窄）。
- 自动守卫：`go test -race ./...` 全绿；`TestReadmeRoutesMatchRouter` 校验 README 引用的 29 条
  路由全部命中注册表（反向注入错误路径会红）。
- systemd 单元：**未执行**（macOS 无 systemd）——仅 Python INI 解析校验语法与关键指令齐备
  （3 个 section、6 条 `Environment=`、`ExecStart` 绝对路径、`FORGE_ADDR` 收在本机）。上线前请在
  目标机 `systemd-analyze verify` 并实测。
- 容器镜像：**未执行**（`auth.docker.io` 不可达，见 §4 降级说明）。
- 孤儿回收（A1）：真实 daemon 下 `POST /agents/:id/start`（opencode）→ 注册表出现条目
  （`pid/pgid/command/runtime/agent/instance`）；`stop` 后条目摘除。启动回收用「上一实例登记 +
  独立进程组 + 忽略 SIGTERM」的替身进程组验证：日志 `reaped=1`、进程消失（SIGTERM 超时后升级
  SIGKILL）、注册表清空；PID 复用保护用「错误 pgid」条目验证：日志 `skipped=1`、无关进程存活。
  另实测：echo/opencode 子进程在 daemon 被 `kill -9` 后随 stdin EOF 自行退出（孤儿在此传输形态下
  少见，注册表是兜底防线）。

- 消息级检索 + 批量导出（A2）：真实 daemon（sqlite）跑两轮 echo 会话后 `GET /search/messages
  ?q=quasar-7731` → `source=sqlite total=4`，命中含 `conversation_id/role/seq/message_id/run_id/
  snippet`；逐个锚点回查 `GET /conversations/:id/history?after=<seq-1>&limit=1` 均落在同一事件
  （user 命中 = `session.user_message`；assistant 命中 = 该消息行首事件，可能为其前置
  `session.thought`）。真 `dsh` 会话（`明白`）实测 assistant 命中 `seq=3` 与 `session.message`
  事件 seq 完全一致。`POST /conversations/export` 三格式均通过：markdown 含标题/元信息/`## 用户`
  /`## 助手`/`> 思考`/`- 工具 …`，text 为纯文本行，json 可 `json.loads`（`conversation` +
  `events`）；未知会话进 `skipped` 且 `exported=0`；`format=pdf` → `code=40001
  [invalid_export_format]`；缺 `q` → `code=40001 [invalid_search_query]`。memory 模式
  （`FORGE_STORE=memory`）实测 `source=transcript` 且导出正常（无 DB 时回扫 transcript 的明确
  降级面）。`pnpm smoke:e2e` 新增 4 项断言全 PASS；真会话 `e2e-live-turn.mjs dsh` 收到
  `turn.ended`。前端 `typecheck` + `build` 通过；`console/searchView.ts` 纯函数用 Node 24 原生
  类型擦除加载实跑 17 条断言（锚点定位/去重/导出摘要）。

- agent 诊断面（A3）：`GET /agents/:id/diagnostics` 实测——未启动 `dsh` → `connected=false
  stderr_source=none stderr_tail=[]`；`POST /agents/dsh/start` → `connected=true pid=91586`；
  `kill -9 91586` → 历史出现 `turn.error connection_lost`，`last_error` 记录 `code/class/
  conversation_id`。`GET /agents/nope/diagnostics` → `code=40401`。真实 dsh/opencode **不写
  stderr**（`stderr_source=none`，0 行），非空尾巴的证据来自真实子进程单测（python ACP 桩 /
  `sh` 失败进程）。`pnpm smoke:e2e` 新增 2 项断言全 PASS。

- 配置目录化（A4）：`FORGE_CONFIG_DIR=/tmp/a4/cfg forge config` 实测打印 15 项与来源
  （`addr`/`db`/`store`/`idle_timeout`/`handoff_max_chars` 来自 `config.json`；`transcripts`/
  `agents`/`reap_grace` 来自 `env`；`FORGE_ADDR=…` 时 `addr` 来源变 `env(process)`）；
  `forge serve` 按文件生效（监听 `127.0.0.1:10008`、sqlite、DB 落 `/tmp/a4/data/forge.db`，
  经该 daemon 建的会话在 DB 中可查、events 5 行）。非法配置 `{"adr":…}` / `idle_timeout:"5
  minutes"` → `forge config` 与 `forge serve` 均退出码 2 并给出文件路径与合法键清单。目录缺失
  = 默认值（`127.0.0.1:10001`，来源全 `default`）。顺带修：sqlite 模式启动前 `MkdirAll` DB
  父目录（此前 DB 路径父目录不存在会以 `unable to open database file` 失败）。
