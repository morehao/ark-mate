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

// TestCapabilityDirIndependentOfSkillMaterialize 回归：FORGE_CAPABILITIES 必须生效，
// 且不受 FORGE_SKILLS_MATERIALIZE_DIR 是否设置影响（曾因 else 分支误覆盖 CapabilityDir
// 导致能力目录被吞、永远落到 ~/.forge/capabilities）。
func TestCapabilityDirIndependentOfSkillMaterialize(t *testing.T) {
	hermetic(t)
	unset(t, "FORGE_SKILLS_MATERIALIZE_DIR")
	tsetenv(t, "FORGE_CAPABILITIES", "/tmp/forge-caps")
	c := Load()
	if c.CapabilityDir != "/tmp/forge-caps" {
		t.Fatalf("FORGE_CAPABILITIES 未生效: %q", c.CapabilityDir)
	}
	if c.SkillMaterializeDir != "" {
		t.Fatalf("未设置物化目录时应为空（默认不写用户目录），got %q", c.SkillMaterializeDir)
	}

	tsetenv(t, "FORGE_SKILLS_MATERIALIZE_DIR", "/tmp/forge-skills")
	c = Load()
	if c.CapabilityDir != "/tmp/forge-caps" || c.SkillMaterializeDir != "/tmp/forge-skills" {
		t.Fatalf("两项应互不影响: caps=%q skills=%q", c.CapabilityDir, c.SkillMaterializeDir)
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
  "handoff_max_chars": 1234
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

// TestSkillMaterializeDirs C4 多 root 物化配置解析（runtime=dir 列表）。
func TestSkillMaterializeDirs(t *testing.T) {
	hermetic(t)
	t.Setenv("FORGE_SKILLS_MATERIALIZE_DIRS", "opencode=/tmp/oc, deepseekharness=/tmp/dsh")
	cfg := Load()
	if len(cfg.SkillMaterializeDirs) != 2 || cfg.SkillMaterializeDirs["opencode"] != "/tmp/oc" ||
		cfg.SkillMaterializeDirs["deepseekharness"] != "/tmp/dsh" {
		t.Fatalf("解析不符: %+v", cfg.SkillMaterializeDirs)
	}

	// 非法形态 → 配置错误。
	// 非法形态 → LoadResolved 报错（daemon 启动路径暴露配置错误）。
	for _, bad := range []string{"opencode", "=dir", "opencode=", "opencode=/a,opencode=/b"} {
		t.Setenv("FORGE_SKILLS_MATERIALIZE_DIRS", bad)
		if _, _, err := LoadResolved(""); err == nil {
			t.Fatalf("非法配置 %q 应报错", bad)
		}
	}
	// 未设置 → 空 map（不启用）。
	t.Setenv("FORGE_SKILLS_MATERIALIZE_DIRS", "")
	if cfg := Load(); len(cfg.SkillMaterializeDirs) != 0 {
		t.Fatalf("空值应不启用: %+v", cfg.SkillMaterializeDirs)
	}
}
