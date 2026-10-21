# 本机隔离纪律（开发期硬约束）

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 这是硬约束，不是取向：开发/构建/验收/手工探针都不得读写用户自己安装与正在使用的底座。

**开发、构建、验收、手工探针，都不得读写用户自己安装与正在使用的底座**（`dsh`，以及任何已下线/未接入但用户机器上装着的底座）——那是用户的环境，
不是本仓的测试场地。这条是硬约束，不是取向：某个底座换实现时新增了一个没被隔离的 home 变量，
差一点就让平台去复用/替换用户自己的后台服务（2026-10-09，全量实测见
`.dsh/docs/notes/2026-10-09-opencode-v2-payload-verification-notes.md`）。

- **不许碰的目录**：`~/.dsh`、`~/.config/opencode`、`~/.local/share/opencode`、`~/.local/state/opencode`、
  `~/.opencode`（下面这些目录里的已下线底座**照样不许碰**：磁盘上还有它的安装与状态，而"它已不在册"不是碰它的理由）。`~/.agents/skills` 是**平台与底座的共享投放面**：可以读，写只由平台的物化负责
  （探针别往那儿写，也别去"顺便修一下"）。
- **平台拉起底座一律经隔离表**（`backend/internal/runtime/isolation.go` → `<MATE_HOME>/runtime/<id>/…`）。
  **换底座版本时必须重查它新引入的 home/状态变量**：v2 就新增了 `XDG_STATE_HOME`
  （`<XDG_STATE_HOME ?? ~/.local/state>/<id>/service.json` 的服务登记面）——不注它，平台拉起的
  底座会读用户自己的 `service.json`：同版本同 url 直接复用用户的服务，版本不符还会 SIGTERM
  旧进程再顶掉登记文件。
- **同一张隔离表在仓库里有三份**（Go 核心 `backend/internal/runtime/isolation.go`——表内容现已由各 adapter 的 `Spec.Isolation` 声明、父包只做机械投影；桌面私有启动器
  `apps/desktop/scripts/gen-runtime-bin.mjs`、V9 探针 `frontend/scripts/desktop-runtime-smoke.mjs`）：
  **改一处就得改三处**。护栏三条：`internal/runtime/isolation_test.go`（逐变量逐取值钉住核心表）、
  `TestDesktopLauncherIsolationMatchesCore`（启动器 ⊆ 核心，且核心新增而启动器没兜底的必须登记进
  `launcherCoreOnly`）、`TestDesktopSmokeIsolationMatchesCore`（探针与核心**逐条对齐**）。
  新增一条 home 变量时，三侧都要过一遍守卫——别只修核心那侧。
- **手工探针**：私有 `HOME` + 私有 `MATE_HOME`，目录一律建在工作区内（`.dsh/tmp/...`），结束杀进程 + 清目录；
  起 Electron 前清掉宿主环境里的 `ELECTRON_RUN_AS_NODE`（它会以纯 Node 跑）。注意有的底座的
  `debug`/`serve` 会起**托管服务**并在自己的 state 目录里登记 pid——收尾要按 `service.json` 里的 pid 收干净。
- **验收脚本**必须核对用户真实目录指纹零变化。外部写者（用户自己正跑着 dsh/opencode）在场时，
  如实报"不可判定/跳过"并打印写者：**既不许假绿，也不许假红**——`desktop-ui-acceptance.mjs`
  的 isolation 场景就是这么区分"外部写者"与"mate 写入"的（`~/.dsh` 是 DSH 宿主会话自己的状态目录，
  它本来就会被持续改写）。
- **沙箱/临时的绕道不许留在工作区**：为绕开沙箱而加的 `MATE_PAYLOAD_STORE_DIR`（或任何自定义
  store / cache 目录）会写进 `apps/desktop/runtime/<name>/node_modules/.modules.yaml` 的 `storeDir`，
  下次**在用户默认环境**里跑 `make desktop/payloads` 时 pnpm 判定"模块目录要重建"，而在没有 TTY 的
  构建里直接以 `ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY` 中止——2026-10-09 就是这么把用户的
  `make desktop/dist-run` 弄红的。规矩：绕道用完就还原环境并删掉那些中间产物（`runtime/*/node_modules`、
  仓库根的自定义 store），且**必须在默认环境下再跑一遍**验证。另外 `prepare-runtime.mjs` 已显式
  `--config.confirmModulesPurge=false`（构建步骤不该依赖"终端恰好是 TTY"），这条兜底不替代上面的清理。
