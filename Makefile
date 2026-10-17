# ark-mate 仓库根构建编排
#
# 仓库结构（前后端平级 monorepo）：
#   backend/   —— 单 Go module：github.com/morehao/ark-mate（go.mod 在 backend/ 根）
#                 pkg/ 可复用基础设施（不 import internal/）+ internal/ 应用分层；
#                 分层边界由 backend/internal/archtest 守卫测试强制
#   frontend/  —— 独立 pnpm workspace（apps/web），产物可单独部署
#
# 交付形态：
#   make desktop/dist —— 桌面安装包（**主线交付**，决策 v8③）
#   make web   —— 只构建前端产物（apps/web/dist）
#   make dev   —— 开发循环：构建一次产物 → 重建 watcher → 核心服务**同一份产物**，Ctrl-C 一起收
#   make server —— 仅后端开发起服（不起前端产物；前端改走 `make dev` 服务同一份 dist）
#                  以上两条都认 `RUNTIME_BIN=<可执行文件>`：显式指定核心拉**哪一份**底座
#                  （本机装的那份与随包那份不同代时用；见下文「开发态底座用哪一份可执行文件」）
#   make desktop/dev —— 起桌面壳（Electron；M1 起壳会自己 spawn mate-core）
#   make image —— 容器镜像（Dockerfile 两个 target，默认最小镜像；见文件末尾「容器镜像」段）
#
# 前端资产只有一份（`frontend/apps/web/dist`）：核心**从不自带**资产，由调用方用
# `--web-dir` / `MATE_WEB_DIR` 指过去——桌面壳指包内那份、容器指镜像内那份、`make dev` 指工作区那份。
# （2026-10-10 起 `//go:embed` 与 `-tags release` 已删除，详见 `.dsh/docs/notes/2026-10-10-embed-removal-notes.md`。）

.PHONY: dev dev/runtime-bin web server web-assets desktop/dev desktop/accept desktop/payloads desktop/runtime-bin desktop/compliance core/desktop desktop/dist desktop/run desktop/dist-run verify/asset-parity verify/ui-product verify/engines verify/release-json verify/signature verify/hardened-runtime verify/compliance verify/desktop-dod build vet test clean image image/runtimes image/run

FRONTEND_ROOT := frontend
# 桌面随包核心的落点（apps/desktop/resources/ 已 gitignore；M4-1 的 electron-builder 从这里取）。
CORE_DESKTOP_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/mate-core

# 桌面包里那一份前端产物的落点（M2-6；壳与核心读同一份，M4-1 把它固定进 extraResources）。
DESKTOP_WEB_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/web
# 随包运行时载荷（生成物，`make desktop/payloads` 装配；dev 态**只读**它来判「命中是哪一代」）。
PAYLOADS_ROOT := $(FRONTEND_ROOT)/apps/desktop/resources/payloads

# 打好的包（`desktop/dist` 的产物）。electron-builder 按**宿主架构**分目录：arm64 → `mac-arm64`，
# x64 → `mac`（`desktop/dist` 只传 `--mac`，所以这里按 `uname -m` 推是准的）。
# `desktop/run` 与 `verify/hardened-runtime` 共用这一条路径，免得两处各写一遍、改一处忘一处。
DESKTOP_ARCH_DIR := $(if $(filter arm64 aarch64,$(shell uname -m)),mac-arm64,mac)
DESKTOP_APP := $(FRONTEND_ROOT)/apps/desktop/release/$(DESKTOP_ARCH_DIR)/ark-mate.app

# —— 桌面发布号（M4-1）——
# 版本的唯一事实源是 apps/desktop/package.json（npm 语义的应用版本）；核心的版本由 make 注入
# （internal/version 的注释写明"发布构建由 Makefile 注入"）。两者**必须相等**——发布号标识整套，
# build-release-json.mjs 会断言这一点，对不上就拒绝出包。
DESKTOP_VERSION := $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' $(FRONTEND_ROOT)/apps/desktop/package.json | head -1)
# 开发态（core/desktop）不注入 → 核心报 `dev`（internal/version 的口径）；只有 desktop/dist 注入。
CORE_VERSION ?=

# —— 底座构建期固定（产品约束：用户不能选 runtime，见 backend/pkg/runtime/build.go）——
# 不指定 = dsh（dsh）。它是**编译期**的 `-X` 注入，不是运行时配置、也不是环境变量：
# 换底座要重新构建（桌面/容器本来就要重打包）。
# 取值只做**词法**校验（存在性交给核心启动时的 ProductRegistry fail-closed）：远程 runtime 的 id
# 由 `.ark-mate/config/runtimes.json` 声明，这里写不出白名单，写死了会挡住合法的远程 id。
RUNTIME ?= dsh
ifneq ($(shell echo '$(RUNTIME)' | grep -Eq '^[a-z0-9][a-z0-9_-]*$$' && echo ok),ok)
$(error RUNTIME 取值非法：$(RUNTIME)（须是 [a-z0-9][a-z0-9_-]*；不指定则默认 dsh）)
endif
CORE_LDFLAG_ARGS = $(strip $(if $(CORE_VERSION),-X github.com/morehao/ark-mate/internal/version.Version=$(CORE_VERSION),) -X github.com/morehao/ark-mate/pkg/runtime.BuildRuntime=$(RUNTIME))
# ldflags 的**内层**单独留一个变量：`dev` 把它作为单个 argv 交给 `scripts/dev-loop.mjs`（那里要
# "一个元素一个空格"地把 -X 递给 `go run`，所以不能把 `-ldflags "..."` 这层壳一起传过去）。
CORE_LDFLAGS  = -ldflags "$(CORE_LDFLAG_ARGS)"

# 打包期下载缓存落仓库内（.cache/ 已 gitignore）：本机沙箱与 CI 都不该往 ~/Library/Caches 写。
# 只认 ELECTRON_BUILDER_CACHE——它是 electron-builder 26 的**唯一**缓存根（Electron 发行包解到
# <cache>/<release>/、app-builder 等工具解到 <cache>/downloads/），历史上的 ELECTRON_CACHE 那一支
# 在本版本里已经不看了。缓存住之后重复打包不再重新下载 Electron（约 110 MB）。
ELECTRON_BUILDER_CACHE ?= $(CURDIR)/.cache/electron-builder
# 受限网络（或企业内网镜像）可覆盖 Electron 发行包来源，与容器那节的 GO_PROXY/NPM_REGISTRY 同一套路：
#   ELECTRON_MIRROR=https://npmmirror.com/mirrors/electron/ make desktop/dist
# 默认空 = 用 electron-builder 的官方源；**缓存命中后不再联网**（第一次约 110 MB）。
ELECTRON_MIRROR ?=

# 前端产物目录（`--web-dir` 指向它）。
# 为什么 dev 也要显式给：核心**从不**内嵌资产（2026-10-10 起连 embed 都没有了），M2-1 的失败语义会让它拒绝启动
# （退出码 3）——那是为"独立二进制装错形态"设计的，dev 的正确做法就是明说自己用哪份资产。
# 目录不存在就先构建一次；vite dev 用的是内存产物，与这份 dist 互不影响。
WEB_DIR := $(CURDIR)/$(FRONTEND_ROOT)/apps/web/dist

# —— 前端工具链（Node / pnpm）——
# 唯一事实源在 frontend/ 内：Node 版本 = frontend/.nvmrc，pnpm 版本 = frontend/package.json 的
# packageManager 字段（pnpm >= 11 会在本机版本不一致时自动按其下载运行，无需 corepack）。
# make dev / make web 在调用 pnpm 前校验当前 node 主版本与 .nvmrc 一致（install 阶段由
# engines 兜底无条件失败，run 阶段由这里兜底），详见 README「前端工具链约定」。
NODE_VERSION_FILE  := $(FRONTEND_ROOT)/.nvmrc
NODE_VERSION       := $(shell sed 's/^v//' $(NODE_VERSION_FILE) 2>/dev/null)
NODE_VERSION_MAJOR := $(word 1,$(subst ., ,$(NODE_VERSION)))

# 后端持久层默认 sqlite（创建 ~/.ark-mate/mate.db，启用 runs/用量读模型）。
# 想要轻量纯内存可临时覆盖：MATE_STORE=memory make dev（环境变量优先于此处默认）。
MATE_STORE ?= sqlite

# 这里原先有 `DEV_ORIGINS`（把 vite :5173 登记进 M3-5 的 Origin 矩阵）。2026-10-11 起 `make dev`
# 的开发态**服务的就是构建产物**、页面与接口同源，不再有跨源请求，这个登记口随之删除。
# 机制本身没动：跨源开发 / 自建反代仍用 `MATE_ALLOWED_ORIGINS` 显式登记（README「运行配置要点」）。

# —— 开发态底座用哪一份可执行文件（可选；`make dev` / `make server` 认它）——
# 为什么需要这个入口：核心拉哪个底座是**写死的名字**（`pkg/runtime/<id>` 里的 `Command`，默认
# `dsh`），由 exec 走 PATH 解析。桌面态由壳把随包载荷注入私有 bin，而
# `make dev` / `make server` 这两条"核心自己从 PATH 找"的形态只会命中**本机装的那份**。
# 于是本机装的那份与仓库的声明/取证不是同一代时，这个人就成了"只有他的开发循环跑在另一代底座上"。
#
# **键名是 `RUNTIME_BIN`**（2026-08 引入时叫 `OPENCODE_BIN`——那会儿仓库里只有 opencode 那一个
# 一等底座；它下线后改回中性名，因为这个名字从一开始指的就是"构建期固定的那个底座"）：它替换的是**构建期固定的那个底座**（`$(RUNTIME)`，
# 默认 `dsh`），与具体是哪个底座无关。转发 shim 按 `$(RUNTIME)` 命名（`dev/runtime-bin`），
# 所以填了在**任何** `RUNTIME` 上都生效（这条曾经不成立：shim 名写死 `opencode`，构建期固定
# `dsh` 时填了它只是让 PATH 上多一个没人调用的文件——一个彻底的静默 no-op）。
#
#   make dev RUNTIME_BIN=/abs/path/to/<可直接执行的底座>
#
# **「可直接执行」是硬要求**（`dev/runtime-bin` 当场校验 `-x`）：随包载荷里如果是原生二进制形态，
# 直接指它即可；而**纯 JS 形态**的载荷（dsh 的入口是 `<载荷>/node_modules/@deepseek-ai/dsh/lib/bin.js`）
# 不能直接指——内核 exec 一个 `.js` 会 ENOEXEC，那种底座要走它自己的启动器。
#
# 也可以写进仓库根的 `.env`（本仓既有的本地配置文件：docker compose 读它、模板是 `.env.example`、
# 已在 .gitignore 里），这样不必每次手敲：
#
#   RUNTIME_BIN=/abs/path/to/<可直接执行的底座>
#
# 取值优先级：**命令行 > 进程环境变量 > `.env` > PATH 上解析**（与 dotenv 系工具的通行约定一致：
# `make dev RUNTIME_BIN=…` 临时盖过 `.env`，`.env` 里的值被同名环境变量盖过）。三种来源的症状
# 完全一样（都只是"某个路径生效了"），所以 `make dev`/`make server` 会**回显来源**：
# `[命令行]` / `[环境变量]` / `[.env]` / `PATH 上解析 [未指定 RUNTIME_BIN]`。
#
# 语义是**显式覆盖、不探测**：都没有 = 完全维持现状（PATH 上那份，行为与今天逐字相同，不做任何
# `which`/`command -v`）。有值就在**核心子进程**的 PATH 前置一个只含**构建期固定那个 runtime 名**
# 一个名字的转发目录（`.cache/dev-bin/`，已 gitignore）——**刻意不复用桌面私有 bin**：那目录里还有
# node/npx/dsh 三个启动器，整段前置会让 vite/pnpm 调到的 `node` 因 MATE_NODE_EXECUTABLE
# 未设置直接以 127 失败。前端进程不拿这个 PATH。
# 有值但不可执行 → 当场失败：静默忽略的症状是"以为换成了 v2、其实还在跑 v1"，那正是这里要消灭的。
# 隔离不受影响：核心拉底座时按 pkg/runtime/isolation.go 注入**该 adapter 自己声明**的隔离变量，
# 它的 home 落在 MATE_HOME 下，不碰用户自己的安装。也因此这里**不替用户探测版本**——
# 有的底座连 `--version` 都会写自己的 home（2026-10-10 dsh 实测），探一下就是动本机。
#
# `.env` 为什么是**只挑一个键**、而不是 `-include .env`：那个文件是 docker compose 的 dotenv，
# 里面的引号 / 多行值 / 保留键（PATH 之类）整份交给 make 解析，轻则串味重则让 make 直接语法错；
# 这里只要 RUNTIME_BIN 一行，其余键归 compose，互不干扰。支持 `KEY=v` / `KEY = v` /
# `export KEY=v` / 双引号包裹 / 行尾 `# 注释`；**单引号不支持**（会被当成值的一部分，随后在
# dev/runtime-bin 的存在性校验里带着原样值响亮失败，不静默）。
ENV_FILE ?= .env
RUNTIME_BIN_FROM_FILE := $(shell awk 'match($$0, /^[[:space:]]*(export[[:space:]]+)?RUNTIME_BIN[[:space:]]*=/) { v = substr($$0, RLENGTH + 1); sub(/^[[:space:]]+/, "", v); sub(/[[:space:]]+$$/, "", v); if (v ~ /^".*"$$/) { v = substr(v, 2, length(v) - 2) } else { sub(/[[:space:]]+\#.*$$/, "", v); sub(/[[:space:]]+$$/, "", v) }; out = v } END { print out }' $(ENV_FILE) 2>/dev/null)
RUNTIME_BIN ?= $(RUNTIME_BIN_FROM_FILE)
DEV_BIN_DIR  := $(CURDIR)/.cache/dev-bin
# 绝对化：文档里的写法是**仓库根相对**（`RUNTIME_BIN=frontend/apps/...`），而开发循环的 cwd 是
# `frontend/`——交给它相对路径会让"这个覆盖是不是随包那一份"判错（2026-10-11 实测：报成 external）。
# 与 `dev/runtime-bin` 里给 shim 写的那条绝对路径同源（同一个 cwd，即 make 被调用的地方）。
RUNTIME_BIN_ABS := $(if $(RUNTIME_BIN),$(abspath $(RUNTIME_BIN)),)
# 来源的唯一取法：make 的 $(origin)（command line / environment / file / undefined）。
RUNTIME_BIN_SOURCE := $(if $(RUNTIME_BIN),$(if $(filter command line,$(origin RUNTIME_BIN)),命令行,$(if $(filter environment,$(origin RUNTIME_BIN)),环境变量,$(if $(filter file,$(origin RUNTIME_BIN)),$(ENV_FILE),其它))),未指定)
# 只有有值才前置 PATH（空值 = 连赋值都不做，保持"今天就是这样"）。
DEV_PATH_PREFIX := $(if $(RUNTIME_BIN),$(DEV_BIN_DIR):,)

# 回显「这次会拉起哪一份底座」的**原始事实**（路径 + 来源）。
#
# 解释与警告**不在这里**：`make dev` 由 `scripts/dev-loop.mjs` 统一报（它能读磁盘上的
# `payload.json`，于是能说出「命中版本 / 随包版本 / 是否同代」，并把同一份事实交给核心的
# `--payloads`）。这里只留原始事实，供 `make server`（不起开发循环）使用。
#
# 那个「未生效」分句已于 2026-10-12 删除（根因已修）：shim 现在按 `$(RUNTIME)` 命名
# （`dev/runtime-bin`），所以 `RUNTIME_BIN=…` 在**任何**构建上都生效，不再需要警告。
# 它当时挡的是一类**静默 no-op**：shim 名写死 `opencode`，本构建固定 `dsh` 时那个覆盖只是让
# PATH 上多了一个没人调用的可执行文件，而回显会把那个路径当「底座」报出来（看的人以为换掉了）。
define runtime_hit_line
$(if $(RUNTIME_BIN),$(RUNTIME_BIN) [$(RUNTIME_BIN_SOURCE)] —— 转发 $(RUNTIME)（shim 名随构建期 runtime）,PATH 上解析 [未指定 RUNTIME_BIN])
endef

# 运行前守卫（单行 shell，供 dev/web 作为独立 recipe 行调用）
define require-node
	@cur=$$(node -v 2>/dev/null | sed 's/^v//'); if [ "$${cur%%.*}" != "$(NODE_VERSION_MAJOR)" ]; then echo "==> Node 版本不符：frontend/.nvmrc 要求 Node $(NODE_VERSION_MAJOR).x（$(NODE_VERSION)），当前 $${cur:-未安装}"; echo "    团队统一 fnm，请先切换：fnm use $(NODE_VERSION_MAJOR)"; exit 1; fi
endef

# 前端产物（`frontend/apps/web/dist` 是**唯一一份**，没有第二个落点）。
#
# 2026-10-10 起不再往 `backend/pkg/web/dist` 拷一份给 embed 用：核心从不自带资产，
# 需要资产的形态各自用 `--web-dir` / `MATE_WEB_DIR` 指过来（桌面壳指包内那份、容器指镜像内那份）。
web:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm build

# 桌面随包核心 `mate-core`：壳 spawn 它（`:0` + 双凭据经 stdin + 就绪行回写）。
#
# 资产由壳服务、核心按 `--web-dir` 读同一份目录（设计 §6.1 最后一条：桌面包里只有一份
# apps/web/dist，核心从不内嵌副本）。
# 落后端产物路径 `apps/desktop/resources/` 已 gitignore（生成物）。
core/desktop: web desktop/runtime-bin
	@# CGO_ENABLED=0：持久层用的是 golib 的 SQLite dialect，v1.32.22 起是**纯 Go** 的
	@# ncruces/go-sqlite3（SQLite 编成 wasm 再翻译回 Go），所以不需要 C 工具链（桌面核在
	@# macOS 上本来就只有系统库，收益体现在 linux 目标：静态产物、交叉编译不必先有 C 交叉
	@# 工具链）。历史别记错：2026-10-10 这里的 0 曾经**编得出、连库即死**（那时的 dialect 是 CGO 的
	@# mattn/go-sqlite3，0 会注册 stub，运行期报 `go-sqlite3 requires cgo to work`），
	@# 于是 2026-10-11 一律改成 1 并接受动态链接。驱动换成纯 Go 之后，0 重新是**正确**取值
	@# ——但"0 安全"这件事的前提是依赖图里没有 CGO 驱动，所以两侧都由
	@# internal/archtest 的 TestSqliteDriverStaysPureGo 钉着（谁把 golib 降级回去，它会红）。
	cd backend && CGO_ENABLED=0 go build $(CORE_LDFLAGS) -o ../$(CORE_DESKTOP_OUT) ./cmd/mate
	@# 运行时树里那一份前端产物（M2-6）：壳服务它、核心用 --web-dir 读**同一份**。
	@# 只有一份 dist 是设计 §6.1 的红线——核心从不自带资产（2026-10-10 起连 `//go:embed`
	@# 都删了），否则桌面态会出现两份资产，版本闸门（M2-4）也就无从判定混搭。
	rm -rf $(DESKTOP_WEB_OUT)
	mkdir -p $(DESKTOP_WEB_OUT)
	cp -R $(FRONTEND_ROOT)/apps/web/dist/. $(DESKTOP_WEB_OUT)/

# 私有 bin 启动器（M3-2）：生成 resources/runtime/bin/{node,npx} 加每个随包底座的入口（各含 POSIX 与 .cmd 两版）。
# 幂等、瞬时（内容逐字节确定，写完即静默），所以挂在 core/desktop 上——桌面态一旦缺它，
# 子进程 PATH 首段就是一条不存在的路径，症状会退化成"运行时莫名起不来"。
desktop/runtime-bin:
	$(require-node)
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/gen-runtime-bin.mjs

# 运行时载荷装配（M3-1）：把 dsh 的生产依赖闭包装到
# apps/desktop/resources/payloads/（本仓 .gitignore 的生成物面）。
#
# **故意不进 `core/desktop`**：载荷是几百 MB 的磁盘 + 冷缓存下要联网，而日常开发
# （make dev / desktop/accept）根本用不到它——只有真正打包（M4）才需要。
# 目标平台默认跟宿主，交叉装配用 PLATFORM=/ARCH= 覆盖（平台化二进制按这两个值选）。
desktop/payloads:
	$(require-node)
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/prepare-runtime.mjs \
		$(if $(PLATFORM),--platform $(PLATFORM),) $(if $(ARCH),--arch $(ARCH),)

# 合规生成物（M4-5，设计 §11）：`resources/compliance/{THIRD-PARTY-NOTICES.txt,sbom.cdx.json}`。
# 从 release.json + 载荷清单（payload.json）+ 随包依赖树 + 锁文件生成；两个文件都是**生成物**
# （C9：字段序固定、无时间戳、原子写），**不手改**——`make verify/compliance` 会逐字节复算。
# 需要载荷已在场（`make desktop/payloads`）与 release.json（`make desktop/dist` 会先生成它）。
desktop/compliance:
	$(require-node)
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/gen-compliance.mjs

# 桌面壳开发起服：先出 mate-core，再构建主进程 / preload（esbuild → dist/*.cjs）后起 Electron。
# 窗口进 `mate-app://app/`（前端产物来自 apps/web/dist），REST 由壳反代核心。
# 与 make dev 的区别：那条起的是浏览器入口（vite :5173 + 后端 :10001），这条起的是桌面入口。
# 依赖 `desktop/payloads`：M3-4 起壳在**启动期**解析随包载荷，缺了就拒绝启动（不回落 PATH，设计 §6.5），
# 所以开发态也必须先装配载荷——否则 `make desktop/dev` 会以 boot-failed 页告终，而不是悄悄用机器上那份。
desktop/dev: core/desktop desktop/payloads
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm -F @ark-mate/desktop dev

# 桌面入口界面/契约验收（真 Electron + 真核心，脚本自己起壳、自造自清；**不进 CI**）：
# 与 `pnpm ui:accept`（浏览器入口，需先 make dev）互为两靶；覆盖 M1 的 scheme/契约/加固/凭据/事件流/退出六组。
# 依赖 `desktop/payloads`：M3-2 起验收会断言"私有 bin 跑起随包载荷"（nodechannel 场景），
# 载荷不在场这条只能是红的——与其让人猜，不如让依赖显式化（冷缓存下这一步要联网，见该目标注释）。
desktop/accept: core/desktop desktop/payloads
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop

# 桌面打包（M4-1）：prepare-runtime → core/desktop（带版本注入）→ release.json → electron-builder → 逐项复核。
#
# 顺序不能换：release.json 必须在 electron-builder **之前**生成（它经 extraResources 进包，"包内那份"
# 的唯一来源就是它），而它的每个值都取自已经产出的资产 / 载荷 / 核心二进制（见 build-release-json 的纪律 2）。
# CORE_VERSION 是 target-specific 变量（对 core/desktop 生效）：出包的核心必须带发布号，
# 否则 build-release-json 会以"核心报 dev"直接失败。
#
# 交付物两种（§12.27）：release/ark-mate-<ver>-macos-<arch>.tar.gz 是**分发**形态（非浏览器渠道，
# 不带 quarantine）；同目录的 .dmg 只作本地自测。签名是 ad-hoc（无证书，identity: "-"）；
# 加固运行时与公证分别是 M4-2a / M4-2b 的范围。
# 当前只出 macOS（`--mac`）：Windows 的 NSIS 分支在 M4-3 接（那时这条 recipe 再按平台分叉）。
desktop/dist: CORE_VERSION = $(DESKTOP_VERSION)
desktop/dist: core/desktop desktop/payloads
	$(require-node)
	@echo "==> release.json（appVersion=$(DESKTOP_VERSION)）"
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/build-release-json.mjs
	@echo "==> 合规生成物（M4-5：THIRD-PARTY-NOTICES + CycloneDX SBOM）"
	@# 必须在 electron-builder **之前**：它们经 extraResources 进包（`<Resources>/compliance/`），
	@# 而"包内那份"的唯一来源就是这里产出的文件。
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/gen-compliance.mjs
	@echo "==> electron-builder --mac（缓存 $(ELECTRON_BUILDER_CACHE)$(if $(ELECTRON_MIRROR)，镜像 $(ELECTRON_MIRROR),)）"
	cd $(FRONTEND_ROOT)/apps/desktop && ELECTRON_BUILDER_CACHE=$(ELECTRON_BUILDER_CACHE) $(if $(ELECTRON_MIRROR),ELECTRON_MIRROR=$(ELECTRON_MIRROR),) pnpm exec electron-builder --mac
	@echo "==> 签名事实逐对象复核（M4-2a；hardened runtime 形态的判据）"
	cd $(FRONTEND_ROOT) && pnpm verify:signature
	@echo "==> 逐项复核 release.json 与产物（含 tar.gz 解包后的首启）"
	@# 直接调 node（不经 pnpm 转发参数）：`pnpm run x -- --flag` 会把 `--` 原样交给脚本，
	@# 而 node 的 parseArgs 视开头的 `--` 为"选项结束"——旗标随即变成位置参数并抛错（实测踩到）。
	cd $(FRONTEND_ROOT) && node apps/desktop/scripts/verify-release-json.mjs --require-artifacts --launch
	@echo "==> 包内合规文件复核（M4-5 / V14：包内 NOTICES 与 SBOM 与源文件逐字节相同）"
	cd $(FRONTEND_ROOT) && node apps/desktop/scripts/verify-compliance.mjs --require-artifacts
	@# 最后一条：**包内载荷真起一次**（M4-2a 的 ②）：判的是签名 + hardened runtime 之后包里的
	@# 随包底座还能不能被核心拉着走完 ACP 握手。它是"verify-release-json --launch"的加强版
	@# （那条只到"界面渲染出来 + 核心可达"）。
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop:packaged

# 打开打好的包（`desktop/run` 与 `desktop/dist-run` 共用这一段，守卫只写一处）。
define open-desktop-app
	@command -v open >/dev/null 2>&1 || (echo "==> 只支持 macOS（用 open 起 .app）；Windows/Linux 请等 M4-3 的出包形态"; exit 1)
	@test -d $(DESKTOP_APP) || (echo "==> 还没有打好的包：$(DESKTOP_APP)"; echo "    先跑 make desktop/dist（当前版本 $(DESKTOP_VERSION)）"; exit 1)
	@# 数据面提醒：桌面包起的核心与 `make dev` / `make desktop/dev` 的核共用同一个 `~/.ark-mate/mate.db`，
	@# 两个核同时活着会让"这个改动到底生效了没"变玄学（脚本化靶子如 `ui:accept:desktop` 自带清场守卫）。
	@echo "==> 打开 $(DESKTOP_APP)（它自己起核；别同时跑 make dev / desktop/dev——共用 ~/.ark-mate/mate.db）"
	@open $(DESKTOP_APP)
endef

# 启动**打好的包**（人工看界面用）：等价于在访达里双击 `release/<arch>/ark-mate.app`。
# 壳自己解析包内载荷、自己起包内核心（`:0` + 双凭据经 stdin），跟开发态是同一套路径，
# 区别只在"全都来自包内"——所以它也是看"签名形态下跑起来是什么样"的最短路径。
#
# 刻意**不**依赖 `desktop/dist`：出包是分钟级的 electron-builder + 数百 MB 载荷，误触代价太大，
# 包不在场就给一条明确的出路。要连打包一起：`make desktop/dist-run`。
desktop/run:
	$(open-desktop-app)

# `desktop/dist` + `desktop/run` 合体：打完直接开。给人用的路径——脚本化验证别用它（会真弹窗口）。
desktop/dist-run: desktop/dist
	$(open-desktop-app)

# 资产一致性（M2-5 / V17）：同一个请求经壳（mate-app://）与核心（回环直连）各取一次，
# 逐项比较 status / Content-Type / Cache-Control / 响应体 sha256。桌面态的两条服务路径由
# 不同代码实现（TS vs Go），只有这条能锁住"行为一致"。
verify/asset-parity:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm asset-parity

# 产物靶界面验收（V-a，2026-10-11 起）：验的就是 `make dev` / 交付件形态服务的那**同一份产物**
# （`frontend/apps/web/dist`）。**前置：`make dev` 已在跑**——脚本刻意不起服务，它验的是"人正在看的
# 那个实例"；开跑前有一条启动断言逐字核对"这个靶给的 index.html 就是本地 dist 那份、哈希资产真取得
# 到、`/meta.webPath` 指的就是它"，指到"从源码现编译"的靶上会**当场红**（那正是 2026-10-11 之前
# 的老形态：验过的界面从未在交付件形态上跑过）。
# 它**不并进 `verify/desktop-dod`**：那条要的是"源码态、自起壳、不依赖已在跑的服务"，与本条的
# 前置互斥（真同时跑还会出现两个核心抢同一个 `~/.ark-mate/mate.db`）。桌面壳那条 lane 是
# `pnpm ui:accept:desktop`（在 DoD 里）。
verify/ui-product:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm ui:accept

# engines / ABI 打包期断言（M3-7）：桌面态的载荷 JS 跑在 **Electron 自带的 Node** 上
# （`ELECTRON_RUN_AS_NODE=1`，见 M3-2 的私有 bin），所以要按 semver 比对载荷声明的
# `engines.node` 与 Electron 的真实 Node 版本，并断言 `payload.json` 的 nodeAbi 与实际 ABI
# 一致（不一致 = 原生 .node 到运行期才炸）。**Electron 主版本升级必须重跑这条。**
# 需要载荷已在场（`make desktop/payloads`）；不需要起壳、不碰 ~/.ark-mate。
verify/engines:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:engines

# 合规门禁（M4-5 / V14）：判 `resources/compliance/` 的两份生成物与 release.json + 载荷清单
# + 锁文件逐项一致——① 逐字节复算（抓手改与"改了输入没重生成"）② 组件版本交叉校验
# ③ SBOM 形状（CycloneDX）④ 包内那份（`--require-artifacts`，只在 desktop/dist 里跑）。
# 需要生成物已在场（`make desktop/compliance` 或 `make desktop/dist`）与载荷树。
verify/compliance:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:compliance

# release.json 逐项复核（M4-1）：值一律**复算**，不问脚本自己报了什么——
#   字段集与键序（唯一发布契约的形状）→ coreVersion/apiContract 重问核心二进制 → webBuildId 重读
#   `resources/web/build-id.txt` → webDigest 重算整棵资产树并与核心经 `/meta` 回报的值对照
#   （Go 与 JS 两套实现必须给出同一个值 = 版本闸门的前提）→ electron/node/nodeAbi 重跑 M3-7 的断言
#   → 载荷 name/version/digest 重读 payload.json。
# 需要核心与资产已在场（`make core/desktop`）；不打包也能跑，不需要载荷。
# 一条口径（2026-10-09）：`coreVersion` **只在打包档判**——源码态的 `resources/mate-core` 按设计
# 不注入版本号（报 `dev`），与声明的发布号没有可比对象，此时打信息行而不是判红；发布版本一致性的
# 唯一真判据是 `desktop/dist` 收尾那条 `--require-artifacts --launch`。别把这条读成放宽。
# `--require-artifacts --launch`（desktop/dist 收尾自动跑）：再加"包内那份 release.json 与源文件
# 逐字节相同""tar.gz 解到干净目录后布局完整、且壳能起来"。
verify/release-json:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:release-json

# 签名事实逐对象复核（M4-2a）：`codesign --verify --deep --strict`（封条完整）+ 包里每个 Mach-O
# 都有签名且带 `--options runtime` + identity 与 release.json 声明一致 + entitlements 真的签进去了。
# `spctl -a -vvv -t exec` 的结论如实打印但**不判失败**（ad-hoc 必然被 Gatekeeper 拒，这是 §12.27 的预期）。
# 默认判 `release/<arch>/ark-mate.app`（即 `$(DESKTOP_APP)`）；也可 `--from-tarball` 判解包后的那一份（分发形态）。
verify/signature:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:signature

# 加固运行时下的**包内执行**（M4-2a）：设计 §6.3 回退条件 ② 的判据。
# 与 `pnpm smoke:desktop` 的唯一区别：壳从**打出来的包**里起（`.app/Contents/MacOS/ark-mate`，
# ad-hoc 签名 + `--options runtime`），核心与其下的随包底座也全部来自包内。
# 需要先有包（`make desktop/dist`）；签名形态本身由 `verify/signature` 判，这条判"能不能跑"。
# 2026-10-09 起再加一条：**包内能力投放**（`ui:accept:desktop-skills:packaged`）——"能不能跑"之后
# 还要问"跑起来有没有把该给底座的给到"（skill 到底进没进包内 dsh 的模型请求体）。
verify/hardened-runtime:
	$(require-node)
	@test -d $(DESKTOP_APP) || (echo "==> 没有打好的包：先跑 make desktop/dist"; exit 1)
	cd $(FRONTEND_ROOT) && pnpm verify:signature
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop:packaged
	# 能力投放的**发布形态**判据（A-1 那一半）：V9 只判"包内两份载荷能握手"，判不到"平台物化的
	# skill 有没有真的进包内 dsh 的模型请求体"。而包内有四件东西是源码态看不见的——私有运行时
	# 启动器在**打包时重新生成**（隔离表就在那个生成物里）、核心与载荷都从包内拉起、hardened
	# runtime 真的生效、探针的 HOME/MATE_HOME 重定向在包内壳上同样成立。所以它挂在这条链上，
	# 而不是挂进 `verify/desktop-dod`（DoD 是源码态、不要求磁盘上有包）。
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop-skills:packaged
	# 不依赖宿主机环境（设计 R2 / M4-6）的**发布形态**判据：包内壳 + 包内核心 + 包内载荷，在
	# 最小 PATH（诱饵 + 系统目录）与私有 HOME 下跑完"一轮 → 退出 → 重开续聊"。包里有四件源码态
	# 看不见的东西（打包时重新生成的私有启动器、包内核心、hardened runtime、包内重启后的会话续聊），
	# 所以它与上面那条一样挂在这条链上，而不是挂进源码态的 DoD。
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop-nohost:packaged

# 桌面「不依赖宿主机环境」（设计 R2 / 计划 M4-6，2026-10-10）：
#   - 静态那一半：`internal/archtest` 的 `TestDesktopRuntimeMustNotBindHostToolPaths`（进包且运行期
#     生效的代码里不得出现宿主工具绑定）。它管"**有没有**这条路"；下面的动态那一半管"**今天这条路
#     有没有被走到**"。少了任何一半结论都不成立。
#   - 动态那一半：最小 PATH（诱饵目录 + `/usr/bin:/bin`；诱饵是会写标记文件的假 dsh/node/npx）
#     + 私有 HOME/MATE_HOME 下起真壳 + 真核心 + **真 dsh 子进程**，跑完「建会话 → 一轮 → 退出 →
#     重开 → 续聊一轮」，并断言诱饵一次都没被调到。
#   - 与 2026-10-10 之前的 `make verify/clean-vm`（干净 VM 首启）的关系：判据改成"打包态 + 最小 PATH"
#     后**不再需要一台干净机器**（宿主环境是被 PATH/HOME 剥掉的，不是被换掉的），VM 降为可选佐证。
#   - 打包态那一半不进 DoD（DoD 是源码态、不要求磁盘上有包），挂 `verify/hardened-runtime`。
verify/no-host-dep:
	$(require-node)
	cd backend && go test ./internal/archtest/ -run TestDesktopRuntimeMustNotBindHostToolPaths -count=1
	cd $(FRONTEND_ROOT) && pnpm verify:no-host

# 桌面 DoD 汇总（计划 §0.1 的 `make verify/desktop-dod`）：把可脚本化的验收串成一条命令，
# 里程碑 Gate 与改动收口都跑它。M5-5 会把 V1–V18 中其余可脚本化的部分也并进来。
verify/desktop-dod:
	$(require-node)
	cd backend && go test ./internal/archtest/ -count=1
	cd $(FRONTEND_ROOT) && pnpm typecheck
	cd $(FRONTEND_ROOT) && pnpm test:contract
	cd $(FRONTEND_ROOT) && pnpm asset-parity
	cd $(FRONTEND_ROOT) && pnpm verify:engines
	# V14（M4-5）：随包 NOTICES 与 SBOM 与 release.json 逐项一致（生成物逐字节复算 + 交叉校验）。
	# 这里的 `--require-artifacts`（包内那份）需要先出包，属 `make desktop/dist` 的收尾；DoD 跑源码侧。
	cd $(FRONTEND_ROOT) && pnpm verify:compliance
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop
	# 能力投放（M8 的桌面侧判据）：真 Electron 壳 + 真核心 + **真 dsh 子进程**，验"平台物化的 skill
	# 落在底座真正读的那个根上、且底座的模型请求体里真看见它"。模型端点用**脚本内置的假 LLM**
	# （零额度、零凭证），探针 HOME/MATE_HOME 全在仓库内、收尾核对用户真实目录零变化。
	# 为什么不并进 `ui:accept:desktop`：那条只打壳/契约/加固/凭据/事件流/退出六组，**看不见能力投放**
	# ——2026-10-09 正是这条才发现"核心侧修好了、桌面私有启动器仍把 dsh 的 skill 读根撬走"。
	# 前置与上面两条同款：`core/desktop`（脚本自己重建）与 `desktop/payloads`（壳启动期就要，缺了拒启）。
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop-skills
	# R2（M4-6，2026-10-10）：最小 PATH + 诱饵 + 私有 HOME/MATE_HOME 下起真壳 + 真核心 + **真 dsh
	# 子进程**，跑完「一轮 → 退出 → 重开续聊」，并断言诱饵一次都没被调到。静态那一半（archtest 第
	# 21 条）已由本目标开头那次 `go test ./internal/archtest/` 覆盖；打包态那一半挂 `verify/hardened-runtime`。
	cd $(FRONTEND_ROOT) && pnpm verify:no-host
	# V9（M3-8）：桌面态真起两份载荷（私有 bin + Electron 的 Node + 隔离环境）+ 一条"载荷目录零变化"
	# 的指纹断言。它**不联网、不跑模型**，所以要真模型会话的那一档（RUNTIME_SMOKE_LIVE=1）不在这里。
	# 三条回退条件里 ②③ 要 M4 的签名/公证/Windows 出包，脚本每次都显式打印"本脚本不判"。
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop

# dev 用的资产就绪（不存在则构建一次）
$(WEB_DIR)/index.html:
	@echo "==> 前端产物不存在，先构建一次（后端 dev 需要 --web-dir 指向它）"
	cd $(FRONTEND_ROOT) && pnpm build

web-assets: $(WEB_DIR)/index.html

# 开发态底座转发（见上方 RUNTIME_BIN 一节）：校验 + 生成一个**只转发 $(RUNTIME)** 的 shim。
#
# **shim 的名字必须是构建期固定的那个 runtime 名**（`$(RUNTIME)`）——核心拉底座用的是写死的名字
# 经 `exec` 走 PATH 解析；名字对不上，这个覆盖就是个没人调用的文件（2026-10-12 之前的形态）。
# 幂等；RUNTIME_BIN 为空时什么都不做（也不清理旧 shim——它不在 PATH 上，留着无害）。
dev/runtime-bin:
	@if [ -n "$(RUNTIME_BIN)" ]; then \
	  if [ ! -f "$(RUNTIME_BIN)" ]; then echo "==> RUNTIME_BIN 不是普通文件：$(RUNTIME_BIN)"; exit 1; fi; \
	  if [ ! -x "$(RUNTIME_BIN)" ]; then echo "==> RUNTIME_BIN 不可执行：$(RUNTIME_BIN)"; exit 1; fi; \
	  abs=$$(cd "$$(dirname "$(RUNTIME_BIN)")" && pwd)/$$(basename "$(RUNTIME_BIN)"); \
	  mkdir -p $(DEV_BIN_DIR); \
	  printf '#!/bin/sh\n# 生成物（Makefile 的 dev/runtime-bin）——不要手改；改 RUNTIME_BIN 重新生成。\nexec "%s" "$$@"\n' "$$abs" > $(DEV_BIN_DIR)/$(RUNTIME); \
	  chmod +x $(DEV_BIN_DIR)/$(RUNTIME); \
	fi

# 后端开发起服（mate 无默认子命令，需显式 serve）
server: web-assets dev/runtime-bin
	@echo "==> 底座  $(RUNTIME) [构建期固定；改它要重新构建]"
	@echo "==> 命中  $(runtime_hit_line)"
	@echo "     （版本与「和随包是否同代」在 make dev 与验收里报：核心**不从本机探测**版本——底座连 --version 都写自己的 home）"
	cd backend && PATH=$(DEV_PATH_PREFIX)$$PATH MATE_STORE=$(MATE_STORE) go run $(CORE_LDFLAGS) ./cmd/mate serve --web-dir $(WEB_DIR)

# 开发循环（2026-10-11 重做，对齐参照实现 DSH 的 `scripts/dev-web.ts`）：
# **构建一次 → 两个常驻 watcher 重建 → 核心服务同一份构建产物**，浏览器开 http://127.0.0.1:10001。
#
# 这条目标里不再有 vite dev server，这是重做的**全部理由**：dev server 从源码现编译，与交付的
# `apps/web/dist` 不是同一次构建——于是"人判断有没有问题时依据的资产"与"用户加载的资产"分叉
# （CSP 三条限制不下发、`build-id.txt` 不存在所以版本闸门只能判"不可判定"、antd 会多打
# `css-dev-only-*` 类名、React 的开发期警告还在）。人看的必须是产物；`apps/web/vite.config.ts`
# 已在 `serve` 上直接报错兜住第二条路（照抄 DSH 的 `rejectStandaloneServe`）。
#
# 分工：Makefile 管**环境装配**（require-node / RUNTIME_BIN 转发目录与 PATH / MATE_STORE /
# ldflags 的**内层**），`scripts/dev-loop.mjs` 管**进程编排**（阶段顺序、健康检查、阶段自死即回红、
# 按进程组收尾）。各做一件，不把同一份环境判定实现两遍。
dev: dev/runtime-bin
	$(require-node)
	@echo "==> 后端  go run ./cmd/mate serve  (默认 127.0.0.1:10001, store=$(MATE_STORE), runtime=$(RUNTIME))"
	cd $(FRONTEND_ROOT) && \
	  PATH=$(DEV_PATH_PREFIX)$$PATH MATE_STORE=$(MATE_STORE) \
	  MATE_DEV_WEB_DIR=$(WEB_DIR) MATE_DEV_CORE_DIR=$(CURDIR)/backend \
	  MATE_DEV_CORE_LDFLAG_ARGS='$(CORE_LDFLAG_ARGS)' MATE_DEV_RUNTIME=$(RUNTIME) \
	  MATE_DEV_RUNTIME_BIN='$(RUNTIME_BIN_ABS)' MATE_DEV_RUNTIME_BIN_SOURCE='$(RUNTIME_BIN_SOURCE)' \
	  MATE_DEV_PAYLOADS_ROOT=$(CURDIR)/$(PAYLOADS_ROOT) \
	  exec node scripts/dev-loop.mjs

# 全仓只有这一个构建配置：不带任何构建标签，不依赖前端产物（核心从不自带资产）。
#
# 它也认 `RUNTIME`（注入 CORE_LDFLAGS）：不然 `make build RUNTIME=<未注册 id>` 会**静默什么都不做**
# ——那正是本仓明令要防的"写了、跑了、没效果"。取值是否存在由核心启动时的 ProductRegistry 判定。
build:
	cd backend && go build $(CORE_LDFLAGS) ./...

# vet / test **刻意不注入** RUNTIME：测试进程内自己设 `runtime.BuildRuntime`（见各包夹具），
# 注入只会让"默认值是 dsh"这条判据在测试里消失。
vet:
	cd backend && go vet ./...

test:
	cd backend && go test ./...

# test-race：两处"只在 -race 下才看得见"的读面（会话快照 / 执行面键），收尾与改动它们时跑。
test-race:
	cd backend && go test -race ./internal/domain/session/ ./internal/service/svcsession/ ./internal/runtimeconn/

clean:
	rm -rf $(FRONTEND_ROOT)/apps/web/dist

# —— 容器镜像（Dockerfile 多阶段：前端 → Go 编译 → 精简运行时）——
#   make image                                    —— 最小镜像（只含 mate 本体）
#   make image/runtimes WITH_DSH=true
#                                                 —— 额外内置 dsh（开关可置 false）
#                                                     产物 tag 由 RUNTIMES_TAG 决定（默认 ark-mate:runtimes）
#   IMAGE_TAG=runtimes make image/run             —— 起容器（数据落具名卷，端口只绑回环）
# 前端资产由镜像内的 web 阶段构建后 COPY 进镜像（`MATE_WEB_DIR=/usr/local/share/mate/web`），
# 二进制本身不带资产（2026-10-10 起）。
#   make image                                    —— 最小镜像（只含 mate 本体）
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
IMAGE            ?= ark-mate
IMAGE_TAG        ?= local
# 含底座镜像的 tag 单独一个变量：`make image/runtimes` 与 `make image/run IMAGE_TAG=runtimes`
# 是文档里成对写的两条命令，若在这里复用 IMAGE_TAG，默认会产出 `ark-mate:local-runtimes`
# 而 run/文档找的是 `ark-mate:runtimes`——开箱即用不了（实测踩到）。
RUNTIMES_TAG     ?= runtimes
MATE_PORT       ?= 10001
WITH_DSH         ?= false

# `MATE_RUNTIME` 走全局的 $(RUNTIME)（默认 dsh）——镜像里**核心认哪一个底座**；
# WITH_DSH 决定镜像里**装哪几份底座二进制**，两者是两件事（见 Dockerfile 头注释）。
IMAGE_ARGS := --build-arg NODE_IMAGE=$(NODE_IMAGE) --build-arg GO_IMAGE=$(GO_IMAGE) \
              --build-arg RUNTIME_IMAGE=$(RUNTIME_IMAGE) --build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
              --build-arg GO_PROXY=$(GO_PROXY) --build-arg DSH_VERSION=$(DSH_VERSION) \
              --build-arg MATE_RUNTIME=$(RUNTIME)

image:
	docker build -t $(IMAGE):$(IMAGE_TAG) $(IMAGE_ARGS) .

# 含底座镜像：target 与开关必须一起给（只给开关会被最小镜像的守卫拒绝）
image/runtimes:
	docker build -t $(IMAGE):$(RUNTIMES_TAG) --target mate-runtimes $(IMAGE_ARGS) \
	  --build-arg WITH_DSH=$(WITH_DSH) .

image/run:
	docker run -d --name $(IMAGE)-$(IMAGE_TAG) \
	  -p 127.0.0.1:$(MATE_PORT):10001 \
	  -v $(IMAGE)-$(IMAGE_TAG)-data:/data \
	  -e MATE_ADDR=0.0.0.0:10001 -e MATE_STORE=sqlite \
	  $(IMAGE):$(IMAGE_TAG)
	@echo "==> 面板 http://127.0.0.1:$(MATE_PORT)（健康检查见 docker ps 的 health 列）"
