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
#   make dev   —— 前后端联调单命令：先起后端 go run（127.0.0.1:10001），等 health 通过再起 vite dev（:5173），Ctrl-C 一起收
#   make server —— 仅后端开发起服（不起前端，前端经 vite dev proxy 联调）
#   make desktop/dev —— 起桌面壳（Electron；M1 起壳会自己 spawn mate-core）
#   make image —— 容器镜像（Dockerfile 两个 target，默认最小镜像；见文件末尾「容器镜像」段）
#
# 前端资产只有一份（`frontend/apps/web/dist`）：核心**从不自带**资产，由调用方用
# `--web-dir` / `MATE_WEB_DIR` 指过去——桌面壳指包内那份、容器指镜像内那份、`make dev` 指工作区那份。
# （2026-10-10 起 `//go:embed` 与 `-tags release` 已删除，详见 `.dsh/docs/notes/2026-10-10-embed-removal-notes.md`。）

.PHONY: dev web server web-assets desktop/dev desktop/accept desktop/payloads desktop/runtime-bin desktop/compliance core/desktop desktop/dist desktop/run desktop/dist-run verify/asset-parity verify/engines verify/release-json verify/signature verify/hardened-runtime verify/compliance verify/desktop-dod build vet test clean image image/runtimes image/run

FRONTEND_ROOT := frontend
# 桌面随包核心的落点（apps/desktop/resources/ 已 gitignore；M4-1 的 electron-builder 从这里取）。
CORE_DESKTOP_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/mate-core

# 桌面包里那一份前端产物的落点（M2-6；壳与核心读同一份，M4-1 把它固定进 extraResources）。
DESKTOP_WEB_OUT := $(FRONTEND_ROOT)/apps/desktop/resources/web

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
CORE_LDFLAGS  = $(if $(CORE_VERSION),-ldflags "-X github.com/morehao/ark-mate/internal/version.Version=$(CORE_VERSION)",)

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

# 开发态跨源白名单（M3-5 的 Origin 矩阵）：vite :5173 与核心 :10001 **不同源**，浏览器会带上
# `Origin: http://localhost:5173`，而默认矩阵只放行"核心自己的 origin"（以及 mate-app://app 与无 Origin）。
# 所以这里显式登记；默认矩阵里没有它——验收 V16 正是要证明别的本地源（127.0.0.1:9999）仍被拒。
DEV_ORIGINS ?= http://localhost:5173,http://127.0.0.1:5173

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
	cd backend && CGO_ENABLED=0 go build $(CORE_LDFLAGS) -o ../$(CORE_DESKTOP_OUT) ./cmd/mate
	@# 运行时树里那一份前端产物（M2-6）：壳服务它、核心用 --web-dir 读**同一份**。
	@# 只有一份 dist 是设计 §6.1 的红线——核心从不自带资产（2026-10-10 起连 `//go:embed`
	@# 都删了），否则桌面态会出现两份资产，版本闸门（M2-4）也就无从判定混搭。
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
	@# dsh / opencode 还能不能被核心拉着走完 ACP 握手。它是"verify-release-json --launch"的加强版
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
# ad-hoc 签名 + `--options runtime`），核心与其下的 dsh / opencode 也全部来自包内。
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
#   - 动态那一半：最小 PATH（诱饵目录 + `/usr/bin:/bin`；诱饵是会写标记文件的假 dsh/opencode/node/npx）
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

# 后端开发起服（mate 无默认子命令，需显式 serve）
server: web-assets
	cd backend && MATE_STORE=$(MATE_STORE) go run ./cmd/mate serve --web-dir $(WEB_DIR)

# 前后端联调单命令：先起后端 go run serve（默认 127.0.0.1:10001），轮询
# /v1/mate/health 通过后再起前端 vite dev（:5173，/v1 代理到后端含 ws），
# 避免后端尚未 listen 时 vite ws 代理出现 ECONNREFUSED 的启动竞态。
# 后端编译/启动失败会立即退出并报错；任一进程退出 / Ctrl-C 时把两个进程一起收掉。
dev:
	$(require-node)
	@echo "==> 后端  go run ./cmd/mate serve  (默认 127.0.0.1:10001, store=$(MATE_STORE))"
	@trap 'kill 0 2>/dev/null; exit' INT TERM; \
	trap 'kill 0 2>/dev/null || true' EXIT; \
	cd backend && MATE_STORE=$(MATE_STORE) MATE_ALLOWED_ORIGINS=$(DEV_ORIGINS) go run ./cmd/mate serve --web-dir $(WEB_DIR) & \
	BACKEND_PID=$$!; \
	echo "==> 等待后端健康检查 http://127.0.0.1:10001/v1/mate/health ..."; \
	HEALTHY=""; \
	for i in $$(seq 1 300); do \
	  if curl -sf http://127.0.0.1:10001/v1/mate/health >/dev/null 2>&1; then \
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

# 全仓只有这一个构建配置：不带任何构建标签，不依赖前端产物（核心从不自带资产）。
build:
	cd backend && go build ./...

vet:
	cd backend && go vet ./...

test:
	cd backend && go test ./...

clean:
	rm -rf $(FRONTEND_ROOT)/apps/web/dist

# —— 容器镜像（Dockerfile 多阶段：前端 → Go 编译 → 精简运行时）——
#   make image                                    —— 最小镜像（只含 mate 本体）
#   make image/runtimes WITH_DSH=true WITH_OPENCODE=true
#                                                 —— 额外内置 dsh / opencode（开关可分别置 false）
#                                                     产物 tag 由 RUNTIMES_TAG 决定（默认 ark-mate:runtimes）
#   IMAGE_TAG=runtimes make image/run             —— 起容器（数据落具名卷，端口只绑回环）
# 前端资产由镜像内的 web 阶段构建后 COPY 进镜像（`MATE_WEB_DIR=/usr/local/share/mate/web`），
# 二进制本身不带资产（2026-10-10 起）。
#   make image                                    —— 最小镜像（只含 mate 本体 + echo 自检底座）
#   make image/runtimes WITH_DSH=true WITH_OPENCODE=true
#                                                 —— 额外内置 dsh / opencode（开关可分别置 false）
#                                                     产物 tag 由 RUNTIMES_TAG 决定（默认 ark-mate:runtimes）
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
IMAGE            ?= ark-mate
IMAGE_TAG        ?= local
# 含底座镜像的 tag 单独一个变量：`make image/runtimes` 与 `make image/run IMAGE_TAG=runtimes`
# 是文档里成对写的两条命令，若在这里复用 IMAGE_TAG，默认会产出 `ark-mate:local-runtimes`
# 而 run/文档找的是 `ark-mate:runtimes`——开箱即用不了（实测踩到）。
RUNTIMES_TAG     ?= runtimes
MATE_PORT       ?= 10001
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
	docker build -t $(IMAGE):$(RUNTIMES_TAG) --target mate-runtimes $(IMAGE_ARGS) \
	  --build-arg WITH_DSH=$(WITH_DSH) --build-arg WITH_OPENCODE=$(WITH_OPENCODE) .

image/run:
	docker run -d --name $(IMAGE)-$(IMAGE_TAG) \
	  -p 127.0.0.1:$(MATE_PORT):10001 \
	  -v $(IMAGE)-$(IMAGE_TAG)-data:/data \
	  -e MATE_ADDR=0.0.0.0:10001 -e MATE_STORE=sqlite \
	  $(IMAGE):$(IMAGE_TAG)
	@echo "==> 面板 http://127.0.0.1:$(MATE_PORT)（健康检查见 docker ps 的 health 列）"
