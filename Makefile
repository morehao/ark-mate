# ark-forge 仓库根构建编排
#
# 仓库结构（前后端平级 monorepo）：
#   backend/   —— 单 Go module：github.com/morehao/ark-forge（go.mod 在 backend/ 根）
#                 pkg/ 可复用基础设施（不 import internal/）+ internal/ 应用分层；
#                 分层边界由 backend/internal/archtest 守卫测试强制
#   frontend/  —— 独立 pnpm workspace（apps/panel），产物可单独部署
#
# 交付形态：
#   make all   —— 单二进制：前端产物拷入 backend/pkg/web/dist 后，
#                 以 -tags release 编译（启用 //go:embed），输出根目录 ./forge
#   make web   —— 仅构建前端并刷新 embed 宿主目录
#   make dev   —— 前后端联调单命令：先起后端 go run（127.0.0.1:10001），等 health 通过再起 vite dev（:5173），Ctrl-C 一起收
#   make server —— 仅后端开发起服（不起前端，面板经 vite dev proxy 联调）
#   make image —— 容器镜像（Dockerfile 两个 target，默认最小镜像；见文件末尾「容器镜像」段）

.PHONY: dev web server build vet test all clean image image/runtimes image/run

FRONTEND_ROOT := frontend
WEB_DIST_DIR  := backend/pkg/web/dist

# —— 前端工具链（Node / pnpm）——
# 唯一事实源在 frontend/ 内：Node 版本 = frontend/.nvmrc，pnpm 版本 = frontend/package.json 的
# packageManager 字段（pnpm >= 11 会在本机版本不一致时自动按其下载运行，无需 corepack）。
# make dev / make web 在调用 pnpm 前校验当前 node 主版本与 .nvmrc 一致（install 阶段由
# engines 兜底无条件失败，run 阶段由这里兜底），详见 README「前端工具链约定」。
NODE_VERSION_FILE  := $(FRONTEND_ROOT)/.nvmrc
NODE_VERSION       := $(shell sed 's/^v//' $(NODE_VERSION_FILE) 2>/dev/null)
NODE_VERSION_MAJOR := $(word 1,$(subst ., ,$(NODE_VERSION)))

# 后端持久层默认 sqlite（创建 ~/.forge/forge.db，启用 runs/用量读模型）。
# 想要轻量纯内存可临时覆盖：FORGE_STORE=memory make dev（环境变量优先于此处默认）。
FORGE_STORE ?= sqlite

# 运行前守卫（单行 shell，供 dev/web 作为独立 recipe 行调用）
define require-node
	@cur=$$(node -v 2>/dev/null | sed 's/^v//'); if [ "$${cur%%.*}" != "$(NODE_VERSION_MAJOR)" ]; then echo "==> Node 版本不符：frontend/.nvmrc 要求 Node $(NODE_VERSION_MAJOR).x（$(NODE_VERSION)），当前 $${cur:-未安装}"; echo "    团队统一 fnm，请先切换：fnm use $(NODE_VERSION_MAJOR)"; exit 1; fi
endef

# 前端产物 → embed 宿主（backend/pkg/web/dist，不入库）
web:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm build
	rm -rf $(WEB_DIST_DIR)
	cp -r $(FRONTEND_ROOT)/apps/panel/dist $(WEB_DIST_DIR)

# 后端开发起服（forge 无默认子命令，需显式 serve）
server:
	cd backend && FORGE_STORE=$(FORGE_STORE) go run ./cmd/forge serve

# 前后端联调单命令：先起后端 go run serve（默认 127.0.0.1:10001），轮询
# /v1/forge/health 通过后再起前端 vite dev（:5173，/v1 代理到后端含 ws），
# 避免后端尚未 listen 时 vite ws 代理出现 ECONNREFUSED 的启动竞态。
# 后端编译/启动失败会立即退出并报错；任一进程退出 / Ctrl-C 时把两个进程一起收掉。
dev:
	$(require-node)
	@echo "==> 后端  go run ./cmd/forge serve  (默认 127.0.0.1:10001, store=$(FORGE_STORE))"
	@trap 'kill 0 2>/dev/null; exit' INT TERM; \
	trap 'kill 0 2>/dev/null || true' EXIT; \
	cd backend && FORGE_STORE=$(FORGE_STORE) go run ./cmd/forge serve & \
	BACKEND_PID=$$!; \
	echo "==> 等待后端健康检查 http://127.0.0.1:10001/v1/forge/health ..."; \
	HEALTHY=""; \
	for i in $$(seq 1 300); do \
	  if curl -sf http://127.0.0.1:10001/v1/forge/health >/dev/null 2>&1; then \
	    HEALTHY=1; break; \
	  fi; \
	  if ! kill -0 $$BACKEND_PID 2>/dev/null; then \
	    echo "==> 后端进程已退出（go run 编译或启动失败），请查看上方错误输出"; \
	    exit 1; \
	  fi; \
	  sleep 0.3; \
	done; \
	if [ -z "$$HEALTHY" ]; then \
	  echo "==> 等待后端健康检查超时（约 90s），请查看上方后端输出"; \
	  exit 1; \
	fi; \
	echo "==> 后端就绪，启动前端 pnpm dev (vite :5173 -> /v1 proxy 127.0.0.1:10001)"; \
	cd $(FRONTEND_ROOT) && pnpm dev & \
	wait

# 默认构建：不带 release tag，web 包的 embed 不参与编译、不依赖前端产物
build:
	cd backend && go build ./...

vet:
	cd backend && go vet ./...

test:
	cd backend && go test ./...

# release 形态的测试：MountWeb 等带 //go:build release 的代码只在这个 tag 下编译，
# 默认 go test 完全覆盖不到（历史上 NoRoute 的非 API 行为就是这么长期没人验的）。
# 依赖 web 目标：pkg/web/dist 必须有产物，否则 embed 编译失败。
test/release: web
	cd backend && go test -tags release ./...

# 单二进制：先 web（构建 + 拷贝 + embed），再以 -tags release 编译，输出仓库根 ./forge
all: web
	cd backend && go build -tags release -o ../forge ./cmd/forge

clean:
	rm -rf $(FRONTEND_ROOT)/apps/panel/dist $(WEB_DIST_DIR) forge

# —— 容器镜像（Dockerfile 多阶段：前端 → Go 内嵌编译 → 精简运行时）——
#   make image                                    —— 最小镜像（只含 forge 本体 + echo 自检底座）
#   make image/runtimes WITH_DSH=true WITH_OPENCODE=true
#                                                 —— 额外内置 dsh / opencode（开关可分别置 false）
#   IMAGE_TAG=runtimes make image/run             —— 起容器（数据落具名卷，端口只绑回环）
# 受限网络（镜像站 / Go 代理 / npm 镜像）直接覆盖下面的变量，例如：
#   make image NODE_IMAGE=docker.m.daocloud.io/library/node:24-bookworm \
#              GO_IMAGE=docker.m.daocloud.io/library/golang:1.26-bookworm \
#              RUNTIME_IMAGE=docker.m.daocloud.io/library/debian:bookworm-slim \
#              GO_PROXY=https://goproxy.cn,direct
NODE_IMAGE       ?= node:24-bookworm
GO_IMAGE         ?= golang:1.26-bookworm
RUNTIME_IMAGE    ?= debian:bookworm-slim
NPM_REGISTRY     ?= https://registry.npmjs.org
GO_PROXY         ?= https://proxy.golang.org,direct
DSH_VERSION      ?= latest
OPENCODE_VERSION ?= latest
IMAGE            ?= ark-forge
IMAGE_TAG        ?= local
FORGE_PORT       ?= 10001
WITH_DSH         ?= false
WITH_OPENCODE    ?= false

IMAGE_ARGS := --build-arg NODE_IMAGE=$(NODE_IMAGE) --build-arg GO_IMAGE=$(GO_IMAGE) \
              --build-arg RUNTIME_IMAGE=$(RUNTIME_IMAGE) --build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
              --build-arg GO_PROXY=$(GO_PROXY) --build-arg DSH_VERSION=$(DSH_VERSION) \
              --build-arg OPENCODE_VERSION=$(OPENCODE_VERSION)

image:
	docker build -t $(IMAGE):$(IMAGE_TAG) $(IMAGE_ARGS) .

# 含底座镜像：target 与开关必须一起给（只给开关会被最小镜像的守卫拒绝）
image/runtimes:
	docker build -t $(IMAGE):$(IMAGE_TAG)-runtimes --target forge-runtimes $(IMAGE_ARGS) \
	  --build-arg WITH_DSH=$(WITH_DSH) --build-arg WITH_OPENCODE=$(WITH_OPENCODE) .

image/run:
	docker run -d --name $(IMAGE)-$(IMAGE_TAG) \
	  -p 127.0.0.1:$(FORGE_PORT):10001 \
	  -v $(IMAGE)-$(IMAGE_TAG)-data:/data \
	  -e FORGE_ADDR=0.0.0.0:10001 -e FORGE_STORE=sqlite \
	  $(IMAGE):$(IMAGE_TAG)
	@echo "==> 面板 http://127.0.0.1:$(FORGE_PORT)（健康检查见 docker ps 的 health 列）"
