package cliops

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/mechanism/credential"

	"github.com/morehao/ark-mate/pkg/config"
)

// configCmd 打印解析后的配置与每项来源（排障/上线核对用；不启动任何进程）。
func Config(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出（含每项来源）")
	dir := fs.String("config-dir", "", "配置目录（默认 MATE_CONFIG_DIR 或 ~/.ark-mate/config）")
	_ = fs.Parse(args)

	cfg, sources, err := config.LoadResolved(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	if *asJSON {
		doc := map[string]any{
			"config_dir": cfg.ConfigDir,
			// 数据目录（M3-3）：与 config_dir 一样是**启动引导键**，来自进程 env 或默认值，
			// 因此不出现在下面 config 的来源表里；单独与 config_dir 并列，排障时第一眼就能核对
			// "这套配置到底读写哪份数据"。
			"home": cfg.HomeDir,
			"config": map[string]any{
				"addr":                cfg.Addr,
				"transcripts":         cfg.TranscriptDir,
				"db":                  cfg.DBPath,
				"store":               cfg.StoreMode,
				"providers":           cfg.ProviderDir,
				"credentials":         credential.CredentialsPath(cfg.HomeDir),
				"capabilities":        cfg.CapabilityDir,
				"workspace_dir":       cfg.WorkspaceDir,
				"workspace_roots":     strings.Join(cfg.WorkspaceRoots, ","),
				"projection_dirs":     formatProjectionDirs(cfg.ProjectionDirs),
				"idle_timeout":        cfg.IdleTimeout.String(),
				"reap_interval":       cfg.ReapInterval.String(),
				"run_timeout":         cfg.RunTimeout.String(),
				"handoff_max_chars":   cfg.HandoffMaxChars,
				"generation_limit":    cfg.GenerationLimit,
				"automations":         cfg.AutomationDir,
				"automation_tick":     cfg.AutomationTick.String(),
				"asset_watch":         cfg.AssetWatch.String(),
				"permission_timeout":  cfg.PermissionTimeout.String(),
				"elicitation_timeout": cfg.ElicitationTimeout.String(),
				"proc_registry":       cfg.ProcRegistryPath,
				"reap_grace":          cfg.ReapGrace.String(),
				"token":               maskToken(cfg.Token),
				"allowed_origins":     strings.Join(cfg.AllowedOrigins, ","),
				"mcp_auto_probe":      cfg.McpAutoProbe,
				"mcp_gateway":         cfg.McpGateway,
				"native_permission":   cfg.NativePermission,
			},
			"remote_runtimes": cfg.RemoteRuntimes,
			"sources":         sources,
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	src := func(k string) string {
		if s, ok := sources[k]; ok {
			return string(s)
		}
		return string(config.SourceDefault)
	}
	// 人话这一路的**每一行**都过一遍显示名（v19）：默认数据目录渲染成 `~/.ark-mate`，
	// `~` 可粘贴，而绝对机器路径会随截图与问题单外流。
	//
	// 为什么在**整行**上替换而不是逐个路径参数：路径会嵌在句中（"↑ 生效名单另含平台代建根 …"），
	// 逐个包容易漏，而漏掉的那一处就是泄露点；`config.DisplayHome` 只认默认目录、且要求前后
	// 都是路径边界，所以对用户自己选的目录零影响。
	//
	// `--json` 分支（上面 return 掉的那支）刻意**不**换：那一支是诊断包与 CI 的输入，要的是
	// 逐字可比的绝对路径。
	pf := func(format string, args ...any) {
		fmt.Print(config.DisplayHome(fmt.Sprintf(format, args...)))
	}
	pf("配置目录: %s\n", cfg.ConfigDir)
	// 数据目录（M3-3）：只吃进程环境变量（MATE_HOME），所以来源只有 env(process) 与 default 两种；
	// 它不出现在 sources 里（那不是配置文件里的键），这里显式说明，免得被当成"来源丢了"。
	homeSrc := string(config.SourceDefault)
	if strings.TrimSpace(os.Getenv("MATE_HOME")) != "" {
		homeSrc = string(config.SourceEnv)
	}
	pf("数据目录: %s [%s]\n", cfg.HomeDir, homeSrc)
	pf("  %-22s %-38s [%s]\n", "addr", cfg.Addr, src("addr"))
	pf("  %-22s %-38s [%s]\n", "transcripts", cfg.TranscriptDir, src("transcripts"))
	pf("  %-22s %-38s [%s]\n", "db", cfg.DBPath, src("db"))
	pf("  %-22s %-38s [%s]\n", "store", storeModeLabel(cfg.StoreMode), src("store"))
	pf("  %-22s %-38s [%s]\n", "providers", cfg.ProviderDir, src("providers"))
	// 托管凭据文档（v20）：它不是配置项（没有来源、没有覆盖键），而是**数据目录下的落点**，
	// 与 `db` 同一性质。单列出来是因为"这台机器的密钥写在哪个文件里"正是排障第一个要问的，
	// 也是唯一一份**可以被复制走的明文**（0600；权限不合平台会拒读）。
	pf("  %-22s %-38s [%s]\n", "credentials", credential.CredentialsPath(cfg.HomeDir), string(config.SourceDefault))
	pf("  %-22s %-38s [%s]\n", "capabilities", cfg.CapabilityDir, src("capabilities"))
	// 空 = 关闭代建（未指定 cwd 的建会话请求会报 workspace_required），故用 emptyLabel 明确显示。
	pf("  %-22s %-38s [%s]\n", "workspace_dir", emptyLabel(cfg.WorkspaceDir), src("workspace_dir"))
	// 空 = 不限制（任何可读目录都能当工作空间）；非空 = 显式 cwd 与目录浏览都被收在根内，
	// 且 workspace_dir 会**隐式并入**生效名单（无需手写进这里）。
	pf("  %-22s %-38s [%s]\n", "workspace_roots", emptyLabel(strings.Join(cfg.WorkspaceRoots, ",")), src("workspace_roots"))
	// 两行注记挂在同一键下面（空标签保持列对齐）：中文标签宽度不一，自己缩进排版必然对不齐。
	//
	// 为什么要显式打印"另含代建根"：运维只配了一个根，却会在浏览面的 roots 里看到第二个——
	// 不解释就等于让人怀疑配置没生效（旧版本这里反而是启动失败，更容易被误读成配错）。
	if len(cfg.WorkspaceRoots) > 0 && cfg.WorkspaceDir != "" {
		pf("  %-22s %-38s [隐含]\n", "", "↑ 生效名单另含平台代建根 "+cfg.WorkspaceDir)
	}
	// 被更高优先级来源锁定时，面板的设置面会**拒绝写**（只读）：排障时要能一眼看出原因。
	if s, ok := sources["workspace_roots"]; ok && s != config.SourceFile && s != config.SourceDefault {
		pf("  %-22s %-38s [%s]\n", "", "↑ 面板设置只读（由该来源锁定）", string(s))
	}
	pf("  %-22s %-38s [%s]\n", "projection_dirs", emptyLabel(formatProjectionDirs(cfg.ProjectionDirs)), src("projection_dirs"))
	pf("  %-22s %-38s [%s]\n", "idle_timeout", cfg.IdleTimeout.String(), src("idle_timeout"))
	pf("  %-22s %-38s [%s]\n", "reap_interval", cfg.ReapInterval.String(), src("reap_interval"))
	pf("  %-22s %-38s [%s]\n", "run_timeout", cfg.RunTimeout.String(), src("run_timeout"))
	pf("  %-22s %-38d [%s]\n", "handoff_max_chars", cfg.HandoffMaxChars, src("handoff_max_chars"))
	pf("  %-22s %-38d [%s]\n", "generation_limit", cfg.GenerationLimit, src("generation_limit"))
	pf("  %-22s %-38s [%s]\n", "automations", cfg.AutomationDir, src("automations"))
	pf("  %-22s %-38s [%s]\n", "automation_tick", cfg.AutomationTick.String(), src("automation_tick"))
	// 开发期资产重建通知（P6-A）：0s = 关闭。这里**照原样打**（不打"关闭"这种好听的词）——
	// 排障时"到底是没配还是配了 0"必须能看出来。
	pf("  %-22s %-38s [%s]\n", "asset_watch", cfg.AssetWatch.String(), src("asset_watch"))
	pf("  %-22s %-38s [%s]\n", "permission_timeout", cfg.PermissionTimeout.String(), src("permission_timeout"))
	pf("  %-22s %-38s [%s]\n", "elicitation_timeout", cfg.ElicitationTimeout.String(), src("elicitation_timeout"))
	pf("  %-22s %-38s [%s]\n", "proc_registry", emptyLabel(cfg.ProcRegistryPath), src("proc_registry"))
	pf("  %-22s %-38s [%s]\n", "reap_grace", cfg.ReapGrace.String(), src("reap_grace"))
	pf("  %-22s %-38s [%s]\n", "token", maskToken(cfg.Token), src("token"))
	// 空 = 只用内置 Origin 矩阵（无 Origin / mate-app://app / 自身监听端口的同源）。
	// 非空 = 跨源开发或自建反代的显式登记（M3-5），且只在回环监听时生效（见 pkg/config 的注释）。
	pf("  %-22s %-38s [%s]\n", "allowed_origins", emptyLabel(strings.Join(cfg.AllowedOrigins, ",")), src("allowed_origins"))
	pf("  %-22s %-38s [%s]\n", "mcp_auto_probe", emptyLabel(cfg.McpAutoProbe), src("mcp_auto_probe"))
	pf("  %-22s %-38s [%s]\n", "native_permission", emptyLabel(cfg.NativePermission), src("native_permission"))
	pf("  %-22s %-38s [%s]\n", "mcp_gateway", emptyLabel(cfg.McpGateway), src("mcp_gateway"))
	if len(cfg.RemoteRuntimes) == 0 {
		pf("  %-22s %-38s [%s]\n", "remote_runtimes", "(无)", string(config.SourceDefault))
	}
	for _, rt := range cfg.RemoteRuntimes {
		pf("  %-22s %-38s [%s]\n", "remote_runtime:"+rt.ID, rt.Transport+" "+rt.Endpoint, string(config.SourceFile))
	}
}

// maskToken 令牌只显示长度与首尾各 2 字符（避免配置核对时把令牌打进日志/截图）。
func maskToken(t string) string {
	if t == "" {
		return "(未设置，不鉴权)"
	}
	if len(t) <= 8 {
		return fmt.Sprintf("(已设置，%d 字符)", len(t))
	}
	return fmt.Sprintf("%s…%s（%d 字符）", t[:2], t[len(t)-2:], len(t))
}

func storeModeLabel(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

func emptyLabel(v string) string {
	if v == "" {
		return "(空)"
	}
	return v
}

// formatProjectionDirs 把 `<runtime>:<kind>` → dir 映射渲染为稳定的 `runtime:kind=dir,…`。
// 键要排序（map 迭代无序，否则同一份配置每次打印的顺序都不同，diff 与排障都难用）。
func formatProjectionDirs(dirs map[string]string) string {
	if len(dirs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(dirs))
	for k := range dirs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+dirs[k])
	}
	return strings.Join(parts, ",")
}
