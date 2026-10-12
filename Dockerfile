# ark-mate 容器镜像（多阶段：Node 构建前端 → Go 编译 → 镜像内 COPY 一份 dist → 精简运行时）
#
# 两个最终 target（文件最后一段 = 默认 target = 最小镜像）：
#   mate           只含 mate 本体（+ echo 底座自检）；接 opencode/dsh 需自行挂载可执行文件
#   mate-runtimes  额外内置 agent 底座 dsh / opencode，可用 build-arg 分别开关
#
# 构建 · 最小镜像：
#   docker build -t ark-mate:local .
# 构建 · 含底座（dsh + opencode 都要）：
#   docker build -t ark-mate:runtimes --target mate-runtimes \
#     --build-arg WITH_DSH=true --build-arg WITH_OPENCODE=true .
# 构建 · 只要 opencode：
#   docker build -t ark-mate:oc --target mate-runtimes \
#     --build-arg WITH_DSH=false --build-arg WITH_OPENCODE=true .
#
# 运行（数据全落具名卷 /data；要映射出去必须监听 0.0.0.0）：
#   docker run -d --name ark-mate -p 127.0.0.1:10001:10001 \
#     -v ark-mate-data:/data -e MATE_ADDR=0.0.0.0:10001 ark-mate:local
#
# 受限网络（镜像站 / Go 代理 / npm 镜像）用 build-arg 覆盖，示例见 docs/deployment.md §4：
#   docker build -t ark-mate:local \
#     --build-arg NODE_IMAGE=docker.m.daocloud.io/library/node:24-bookworm \
#     --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.26-bookworm \
#     --build-arg RUNTIME_IMAGE=docker.m.daocloud.io/library/debian:bookworm-slim \
#     --build-arg GO_PROXY=https://goproxy.cn,direct .
#
# 注意：WITH_DSH / WITH_OPENCODE 属于 mate-runtimes target。传给默认（最小）target 会**明确报错**
# 而不是静默忽略——静默忽略会让人以为镜像里有底座。
#
# MATE_RUNTIME（**所有 target**，默认 dsh）：核心**认哪一个**底座，走 `-X` 编译期注入
# （见 backend/pkg/runtime/build.go）。它与 WITH_DSH / WITH_OPENCODE 是**两件事**：后者决定镜像里
# 装哪几份底座二进制，前者决定核心认哪一份。两者不一致是允许的（最小镜像不装任何底座时核心照样
# 认 dsh，只是拉不起来，诊断面会说清）；选了一个镜像里没装的底座，同样在会话建立时才暴露成
# `runtime_unavailable`。用户面（REST / 控制台）改不了它——换底座要重新构建。选中的 id 若不存在，
# 核心启动即失败（fail-closed，不给"能启动但没底座"的进程）。

ARG NODE_IMAGE=node:24-bookworm
ARG GO_IMAGE=golang:1.26-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
ARG NPM_REGISTRY=https://registry.npmjs.org
ARG GO_PROXY=https://proxy.golang.org,direct
# 底座版本（默认 latest；dsh 需含 acp profile 模板，即 >= 0.1.2-rc.1，见 pkg/runtime/adapters.go）
ARG DSH_VERSION=latest
ARG OPENCODE_VERSION=latest

# —— 阶段 1：前端产物（Node 主版本对齐 frontend/.nvmrc；pnpm 版本取 package.json 的 packageManager）——
FROM ${NODE_IMAGE} AS web
ARG NPM_REGISTRY
ENV npm_config_registry=${NPM_REGISTRY}
WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.nvmrc frontend/pnpm-workspace.yaml ./
COPY frontend/packages ./packages
COPY frontend/apps ./apps
# 直接用 packageManager 里钉住的 pnpm 版本（不依赖 corepack 的隐式下载与其弃用状态）；
# store 走 cache mount，改源码重建时不必重下依赖。
RUN --mount=type=cache,target=/pnpm-store,id=pnpm-store \
    set -eux; \
    PNPM_VERSION=$(node -p "require('./package.json').packageManager.replace(/^pnpm@/, '')"); \
    npm install --global --no-audit --no-fund "pnpm@${PNPM_VERSION}"; \
    pnpm install --frozen-lockfile --store-dir=/pnpm-store; \
    pnpm build

# —— 阶段 2：编译 mate 本体（前端资产**不进二进制**）——
# 2026-10-10 起核心不再内嵌前端资产（`//go:embed` 与 `-tags release` 一并删除）：资产是一份
# `apps/web/dist`，最终镜像把它 COPY 进去、用 `MATE_WEB_DIR` 指过去（见阶段 3）。
# 于是镜像里的二进制与 `make dev` / 桌面核跑的是**同一个构建配置**，不再有一条"只有发布才编译"的路。
# GOTOOLCHAIN 保持官方镜像默认的 local：go.mod 要求的版本比镜像里的新时直接失败，
# 而不是悄悄下载另一个工具链（fail closed）。
FROM ${GO_IMAGE} AS build
ARG GO_PROXY
# 构建期固定底座（默认 dsh）。取值只做词法校验：存在性交给核心启动时的 ProductRegistry
# fail-closed——远程 runtime 的 id 由 runtimes.json 声明，这里写不出白名单。
ARG MATE_RUNTIME=dsh
# CGO_ENABLED=0：持久层的 SQLite dialect 自 golib v1.32.22 起是**纯 Go** 的
# ncruces/go-sqlite3（SQLite 编成 wasm 再翻译回 Go），于是镜像不再需要 C 工具链，
# 产物是静态二进制（`RUNTIME_IMAGE` 也不再受 glibc 约束，换 musl/scratch 基底的前提
# 重新成立）。历史：2026-10-11 之前这里写 0，但那时 dialect 是 CGO 的 mattn/go-sqlite3，
# 0 编出的 stub 在**运行期**连不上库；2026-10-11 起改成 1 并接受动态链接。谁把 golib
# 降级回去而这里仍是 0，就会复现那个「编得出、连库即死」的故障，所以依赖侧由
# internal/archtest 的 TestSqliteDriverStaysPureGo 钉着。
ENV GOPROXY=${GO_PROXY} \
    CGO_ENABLED=0
WORKDIR /src/backend
# 先只拷 go.mod/go.sum 拉依赖：改源码不会导致重新下载模块
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod go mod download
COPY backend ./
RUN --mount=type=cache,target=/go/pkg/mod,id=gomod \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild \
    set -eux; \
    case "${MATE_RUNTIME}" in \
      ""|*[!a-z0-9_-]*|[!a-z0-9]*) echo "MATE_RUNTIME 取值非法：${MATE_RUNTIME}（须是 [a-z0-9][a-z0-9_-]*）" >&2; exit 1 ;; \
    esac; \
    go build -trimpath \
      -ldflags "-s -w -X github.com/morehao/ark-mate/pkg/runtime.BuildRuntime=${MATE_RUNTIME}" \
      -o /out/mate ./cmd/mate

# —— 阶段 3：运行时基线（两个最终镜像共用：非 root + 数据卷 /data + tini 收孤儿）——
# tini 作为 PID 1：daemon 会派生 opencode/dsh 子进程，容器里的孤儿进程需要有 init 回收。
FROM ${RUNTIME_IMAGE} AS runtime-base
ARG MATE_UID=10001
ARG MATE_GID=10001
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl tini; \
    rm -rf /var/lib/apt/lists/*; \
    groupadd --system --gid "${MATE_GID}" mate; \
    useradd --system --uid "${MATE_UID}" --gid "${MATE_GID}" \
        --home-dir /data --shell /usr/sbin/nologin mate; \
    install -d -o mate -g mate /data /data/workspaces /workspaces
# 前端资产（M2-1）：核心**不自带**资产，由这里把 web 阶段那份 dist 放进镜像，并用 MATE_WEB_DIR
# 指过去。两个最终镜像共用本层，因此两份镜像的资产来源一致（宿主机上的旧 dist 进不来：
# .dockerignore 排除了 frontend/**/dist，这里取的只能是镜像内 web 阶段刚构建的那份）。
COPY --from=web /src/frontend/apps/web/dist /usr/local/share/mate/web
# 容器里必须显式 0.0.0.0:10001 才能被 -p 映射出去（二进制默认只绑回环）；
# 数据/配置全部收在 /data 一个卷里，HOME 也指到卷上（内存/transcript/agents 目录都按它派生）。
ENV HOME=/data \
    GIN_MODE=release \
    MATE_ADDR=0.0.0.0:10001 \
    MATE_STORE=sqlite \
    MATE_DB=/data/mate.db \
    MATE_TRANSCRIPTS=/data/transcripts \
    MATE_AGENTS=/data/agents \
    MATE_CAPABILITIES=/data/capabilities \
    MATE_WORKSPACE_DIR=/data/workspaces \
    MATE_CONFIG_DIR=/data/config \
    MATE_WEB_DIR=/usr/local/share/mate/web
WORKDIR /data
USER mate
STOPSIGNAL SIGTERM
EXPOSE 10001
VOLUME ["/data"]
# 端口从 MATE_ADDR 现取（形如 0.0.0.0:10001 / :10001）；/health 免鉴权，带 MATE_TOKEN 也通
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${MATE_ADDR##*:}/v1/mate/health" >/dev/null || exit 1
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/mate"]
CMD ["serve"]

# —— 阶段 4：agent 底座（只产出 /opt/runtimes；node 本体也从这里取）——
# dsh（@deepseek-ai/dsh）与 opencode（opencode-ai，平台二进制随 optionalDependencies 下来）
# 都走 npm；装到 /opt/runtimes 而非 /usr/local，方便最终镜像整目录按需拷入。
FROM ${NODE_IMAGE} AS runtimes-pkg
ARG NPM_REGISTRY
ARG WITH_DSH=true
ARG WITH_OPENCODE=true
ARG DSH_VERSION=latest
ARG OPENCODE_VERSION=latest
ENV npm_config_registry=${NPM_REGISTRY}
RUN --mount=type=cache,target=/root/.npm,id=npm-cache \
    set -eux; \
    # 开关只认 true/false（1/0 亦可）：拼错直接失败，不静默变成「没装」
    case "${WITH_DSH}" in true|1) DSH=on ;; false|0|"") DSH=off ;; \
      *) echo "构建参数 WITH_DSH 取值非法：${WITH_DSH}（只接受 true|false）" >&2; exit 1 ;; esac; \
    case "${WITH_OPENCODE}" in true|1) OC=on ;; false|0|"") OC=off ;; \
      *) echo "构建参数 WITH_OPENCODE 取值非法：${WITH_OPENCODE}（只接受 true|false）" >&2; exit 1 ;; esac; \
    if [ "${DSH}" = off ] && [ "${OC}" = off ]; then \
      echo "WITH_DSH 与 WITH_OPENCODE 不能同时关闭：请改用 --target mate（最小镜像）" >&2; exit 1; \
    fi; \
    mkdir -p /opt/runtimes; \
    if [ "${DSH}" = on ]; then \
      npm install --global --prefix /opt/runtimes --no-audit --no-fund "@deepseek-ai/dsh@${DSH_VERSION}"; \
      test -e /opt/runtimes/bin/dsh; \
    fi; \
    if [ "${OC}" = on ]; then \
      npm install --global --prefix /opt/runtimes --no-audit --no-fund "opencode-ai@${OPENCODE_VERSION}"; \
      test -e /opt/runtimes/bin/opencode; \
    fi

# —— 阶段 5：最终镜像 · 含 agent 底座（--target mate-runtimes）——
FROM runtime-base AS mate-runtimes
# node 本体（dsh / opencode 的 JS 入口都靠它；基础镜像不带 node，故从构建阶段拷入）
COPY --from=runtimes-pkg /usr/local/bin/node /usr/local/bin/node
COPY --from=runtimes-pkg /opt/runtimes /opt/runtimes
ENV PATH=/opt/runtimes/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin \
    DSH_HOME=/data/dsh
COPY --from=build /out/mate /usr/local/bin/mate

# —— 阶段 6：默认 target · 最小镜像（只含 mate 本体）——
FROM runtime-base AS mate
# 本 target 不含底座：开关传成「开」时明确报错并给出正确命令，不静默忽略
ARG WITH_DSH=false
ARG WITH_OPENCODE=false
RUN set -eux; \
    case "${WITH_DSH}" in false|0|"") ;; \
      *) echo "--build-arg WITH_DSH=${WITH_DSH} 需要底座 target：请加 --target mate-runtimes（或改用 make image/runtimes）" >&2; exit 1 ;; esac; \
    case "${WITH_OPENCODE}" in false|0|"") ;; \
      *) echo "--build-arg WITH_OPENCODE=${WITH_OPENCODE} 需要底座 target：请加 --target mate-runtimes（或改用 make image/runtimes）" >&2; exit 1 ;; esac
COPY --from=build /out/mate /usr/local/bin/mate
