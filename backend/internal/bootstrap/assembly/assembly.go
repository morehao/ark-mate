package assembly

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/credential"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	pkgidentity "github.com/morehao/ark-mate/internal/core/identity"
	"github.com/morehao/ark-mate/internal/core/resource"
	productidentity "github.com/morehao/ark-mate/internal/domain/identity"
	"github.com/morehao/ark-mate/internal/domain/interaction"
	"github.com/morehao/ark-mate/internal/drivers/mechanism"
	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/drivers/transfer"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/router"

	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/procreg"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/golib/glog"
)

// ——— P5a：启停的**声明序列** ———
//
// 改造前这些动作是 `Init` 里 380 行的顺序散文：启动顺序、critical 与否、关闭顺序全部藏在
// "读下来就懂了"的控制流里。失效方式都不会让测试变红：
//
//	① 少起一个后台循环（空闲回收 / 自动化调度）⇒ 功能"看起来正常"，只是永远不触发；
//	② 关闭顺序与启动不对称 ⇒ 退出时偶发丢数据（transcript 尾部、在途轮次）；
//	③ 把只该 WARN 的步骤写成 critical ⇒ 某个可选目录缺失就起不来。
//
// 现在每一行是一个 `registry.Step`：名字、阶段、critical、启动/关闭超时、Start/Stop 都是
// 显式的。逐条对照表见 `.dsh/docs/plans/2026-10-17-p5a-registry-assembly-table.md`。
//
// **装配事实与装配动作分开**：`registry.Planes` 拥有领域平面（只构造、不读盘），本文件只按
// 顺序把配置与库句柄交给它们。这样核心、网关子进程与 doctor 才能共用同一套平面。

// assembly 一次 Build 的可变状态（各步骤之间传递的产物）。
//
// 字段都是"上一步产生、后面的步骤消费"的事实：库句柄、资产、监听器、来源策略、控制器。
// 放在一个结构体里而不是闭包捕获局部变量，是为了让每个步骤都是**可单独阅读的方法**。
type assembly struct {
	cfg     config.Config
	sources config.Sources
	mode    string
	opts    Options

	// ——— 只构造、无 IO 的平台事实（Build 的前半段建好）———
	bus    *eventbus.Bus
	rconn  *runtimeconn.Plane
	planes *registry.Planes
	svc    *sessionruntime.Service
	orch   *sessionruntime.Orchestrator
	reg    *runtime.Registry
	// lifecycle 本次启停的声明序列（Build 的 Start / Close 的逆序 Stop）。
	lifecycle *registry.Sequence

	// ——— 步骤之间传递的产物 ———
	st          *store.Store
	attachStore *attachmentstore.Store
	assets      router.AssetInfo
	ln          net.Listener
	origin      *middleware.OriginPolicy
	assetWatch  *router.AssetWatcher
	services    Services
	controllers router.Controllers
	engine      http.Handler

	// cmdRules 命令门禁规则（加载期已校验）。装配期**先读**（坏文件拒绝启动），
	// 编排器造出来之后再交给裁决点——早一步交给一个还不存在的编排器等于把规则丢掉。
	cmdRules []interaction.PrefixRule
}

// steps 声明启动序列（顺序即语义；关闭按它的逆序执行）。
func (as *assembly) steps() []registry.Step {
	return []registry.Step{
		{Name: "命令门禁", Phase: registry.PhasePlatform, Critical: true, Start: as.startCommandPolicy},
		{Name: "旧定义目录导入", Phase: registry.PhasePlatform, Timeout: 10 * time.Second, Start: as.startLegacyProviderImport},
		{Name: "凭据面", Phase: registry.PhasePlatform, Timeout: 10 * time.Second, Start: as.startCredentials},
		// 产品身份（部署级常量）：必须在**任何连接被拉起之前**把系统提示面装进启动参数。
		// 非 critical：交付失败只让模型的自述退回首轮块（`auto`），不该拒绝启动。
		{Name: "产品身份", Phase: registry.PhasePlatform, Timeout: 10 * time.Second, Start: as.startIdentity},
		{
			Name: "持久层打开", Phase: registry.PhasePersistence, Critical: true,
			Timeout: 30 * time.Second, Start: as.startStore, Stop: as.stopStore, StopTimeout: 10 * time.Second,
		},
		{Name: "旧密钥搬运", Phase: registry.PhasePersistence, Timeout: time.Minute, Start: as.startLegacySecretMigration},
		{Name: "持久层接线", Phase: registry.PhasePersistence, Timeout: 2 * time.Minute, Start: as.startStoreWiring},
		{Name: "启动导入", Phase: registry.PhasePersistence, Timeout: 2 * time.Minute, Start: as.startImports},
		{Name: "会话恢复", Phase: registry.PhasePersistence, Timeout: 2 * time.Minute, Start: as.startRestore},
		{Name: "工作空间策略", Phase: registry.PhaseDomain, Critical: true, Start: as.startWorkspacePolicy},
		{Name: "默认工作空间", Phase: registry.PhaseDomain, Critical: true, Start: as.startDefaultWorkspace},
		{Name: "工作空间白名单端口", Phase: registry.PhaseDomain, Start: as.startWorkspaceRootsStore},
		{
			Name: "编排器", Phase: registry.PhaseDomain, Critical: true,
			Start: as.startOrchestrator, Stop: as.stopOrchestrator, StopTimeout: 30 * time.Second,
		},
		{Name: "子进程注册表", Phase: registry.PhaseDomain, Timeout: as.reapTimeout(), Start: as.startProcRegistry},
		{Name: "定义装载", Phase: registry.PhaseDomain, Critical: true, Timeout: 2 * time.Minute, Start: as.startDefinitions},
		{Name: "权限记忆恢复", Phase: registry.PhaseDomain, Timeout: 30 * time.Second, Start: as.startPermissionMemories},
		{Name: "编排参数", Phase: registry.PhaseSettings, Start: as.startRuntimeSettings},
		{Name: "MCP 网关开关", Phase: registry.PhaseSettings, Start: as.startMcpGateway},
		{Name: "原生权限开关", Phase: registry.PhaseSettings, Start: as.startNativePermission},
		{Name: "空闲回收", Phase: registry.PhaseBackground, Start: as.startIdleReaper},
		{
			Name: "自动化调度", Phase: registry.PhaseBackground,
			Start: as.startAutomationScheduler, Stop: as.stopAutomationScheduler,
		},
		{Name: "用例与控制器", Phase: registry.PhaseDelivery, Critical: true, Start: as.startControllers},
		{Name: "前端资产", Phase: registry.PhaseDelivery, Critical: true, Timeout: 10 * time.Second, Start: as.startAssets},
		{
			Name: "资产重建通知", Phase: registry.PhaseDelivery,
			Start: as.startAssetWatch, Stop: as.stopAssetWatch, StopTimeout: 5 * time.Second,
		},
		{Name: "监听绑定", Phase: registry.PhaseDelivery, Critical: true, Timeout: 10 * time.Second, Start: as.startListener},
		{Name: "路由", Phase: registry.PhaseDelivery, Critical: true, Timeout: 10 * time.Second, Start: as.startRouter},
	}
}

// reapTimeout 孤儿回收的单步预算：回收本身按 `ReapGrace` 等子进程退出，再留 30s 余量。
func (as *assembly) reapTimeout() time.Duration {
	if as.cfg.ReapGrace > 0 {
		return as.cfg.ReapGrace + 30*time.Second
	}
	return 30 * time.Second
}

// startCommandPolicy 命令门禁（A2）：规则文件默认 `<MATE_HOME>/command_policy.json`，
// 文件不存在 = 未启用；**文件存在但 JSON/规则非法 = 拒绝启动**（坏文件不能被当成"没有规则"
// ——那会让门禁静默退回"全部问人"，而用户以为自己配了规则）。
func (as *assembly) startCommandPolicy(ctx context.Context) error {
	cfg := as.cfg
	rules, err := interaction.LoadCommandPolicyFile(cfg.CommandPolicyPath)
	if err != nil {
		return fmt.Errorf("命令门禁规则加载失败（拒绝启动；移除或修复 %s 后重试）: %w", cfg.CommandPolicyPath, err)
	}
	if len(rules) == 0 {
		return nil
	}
	as.cmdRules = rules
	glog.Infow(ctx, "命令门禁已启用", "rules", len(rules), "path", cfg.CommandPolicyPath)
	return nil
}

// startLegacyProviderImport v19 的目录改名一次性导入（`agents/providers.json` →
// `providers/providers.json`）：必须在任何读 `ProviderDir` 的动作之前。只在默认目录上做
// （自定义 `MATE_PROVIDERS` 时静默返回），**不删旧文件**，新文件已存在就不动。
func (as *assembly) startLegacyProviderImport(ctx context.Context) error {
	cfg := as.cfg
	imported, warn, err := config.ImportLegacyProviderDir(cfg.ProviderDir, cfg.HomeDir)
	if err != nil {
		glog.Warnw(ctx, "serve: 导入旧定义目录失败（照常启动；自定义目录的用户请自行搬运 providers.json）",
			"err", err, "dir", cfg.ProviderDir)
		return nil
	}
	if imported {
		glog.Warnw(ctx, "serve: "+warn, "dir", cfg.ProviderDir)
	}
	return nil
}

// startCredentials 凭据面**先行装配**（v20，顺序敏感）：下面持久层的第一次 runtime 同步
// 就要读 provider 密钥，而"密钥从哪来"由这一层回答（四层回落的唯一实现）。
//
// 打开托管文档**不读内容**（权限过宽在读的那一刻硬失败），所以这里失败只可能来自"数据目录
// 解析不出来"；那时如实 WARN，并让解析退化成"只有库里的那一份可用"。
func (as *assembly) startCredentials(ctx context.Context) error {
	cfg := as.cfg
	cwd, _ := os.Getwd()
	// 凭据 cwd **显式登记**（不是让用到的每一处各自 `os.Getwd()`）：它要随网关条目交给网关
	// **子进程**，而网关的 cwd 是底座给的（会话工作区）——见 `mcp.GatewayCredCwdEnv`。
	as.planes.Credentials.SetCwd(cwd)
	layered, mirrorPath, err := mechanism.OpenCredentialSource(cfg.HomeDir, cwd)
	if err != nil {
		glog.Warnw(ctx, "serve: 凭据分层未装配（只有库里的那一份可用）", "err", err,
			"home", config.DisplayHome(cfg.HomeDir))
		return nil
	}
	as.planes.Credentials.SetLayered(layered)
	glog.Infow(ctx, "serve: 凭据面已装配（层序：继承环境 > 托管文档 > <cwd>/.env > <MATE_HOME>/.env）",
		"credentials", mirrorPath)
	return nil
}

// startStore 持久层装配（P1）：MATE_STORE=sqlite → SQLite（DB 为事实源，transcript 停写保留
// 只读）；=memory → 维持 transcript 落盘 + 启动回读；auto（默认）→ DB 文件已存在则 sqlite。
func (as *assembly) startStore(ctx context.Context) error {
	cfg := as.cfg
	switch as.mode {
	case config.StoreModeSQLite:
		// 配置目录化后 DB 路径可由文件指定，父目录未必存在：先补齐。
		if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fatalErr("serve: 创建 DB 目录 %s: %v", dir, err)
			}
		}
		opened, err := store.Open(cfg.DBPath)
		if err != nil {
			return fatalErr("serve: 打开持久层 %s: %v", cfg.DBPath, err)
		}
		as.st = opened
		// 附件字节存储（v32）：core_file / core_file_upload 两表由 golib filestore 建——这是
		// 本仓「不用 gorm AutoMigrate」纪律的**唯一显式例外**（事实源在组件里，已登记进
		// docs/agents/build-and-runtime.md）。在自管迁移（store.Open 里）之后调一次，幂等。
		if err := attachmentstore.Migrate(opened.DB()); err != nil {
			return fatalErr("serve: 建附件存储表（core_file*）: %v", err)
		}
		attachStore, err := attachmentstore.New(opened.DB(), attachmentstore.Options{
			BaseDir:        filepath.Join(cfg.HomeDir, "attachments"),
			MaxUploadBytes: cfg.AttachmentMaxBytes,
		})
		if err != nil {
			return fatalErr("serve: 构造附件存储: %v", err)
		}
		as.attachStore = attachStore
		// 附件解析端口（v32）：持久层就绪后再接给会话服务（Build 阶段 Service 已构造，那时还没库）。
		as.svc.SetAttachmentResolver(as.facts().buildAttachmentResolver())
		// 定义目录**先行登记**（顺序敏感）：下面的装配期同步那一刻 `providerLocator` 已经要有
		// 确定值（否则报错文案指向空路径）。v20 起它与密钥无关。
		as.planes.Providers.SetProviderDir(cfg.ProviderDir)
	case config.StoreModeMemory, "":
		// 会话 wire transcript（~/.ark-mate/transcripts 或 MATE_TRANSCRIPTS）。
		as.svc.SessionPlane().SetTranscriptDir(cfg.TranscriptDir)
	default:
		// 不可达：internal/config 的 materialize 已按 ValidStoreModes 校验并把 auto 归一成空串。
		// 保留为内部错误（不是 usageErr——这不是用户输入错，是代码不一致）。
		return fatalErr("serve: 内部错误：持久层模式 %q 未被处理（配置层校验与装机层取值集合不一致）", as.mode)
	}
	return nil
}

// stopStore 关闭库句柄（启动失败回滚与正常退出共用）。
func (as *assembly) stopStore(context.Context) error {
	if as.st == nil {
		return nil
	}
	return as.st.Close()
}

// startLegacySecretMigration 旧密钥的一次性搬运（v20 迁移的第二步）：**必须在 EnableStore 之前**
// ——那一次同步就要读 provider 密钥，而此刻库里的密钥可能还只在旧密文里。
//
// 三条性质由 `store.ImportLegacyVault` 保证：幂等、单条失败进 Problems、**只读不删**。
// 日志只带引用与条数，**绝不含密钥值**。
func (as *assembly) startLegacySecretMigration(ctx context.Context) error {
	if as.st == nil {
		return nil
	}
	cfg := as.cfg
	anchor := doctorfacts.LegacySecretAnchor(cfg.HomeDir, cfg.ProviderDir)
	if anchor == "" {
		return nil
	}
	move, err := as.st.ImportLegacyVault(ctx, anchor)
	if err != nil {
		glog.Warnw(ctx, "serve: 旧密钥搬运未执行（库里的密钥不受影响；跑 `mate doctor` 看遗留物）",
			"err", err, "anchor", anchor)
		return nil
	}
	if move.Empty() {
		return nil
	}
	glog.Infow(ctx, "serve: 旧密钥搬运完成（只读不删；旧密文与主密钥仍在原处）",
		"ciphertexts", move.Ciphertexts, "provider_keys", move.ProviderKeys,
		"mcp_secrets", move.McpSecrets, "already_moved", move.AlreadyMoved)
	for _, p := range move.Problems {
		glog.Warnw(ctx, "serve: 有一条旧密钥没搬进库（其余照常；修好后重启即会重跑）",
			"ref", p.Ref, "reason", p.Reason)
	}
	return nil
}

// startStoreWiring 把库句柄接到所有持有它的部件，并做装配期同步。
//
// 实现只有 `sessionruntime.EnableStore` / `registry.Planes.EnableStore` 一份：**漏接任一个**的表现
// 都是静默的（该走库的走了文件、或反过来——定义只写进内存、凭据读不到库）。
func (as *assembly) startStoreWiring(ctx context.Context) error {
	if as.st == nil {
		return nil
	}
	as.svc.EnableStore(as.st)
	// v15 破坏性重建的**启动 WARN**（§8.5 R2，强制项）：升级既有库时静默清空会话历史是最坏的
	// 一种失败——用户只会觉得"我的会话没了"。所以这里必须说出来，并带上回退路径。
	if note := as.st.DestructiveRebuildNote(); note != "" {
		glog.Warnw(ctx, "serve: 数据模型 v15 已执行一次性破坏性重建（仅此一次，不构成先例）",
			"note", note, "backup", as.st.MigrationBackupPath())
	}
	return nil
}

// startImports 存量定义的一次性导入 + 托管镜像对账。
//
// 三个定义域**同一次触发、同一个理由**：v8/v10/v11 之后 DB 才是事实源，而存量定义还在目录里。
// 都必须发生在 loadDefinitions 之前——导入晚了这一轮启动的面就是空的。失败只 WARN 不阻断
// （"面为空"是可见的，而"起不来"是更坏的一种失败）。
func (as *assembly) startImports(ctx context.Context) error {
	if as.st == nil {
		return nil
	}
	cfg := as.cfg
	if imp, err := transfer.ImportCapabilityDir(ctx, as.st, cfg.CapabilityDir,
		transfer.DefinitionImportOptions{Auto: true}, nil); err != nil {
		glog.Warnw(ctx, "serve: 导入存量能力定义失败（能力面可能为空；可跑 `mate capability import` 重试）",
			"err", err, "dir", cfg.CapabilityDir)
	} else if !imp.Already && imp.Inserted+imp.Existing+imp.Invalid+imp.Tools > 0 {
		glog.Infow(ctx, "serve: 存量能力定义导入完成",
			"dir", imp.Dir, "skills", imp.Skills, "mcps", imp.Mcps,
			"inserted", imp.Inserted, "existing", imp.Existing,
			"invalid", imp.Invalid, "tools_files_skipped", imp.Tools)
	}
	if imp, err := transfer.ImportAutomationDir(ctx, as.st, cfg.AutomationDir,
		transfer.DefinitionImportOptions{Auto: true}, nil); err != nil {
		glog.Warnw(ctx, "serve: 导入存量 automation 定义失败（定时任务面可能为空；可跑 `mate automation import` 重试）",
			"err", err, "dir", cfg.AutomationDir)
	} else if !imp.Already && imp.Automations+imp.Inserted+imp.Existing+imp.Invalid > 0 {
		glog.Infow(ctx, "serve: 存量 automation 定义导入完成",
			"dir", imp.Dir, "automations", imp.Automations, "inserted", imp.Inserted,
			"existing", imp.Existing, "invalid", imp.Invalid)
	}
	// provider 档案一次性导入（v11）：幂等依据是**文件内容指纹**；迁移前备份
	// `providers.json.bak-v11`（全篇唯一不可逆动作——回退窗口在导入完成后关闭）。
	if imp, err := transfer.ImportProviderFile(ctx, as.st, cfg.ProviderDir,
		transfer.DefinitionImportOptions{Auto: true}, nil); err != nil {
		glog.Warnw(ctx, "serve: 导入存量 provider 档案失败（供应商面可能为空；可跑 `mate provider import` 重试）",
			"err", err, "dir", cfg.ProviderDir)
	} else if !imp.Already && imp.Providers+imp.Inserted+imp.Existing > 0 {
		glog.Infow(ctx, "serve: 存量 provider 档案导入完成",
			"dir", imp.Dir, "providers", imp.Providers, "inserted", imp.Inserted,
			"existing", imp.Existing, "backup", imp.Backup, "key_problems", len(imp.KeyProblems))
		for _, p := range imp.KeyProblems {
			glog.Warnw(ctx, "serve: 有密钥没搬进库（清单见上；档案里的明文不会再被读）", "item", p)
		}
	}
	// 托管镜像按库对账（v20）：**合并、非破坏**。失败只 WARN：镜像是可重建副本。
	if changed, err := as.planes.Credentials.SyncCredentialMirror(ctx, "serve 装配期"); err != nil {
		glog.Warnw(ctx, "serve: 托管凭据文档未能按库对账（库是权威，键不受影响；`mate doctor` 会列出差异）",
			"err", err, "path", credential.CredentialsPath(cfg.HomeDir))
	} else if changed > 0 {
		glog.Infow(ctx, "serve: 托管凭据文档已按库对账",
			"changed", changed, "path", credential.CredentialsPath(cfg.HomeDir))
	}
	return nil
}

// startRestore 从库恢复会话与历史（sqlite），并提示未迁移的 transcript。
func (as *assembly) startRestore(ctx context.Context) error {
	if as.st == nil {
		return nil
	}
	cfg := as.cfg
	sessions, events, err := as.svc.SessionPlane().Restore(ctx)
	if err != nil {
		glog.Warnw(ctx, "serve: 从持久层恢复失败", "err", err)
	}
	// transcript 未迁移提示：DB 为空但 transcript 目录有文件 → 提示先 `mate migrate`。
	if sessions == 0 && events == 0 {
		if files, gErr := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl")); gErr == nil && len(files) > 0 {
			fmt.Fprintf(os.Stderr, "serve: %s 下仍有 %d 个 transcript 未导入；运行 `mate migrate` 后重启即可在 sqlite 模式下看到旧会话\n", cfg.TranscriptDir, len(files))
		}
	}
	return nil
}

// startWorkspacePolicy 工作空间策略：未指定 cwd 的建会话请求落在**默认工作空间**；
// workspace_roots 非空时把显式 cwd、目录浏览与工作空间登记收在白名单内。
func (as *assembly) startWorkspacePolicy(ctx context.Context) error {
	cfg := as.cfg
	if err := as.planes.Workspaces.SetWorkspacePolicy(cfg.WorkspaceDir, cfg.WorkspaceRoots); err != nil {
		return usageErr("serve: 工作空间策略非法: %v", err)
	}
	return nil
}

// startDefaultWorkspace 默认工作空间的建立**必须在配置装载之后**（迁移里拿不到 workspace_dir）：
// 幂等，且只在登记表为空时创建——删掉它之后不再自动重建。
func (as *assembly) startDefaultWorkspace(ctx context.Context) error {
	if err := as.planes.Workspaces.EnsureDefaultWorkspace(); err != nil {
		return usageErr("serve: 建立默认工作空间失败: %v", err)
	}
	return nil
}

// startWorkspaceRootsStore 白名单的持久化端口：面板 PUT /settings/workspace 经它写回
// config.json。传入该键的**最终来源**：来源高于配置文件时端口会报"锁定"，面板随之转只读。
func (as *assembly) startWorkspaceRootsStore(ctx context.Context) error {
	cfg := as.cfg
	as.planes.Workspaces.SetRootsStore(config.NewSettingsStore(cfg.ConfigDir, as.sources["workspace_roots"]))
	return nil
}

// startOrchestrator 构造编排器并回挂到 Service。
func (as *assembly) startOrchestrator(ctx context.Context) error {
	as.orch = sessionruntime.NewOrchestrator(as.svc, as.reg)
	// 命令门禁规则在**加载期**就读完了（坏文件拒绝启动），这里交给裁决点。
	// 改造前 `Service.SetCommandPolicy` 在编排器还不存在时静默丢掉这批规则——那是"门禁
	// 有实现、没接线"的另一种形态；现在交接点只有一个。
	if len(as.cmdRules) > 0 {
		as.orch.SetCommandPolicy(as.cmdRules)
	}
	// 附件目录加入会话只读白名单（P4 兜底，v32）：让模型能 read 到 <MATE_HOME>/attachments 下的
	// 附件（与 cwd 不同目录）。只读、不物化、不写工作区（G5）。只在 sqlite 模式（有库有附件面）
	// 才设；内存模式没有附件面，不追加。若实测表明 dsh 本就能读（P0 探针=可达），这里是冗余但
	// 无害的兜底。
	if as.mode == config.StoreModeSQLite {
		as.orch.SetAdditionalReadDirs([]string{filepath.Join(as.cfg.HomeDir, "attachments")})
	}
	return nil
}

// stopOrchestrator 关闭全部连接并收尾 transcript（连接池的关闭含空闲回收停止与子进程回收）。
func (as *assembly) stopOrchestrator(context.Context) error {
	if as.orch == nil {
		return nil
	}
	as.orch.Close()
	return nil
}

// startProcRegistry 登记本 daemon 拉起的 runtime 子进程；启动时回收上一实例遗留的孤儿进程
// （daemon 被 SIGKILL/崩溃时子进程因独立进程组而存活）。
func (as *assembly) startProcRegistry(ctx context.Context) error {
	cfg := as.cfg
	p := cfg.ProcRegistryPath
	if p == "" {
		return nil
	}
	pr, err := procreg.Open(p)
	if err != nil {
		glog.Warnw(ctx, "serve: 打开进程注册表失败（孤儿回收已禁用）", "path", p, "err", err)
		return nil
	}
	as.rconn.SetProcRegistry(pr)
	rep, err := as.rconn.ReapOrphans(cfg.ReapGrace)
	if err != nil {
		glog.Warnw(ctx, "serve: 孤儿进程回收失败", "err", err)
		return nil
	}
	if rep.Total() > 0 {
		glog.Infow(ctx, "serve: 孤儿进程回收", "reaped", len(rep.Reaped),
			"cleaned", len(rep.Cleaned), "skipped", len(rep.Skipped), "registry", p)
	}
	return nil
}

// startDefinitions 装载定义并重校 skill 物化（配置即事实源的装载点；失败只告警不阻断）。
func (as *assembly) startDefinitions(ctx context.Context) error {
	return as.loadDefinitions(ctx, as.mode)
}

// startPermissionMemories 启动恢复会话级权限记忆（allow/reject 双侧、跨重启不丢）。
func (as *assembly) startPermissionMemories(ctx context.Context) error {
	if as.mode != config.StoreModeSQLite || as.orch == nil {
		return nil
	}
	if err := as.orch.RestoreMemoriesFromStore(ctx); err != nil {
		glog.Warnw(ctx, "serve: 恢复权限记忆失败", "err", err)
	}
	return nil
}

// startRuntimeSettings 编排参数（逐条保留原来的"0 = 用默认/关闭"语义）。
func (as *assembly) startRuntimeSettings(ctx context.Context) error {
	cfg := as.cfg
	if cfg.RunTimeout > 0 {
		as.orch.SetRunTimeout(cfg.RunTimeout)
	}
	// handoff memo 字符预算（P3 F3；MATE_HANDOFF_MAX_CHARS 覆盖，默认 6000）。
	as.orch.SetHandoffMaxChars(cfg.HandoffMaxChars)
	// 能力代际上限（M3-7；MATE_GENERATION_LIMIT，0 = 不限制）。
	// P4a 正文第五十四刀起连接池归执行面：装配层经**事实出口**直连池，不再经编排器转发。
	as.svc.RuntimePool().SetGenerationLimit(cfg.GenerationLimit)
	// 权限/elicitation 挂起超时（默认 30min）。
	as.orch.SetPermissionTimeout(cfg.PermissionTimeout)
	as.orch.SetElicitationTimeout(cfg.ElicitationTimeout)
	return nil
}

// startIdentity 产品身份：把构建期常量按模式作用后交付给构建期选定的底座。
//
// 三件事的顺序是语义的：
//  1. **先算模式**（`MATE_IDENTITY_MODE`）：它决定两个面（系统提示 / 首轮块）各要不要用；
//  2. **先把 Spec 落到平面**：交付失败时首轮块仍要能用——`auto` 的兜底正是这么工作的；
//  3. **再交付**：结论如实记录（失败不阻塞启动，但必须留痕，否则"模型还叫 coding agent"
//     会变成一条无从追查的现象）。
//
// 为什么它不是 critical：身份只影响"模型怎么自称"，不影响任何会话能不能跑。把它做成 critical
// 会让一次磁盘只读/权限异常把整个应用挡在门外——那是把产品面缺陷升级成可用性事故。
func (as *assembly) startIdentity(ctx context.Context) error {
	mode, err := pkgidentity.ParseMode(as.cfg.IdentityMode)
	if err != nil {
		// 配置层已校验过；走到这里说明装配绕过了校验 ⇒ fail-closed，不静默回落 auto
		// （回落会让"我以为关掉了身份"与"其实还在交付"长得一模一样）。
		return usageErr("serve: identity_mode: %v", err)
	}
	spec := mode.Apply(productidentity.Render())
	as.planes.Identity.SetSpec(spec)
	outcome, derr := as.planes.Identity.Deliver(ctx, spec)
	if derr != nil || outcome.Status == resource.StatusFailed {
		cause := ""
		if derr != nil {
			cause = derr.Error()
		}
		glog.Warnw(ctx, "serve: 产品身份的系统提示面未交付（模型的自述会退回首轮块或无名）",
			"mode", string(mode), "status", outcome.Status, "problems", strings.Join(outcome.Problems, "；"), "err", cause)
		if as.rconn != nil {
			ae := failure.NewError(failure.ClassService, "identity_patch_failed",
				"产品身份的系统提示面未交付（会话仍可用；模型的自述退回首轮块或无身份）："+
					strings.Join(outcome.Problems, "；"), nil)
			as.rconn.RecordRuntimeError("", ae, nil)
		}
		return nil
	}
	glog.Infow(ctx, "serve: 产品身份已装配",
		"mode", string(mode), "status", outcome.Status,
		"system_prompt", spec.UsesSystemPrompt(), "first_turn_block", spec.UsesUserContext())
	return nil
}

// startMcpGateway 平台侧 MCP 网关开关（M7-3）：默认 auto（开），只有显式 off 才回退到
// "远程直连注入"。
func (as *assembly) startMcpGateway(ctx context.Context) error {
	enabled := as.cfg.McpGatewayEnabled()
	as.planes.SetMcpGatewayEnabled(enabled)
	if !enabled {
		glog.Warnw(ctx, "serve: MATE_MCP_GATEWAY=off —— 远程 MCP 回到直连注入（按底座承载能力过滤）")
	}
	return nil
}

// startNativePermission 原生权限映射（B5）：MATE_NATIVE_PERMISSION=off 时关闭（默认 auto）。
//
// v19 起平台**没有策略来源**（per-agent 默认权限策略随 agent 面撤销），这条开关因此不改变任何
// 注入结果。保留接线点而不是删掉它，是因为 `internal/config` 的键仍在（部署脚本会设它）：
// 静默失效比"读一次并如实说出当前没有策略来源"更糟。
func (as *assembly) startNativePermission(ctx context.Context) error {
	if as.cfg.NativePermission != config.NativePermissionOff {
		return nil
	}
	as.planes.SetNativePermission(false)
	glog.Infow(ctx, "serve: 原生权限映射已关闭（MATE_NATIVE_PERMISSION=off；当前平台无策略来源，该开关不改变注入结果）")
	return nil
}

// startIdleReaper 空闲回收（P2）：暖进程闲置超阈值自动停（MATE_IDLE_TIMEOUT，默认 5m；0 = 关闭）。
func (as *assembly) startIdleReaper(ctx context.Context) error {
	cfg := as.cfg
	as.svc.RuntimePool().StartIdleReaper(cfg.ReapInterval, cfg.IdleTimeout)
	return nil
}

// startAutomationScheduler M4-4 调度循环：`MATE_AUTOMATION_TICK=0` 关闭（装配期同步跑一次
// 启动恢复 pass）。**必须在定义装载之后**——pass 扫的是内存注册表。
func (as *assembly) startAutomationScheduler(ctx context.Context) error {
	as.planes.Automation.StartAutomationScheduler(as.cfg.AutomationTick)
	return nil
}

// stopAutomationScheduler 停止调度循环并等它退出（幂等）。
func (as *assembly) stopAutomationScheduler(context.Context) error {
	as.planes.Automation.StopAutomationScheduler()
	return nil
}

// startControllers 构造全部用例与控制器（**唯一装配点**），再交给路由。
//
// 顺序：用例 → 控制器。控制器只收 service 接口与（必要的）交付层设施（凭据/Origin 由
// `startRouter` 交给 router）。构造失败不会发生（构造函数只存字段），这里保持 error 签名
// 只为与 Step 的形状一致。
func (as *assembly) startControllers(context.Context) error {
	as.services = as.buildServices()
	as.controllers = as.buildControllers()
	return nil
}

// startRouter 用已绑定的监听器与已构造的控制器装配 HTTP 引擎（HTTP 服务由 app 壳启停）。
func (as *assembly) startRouter(ctx context.Context) error {
	as.engine = router.New(router.Deps{
		Controllers: as.controllers,
		Web:         as.assets,
		AssetWatch:  as.assetWatch,
		Origin:      as.origin,
	})
	return nil
}

// listen 绑定监听地址（HTTP 服务归 app，但端口必须先定下来才能构造 Origin 矩阵）。
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	return ln, nil
}

// startAssets 资产层（M2-1）：资产来自调用方给的目录，没给即**拒绝启动**（退出码 3）。
// 放在监听之前：装错资产时不该先占住端口再报错。
func (as *assembly) startAssets(ctx context.Context) error {
	assets, err := router.ResolveAssets(as.cfg.WebDir)
	if err != nil {
		return assetErr("serve: %v", err)
	}
	as.assets = assets
	if assets.Has() {
		glog.Infow(ctx, "serve: 前端资产已就绪", "dir", assets.Path)
	}
	return nil
}

// startAssetWatch 开发期资产重建通知（P6-A）：**默认关闭**，只有显式给了正时长才起。
// 位置放在资产解析之后、监听之前：它盯的就是刚解析好的那份目录，而没有资产时不该起。
func (as *assembly) startAssetWatch(ctx context.Context) error {
	if !as.assets.Has() || as.cfg.AssetWatch <= 0 {
		return nil
	}
	as.assetWatch = router.NewAssetWatcher(as.assets, as.cfg.AssetWatch)
	as.assetWatch.Start(ctx)
	glog.Infow(ctx, "serve: 已开启开发期资产重建通知（P6-A）",
		"endpoint", router.AssetWatchPath, "interval", as.cfg.AssetWatch, "dir", as.assets.Path)
	return nil
}

// stopAssetWatch 停止资产目录轮询（幂等；不依赖 ctx 时序）。
func (as *assembly) stopAssetWatch(context.Context) error {
	if as.assetWatch != nil {
		as.assetWatch.Stop()
	}
	return nil
}

// startListener 绑定监听器，再用**实际监听地址**构造 Host 白名单 + Origin 矩阵。
//
// 顺序是被判据逼出来的：策略里"同源"的定义就是本进程的监听端口，而桌面态（`127.0.0.1:0`）
// 的端口由内核分配，只有 net.Listen 之后才存在——配置里的 `:0` 不是判据。
func (as *assembly) startListener(ctx context.Context) error {
	cfg := as.cfg
	ln, err := listen(cfg.Addr)
	if err != nil {
		return usageErr("serve: %v", err)
	}
	as.ln = ln
	// 宿主原生目录选择器的第一条判据就是"绑的是不是回环"，而真端口只有 Listen 之后才有。
	as.planes.Picker.SetBindAddr(ln.Addr().String())
	as.origin = middleware.NewOriginPolicy(ln.Addr().String(), cfg.AllowedOrigins)
	if as.origin.Enabled() {
		glog.Infow(ctx, "serve: 已启用回环 Host 白名单 + Origin 矩阵（M3-5）",
			"listen", ln.Addr().String(), "port", as.origin.Port(), "allowed_origins", len(cfg.AllowedOrigins))
		return nil
	}
	// 非回环监听的**明确放弃**，不是漏了：设计 §9.4 把 Host/Origin 白名单列为对外产品化缺的
	// 四件事之一，本期范围外；这里必须打 WARN——用户以为自己在对外服务却没有任何防护。
	glog.Warnw(ctx, "serve: 监听地址不是回环白名单主机（127.0.0.1/::1/localhost），Host 白名单 + Origin 矩阵**未启用**；"+
		"设计 §9.4 明确对外产品化所需的 Host/Origin 白名单不在本期范围，请自行用反向代理/防火墙收口",
		"listen", ln.Addr().String(), "allowed_origins", len(cfg.AllowedOrigins))
	return nil
}
