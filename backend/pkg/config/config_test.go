package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tsetenv 设置环境变量并在测试结束后恢复（为空值视为“未设置”）。
func tsetenv(t *testing.T, key, val string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// unset 清除环境变量并在测试结束后恢复。
func unset(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, old)
		}
	})
}

// TestLoadDefaults 无环境变量时默认 127.0.0.1:10001（仅回环，无鉴权面不收外网）+ ~/.forge/transcripts。
func TestLoadDefaults(t *testing.T) {
	hermetic(t)
	unset(t, "FORGE_ADDR")
	unset(t, "FORGE_TRANSCRIPTS")
	c := Load()
	if c.Addr != "127.0.0.1:10001" {
		t.Fatalf("默认 Addr 不符: %q", c.Addr)
	}
	if c.TranscriptDir == "" {
		t.Fatal("TranscriptDir 默认不应为空")
	}
	if filepath.Base(c.TranscriptDir) != "transcripts" {
		t.Fatalf("默认 TranscriptDir 不符: %q", c.TranscriptDir)
	}
}

// TestLoadEnvOverrides 环境变量覆盖默认值。
func TestLoadEnvOverrides(t *testing.T) {
	hermetic(t)
	tsetenv(t, "FORGE_ADDR", ":9999")
	tsetenv(t, "FORGE_TRANSCRIPTS", "/tmp/forge-tx")
	c := Load()
	if c.Addr != ":9999" {
		t.Fatalf("Addr 覆盖不符: %q", c.Addr)
	}
	if c.TranscriptDir != "/tmp/forge-tx" {
		t.Fatalf("TranscriptDir 覆盖不符: %q", c.TranscriptDir)
	}
}

// TestDefault 默认值结构。
func TestDefault(t *testing.T) {
	if d := Default(); d.Addr != "127.0.0.1:10001" || d.TranscriptDir != "" {
		t.Fatalf("Default 不符: %+v", d)
	}
}

// TestCapabilityDirIndependentOfProjectionDirs 回归：FORGE_CAPABILITIES 必须生效，
// 且不受 FORGE_PROJECTION_DIRS 是否设置影响（曾因 else 分支误覆盖 CapabilityDir
// 导致能力目录被吞、永远落到 ~/.forge/capabilities）。
func TestCapabilityDirIndependentOfProjectionDirs(t *testing.T) {
	hermetic(t)
	unset(t, "FORGE_PROJECTION_DIRS")
	tsetenv(t, "FORGE_CAPABILITIES", "/tmp/forge-caps")
	c := Load()
	if c.CapabilityDir != "/tmp/forge-caps" {
		t.Fatalf("FORGE_CAPABILITIES 未生效: %q", c.CapabilityDir)
	}
	if len(c.ProjectionDirs) != 0 {
		t.Fatalf("未设置投影覆盖时应为空（默认全用 adapter 声明的原生根），got %+v", c.ProjectionDirs)
	}

	tsetenv(t, "FORGE_PROJECTION_DIRS", "opencode:skill=/tmp/forge-skills")
	c = Load()
	if c.CapabilityDir != "/tmp/forge-caps" || c.ProjectionDirs["opencode:skill"] != "/tmp/forge-skills" {
		t.Fatalf("两项应互不影响: caps=%q projection=%q", c.CapabilityDir, c.ProjectionDirs)
	}
}

// hermetic 把配置目录指向空临时目录（避免开发者本机 ~/.forge/config 影响断言）。
func hermetic(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tsetenv(t, "FORGE_CONFIG_DIR", dir)
	return dir
}

// TestConfigDirFilePriority 优先级：config.json < env 文件 < 进程环境变量（A4）。
func TestConfigDirFilePriority(t *testing.T) {
	dir := hermetic(t)
	unset(t, "FORGE_ADDR")
	unset(t, "FORGE_DB")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
  "addr": "127.0.0.1:20001",
  "db": "/tmp/from-config/db.sqlite",
  "FORGE_IDLE_TIMEOUT": "90s",
  "handoff_max_chars": 1234,
  "generation_limit": 7
}`), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(`
# dotenv 风格：注释与空行忽略
FORGE_ADDR=127.0.0.1:20002
export FORGE_REAP_GRACE=7s
agents = /tmp/from-envfile/agents
`), 0o600); err != nil {
		t.Fatalf("写 env: %v", err)
	}

	c, src, err := LoadResolved("")
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if c.Addr != "127.0.0.1:20002" {
		t.Fatalf("env 文件应覆盖 config.json: %q", c.Addr)
	}
	if src["addr"] != SourceEnvFile {
		t.Fatalf("addr 来源应为 env 文件: %v", src["addr"])
	}
	if c.DBPath != "/tmp/from-config/db.sqlite" || src["db"] != SourceFile {
		t.Fatalf("config.json 的 db 未生效: %q %v", c.DBPath, src["db"])
	}
	if c.IdleTimeout.String() != "1m30s" || src["idle_timeout"] != SourceFile {
		t.Fatalf("FORGE_ 前缀键名应被接受: %v %v", c.IdleTimeout, src["idle_timeout"])
	}
	if c.HandoffMaxChars != 1234 {
		t.Fatalf("数字标量应被接受: %d", c.HandoffMaxChars)
	}
	if c.GenerationLimit != 7 {
		t.Fatalf("generation_limit 应被读入: %d", c.GenerationLimit)
	}
	if c.ReapGrace.String() != "7s" || src["reap_grace"] != SourceEnvFile {
		t.Fatalf("export 前缀/别名键未生效: %v %v", c.ReapGrace, src["reap_grace"])
	}
	if c.AgentDir != "/tmp/from-envfile/agents" {
		t.Fatalf("含空格键值未生效: %q", c.AgentDir)
	}
	// proc_registry 未显式设置 → 派生自 db 目录。
	if c.ProcRegistryPath != "/tmp/from-config/processes.json" {
		t.Fatalf("proc_registry 应派生自 db 目录: %q", c.ProcRegistryPath)
	}

	// 进程环境变量优先级最高。
	tsetenv(t, "FORGE_ADDR", "127.0.0.1:20003")
	c2, src2, err := LoadResolved("")
	if err != nil {
		t.Fatalf("LoadResolved(env): %v", err)
	}
	if c2.Addr != "127.0.0.1:20003" || src2["addr"] != SourceEnv {
		t.Fatalf("进程环境变量应最高: %q %v", c2.Addr, src2["addr"])
	}
	if c2.DBPath != "/tmp/from-config/db.sqlite" {
		t.Fatalf("未被环境变量覆盖的项应保持文件值: %q", c2.DBPath)
	}
}

// TestConfigDirMissingIsNoop 目录/文件缺失 = 历史行为不变（向后兼容）。
func TestConfigDirMissingIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	tsetenv(t, "FORGE_CONFIG_DIR", dir)
	unset(t, "FORGE_ADDR")
	c, src, err := LoadResolved("")
	if err != nil {
		t.Fatalf("目录缺失不应报错: %v", err)
	}
	if c.Addr != "127.0.0.1:10001" || c.ConfigDir != dir {
		t.Fatalf("缺省行为变化: %+v", c)
	}
	if len(src) != 0 {
		t.Fatalf("无文件时不应有任何来源: %v", src)
	}
}

// TestConfigDirErrors 非法键名/取值必须启动即报错（不静默回落）。
func TestConfigDirErrors(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{"未知键(config.json)", "config.json", `{"adr":"x"}`, "未知配置键"},
		{"非法 JSON", "config.json", `{`, "不是合法 JSON 对象"},
		{"非标量", "config.json", `{"addr":{"a":1}}`, "标量"},
		{"非法 duration", "config.json", `{"idle_timeout":"5 minutes"}`, "duration"},
		{"非法整数", "config.json", `{"handoff_max_chars":"abc"}`, "非负整数"},
		{"非法整数(代际上限)", "config.json", `{"generation_limit":"abc"}`, "非负整数"},
		{"负数(代际上限)", "config.json", `{"generation_limit":-1}`, "非负整数"},
		{"未知键(env)", "env", "FORGE_NOPE=1\n", "未知配置键"},
		{"缺等号(env)", "env", "FORGE_ADDR\n", "KEY=VALUE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("写文件: %v", err)
			}
			if _, _, err := LoadResolved(dir); err == nil {
				t.Fatal("应报错")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestLoadResolvedExplicitDir 显式目录参数优先于 FORGE_CONFIG_DIR。
func TestLoadResolvedExplicitDir(t *testing.T) {
	hermetic(t) // 指向空目录（会被忽略）
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"addr":"127.0.0.1:29999"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, src, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if c.Addr != "127.0.0.1:29999" || c.ConfigDir != dir || src["addr"] != SourceFile {
		t.Fatalf("显式目录未生效: %+v %v", c, src)
	}
}

// TestRemoteRuntimesJSON 远程 runtime 声明（runtimes.json，A5）：解析 + 归一化。
func TestRemoteRuntimesJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtimes.json"), []byte(`[
  {"id":"remote-echo","name":"Remote Echo","transport":"WS","endpoint":"ws://127.0.0.1:10020/acp","headers":{"Authorization":"Bearer t"}},
  {"id":"remote-http","transport":"streamable_http","endpoint":"http://127.0.0.1:10021/acp"}
]`), 0o600); err != nil {
		t.Fatalf("写 runtimes.json: %v", err)
	}
	c, _, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("LoadResolved: %v", err)
	}
	if len(c.RemoteRuntimes) != 2 {
		t.Fatalf("远程 runtime 数量不符: %+v", c.RemoteRuntimes)
	}
	if got := c.RemoteRuntimes[0]; got.ID != "remote-echo" || got.Transport != "ws" ||
		got.Endpoint != "ws://127.0.0.1:10020/acp" || got.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("第一条归一化不符: %+v", got)
	}
	if got := c.RemoteRuntimes[1]; got.Transport != "http" || got.Endpoint != "http://127.0.0.1:10021/acp" {
		t.Fatalf("第二条归一化不符: %+v", got)
	}
	// 未提供 name → 留空（注册层回落到 id）。
	if c.RemoteRuntimes[1].Name != "" {
		t.Fatalf("name 应留空: %q", c.RemoteRuntimes[1].Name)
	}
}

// TestRemoteRuntimesJSONErrors 非法声明必须启动即报错。
func TestRemoteRuntimesJSONErrors(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"缺 id", `[{"transport":"ws","endpoint":"ws://127.0.0.1:1/acp"}]`, "id 必填"},
		{"id 非法", `[{"id":"Remote Echo","transport":"ws","endpoint":"ws://127.0.0.1:1/acp"}]`, "id"},
		{"id 重复", `[{"id":"a","transport":"ws","endpoint":"ws://127.0.0.1:1/a"},{"id":"a","transport":"ws","endpoint":"ws://127.0.0.1:1/b"}]`, "重复"},
		{"缺 transport", `[{"id":"a","endpoint":"ws://127.0.0.1:1/a"}]`, "transport 必填"},
		{"transport 不支持", `[{"id":"a","transport":"grpc","endpoint":"ws://127.0.0.1:1/a"}]`, "不支持"},
		{"scheme 不匹配", `[{"id":"a","transport":"ws","endpoint":"http://127.0.0.1:1/a"}]`, "ws(s)"},
		{"endpoint 非法", `[{"id":"a","transport":"ws","endpoint":"nope"}]`, "endpoint"},
		{"非数组", `{"id":"a"}`, "JSON 数组"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "runtimes.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := LoadResolved(dir); err == nil {
				t.Fatal("应报错")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestMcpAutoProbeMode B4：mcp_auto_probe 取值解析（off 默认 / startup / 大小写 / 非法值报错）。
func TestMcpAutoProbeMode(t *testing.T) {
	hermetic(t)
	unset(t, "FORGE_MCP_AUTO_PROBE")

	c, _, err := LoadResolved("")
	if err != nil {
		t.Fatalf("默认加载失败: %v", err)
	}
	if c.McpAutoProbe != McpAutoProbeOff {
		t.Fatalf("默认应为 off，got %q", c.McpAutoProbe)
	}

	tsetenv(t, "FORGE_MCP_AUTO_PROBE", "startup")
	c, srcs, err := LoadResolved("")
	if err != nil {
		t.Fatalf("startup 加载失败: %v", err)
	}
	if c.McpAutoProbe != McpAutoProbeStartup {
		t.Fatalf("应为 startup，got %q", c.McpAutoProbe)
	}
	if srcs["mcp_auto_probe"] == "" {
		t.Fatal("应记录取值来源")
	}

	tsetenv(t, "FORGE_MCP_AUTO_PROBE", "STARTUP")
	if c, _, err := LoadResolved(""); err != nil || c.McpAutoProbe != McpAutoProbeStartup {
		t.Fatalf("大小写不敏感失败: %v %q", err, c.McpAutoProbe)
	}

	tsetenv(t, "FORGE_MCP_AUTO_PROBE", "off")
	if c, _, err := LoadResolved(""); err != nil || c.McpAutoProbe != McpAutoProbeOff {
		t.Fatalf("off 解析失败: %v %q", err, c.McpAutoProbe)
	}

	tsetenv(t, "FORGE_MCP_AUTO_PROBE", "bogus")
	if _, _, err := LoadResolved(""); err == nil || !strings.Contains(err.Error(), "off|startup") {
		t.Fatalf("非法值应报错并列出合法取值，got %v", err)
	}
}

// TestStoreMode 钉住持久层模式的取值语义。
//
// 重点是**非法值必须报错**：此前非法/空值都静默回落 auto，而 auto 会按"DB 文件在不在"
// 解析成 sqlite 或 memory——一个错字可能把用户悄悄丢进相反的模式（sqlit 在无 DB 时进
// memory，表现为"数据丢了"），且回落后的 `forge config` 输出与合法的 auto 逐字相同，
// 用户拿不到任何"值被丢弃了"的信号。未设置（无偏好）仍必须宽松地走 auto。
func TestStoreMode(t *testing.T) {
	hermetic(t)
	unset(t, "FORGE_STORE")

	// 未设置 = 无偏好 = auto（空串），且不应报错。
	c, _, err := LoadResolved("")
	if err != nil {
		t.Fatalf("未设置时不应报错: %v", err)
	}
	if c.StoreMode != "" {
		t.Fatalf("未设置应为 auto（空串），got %q", c.StoreMode)
	}

	// 显式 auto 与未设置等价，但它是**合法取值**（文档一直这么写）。
	tsetenv(t, "FORGE_STORE", StoreModeAuto)
	c, srcs, err := LoadResolved("")
	if err != nil {
		t.Fatalf("显式 auto 不应报错: %v", err)
	}
	if c.StoreMode != "" {
		t.Fatalf("显式 auto 应归一成空串，got %q", c.StoreMode)
	}
	if srcs["store"] == "" {
		t.Fatal("应记录取值来源")
	}

	for _, tc := range []struct{ in, want string }{
		{"sqlite", StoreModeSQLite},
		{"memory", StoreModeMemory},
		{"SQLITE", StoreModeSQLite},     // 大小写不敏感
		{"  memory  ", StoreModeMemory}, // 前后空白容忍
	} {
		tsetenv(t, "FORGE_STORE", tc.in)
		c, _, err := LoadResolved("")
		if err != nil {
			t.Fatalf("%q 不应报错: %v", tc.in, err)
		}
		if c.StoreMode != tc.want {
			t.Errorf("%q 应解析为 %q，got %q", tc.in, tc.want, c.StoreMode)
		}
	}

	// 非法值必须报错，且提示里要列出合法取值（用户照着改就行）。
	// 非法值必须报错，且提示里要列出合法取值（用户照着改就行）。
	// 注意 "auto "（带尾空格）**不算非法**：与同级枚举键一致地做了 TrimSpace，见上面的空白容忍用例。
	for _, bad := range []string{"sqlit", "memry", "garbage", "postgres"} {
		tsetenv(t, "FORGE_STORE", bad)
		_, _, err := LoadResolved("")
		if err == nil {
			t.Errorf("%q 是非法取值，必须报错（此前会静默回落 auto）", bad)
			continue
		}
		if !strings.Contains(err.Error(), "auto|sqlite|memory") {
			t.Errorf("%q 的报错应列出合法取值 auto|sqlite|memory，got %v", bad, err)
		}
	}
}

// TestProjectionDirs 投影根覆盖配置解析（`runtime:kind=dir` 列表）。
func TestProjectionDirs(t *testing.T) {
	hermetic(t)
	t.Setenv("FORGE_PROJECTION_DIRS", "opencode:skill=/tmp/oc, deepseekharness:skill=/tmp/dsh")
	cfg := Load()
	if len(cfg.ProjectionDirs) != 2 || cfg.ProjectionDirs["opencode:skill"] != "/tmp/oc" ||
		cfg.ProjectionDirs["deepseekharness:skill"] != "/tmp/dsh" {
		t.Fatalf("解析不符: %+v", cfg.ProjectionDirs)
	}
	// 目录里的 `=` 只按**第一个** `=` 切分（路径含 `=` 也能表达）。
	t.Setenv("FORGE_PROJECTION_DIRS", "opencode:skill=/tmp/a=b")
	if cfg := Load(); cfg.ProjectionDirs["opencode:skill"] != "/tmp/a=b" {
		t.Fatalf("含 `=` 的路径应保留: %+v", cfg.ProjectionDirs)
	}
	// 相对路径会被 Clean，但仍是相对 → 必须报错（相对谁？没有答案）。
	for _, bad := range []string{
		"opencode",                            // 缺 `=`
		"opencode=/tmp/oc",                    // 缺 `:kind`
		":skill=/tmp/oc",                      // 缺 runtime
		"opencode:=/tmp/oc",                   // 缺 kind
		"opencode:bogus=/tmp/oc",              // kind 不在合法集（写错就让覆盖永远匹配不上）
		"opencode:skill=",                     // 目录为空
		"opencode:skill=relative/dir",         // 非绝对路径
		"opencode:skill=/a,opencode:skill=/b", // 重复声明同一个投影
	} {
		// 注意：显式设为**空串**不算错误——进程环境只对"非空"生效（历史语义，全键一致），
		// 空 = 未设置 = 全用 adapter 的原生根，见下面的断言。
		t.Setenv("FORGE_PROJECTION_DIRS", bad)
		if _, _, err := LoadResolved(""); err == nil {
			t.Fatalf("非法配置 %q 应报错", bad)
		}
	}
	// 未设置 → 空 map（全用 adapter 声明的原生根）。
	t.Setenv("FORGE_PROJECTION_DIRS", "")
	if cfg := Load(); len(cfg.ProjectionDirs) != 0 {
		t.Fatalf("空值应不启用: %+v", cfg.ProjectionDirs)
	}
}

// TestRenamedKeysRejectedWithHint 旧键不兼容但**必须被认出来**：报错文案要给出新键名与等价写法。
//
// 直接删键的话旧键会掉进通用的「未知配置键」里，用户只能靠自己猜新名字；而静默忽略更糟——
// 物化会悄悄不发生，表现为「技能怎么不生效了」。所以这里断言的正是"文案里有没有出路"。
func TestRenamedKeysRejectedWithHint(t *testing.T) {
	hermetic(t)
	for _, old := range []struct{ key, env, wantHint string }{
		{"skills_materialize_dir", "FORGE_SKILLS_MATERIALIZE_DIR", "FORGE_PROJECTION_DIRS"},
		{"skills_materialize_dirs", "FORGE_SKILLS_MATERIALIZE_DIRS", "FORGE_PROJECTION_DIRS"},
	} {
		// 进程环境变量：只按 knownKeys 读取，故旧变量名必须显式检查（否则设了也不生效且无声）。
		t.Setenv(old.env, "/tmp/whatever")
		_, _, err := LoadResolved("")
		if err == nil || !strings.Contains(err.Error(), old.env) || !strings.Contains(err.Error(), old.wantHint) {
			t.Fatalf("旧环境变量 %s 应报可操作的错，got %v", old.env, err)
		}
		os.Unsetenv(old.env)
		unset(t, old.env)
		// config.json 里的旧键同样被认出来。
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"`+old.key+`":"/tmp/x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadResolved(dir); err == nil ||
			!strings.Contains(err.Error(), old.key) || !strings.Contains(err.Error(), old.wantHint) {
			t.Fatalf("config.json 旧键 %s 应报可操作的错，got %v", old.key, err)
		}
		// env 文件里的旧键同理。
		dir2 := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir2, "env"), []byte(old.env+"=/tmp/x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadResolved(dir2); err == nil ||
			!strings.Contains(err.Error(), old.key) || !strings.Contains(err.Error(), old.wantHint) {
			t.Fatalf("env 文件旧键 %s 应报可操作的错，got %v", old.key, err)
		}
	}
}

// TestWorkspaceRoots 工作区根白名单（FORGE_WORKSPACE_ROOTS，A 的边界）解析与形态校验。
func TestWorkspaceRoots(t *testing.T) {
	hermetic(t)

	// 逗号分隔 + 空白容忍 + 去重（同一目录写两次只会让错误信息变长）。
	t.Setenv("FORGE_WORKSPACE_ROOTS", " /srv/ws , /data/work ,/srv/ws,")
	cfg := Load()
	want := []string{"/srv/ws", "/data/work"}
	if len(cfg.WorkspaceRoots) != len(want) {
		t.Fatalf("解析不符: %+v", cfg.WorkspaceRoots)
	}
	for i := range want {
		if cfg.WorkspaceRoots[i] != want[i] {
			t.Fatalf("第 %d 项不符: %+v", i, cfg.WorkspaceRoots)
		}
	}

	// 相对路径不是边界（比较基于真身，必须能解析成绝对路径）→ 启动即报错。
	for _, bad := range []string{"relative/root", "/srv/ws,relative"} {
		t.Setenv("FORGE_WORKSPACE_ROOTS", bad)
		if _, _, err := LoadResolved(""); err == nil {
			t.Fatalf("非法配置 %q 应报错", bad)
		}
	}

	// 未设置 / 显式空 = 不限制（空切片，与"未设置"同义）。
	for _, v := range []string{"", "   "} {
		t.Setenv("FORGE_WORKSPACE_ROOTS", v)
		if cfg := Load(); len(cfg.WorkspaceRoots) != 0 {
			t.Fatalf("空值应等于不限制: %+v", cfg.WorkspaceRoots)
		}
	}
}

// TestWorkspaceRootsArrayForm config.json 的**数组形式**：路径里含逗号时唯一能表达它的写法，
// 也是面板写回用的形式（写回格式与读回格式必须自洽，见 SettingsStore.SaveWorkspaceRoots）。
func TestWorkspaceRootsArrayForm(t *testing.T) {
	dir := hermetic(t)
	unset(t, "FORGE_WORKSPACE_ROOTS")
	// 第二条路径含逗号：逗号分隔写法根本无法表达，只能数组。
	body := `{"workspace_roots": ["/srv/ws", "/data/a,b", "/srv/ws"]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	cfg, src, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("数组形式应合法: %v", err)
	}
	if want := []string{"/srv/ws", "/data/a,b"}; len(cfg.WorkspaceRoots) != 2 ||
		cfg.WorkspaceRoots[0] != want[0] || cfg.WorkspaceRoots[1] != want[1] {
		t.Fatalf("数组解析不符（含逗号路径应保真 + 去重）: %+v", cfg.WorkspaceRoots)
	}
	if src["workspace_roots"] != SourceFile {
		t.Fatalf("来源应为 config.json: %v", src["workspace_roots"])
	}

	// 进程 env（更高优先级）覆盖数组形式：两种**书写形式**不能各自为政。
	tsetenv(t, "FORGE_WORKSPACE_ROOTS", "/env/root")
	if cfg := Load(); len(cfg.WorkspaceRoots) != 1 || cfg.WorkspaceRoots[0] != "/env/root" {
		t.Fatalf("env 应覆盖 config.json 的数组值: %+v", cfg.WorkspaceRoots)
	}
	unset(t, "FORGE_WORKSPACE_ROOTS")

	// 单值键写成数组 = 配置错误（静默取第一项会让运维以为整份配置生效了）。
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"addr": ["a","b"]}`), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	if _, _, err := LoadResolved(dir); err == nil || !strings.Contains(err.Error(), "不支持数组值") {
		t.Fatalf("单值键写成数组应报错，got %v", err)
	}
	// 数组元素非字符串：不静默字符串化。
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"workspace_roots": [1]}`), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	if _, _, err := LoadResolved(dir); err == nil || !strings.Contains(err.Error(), "需为字符串") {
		t.Fatalf("数组元素类型错误应报错，got %v", err)
	}
}

// TestSaveWorkspaceRootsRoundTrip 写回与读回自洽：面板保存的东西，重启后解析出**同一份**名单，
// 且文件里其它键原样保留（read-modify-write 不是"覆盖整份配置"）。
func TestSaveWorkspaceRootsRoundTrip(t *testing.T) {
	dir := hermetic(t)
	unset(t, "FORGE_WORKSPACE_ROOTS")
	// 预置其它键（含 FORGE_* 形式的键：写回不得把它们改写成规范名或丢掉）。
	seed := `{"addr":"127.0.0.1:20001","FORGE_IDLE_TIMEOUT":"90s"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("写 config.json: %v", err)
	}
	st := NewSettingsStore(dir, SourceDefault)
	// 含逗号的路径：数组形式是唯一能原样往返的写法（写回用 join(",") 就会劈成两条）。
	roots := []string{"/srv/ws", "/data/a,b"}
	if err := st.SaveWorkspaceRoots(roots); err != nil {
		t.Fatalf("写回: %v", err)
	}
	cfg, src, err := LoadResolved(dir)
	if err != nil {
		t.Fatalf("写回后配置应仍合法: %v", err)
	}
	if len(cfg.WorkspaceRoots) != 2 || cfg.WorkspaceRoots[1] != "/data/a,b" {
		t.Fatalf("写回-读回不自洽: %+v", cfg.WorkspaceRoots)
	}
	if cfg.Addr != "127.0.0.1:20001" || cfg.IdleTimeout.String() != "1m30s" {
		t.Fatalf("其它键被写回破坏: addr=%q idle=%s", cfg.Addr, cfg.IdleTimeout)
	}
	if src["workspace_roots"] != SourceFile {
		t.Fatalf("写回后来源应为 config.json: %v", src["workspace_roots"])
	}
	// 写回 = 面板可写状态：来源是文件，不锁。
	if _, locked := st.Status(); locked != "" {
		t.Fatalf("config.json 来源不应锁定: %q", locked)
	}

	// 空名单 = 恢复不限制：删键（而不是留一个空数组在文件里）。
	if err := st.SaveWorkspaceRoots(nil); err != nil {
		t.Fatalf("清空: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if strings.Contains(string(raw), "workspace_roots") {
		t.Fatalf("空名单应删除该键: %s", raw)
	}
	if cfg := Load(); len(cfg.WorkspaceRoots) != 0 {
		t.Fatalf("清空后应为不限制: %+v", cfg.WorkspaceRoots)
	}
	// 坏文件不"顺手修好"：替运维决定等于把问题藏起来（下次启动会直接失败）。
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{`), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	if err := st.SaveWorkspaceRoots([]string{"/srv/ws"}); err == nil {
		t.Fatal("坏文件应报错而不是覆盖")
	}
	// 无配置目录的进程（acp-serve/demo）不可写。
	if err := NewSettingsStore("", SourceDefault).SaveWorkspaceRoots([]string{"/srv/ws"}); err == nil {
		t.Fatal("无配置目录应拒绝写")
	}
}

// TestSettingsStoreStatus 锁定来源必须指向运维**可直接去改的位置**（env 变量名 / env 文件路径），
// 而不是抽象的 "env(process)"：面板据此转只读，报错要能直接指向下一步动作。
func TestSettingsStoreStatus(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		src        Source
		wantSrc    string
		wantLocked string
	}{
		{"", "default", ""},
		{SourceDefault, "default", ""},
		{SourceFile, "config.json", ""},
		{SourceEnv, "env(process)", "FORGE_WORKSPACE_ROOTS"},
		{SourceEnvFile, "env", filepath.Join(dir, "env")},
		{SourceFlag, "flag", "命令行参数"},
	}
	for _, tc := range cases {
		src, locked := NewSettingsStore(dir, tc.src).Status()
		if src != tc.wantSrc || locked != tc.wantLocked {
			t.Fatalf("src=%q: got (%q,%q) want (%q,%q)", tc.src, src, locked, tc.wantSrc, tc.wantLocked)
		}
	}
}
