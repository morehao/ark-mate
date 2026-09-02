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
#   make server —— 后端开发起服（不起前端，面板经 vite dev proxy 联调）

.PHONY: web server build vet test all clean

FRONTEND_ROOT := frontend
WEB_DIST_DIR  := backend/internal/web/dist

# 前端产物 → embed 宿主（backend/internal/web/dist，不入库）
web:
	cd $(FRONTEND_ROOT) && pnpm build
	rm -rf $(WEB_DIST_DIR)
	cp -r $(FRONTEND_ROOT)/apps/panel/dist $(WEB_DIST_DIR)

# 后端开发起服
server:
	cd backend && go run ./cmd/forge

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
