package command

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/morehao/ark-forge/pkg/config"
)

// configCmd 打印解析后的配置与每项来源（排障/上线核对用；不启动任何进程）。
func configCmd(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出（含每项来源）")
	dir := fs.String("config-dir", "", "配置目录（默认 FORGE_CONFIG_DIR 或 ~/.forge/config）")
	_ = fs.Parse(args)

	cfg, sources, err := config.LoadResolved(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	if *asJSON {
		doc := map[string]any{
			"config_dir": cfg.ConfigDir,
			"config": map[string]any{
				"addr":                    cfg.Addr,
				"transcripts":             cfg.TranscriptDir,
				"db":                      cfg.DBPath,
				"store":                   cfg.StoreMode,
				"agents":                  cfg.AgentDir,
				"capabilities":            cfg.CapabilityDir,
				"workspace_dir":           cfg.WorkspaceDir,
				"workspace_roots":         strings.Join(cfg.WorkspaceRoots, ","),
				"projection_dirs":         formatProjectionDirs(cfg.ProjectionDirs),
				"idle_timeout":            cfg.IdleTimeout.String(),
				"reap_interval":           cfg.ReapInterval.String(),
				"turn_timeout":            cfg.TurnTimeout.String(),
				"handoff_max_chars":       cfg.HandoffMaxChars,
				"permission_timeout":      cfg.PermissionTimeout.String(),
				"elicitation_timeout":     cfg.ElicitationTimeout.String(),
				"proc_registry":           cfg.ProcRegistryPath,
				"reap_grace":              cfg.ReapGrace.String(),
				"token":                   maskToken(cfg.Token),
				"mcp_auto_probe":          cfg.McpAutoProbe,
				"native_permission":       cfg.NativePermission,
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
	fmt.Printf("配置目录: %s\n", cfg.ConfigDir)
	fmt.Printf("  %-22s %-38s [%s]\n", "addr", cfg.Addr, src("addr"))
	fmt.Printf("  %-22s %-38s [%s]\n", "transcripts", cfg.TranscriptDir, src("transcripts"))
	fmt.Printf("  %-22s %-38s [%s]\n", "db", cfg.DBPath, src("db"))
	fmt.Printf("  %-22s %-38s [%s]\n", "store", storeModeLabel(cfg.StoreMode), src("store"))
	fmt.Printf("  %-22s %-38s [%s]\n", "agents", cfg.AgentDir, src("agents"))
	fmt.Printf("  %-22s %-38s [%s]\n", "capabilities", cfg.CapabilityDir, src("capabilities"))
	// 空 = 关闭代建（未指定 cwd 的建会话请求会报 workspace_required），故用 emptyLabel 明确显示。
	fmt.Printf("  %-22s %-38s [%s]\n", "workspace_dir", emptyLabel(cfg.WorkspaceDir), src("workspace_dir"))
	// 空 = 不限制（任何可读目录都能当工作空间）；非空 = 显式 cwd 与目录浏览都被收在根内，
	// 且 workspace_dir 会**隐式并入**生效名单（无需手写进这里）。
	fmt.Printf("  %-22s %-38s [%s]\n", "workspace_roots", emptyLabel(strings.Join(cfg.WorkspaceRoots, ",")), src("workspace_roots"))
	// 两行注记挂在同一键下面（空标签保持列对齐）：中文标签宽度不一，自己缩进排版必然对不齐。
	//
	// 为什么要显式打印"另含代建根"：运维只配了一个根，却会在浏览面的 roots 里看到第二个——
	// 不解释就等于让人怀疑配置没生效（旧版本这里反而是启动失败，更容易被误读成配错）。
	if len(cfg.WorkspaceRoots) > 0 && cfg.WorkspaceDir != "" {
		fmt.Printf("  %-22s %-38s [隐含]\n", "", "↑ 生效名单另含平台代建根 "+cfg.WorkspaceDir)
	}
	// 被更高优先级来源锁定时，面板的设置面会**拒绝写**（只读）：排障时要能一眼看出原因。
	if s, ok := sources["workspace_roots"]; ok && s != config.SourceFile && s != config.SourceDefault {
		fmt.Printf("  %-22s %-38s [%s]\n", "", "↑ 面板设置只读（由该来源锁定）", string(s))
	}
	fmt.Printf("  %-22s %-38s [%s]\n", "projection_dirs", emptyLabel(formatProjectionDirs(cfg.ProjectionDirs)), src("projection_dirs"))
	fmt.Printf("  %-22s %-38s [%s]\n", "idle_timeout", cfg.IdleTimeout.String(), src("idle_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "reap_interval", cfg.ReapInterval.String(), src("reap_interval"))
	fmt.Printf("  %-22s %-38s [%s]\n", "turn_timeout", cfg.TurnTimeout.String(), src("turn_timeout"))
	fmt.Printf("  %-22s %-38d [%s]\n", "handoff_max_chars", cfg.HandoffMaxChars, src("handoff_max_chars"))
	fmt.Printf("  %-22s %-38s [%s]\n", "permission_timeout", cfg.PermissionTimeout.String(), src("permission_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "elicitation_timeout", cfg.ElicitationTimeout.String(), src("elicitation_timeout"))
	fmt.Printf("  %-22s %-38s [%s]\n", "proc_registry", emptyLabel(cfg.ProcRegistryPath), src("proc_registry"))
	fmt.Printf("  %-22s %-38s [%s]\n", "reap_grace", cfg.ReapGrace.String(), src("reap_grace"))
	fmt.Printf("  %-22s %-38s [%s]\n", "token", maskToken(cfg.Token), src("token"))
	fmt.Printf("  %-22s %-38s [%s]\n", "mcp_auto_probe", emptyLabel(cfg.McpAutoProbe), src("mcp_auto_probe"))
	fmt.Printf("  %-22s %-38s [%s]\n", "native_permission", emptyLabel(cfg.NativePermission), src("native_permission"))
	if len(cfg.RemoteRuntimes) == 0 {
		fmt.Printf("  %-22s %-38s [%s]\n", "remote_runtimes", "(无)", string(config.SourceDefault))
	}
	for _, rt := range cfg.RemoteRuntimes {
		fmt.Printf("  %-22s %-38s [%s]\n", "remote_runtime:"+rt.ID, rt.Transport+" "+rt.Endpoint, string(config.SourceFile))
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
