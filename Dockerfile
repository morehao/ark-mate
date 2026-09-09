# ark-forge 容器镜像（多阶段：Node 构建前端 → Go 内嵌编译 → 精简运行时）
#
# 构建：docker build -t ark-forge:local .
# 运行：docker run -d --name ark-forge -p 127.0.0.1:10001:10001 -v ark-forge-data:/data \
#         -e FORGE_ADDR=0.0.0.0:10001 -e FORGE_DB=/data/forge.db ark-forge:local
# 说明：容器内默认仅 memory/echo 可自检；接 opencode/dsh 需自行提供可执行文件与配置。

# —— 阶段 1：构建前端产物 ——
FROM node:24-bookworm AS web
WORKDIR /src/frontend
RUN corepack enable
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.nvmrc ./
COPY frontend/pnpm-workspace.yaml ./
COPY frontend/packages ./packages
COPY frontend/apps ./apps
RUN pnpm install --frozen-lockfile
RUN pnpm build

# —— 阶段 2：编译内嵌前端的单二进制 ——
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY backend ./backend
COPY --from=web /src/frontend/apps/panel/dist ./backend/internal/web/dist
WORKDIR /src/backend
RUN go build -tags release -o /out/forge ./cmd/forge

# —— 阶段 3：运行时（仅二进制 + CA 证书；数据卷挂 /data）——
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home /data --shell /usr/sbin/nologin forge \
    && mkdir -p /data && chown forge:forge /data
COPY --from=build /out/forge /usr/local/bin/forge
USER forge
WORKDIR /data
ENV GIN_MODE=release \
    FORGE_ADDR=0.0.0.0:10001 \
    FORGE_DB=/data/forge.db \
    FORGE_TRANSCRIPTS=/data/transcripts \
    FORGE_AGENTS=/data/agents \
    FORGE_CAPABILITIES=/data/capabilities
EXPOSE 10001
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:${FORGE_ADDR##*:}/v1/forge/health" || exit 1
ENTRYPOINT ["/usr/local/bin/forge"]
CMD ["serve"]
