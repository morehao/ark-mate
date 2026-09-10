# ark-forge 部署与运维

> 面向单操作员（决策 v3③：无多用户/团队语义、无登录鉴权）。本文只写**可复核**的部署形态与
> 参数；除明确标注**未执行**的一项（systemd 单元）外，命令均在本机实跑过（容器镜像 §4 已实跑，
> 见文末「验证记录」）。设计背景见 `docs/design-and-roadmap.md`
> §M3 与 `docs/agent-runtime-refactor-design-20260908.md`。

## 1. 交付形态

| 形态 | 命令 | 产物 | 说明 |
| --- | --- | --- | --- |
| 单二进制（推荐） | `make all` | 仓库根 `./forge` | 先 `pnpm build` → 拷入 `backend/internal/web/dist` → `go build -tags release`；前端面板经 `//go:embed` 内嵌，无外部静态文件依赖 |
| 仅后端内核 | `make build` | 无独立产物 | 不带 `release` tag，`web` 包不参与编译（`MountWeb` 为空实现），面板不内嵌 |
| 前后端分离 | `make dev` / 分开起 | `frontend/apps/panel/dist` | 前端可独立部署（需自行把 `/v1` 反代到后端，含 WebSocket 升级） |
| 容器 | `make image` / `docker build -t ark-forge .` | 镜像（约 140 MB） | 多阶段：Node 构建前端 → Go 内嵌编译 → 精简运行时；两个 target，可选内置 dsh/opencode（见 §4） |


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
  - 需跨机访问：**先设 `FORGE_TOKEN`（A6 单操作员令牌，见 §2.3.2）**，再显式
    `--addr 0.0.0.0:10001`（或 `FORGE_ADDR=0.0.0.0:10001`）并置于反向代理/内网之后，用防火墙或
    安全组限制来源；**不要**无令牌直接暴露到公网。容器场景需显式 `FORGE_ADDR=0.0.0.0:10001`（见 §4）。
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
| 代建工作空间 | `~/.forge/workspaces/` | `FORGE_WORKSPACE_DIR` | 建会话未指定 `cwd` 时按**会话 id** 代建 `<id>/`（该目录即会话沙箱边界） |
| 工作区根白名单 | 空（不限制） | `FORGE_WORKSPACE_ROOTS` | 逗号分隔绝对路径；非空时显式 `cwd` 与目录浏览都被收在根内 |

持久层开关：`FORGE_STORE=sqlite|memory|auto`（空 = auto：库文件已存在且非空 → sqlite，否则
memory）。**memory 模式无 runs 读模型**：`GET /usage/summary` 返回 40501
`usage_stats_unsupported`（明确降级，不返回假零值）。

### 2.2.1 工作空间（会话沙箱边界）的运维要点

会话工作空间 = 该会话的 ACP `cwd`，**创建时定稿、之后不可变**（要换目录就新开会话）。

- **默认行为**：`POST /conversations` 不带 `cwd` → 平台在 `FORGE_WORKSPACE_DIR` 下按会话 id
  代建 `<id>/`，响应回 `workspace_temporary=true`。**不会**回落 serve 进程的工作目录——
  那会让所有会话共用一个沙箱边界（旧行为，已移除）。
- **关闭代建**：`FORGE_WORKSPACE_DIR` 设为空 / `off` / `disable` / `none` → 不带 `cwd` 的建会话
  请求报 `workspace_required`（40001）。适合"目录必须由调用方显式指定"的受控部署。
- **代建目录不自动清理**：v1 只记录权威标志（`conversations.workspace_temporary`），不做
  TTL/回收——清理留待显式的 prune 能力，避免误删用户内容。磁盘敏感场景请把
  `FORGE_WORKSPACE_DIR` 指到受管卷，或关闭代建 + 由调用方指定目录。
- **目录浏览面**：`GET /workspaces/browse` 是 Web 端替代原生目录选择器的必要能力（只列目录，
  不列文件）。**未配 `FORGE_WORKSPACE_ROOTS` 时可枚举任意可读目录**——同机单操作员这样最省事，
  但对外提供服务（反代/容器/多人）就等于开放整个文件系统的结构，务必做两件事：
  1. 设 `FORGE_TOKEN`（平台默认只绑回环，对外必须先鉴权）；
  2. 配 `FORGE_WORKSPACE_ROOTS`（如 `/workspaces`、`/srv/repos`），把可浏览与可创建的范围收在
     挂载进来的工作区根上。白名单同时约束**显式 `cwd`** 与**目录浏览**（两者共用一条校验），
     符号链接按真身判定，根内指向根外的链接不会被列出。
- **资料库 = 工作区根的一级子目录**（`GET /workspaces/libraries`）：把 `FORGE_WORKSPACE_ROOTS`
  指向资料挂载点（如 `/srv/docs`），其下的每个一级子目录就自动成为面板里的一个"资料库"——
  **零新状态**（不存登记表，目录名即名字），用户选名字而不必面对绝对路径。两条附带规则：
  平台自己的代建根（`workspace_dir`）会被排除（它按策略必然落在某个根内，否则会以"tmp"之名混进
  资料库列表）；某个根暂时不可读（挂载掉了）只少显示一个，不让整页报错。
  `GET /workspaces/files?path=` 提供"看资料"面（列文件 + 大小 + 修改时间），与只列目录的
  `browse`（选工作空间）分工：**files 的条目不可作为 `cwd`**（文件不是合法工作空间）。
- **容器化部署的两个硬要求**：① `FORGE_WORKSPACE_DIR` 必须落在**可写挂载**上（默认
  `<数据目录>/workspaces`，即 `~/.forge/workspaces`；容器里若 `HOME` 不在卷上，代建目录会写进
  可写层，容器重建即丢）；② 能选的工作空间只能是**容器挂载视图内**的目录——宿主机目录要当工作
  空间，必须先 bind mount 进来，并以**容器内路径**作为 `cwd`（agent 也在同一容器内，故这才是
  对 agent 正确的那个路径）。
- **已知限制**：平台注册表 MCP 随 `session/new` 下发，其**相对路径参数仍以 agent 进程的 cwd
  解析**（serve 进程目录），而非会话工作空间。需要工作空间相对路径的 MCP 请写绝对路径。

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
| `FORGE_WORKSPACE_DIR` | `~/.forge/workspaces` | 代建工作空间根目录；**空/off/disable/none = 关闭代建**（建会话未指定 `cwd` 时报 `workspace_required`，不回落 serve 进程目录） |
| `FORGE_WORKSPACE_ROOTS` | 空（不限制） | 允许作为工作空间的根目录白名单（逗号分隔绝对路径）。非空时**显式 `cwd` 与目录浏览都只在根内**：越界报 `workspace_outside_roots`；浏览的「上级」到根为止；家目录不在根内则不返回该入口。**代建根必须在白名单内**，否则 `forge serve` 启动即失败（配置自相矛盾 → fail closed，不静默产出越界会话） |
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

同目录可选 `runtimes.json`（A5）：远程 runtime 声明数组
`[{"id","name","transport":"ws|http","endpoint","headers"}]`，启动时注册进 runtime 注册表，
agent 的 `runtimes` 字段即可引用；与内置 id 冲突、传输/URL 形态非法都会让 `forge serve`
启动即退出（退出码 2）。远程 runtime 无本地命令，可用性判定只看端点是否声明，可达性在拨号
阶段判定（失败为 `50201` 连接类错误，端点地址出现在错误信息里）。

支持的键（与 §2.3 表一一对应）：`addr` `transcripts` `db` `store` `agents` `capabilities`
`skills_materialize_dir` `skills_materialize_dirs` `idle_timeout` `reap_interval` `turn_timeout` `handoff_max_chars`
`permission_timeout` `elicitation_timeout` `proc_registry` `reap_grace`；另可用 `FORGE_*` 原名、
`-` 写成分隔符，以及少量别名（`transcript`/`db_path`/`agent_dir`/`capability_dir`）。

**优先级**：内置默认值 < `config.json` < `env` < 进程环境变量 `FORGE_*` < CLI flag。所以
「文件里写基线，环境变量做覆盖，flag 做临时调试」三段式可用。目录路径由 `FORGE_CONFIG_DIR`
决定（默认 `~/.forge/config`）。

**严格性**：未知键、非法 JSON、非法 duration/整数都会让 `forge serve` 在启动阶段退出（退出码
2，stderr 带文件路径与合法键清单）——不静默回落，避免「改了配置却没生效」。核对用
`forge config`（文本）或 `forge config --json`（含每项来源 `default|config.json|env|env(process)`）。

systemd 场景可直接用同一目录：`EnvironmentFile=-%h/.forge/config/env`（§3 单元里已注释示例）。

### 2.3.2 单操作员令牌（A6）

`FORGE_TOKEN`（或配置目录 `token`）非空时，除 `/health` 外**全部** `/v1/forge` 路由要求令牌：

```
FORGE_TOKEN=$(openssl rand -hex 24) ./forge serve --addr 0.0.0.0:10001
curl -H "Authorization: Bearer $FORGE_TOKEN" http://127.0.0.1:10001/v1/forge/agents
```

- REST：`Authorization: Bearer <token>`（大小写不敏感，允许多余空白）；失败返回 HTTP 401 +
  信封 `{code:40101,...}`（不是 HTTP 200 的业务错误——鉴权失败在传输层就该是 401）。
- WS `/v1/forge/ws/events`：浏览器无法为握手设置请求头，故接受 `?token=<token>`。
- `/health` 保持免鉴权（容器编排/反代 liveness 探针）。
- 恒定时间比较；令牌不出现在日志里，`forge config` 只打印掩码。
- 面板：顶栏「令牌」按钮填写（localStorage，仅本浏览器）；401 时自动弹出提示。
- 这是**单操作员**令牌，不是多用户/RBAC：无用户、无角色、无审计区分。多人共用请置于
  带认证的反向代理之后（或等后续切片）。

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

`Dockerfile` 是多阶段构建（Node 构建前端 → Go 内嵌编译 → 精简运行时），**两个最终 target**：

| target | 内容 | 体积（本机实测） | 适用 |
| --- | --- | --- | --- |
| `forge`（默认） | 只含 `forge` 本体 + `curl`/`tini` + 非 root 运行时 | 141 MB | 底座由宿主机挂载、派生镜像或 `acp-serve` 远端提供；容器内先用 `echo` 自检 |
| `forge-runtimes` | 额外内置 `dsh`（npm `@deepseek-ai/dsh`）与 `opencode`（npm `opencode-ai` 平台二进制） | 671 MB | 想让容器开箱就能跑真实 agent |

```bash
# 最小镜像
docker build -t ark-forge:local .
# 含底座（两个都装；只装一个把另一个置 false）
docker build -t ark-forge:runtimes --target forge-runtimes \
  --build-arg WITH_DSH=true --build-arg WITH_OPENCODE=true .
# 等价的 make 封装
make image                                             # 最小
make image/runtimes WITH_DSH=true WITH_OPENCODE=true   # 含底座
make image/run IMAGE_TAG=runtimes                      # 起容器
```

开关语义（刻意 fail closed，不静默降级）：

- `WITH_DSH` / `WITH_OPENCODE` 只认 `true|false`（`1|0` 亦可）；取值拼错 → **构建即失败**并列出合法取值。
- 开关必须与 target 配套：`--build-arg WITH_DSH=true` 传给默认（最小）target → 构建失败并提示改用
  `--target forge-runtimes`（否则会得到一个「以为有底座、其实没有」的镜像）。两个开关同时关也失败。
- 版本可用 `--build-arg DSH_VERSION=… / OPENCODE_VERSION=…` 钉住，默认 `latest`。**dsh 需含 acp profile
  模板**（`>= 0.1.2-rc.1`，见 `internal/runtime/adapters.go` 的能力说明），`latest` 满足。
- 镜像只装 forge 与所选底座；agent 真要用的工具（git / ripgrep / 语言 SDK 等）请在派生镜像里补：
  `FROM ark-forge:runtimes` + `USER root` + `apt-get install -y …`。

### 4.1 运行

```bash
# 一条命令（仓库根 docker-compose.yml：具名卷 + 只绑回环 + /data 全集持久化）
docker compose up -d --build
# 或纯 docker run
docker run -d --name ark-forge -p 127.0.0.1:10001:10001 \
  -v ark-forge-data:/data -e FORGE_ADDR=0.0.0.0:10001 ark-forge:local
curl -s http://127.0.0.1:10001/v1/forge/health     # {"code":0,"msg":"ok"}
```

compose 的全部可调变量（端口/令牌/工作区白名单/镜像形态/镜像站）都有默认值，模板见仓库根
`.env.example`：`cp .env.example .env` 后放开需要的行即可。

镜像内的既定行为（都可用 `-e` / compose 覆盖）：

| 项 | 值 | 原因 |
| --- | --- | --- |
| `FORGE_ADDR` | `0.0.0.0:10001` | 二进制默认只绑回环，容器里不改就 `-p` 不进来 |
| `HOME`、`FORGE_CONFIG_DIR` | `/data`、`/data/config` | 库 / transcript / agents / capabilities / 配置 / 代建工作空间全在 `/data` 一个卷里（§2.2 的默认路径被显式改到卷上） |
| `FORGE_STORE` | `sqlite` | 才有 runs 读模型与 `/usage/summary`（§2.2） |
| `FORGE_WORKSPACE_DIR` | `/data/workspaces` | 代建工作空间必须落在可写挂载上，否则容器重建即丢（§2.2.1） |
| PID 1 | `tini` | daemon 会派生 opencode/dsh，容器里需要 init 回收孤儿进程 |
| 运行用户 | `forge`（uid/gid 10001，`--build-arg FORGE_UID/FORGE_GID` 可改） | 非 root；bind mount 的属主需与之一致 |
| `HEALTHCHECK` | `curl` 打 `/v1/forge/health` | 端口从 `FORGE_ADDR` 现取；`/health` 免鉴权，配了 `FORGE_TOKEN` 也通（§2.3.2） |

### 4.2 容器里的底座与工作空间（两个硬要求）

1. **底座**：最小镜像里能自检的只有 `echo`（daemon 派生子命令 `forge acp-demo`，无需模型 key）。
   要跑真实 agent，三选一——用 `forge-runtimes` 镜像（`dsh`/`opencode` 已在 `PATH` 上）、自建派生镜像
   （`FROM ark-forge:local` + 自己装），或 **Linux 宿主机**下把本机可执行文件 bind mount 进最小镜像
   （`-v /usr/local/bin/opencode:/usr/local/bin/opencode:ro`，agent 需要的配置/密钥也要一并进来）。
   注意 macOS/Windows 上的可执行文件**不能**塞进 Linux 容器（平台/架构/glibc 都不匹配），这类机器请走前两条。
   模型密钥经 `-e DEEPSEEK_API_KEY=…` 由 daemon 透传给 runtime 子进程；dsh 的 profile 根固定在
   `DSH_HOME=/data/dsh`（落在卷上，重启不重装）。
2. **工作空间**：能选的工作空间只能是**容器挂载视图内**的目录（§2.2.1）。宿主目录要当工作空间，
   先 bind mount 进来，会话 `cwd` 用**容器内路径**（agent 也在同一容器里，这才是对 agent 正确的路径）：

   ```bash
   docker run -d --name ark-forge -p 127.0.0.1:10001:10001 \
     -v ark-forge-data:/data \
     -v "$PWD:/workspaces/repo" \
     -e FORGE_ADDR=0.0.0.0:10001 \
     -e FORGE_WORKSPACE_ROOTS=/workspaces \
     -e FORGE_WORKSPACE_DIR=/workspaces/.forge-tmp \
     ark-forge:local
   ```

   `FORGE_WORKSPACE_ROOTS`/`FORGE_WORKSPACE_DIR` **必须成对给**：代建根不在白名单内时启动即失败
   （§2.2.1 的 fail closed），把代建根放进白名单即可（上面用 `/workspaces/.forge-tmp`）。
   bind mount 的写权限按宿主 uid 判定，与容器内 10001 不一致时用 `--build-arg FORGE_UID=$(id -u)` 重建。
   对外暴露端口（反代 / 跨机 / 多人）**先设 `FORGE_TOKEN`**，否则等于把控制面与整个挂载视图开放出去（§2.1）。

### 4.3 受限网络（镜像站 / 代理 / npm 镜像）

基础镜像与仓库地址都是 build-arg，默认走官方源（`node:24-bookworm` / `golang:1.26-bookworm` /
`debian:bookworm-slim` / `proxy.golang.org` / `registry.npmjs.org`）；在拉不到 Docker Hub 或
`proxy.golang.org` 的网络里整组覆盖即可：

```bash
make image \
  NODE_IMAGE=docker.m.daocloud.io/library/node:24-bookworm \
  GO_IMAGE=docker.m.daocloud.io/library/golang:1.26-bookworm \
  RUNTIME_IMAGE=docker.m.daocloud.io/library/debian:bookworm-slim \
  GO_PROXY=https://goproxy.cn,direct \
  NPM_REGISTRY=https://registry.npmmirror.com
```

compose 路径把同名变量写进 `.env` 即可（`NODE_IMAGE`/`GO_IMAGE`/`RUNTIME_IMAGE`/`GO_PROXY`/`NPM_REGISTRY`
/`DSH_VERSION`/`OPENCODE_VERSION`）。`GO_PROXY` 走 `goproxy.cn` 时不依赖 `sum.golang.org`
（`GONOSUMDB`/`GOSUMDB` 无需改动，`go.sum` 校验仍在本地完成）。


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

重启恢复语义：SQLite 模式从 `conversations`/`events` 恢复会话与历史；`interrupted` 状态表示重启前
被中断的轮次（无终态 → 可续聊重发），而**已完成的轮次保持 `completed`**——停机是运维动作，不是故障，
不会改写会话状态（见 §8 连接拆除语义）。所有已存在的会话（不论上一轮终态）在重启后都可继续提问：
`dsh`/`opencode` 走原生 resume，echo 等无原生恢复能力的底座走 handoff memo。transcript 模式（memory）
启动时回读 JSONL 重建会话列表。

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
| `docker restart` / `compose up -d` 后会话是否还在 | 在。停机只作废 runtime 执行句柄（日志 `停机：会话句柄作废（会话与状态保留…）`）：已完成的轮次保持 `completed`，被停机打断的在途轮次为 `interrupted`，重启后同一会话可直接继续提问（`session.resumed`；echo 走 handoff memo，dsh 走原生 `resume`） |
| 重启后某会话仍是 `in_progress`（未收敛为 `interrupted`） | 极少数时序：进程在在途轮次收尾前退出。下次启动的恢复会把残留 `in_progress` 收敛为 `interrupted`（可续聊），见 §7 上一条 |
| 会话继续提问报 `40401 未知或不可恢复会话` | 只可能是会话 id 不存在（历史 id 打错 / 库换过卷）；已存在的会话（含 `completed`/`failed`/`cancelled`）都可续聊。若 agent 被删则报 `agent_not_found`（同 id 重新注册即可复活绑定） |
| 重启后 runtime 子进程没了 / `connected=false` | 预期：连接不在卷上，重启后按需重拨（暖进程空闲回收同理）。会话数据在卷里，不受影响 |
| 容器里 `-p` 映射了端口但面板打不开 | 容器内必须 `FORGE_ADDR=0.0.0.0:10001`（镜像已默认）；`FORGE_ADDR` 改了端口时 `-p` 与 HEALTHCHECK 一并跟（后者从该变量现取） |
| 容器里建会话报 `workspace_outside_roots` | 工作空间只能是挂载视图内的路径：`-v 宿主机目录:容器内路径`，会话 `cwd` 用容器内路径；配了 `FORGE_WORKSPACE_ROOTS` 时 `FORGE_WORKSPACE_DIR` 也要落在白名单内（否则启动即失败） |
| 容器里 agent 起不来 / diagnostic `connected=false` | 最小镜像不含 dsh/opencode → 用 `--target forge-runtimes` 重建，或把可执行文件 + 配置 bind mount 进容器；看 `GET /agents/:id/diagnostics` 的 `stderr_tail` |
| `docker build` 卡在拉基础镜像 / `go mod download` 超时 | 受限网络用 §4.3 的 build-arg（`NODE_IMAGE`/`GO_IMAGE`/`RUNTIME_IMAGE`/`GO_PROXY`/`NPM_REGISTRY`）整组覆盖 |
| 把 `WITH_DSH=true` 传给默认 target 构建失败 | 预期行为：开关必须配 `--target forge-runtimes`（或 `make image/runtimes`），不做静默忽略 |

## 8. 验证记录（本机实跑）

环境：macOS（无 systemd）、Go 1.26、Docker 守护进程可用（Docker Hub 直连受限，容器验证经镜像站覆盖
基础镜像，见 §4.3）。

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
- 容器镜像（§4）：`registry-1.docker.io`/`auth.docker.io` 在本机持续超时，故基础镜像改用镜像站覆盖
  （`docker.m.daocloud.io/library/{node:24-bookworm,golang:1.26-bookworm,debian:bookworm-slim}` +
  `GO_PROXY=https://goproxy.cn,direct`），其余与默认路径一致：
  - **最小镜像**：`docker build -t ark-forge:local .` 成功（141 MB）。容器 `-p 127.0.0.1:18001:10001
    -v ark-forge-test-data:/data -e FORGE_ADDR=0.0.0.0:10001`：`GET /v1/forge/health` =
    `{"code":0,"msg":"ok"}`；`GET /` = 200 `text/html` + `<title>ark-forge · Agent 问答控制台</title>`；
    容器内 `id` = `uid=10001(forge) gid=10001(forge)`、`HOME=/data`、PID 1 为 `tini`；
    `forge config` 全部来源 `env(process)`：`/data/forge.db`、`/data/transcripts`、`/data/agents`、
    `/data/capabilities`、`/data/workspaces`、配置目录 `/data/config`、`proc_registry=/data/processes.json`。
  - **容器内跑通一轮真实会话**（echo 底座，daemon 派生自己的 `forge acp-demo`）：`POST /agents`
    （echo）→ `POST /conversations`（响应 `cwd=/data/workspaces/<会话id>`、`workspace_temporary=true`）
    → 挂起权限 `POST /conversations/:id/permission`（`call_id` + `option_id=allow-all`，
    `outcome=selected`）→ `status=completed`；`GET /usage/summary` = `source=sqlite runs=1 completed=1
    duration_ms=13248 context_used=134 cost_amount=0.00134 USD`。卷内出现
    `forge.db`、`agents/echo.json`、`processes.json`、`workspaces/<id>/`。
  - **重启恢复**：`docker restart` 后同一卷 → `GET /conversations` 仍列出该会话、`GET /agents` 含
    `dsh,echo,opencode`、`docker ps` 的 health 列由 `starting` 转 `healthy`（镜像内 HEALTHCHECK 生效）。
  - **重启不损伤会话**（连接拆除语义修复后实测）：容器内 echo 会话跑完一轮（`completed`）→
    `docker restart` → 会话仍为 `completed`、事件表无任何 `*error*` 事件；随后 `POST
    /conversations/:id/prompt` = `code:0`（不再 `40401 未知或不可恢复会话`）→ 再次 `completed`，
    新增 `session.resumed` 事件（echo 无原生 resume → handoff memo 兜底）。停机日志：
    `停机：会话句柄作废（会话与状态保留，重启后按需续聊） runtime=echo cause=intentional
    conversations=1`。
  - **鉴权**：`-e FORGE_TOKEN=test-token-abc` → `/health` 200（免鉴权）；`/agents` 无令牌 = **401**、
    错令牌 = **401**、正确 `Bearer` = 200。
  - **开关守卫**：`docker build --build-arg WITH_DSH=true .`（默认最小 target）**构建失败**，报
    「需要底座 target：请加 `--target forge-runtimes`」——刻意不静默忽略。
  - **含底座镜像**：`--target forge-runtimes --build-arg WITH_DSH=true --build-arg WITH_OPENCODE=true`
    构建成功（671 MB）。容器内 `node -v`=v24.21.0、`dsh --version`=0.1.5-rc.1、`opencode --version`=
    1.18.30（均在 `PATH` 上，以 `forge` 用户可执行）；`POST /agents/dsh/start` → `GET
    /agents/dsh/diagnostics` = `connected=true pid=15`，握手能力 `sessions_persistent=true resume=true
    list=true`；`POST /agents/opencode/start` → `connected=true pid=35`；`DSH_HOME=/data/dsh` 落卷
    （`/data` 下出现 `dsh/`、`.config/`、`.local/`）。**未跑真实模型轮次**：本机无 `DEEPSEEK_API_KEY`
    / `OPENAI_API_KEY`。
  - **compose 路径**：`docker compose up -d --build`（同一组镜像站变量）→ `docker compose ps` =
    `127.0.0.1:18003->10001/tcp`、health OK、`docker compose down -v` 干净回收容器与卷。
- 连接拆除语义（上述验证顺带复现并**已修复**）：daemon 收到 SIGTERM 关闭 runtime 连接时，曾走
  `Orchestrator.disconnectConversation` 的「空闲会话 → 标 `failed`」分支，于是**上一轮已 `completed`
  的会话在 `docker restart` / 重启 daemon 后变成 `failed`**（同机 `kill -TERM ./forge serve` 同样复现；
  容器部署里 `docker restart` / `compose up -d` 是常规操作，影响面很大）。根因是「连接生命周期」与
  「轮次结果」共用一条写状态的路径，把**主动停机**当成了**连接故障**。现按拆除原因分流：
  主动拆除（daemon 停机 / `POST /agents/:id/stop` / 空闲回收）在途轮次归 `interrupted`（无终态、可续聊
  重发），空闲会话**保持原状态**、不发 `turn.error`；只有对端/子进程意外断开才可能把在途轮次收成
  失败，且空闲会话同样不被改写（只落 agent 诊断 `last_error.code=connection_lost`）。会话句柄在断连时
  作废但**会话本身归平台所有**（ADR-4：执行可丢弃）→ 下一次 prompt 经 native resume / handoff memo
  重建，因此重启后历史会话可继续提问。
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

- 远程 runtime 传输（A5）：`forge acp-serve --transport ws --listen 127.0.0.1:10020` +
  `--transport http --listen 127.0.0.1:10021`，`.forge/config/runtimes.json` 声明两个端点后
  `forge serve` 注册成功（日志 `注册远程 runtime`），`GET /runtimes` 返回 5 个（3 内置 + 2 远程）。
  两个远程 agent 各建一次会话：事件链 `session.created → user_message → thought → message →
  permission_request`，`POST /conversations/:id/permission`（`allowed=true, option_id=allow-all`）
  → `permission_resolved → permission → tool_call → tool_update → message → usage → turn.ended`
  （echo 远端底座回显「已授权（始终允许）。模拟执行完成」）。`GET /agents/:id/diagnostics`
  显示远程 agent `connected=true, pid=None`（无子进程）。把远端 `acp-serve` 杀掉后重建会话 →
  `code=50201`「连接远程 ACP 端点失败（ws: ws://127.0.0.1:10020/acp）」，诊断 `connected=false,
  stderr_source=last_error`，尾巴含 `ws read: ... EOF`，`last_error.code=connection_lost`。
  能力探测（`/agents/:id/capabilities`）经远程传输正常返回。

- 单操作员令牌（A6）：`token: cfg-token-9f3a`（config.json）→ `forge config` 输出掩码
  `cf…3a（14 字符）`；`/health` 无令牌 200；`/agents` 无令牌/错令牌 → **HTTP 401 +
  `code=40101`**（信封含 requestID 与提示文案），正确 `Bearer` 与 `?token=` 均 200；
  `POST /conversations/export` 无令牌 401、带令牌 200。WS 探针（Node 24 内置 WebSocket）：
  无令牌 `ERROR → CLOSE 1006`、错令牌同样被拒、带 `?token=` `OPEN → {"type":"pong"}`。
  `FORGE_SMOKE_TOKEN=… pnpm smoke:e2e` 全 PASS（含 WS 带令牌升级）；`FORGE_SMOKE_TOKEN=…
  node scripts/e2e-live-turn.mjs dsh …` 收到 `turn.ended`（REST+WS 均带令牌的真实 dsh 轮次）。

- MCP 工具调用（B1）：真实 stdio MCP server（`/tmp/b1/mcp_pyecho.py`，`FORGE_CAPABILITIES` 指向
  含 `pyecho.json` 的目录）→ `POST /mcps/pyecho/probe` `added=3`（echo/boom/slow）；
  `POST /mcps/pyecho/tools/echo/call` body `{"arguments":{"msg":"你好","n":42}}` → `code=0`、
  `content=[{type:text,text:"echo: {...}"}]`、`structured_content.echoed` 保真、`duration_ms=22`；
  `boom` → **`code=0` + `is_error=true`**（工具级失败）；未知工具 → `50201`
  `[mcp_tools_call_failed] JSON-RPC 错误 code=-32602`；未知 server → `40401`；`slow` +
  `timeout_ms=300` → `40901` `[mcp_timeout]`；`arguments` 传字符串 → `40001`
  `[mcp_tool_args_invalid]`；`arguments:null` / 空 body → 无参数调用成功。
  `pnpm smoke:e2e`（E2E_BASE 指向该 daemon）新增两条契约断言并全 PASS。

- MCP 资源与提示词（B2，同一 stdio server）：`GET /mcps/pyecho/resources` → 2 条资源
  （uri/name/mime_type/size 保真）+ 1 条 `resourceTemplates`；`POST /mcps/pyecho/resources/read`
  `{"uri":"file:///tmp/b2/notes.txt"}` → `text="hello b2\n"`；blob 资源 → `blob="AAECAwQ="`
  （base64 原样，不自动解码）；未知 uri → `50201 [mcp_resource_read_failed]`。
  `GET /mcps/pyecho/prompts` → 1 条模板（含 `arguments` 的 `required` 标注）；
  `POST /mcps/pyecho/prompts/greet/get` `{"arguments":{"who":"世界","lang":"zh"}}` →
  `messages[0].content.text="你好 世界（zh）"`；未知提示词 → `50201 [mcp_prompt_get_failed]`。
  未知 server（资源/提示词）→ `40401`。**模板降级实测**：另一个条目
  `pyecho_notpl`（脚本返回 -32601）→ `code=0`、`resources=2`、`templates=0`、
  `templates_error="resources/templates/list 失败：JSON-RPC 错误：code=-32601 …"`。
  注：新增能力目录文件需重启 daemon（目录启动时加载，无热重载）。
  `pnpm smoke:e2e` 新增两条 B2 契约断言，全 PASS。

- MCP sse 传输（B3）：python 写的 legacy SSE 服务端（`GET /sse` 发 `endpoint` 事件 + `message`
  事件，`POST /messages` 收请求）→ `POST /mcps/py-sse/probe` `code=0`、`added=1`、
  `server=py-sse/0.1.0`、`tools=[sse_echo]`（连续 3 次探测稳定）；`POST /mcps/py-sse/tools/sse_echo/call`
  → `text='sse: {"msg": "你好 SSE"}'`（连续 3 次调用均成功）；未知工具 → `50201
  [mcp_tools_call_failed] JSON-RPC 错误 code=-32602`；端点不可达 → `50201
  [mcp_sse_connect_failed] sse GET 连接失败：connection refused`。

- MCP 自动探测（B4）：默认 `off` → 启动后 `/mcps` 全为 `tool_count=0`、`/tools` 为空（无行为变化）；
  `FORGE_MCP_AUTO_PROBE=startup` → 启动 5s 内 `/mcps` 出现 `last_probe_at`（pyecho `tool_count=3`、
  down 记 `last_probe_error` 连接失败）、`/tools` 自动含 `boom/echo/slow`，无需任何手动 POST；
  日志 `mcp 自动探测开始 servers=2` → 完成/失败各一行 → `mcp 自动探测结束 ok=1 failed=1`。
  **不阻塞启动实测**：慢 server（initialize 睡 4s）→ t+2s `GET /health` 立即 200（27ms）、快 server
  的工具已入目录，t+7s 慢 server 也完成（`slow_tool`）。`FORGE_MCP_AUTO_PROBE=bogus` → 启动即
  exit 2 并列出合法取值。
- 原生权限映射（B5）：4 个 agent 的 runtime 子进程 env 实测——`dsh-ro-deny`/`dsh-native-ro`
  `DSH_PERMISSION_MODE=read-only`、`dsh-native-full` `=danger-full-access`、`oc-nobash`
  `OPENCODE_CONFIG_CONTENT={"permission":{"*":"ask","bash":"deny","edit":"deny"}}`（`ps eww -p <pid>` 读取）；
  同一 runtime 不同原生配置**各自独立进程**（连接缓存键含原生 env 签名）。效果实测：
  ① 隔离 ACP 客户端（无平台介入）read-only/workspace-write 写工作区外路径 →
  `bash: ...: Operation not permitted` + `[sandbox: file access denied under <mode> mode]`，
  danger-full-access 同命令直接成功；② 平台内 opencode 对照：无原生配置的 agent 收到
  `session.permission_request`（`echo hi`），`bash:deny` 的 agent **无任何权限请求**；
  ③ 平台内 dsh `default:deny` + read-only → 写入被沙箱拒绝、文件未生成、无挂起请求。
  `FORGE_NATIVE_PERMISSION=off` → 两个 runtime 的子进程均无原生权限 env（实测）。
- 前端用量视图 + 权限策略展示（C1）：`GET /usage/summary` 实测 `code=0 source=sqlite group_by=agent`
  （`totals runs=5 completed=4 failed=1 duration_ms=25029 context_used=28577`，3 个 agent 桶）；
  `group_by=day&from=2026-09-08` → `buckets=[("2026-09-09",5)]`；`group_by=tool` → `40001
  [invalid_usage_group_by]`。`make all` 后 release 二进制同源服务：`/` 200、bundle
  `/assets/index-74hlEOI0.js` 含「用量」「权限策略」「danger-full-access」字样。策略 PATCH 实测（前端同形载荷）
  → `code=0` 且 `GET /agents` 回读 `{"default":"allow","tools":{"bash":"deny"},"native_mode":"auto"}`；
  非法 `native_mode` → `40001 permission_policy.native_mode: 取值 "yolo" 非法（auto|off|read-only|workspace-write|danger-full-access）`。
  `scripts/smoke-e2e.mjs` 新增 3 项用量断言全 PASS（含 group_by=day 与非法分组）。
  视图纯函数 Node 侧校验 34 项 PASS（`fmtDuration/fmtCost/fmtContext/successRate/usageRows/parsePolicyTools/policySummary/policyColor`）。
- `/history` before 游标（C2）：真进程（release 二进制 127.0.0.1:10019，sqlite，单会话 65540 条事件，
  会话 id 为平台 uuid `b937db79-750c-4406-ae6c-9cfa91916605`）
  `?before=0&limit=3` → `first=65538 last=65540 texts=[m65538,m65539,m65540] has_more=true`；
  连续 `before=<first>` 逐页回退（65535→65532→65529…）；`limit=1000` 翻 66 页到 `first_seq=1`（`m1`）且
  `has_more=false`。同库对照：WS `attach(since_seq=0)` 回 `snapshot` 且重放事件数 0（eventbus 上限 65536，
  seq 1..4 已被淘汰）——REST 仍能翻到 seq 1，证明历史读 SQLite events 表而非 bus。
  `after=1&before=2` → `40001 after 与 before 互斥`；`before=-1` → `40000 before 需为非负整数`。
- elicitation scope 归属（C3）：真进程（release 二进制 127.0.0.1:10020，echo agent）
  `POST /conversations`（prompt 含「表单」）→ `GET /conversations/:id/elicitations` 回带
  `conversation_id=d24de5af-… scope_source=inferred_inflight mode=form`；用**另一真实会话**
  `POST /conversations/5a393b1e-…/elicitation` 决策同一 `elicitation_id` → `40001 结构化提问 "…" 不存在或已过期`
  且归属会话挂起项仍在（1 条），服务端日志 `WARN elicitation: 决策会话与归属会话不符，拒绝（scope mismatch）
  owner=d24de5af-… caller=5a393b1e-…`；归属会话决策 → `code=0`，随后挂起 0 条。
  未知会话 → `40401 未知会话 "…"`（先于 elicitation 校验）。WS 事件 `session.elicitation_request` 载荷实测
  `{"session_id":"3adc5250-…","conversation_id":"3adc5250-…","scope_source":"inferred_inflight","mode":"form"}`。
  同连接两会话并发在途时仍按取消处理并留 WARN（SDK 无 scope，不猜测）。
- 多 root skill 物化（C4）：真进程（release 二进制 127.0.0.1:10021，
  `FORGE_SKILLS_MATERIALIZE_DIRS=opencode=/tmp/c4/oc,deepseekharness=/tmp/c4/dsh`，三个 skill：
  both/oc-only/dsh-only）→ 启动日志 `skill 物化重校完成 dir=/tmp/c4/dsh runtime=deepseekharness written=2` +
  `dir=/tmp/c4/oc runtime=opencode written=2`；产物：`/tmp/c4/oc/{both,oc-only}`、
  `/tmp/c4/dsh/{both,dsh-only}`（归属标记第二行分别为 `opencode`/`deepseekharness`）。
  **真实底座解析**：`XDG_CONFIG_HOME` 隔离下 `opencode debug skill`（v1.18.29）列出
  `both`(/tmp/c4/oc/both/SKILL.md)、`oc-only`(/tmp/c4/oc/oc-only/SKILL.md)；
  真实 `@deepseek-ai/dsh-skill-filesystem` provider 解析 `/tmp/c4/dsh` → `both`、`dsh-only`，**零告警**。
  删除联动：移除 `dsh-only.json` 重启 → `dir=/tmp/c4/dsh runtime=deepseekharness written=0 removed=1`，
  oc root 不受影响。兼容路径：`FORGE_SKILLS_MATERIALIZE_DIR=/tmp/c4/shared` 单 root → 3 个 skill 全物化
  （同一目标文件多 format 时按既有约定取首个并告警）。
- smoke 脚本模式自适应（C4 附带）：`E2E_BASE=http://127.0.0.1:10001 node frontend/scripts/smoke-e2e.mjs`
  在**内存模式**（默认启动）下 `/usage/summary` 按设计返回 `40501 usage_stats_unsupported`，
  脚本记 PASS 并注明降级（不再误报 FAIL）；**sqlite 模式**下做完整形状断言
  （`source=sqlite`、`group_by=agent|day` 分桶、非法 `group_by=tool` → 40001）。两种模式实测均
  `✅ 通路全部 PASS`。
- 权限决策回执（C5）：真进程（release 二进制 127.0.0.1:10023，echo agent，选项
  `allow-once/allow-all/reject-once/reject-all`）：
  - **放行但不传 option_id** → `{"allowed":true,"rejected":false,"option_id":"","outcome":"selected",
    "selected_option_id":"allow-once"}`——回执给出兜底选中的选项（证明不是参数回显）；
    同一会话事件里 agent 回「已授权（允许一次）。模拟执行完成：`echo hello` → hello。」
  - **拒绝且传无效 option_id（`nope`）** → `{"rejected":true,"option_id":"nope","outcome":"cancelled",
    "selected_option_id":""}`，agent 回「未获授权（决策被拒绝或取消，按 ACP 语义即 Cancelled）」；
  - **拒绝且传有效 `reject-once`** → `outcome=selected`、`selected_option_id=reject-once`，
    agent 回「已拒绝（拒绝一次）」——回执与 agent 侧行为一致。
- 共享 root 归属集合与残留清理（C6）：真进程（127.0.0.1:10025，
  `FORGE_SKILLS_MATERIALIZE_DIRS=opencode=/tmp/c6/shared,deepseekharness=/tmp/c6/shared`，两 runtime 同 root）
  首轮标记 `both|deepseekharness,opencode`、`dsh-only|deepseekharness`、`oc-only|opencode`；
  移除 `oc-only.json` 重启 → `runtime=opencode written=0 removed=1`，`both` 归属不变；
  去掉 both 的 opencode 源重启 → 标记降为 `both|deepseekharness`（目录保留，他方仍在用）；
  再删 `both.json` 重启 → `runtime=deepseekharness written=0 removed=1`，`/tmp/c6/shared` 仅剩 `dsh-only`，无残留。
