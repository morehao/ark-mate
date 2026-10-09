# ark-forge 仓库根构建编排
#
# 仓库结构（前后端平级 monorepo）：
#   backend/   —— 单 Go module：github.com/morehao/ark-forge（go.mod 在 backend/ 根）
#                 pkg/ 可复用基础设施（不 import internal/）+ internal/ 应用分层；
#                 分层边界由 backend/internal/archtest 守卫测试强制
#   frontend/  —— 独立 pnpm workspace（apps/web），产物可单独部署
#
# 交付形态：
#   make all   —— 单二进制：前端产物拷入 backend/pkg/web/dist 后，
#                 以 -tags release 编译（启用 //go:embed），输出根目录 ./forge
#   make web   —— 仅构建前端并刷新 embed 宿主目录
#   make dev   —— 前后端联调单命令：先起后端 go run（127.0.0.1:10001），等 health 通过再起 vite dev（:5173），Ctrl-C 一起收
#   make server —— 仅后端开发起服（不起前端，前端经 vite dev proxy 联调）
#   make desktop/dev —— 起桌面壳（Electron；M1 起壳会自己 spawn forge-core）
#   make image —— 容器镜像（Dockerfile 两个 target，默认最小镜像；见文件末尾「容器镜像」段）

.PHONY: dev web server web-assets desktop/dev desktop/accept desktop/payloads desktop/runtime-bin desktop/compliance core/desktop desktop/dist desktop/run verify/asset-parity verify/engines verify/release-json verify/signature verify/hardened-runtime verify/compliance verify/desktop-dod build vet test all clean image image/runtimes image/run

FRONTEND_ROOT := frontend
WEB_DIST_DIR  := backend/pkg/web/dist
# 桌面随包核心的落点（apps/desktop/resources/ 已 gitignore；M4-1 的 electron-builder 从这里取）。
CORE_DESKTOP_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/forge-core

# 桌面包里那一份前端产物的落点（M2-6；壳与核心读同一份，M4-1 把它固定进 extraResources）。
DESKTOP_WEB_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/web

# 打好的包（`desktop/dist` 的产物）。electron-builder 按**宿主架构**分目录：arm64 → `mac-arm64`，
# x64 → `mac`（`desktop/dist` 只传 `--mac`，所以这里按 `uname -m` 推是准的）。
# `desktop/run` 与 `verify/hardened-runtime` 共用这一条路径，免得两处各写一遍、改一处忘一处。
DESKTOP_ARCH_DIR := $(if $(filter arm64 aarch64,$(shell uname -m)),mac-arm64,mac)
DESKTOP_APP := $(FRONTEND_ROOT)/apps/desktop/release/$(DESKTOP_ARCH_DIR)/ark-forge.app

# —— 桌面发布号（M4-1）——
# 版本的唯一事实源是 apps/desktop/package.json（npm 语义的应用版本）；核心的版本由 make 注入
# （internal/version 的注释写明"发布构建由 Makefile 注入"）。两者**必须相等**——发布号标识整套，
# build-release-json.mjs 会断言这一点，对不上就拒绝出包。
DESKTOP_VERSION := $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' $(FRONTEND_ROOT)/apps/desktop/package.json | head -1)
# 开发态（core/desktop）不注入 → 核心报 `dev`（internal/version 的口径）；只有 desktop/dist 注入。
CORE_VERSION ?=
CORE_LDFLAGS  = $(if $(CORE_VERSION),-ldflags "-X github.com/morehao/ark-forge/internal/version.Version=$(CORE_VERSION)",)

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
# 为什么 dev 也要显式给：默认 tag 的核心**没有**内嵌资产，M2-1 的失败语义会让它拒绝启动
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

# 后端持久层默认 sqlite（创建 ~/.forge/forge.db，启用 runs/用量读模型）。
# 想要轻量纯内存可临时覆盖：FORGE_STORE=memory make dev（环境变量优先于此处默认）。
FORGE_STORE ?= sqlite

# 开发态跨源白名单（M3-5 的 Origin 矩阵）：vite :5173 与核心 :10001 **不同源**，浏览器会带上
# `Origin: http://localhost:5173`，而默认矩阵只放行"核心自己的 origin"（以及 forge-app://app 与无 Origin）。
# 所以这里显式登记；默认矩阵里没有它——验收 V16 正是要证明别的本地源（127.0.0.1:9999）仍被拒。
DEV_ORIGINS ?= http://localhost:5173,http://127.0.0.1:5173

# 运行前守卫（单行 shell，供 dev/web 作为独立 recipe 行调用）
define require-node
	@cur=$$(node -v 2>/dev/null | sed 's/^v//'); if [ "$${cur%%.*}" != "$(NODE_VERSION_MAJOR)" ]; then echo "==> Node 版本不符：frontend/.nvmrc 要求 Node $(NODE_VERSION_MAJOR).x（$(NODE_VERSION)），当前 $${cur:-未安装}"; echo "    团队统一 fnm，请先切换：fnm use $(NODE_VERSION_MAJOR)"; exit 1; fi
endef

# 前端产物 → embed 宿主（backend/pkg/web/dist，不入库）
web:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm build
	rm -rf $(WEB_DIST_DIR)
	cp -r $(FRONTEND_ROOT)/apps/web/dist $(WEB_DIST_DIR)

# 桌面随包核心 `forge-core`：壳 spawn 它（`:0` + 双凭据经 stdin + 就绪行回写）。
#
# **故意不带 `-tags release`**：桌面核不内嵌前端资产——资产由壳服务，核心按 `--web-dir`
# 读同一份目录（设计 §6.1 最后一条：桌面包里只有一份 apps/web/dist，核心不再内嵌一份副本）。
# 落后端产物路径 `apps/desktop/resources/` 已 gitignore（生成物）。
core/desktop: web desktop/runtime-bin
	cd backend && CGO_ENABLED=0 go build $(CORE_LDFLAGS) -o ../$(CORE_DESKTOP_OUT) ./cmd/forge
	@# 运行时树里那一份前端产物（M2-6）：壳服务它、核心用 --web-dir 读**同一份**。
	@# 只有一份 dist 是设计 §6.1 的红线——核心故意不带 -tags release（不内嵌），
	@# 否则桌面态会出现两份资产，版本闸门（M2-4）也就无从判定混搭。
	rm -rf $(DESKTOP_WEB_OUT)
	mkdir -p $(DESKTOP_WEB_OUT)
	cp -R $(FRONTEND_ROOT)/apps/web/dist/. $(DESKTOP_WEB_OUT)/

# 私有 bin 启动器（M3-2）：生成 resources/runtime/bin/{node,npx,dsh,opencode}（各含 POSIX 与 .cmd 两版）。
# 幂等、瞬时（内容逐字节确定，写完即静默），所以挂在 core/desktop 上——桌面态一旦缺它，
# 子进程 PATH 首段就是一条不存在的路径，症状会退化成"运行时莫名起不来"。
desktop/runtime-bin:
	$(require-node)
	cd $(FRONTEND_ROOT)/apps/desktop && node scripts/gen-runtime-bin.mjs

# 运行时载荷装配（M3-1）：把 dsh 的生产依赖闭包与 opencode 的平台化二进制装到
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

# 桌面壳开发起服：先出 forge-core，再构建主进程 / preload（esbuild → dist/*.cjs）后起 Electron。
# 窗口进 `forge-app://app/`（前端产物来自 apps/web/dist），REST 由壳反代核心。
# 与 make dev 的区别：那条起的是浏览器入口（vite :5173 + 后端 :10001），这条起的是桌面入口。
# 依赖 `desktop/payloads`：M3-4 起壳在**启动期**解析随包载荷，缺了就拒绝启动（不回落 PATH，设计 §6.5），
# 所以开发态也必须先装配载荷——否则 `make desktop/dev` 会以 boot-failed 页告终，而不是悄悄用机器上那份。
desktop/dev: core/desktop desktop/payloads
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm -F @ark/desktop dev

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
# 交付物两种（§12.27）：release/ark-forge-<ver>-macos-<arch>.tar.gz 是**分发**形态（非浏览器渠道，
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
	@# dsh / opencode 还能不能被核心拉着走完 ACP 握手。它是"verify-release-json --launch"的加强版
	@# （那条只到"界面渲染出来 + 核心可达"）。
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop:packaged

# 启动**打好的包**（人工看界面用）：等价于在访达里双击 `release/<arch>/ark-forge.app`。
# 壳自己解析包内载荷、自己起包内核心（`:0` + 双凭据经 stdin），跟开发态是同一套路径，
# 区别只在"全都来自包内"——所以它也是看"签名形态下跑起来是什么样"的最短路径。
#
# 刻意**不**依赖 `desktop/dist`：出包是分钟级的 electron-builder + 数百 MB 载荷，误触代价太大，
# 包不在场就给一条明确的出路（要连打包一起：`make desktop/dist && make desktop/run`）。
desktop/run:
	@command -v open >/dev/null 2>&1 || (echo "==> desktop/run 目前只支持 macOS（用 open 起 .app）；Windows/Linux 请等 M4-3 的出包形态"; exit 1)
	@test -d $(DESKTOP_APP) || (echo "==> 还没有打好的包：$(DESKTOP_APP)"; echo "    先跑 make desktop/dist（当前版本 $(DESKTOP_VERSION)）"; exit 1)
	@# 数据面提醒：桌面包起的核心与 `make dev` / `make desktop/dev` 的核共用同一个 `~/.forge/forge.db`，
	@# 两个核同时活着会让"这个改动到底生效了没"变玄学（脚本化靶子如 `ui:accept:desktop` 自带清场守卫）。
	@echo "==> 打开 $(DESKTOP_APP)（它自己起核；别同时跑 make dev / desktop/dev——共用 ~/.forge/forge.db）"
	@open $(DESKTOP_APP)

# 资产一致性（M2-5 / V17）：同一个请求经壳（forge-app://）与核心（回环直连）各取一次，
# 逐项比较 status / Content-Type / Cache-Control / 响应体 sha256。桌面态的两条服务路径由
# 不同代码实现（TS vs Go），只有这条能锁住"行为一致"。
verify/asset-parity:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm asset-parity

# engines / ABI 打包期断言（M3-7）：桌面态的载荷 JS 跑在 **Electron 自带的 Node** 上
# （`ELECTRON_RUN_AS_NODE=1`，见 M3-2 的私有 bin），所以要按 semver 比对载荷声明的
# `engines.node` 与 Electron 的真实 Node 版本，并断言 `payload.json` 的 nodeAbi 与实际 ABI
# 一致（不一致 = 原生 .node 到运行期才炸）。**Electron 主版本升级必须重跑这条。**
# 需要载荷已在场（`make desktop/payloads`）；不需要起壳、不碰 ~/.forge。
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
# `--require-artifacts --launch`（desktop/dist 收尾自动跑）：再加"包内那份 release.json 与源文件
# 逐字节相同""tar.gz 解到干净目录后布局完整、且壳能起来"。
verify/release-json:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:release-json

# 签名事实逐对象复核（M4-2a）：`codesign --verify --deep --strict`（封条完整）+ 包里每个 Mach-O
# 都有签名且带 `--options runtime` + identity 与 release.json 声明一致 + entitlements 真的签进去了。
# `spctl -a -vvv -t exec` 的结论如实打印但**不判失败**（ad-hoc 必然被 Gatekeeper 拒，这是 §12.27 的预期）。
# 默认判 `release/<arch>/ark-forge.app`（即 `$(DESKTOP_APP)`）；也可 `--from-tarball` 判解包后的那一份（分发形态）。
verify/signature:
	$(require-node)
	cd $(FRONTEND_ROOT) && pnpm verify:signature

# 加固运行时下的**包内执行**（M4-2a）：设计 §6.3 回退条件 ② 的判据。
# 与 `pnpm smoke:desktop` 的唯一区别：壳从**打出来的包**里起（`.app/Contents/MacOS/ark-forge`，
# ad-hoc 签名 + `--options runtime`），核心与其下的 dsh / opencode 也全部来自包内。
# 需要先有包（`make desktop/dist`）；签名形态本身由 `verify/signature` 判，这条判"能不能跑"。
verify/hardened-runtime:
	$(require-node)
	@test -d $(DESKTOP_APP) || (echo "==> 没有打好的包：先跑 make desktop/dist"; exit 1)
	cd $(FRONTEND_ROOT) && pnpm verify:signature
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop:packaged

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
	# （零额度、零凭证），探针 HOME/FORGE_HOME 全在仓库内、收尾核对用户真实目录零变化。
	# 为什么不并进 `ui:accept:desktop`：那条只打壳/契约/加固/凭据/事件流/退出六组，**看不见能力投放**
	# ——2026-10-09 正是这条才发现"核心侧修好了、桌面私有启动器仍把 dsh 的 skill 读根撬走"。
	# 前置与上面两条同款：`core/desktop`（脚本自己重建）与 `desktop/payloads`（壳启动期就要，缺了拒启）。
	cd $(FRONTEND_ROOT) && pnpm ui:accept:desktop-skills
	# V9（M3-8）：桌面态真起两份载荷（私有 bin + Electron 的 Node + 隔离环境）+ 一条"载荷目录零变化"
	# 的指纹断言。它**不联网、不跑模型**，所以要真模型会话的那一档（RUNTIME_SMOKE_LIVE=1）不在这里。
	# 三条回退条件里 ②③ 要 M4 的签名/公证/Windows 出包，脚本每次都显式打印"本脚本不判"。
	cd $(FRONTEND_ROOT) && pnpm smoke:desktop

# dev 用的资产就绪（不存在则构建一次）
$(WEB_DIR)/index.html:
	@echo "==> 前端产物不存在，先构建一次（后端 dev 需要 --web-dir 指向它）"
	cd $(FRONTEND_ROOT) && pnpm build

web-assets: $(WEB_DIR)/index.html

# 后端开发起服（forge 无默认子命令，需显式 serve）
server: web-assets
	cd backend && FORGE_STORE=$(FORGE_STORE) go run ./cmd/forge serve --web-dir $(WEB_DIR)

# 前后端联调单命令：先起后端 go run serve（默认 127.0.0.1:10001），轮询
# /v1/forge/health 通过后再起前端 vite dev（:5173，/v1 代理到后端含 ws），
# 避免后端尚未 listen 时 vite ws 代理出现 ECONNREFUSED 的启动竞态。
# 后端编译/启动失败会立即退出并报错；任一进程退出 / Ctrl-C 时把两个进程一起收掉。
dev:
	$(require-node)
	@echo "==> 后端  go run ./cmd/forge serve  (默认 127.0.0.1:10001, store=$(FORGE_STORE))"
	@trap 'kill 0 2>/dev/null; exit' INT TERM; \
	trap 'kill 0 2>/dev/null || true' EXIT; \
	cd backend && FORGE_STORE=$(FORGE_STORE) FORGE_ALLOWED_ORIGINS=$(DEV_ORIGINS) go run ./cmd/forge serve --web-dir $(WEB_DIR) & \
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
	rm -rf $(FRONTEND_ROOT)/apps/web/dist $(WEB_DIST_DIR) forge

# —— 容器镜像（Dockerfile 多阶段：前端 → Go 内嵌编译 → 精简运行时）——
#   make image                                    —— 最小镜像（只含 forge 本体 + echo 自检底座）
#   make image/runtimes WITH_DSH=true WITH_OPENCODE=true
#                                                 —— 额外内置 dsh / opencode（开关可分别置 false）
#                                                     产物 tag 由 RUNTIMES_TAG 决定（默认 ark-forge:runtimes）
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
# 含底座镜像的 tag 单独一个变量：`make image/runtimes` 与 `make image/run IMAGE_TAG=runtimes`
# 是文档里成对写的两条命令，若在这里复用 IMAGE_TAG，默认会产出 `ark-forge:local-runtimes`
# 而 run/文档找的是 `ark-forge:runtimes`——开箱即用不了（实测踩到）。
RUNTIMES_TAG     ?= runtimes
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
	docker build -t $(IMAGE):$(RUNTIMES_TAG) --target forge-runtimes $(IMAGE_ARGS) \
	  --build-arg WITH_DSH=$(WITH_DSH) --build-arg WITH_OPENCODE=$(WITH_OPENCODE) .

image/run:
	docker run -d --name $(IMAGE)-$(IMAGE_TAG) \
	  -p 127.0.0.1:$(FORGE_PORT):10001 \
	  -v $(IMAGE)-$(IMAGE_TAG)-data:/data \
	  -e FORGE_ADDR=0.0.0.0:10001 -e FORGE_STORE=sqlite \
	  $(IMAGE):$(IMAGE_TAG)
	@echo "==> 面板 http://127.0.0.1:$(FORGE_PORT)（健康检查见 docker ps 的 health 列）"
