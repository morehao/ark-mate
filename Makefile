# ark-forge 仓库根构建编排
#
# 仓库结构（前后端平级 monorepo）：
#   backend/   —— 单 Go module：github.com/morehao/ark-forge（go.mod 在 backend/ 根）
#                 internal/ 分层目录即包边界；层目录保留 seam，未来可按需抽独立 module/仓
#   frontend/  —— 独立 pnpm workspace（apps/panel），产物可单独部署
#
# 交付形态：
#   make all   —— 单二进制：前端产物拷入 backend/internal/web/dist 后，
#                 以 -tags release 编译（启用 //go:embed），输出根目录 ./forge
#   make web   —— 仅构建前端并刷新 embed 宿主目录
#   make dev   —— 前后端联调单命令：先起后端 go run（127.0.0.1:10001），等 health 通过再起 vite dev（:5173），Ctrl-C 一起收
#   make server —— 仅后端开发起服（不起前端，面板经 vite dev proxy 联调）

.PHONY: dev web server build vet test all clean

FRONTEND_ROOT := frontend
WEB_DIST_DIR  := backend/internal/web/dist

# —— 前端工具链（Node / pnpm）——
# 唯一事实源在 frontend/ 内：Node 版本 = frontend/.nvmrc，pnpm 版本 = frontend/package.json 的
# packageManager 字段（pnpm >= 11 会在本机版本不一致时自动按其下载运行，无需 corepack）。
# make dev / make web 在调用 pnpm 前校验当前 node 主版本与 .nvmrc 一致（install 阶段由
# engines 兜底无条件失败，run 阶段由这里兜底），详见 README「前端工具链约定」。
NODE_VERSION_FILE  := $(FRONTEND_ROOT)/.nvmrc
NODE_VERSION       := $(shell sed 's/^v//' $(NODE_VERSION_FILE) 2>/dev/null)
NODE_VERSION_MAJOR := $(word 1,$(subst ., ,$(NODE_VERSION)))

# 运行前守卫（单行 shell，供 dev/web 作为独立 recipe 行调用）
define require-node
	@cur=$$(node -v 2>/dev/null | sed 's/^v//'); if [ "$${cur%%.*}" != "$(NODE_VERSION_MAJOR)" ]; then echo "==> Node 版本不符：frontend/.nvmrc 要求 Node $(NODE_VERSION_MAJOR).x（$(NODE_VERSION)），当前 $${cur:-未安装}"; echo "    团队统一 fnm，请先切换：fnm use $(NODE_VERSION_MAJOR)"; exit 1; fi
endef

# 前端产物 → embed 宿主（backend/internal/web/dist，不入库）
web:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm build
	rm -rf $(WEB_DIST_DIR)
	cp -r $(FRONTEND_ROOT)/apps/panel/dist $(WEB_DIST_DIR)

# 后端开发起服（forge 无默认子命令，需显式 serve）
server:
	cd backend && go run ./cmd/forge serve

# 前后端联调单命令：先起后端 go run serve（默认 127.0.0.1:10001），轮询
# /v1/forge/health 通过后再起前端 vite dev（:5173，/v1 代理到后端含 ws），
# 避免后端尚未 listen 时 vite ws 代理出现 ECONNREFUSED 的启动竞态。
# 后端编译/启动失败会立即退出并报错；任一进程退出 / Ctrl-C 时把两个进程一起收掉。
dev:
	$(require-node)
	@echo "==> 后端  go run ./cmd/forge serve  (默认 127.0.0.1:10001)"
	@trap 'kill 0 2>/dev/null; exit' INT TERM; \
	trap 'kill 0 2>/dev/null || true' EXIT; \
	cd backend && go run ./cmd/forge serve & \
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

# 单二进制：先 web（构建 + 拷贝 + embed），再以 -tags release 编译，输出仓库根 ./forge
all: web
	cd backend && go build -tags release -o ../forge ./cmd/forge

clean:
	rm -rf $(FRONTEND_ROOT)/apps/panel/dist $(WEB_DIST_DIR) forge
