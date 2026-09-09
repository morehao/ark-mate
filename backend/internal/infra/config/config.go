// Package config 提供进程级配置加载。
//
// 优先级（后者覆盖前者）：
//  1. 内置默认值（Default()）
//  2. 配置目录 `config.json`（`.forge/config` 目录化；A4）
//  3. 配置目录 `env`（dotenv 风格 KEY=VALUE，便于与 `docker --env-file`/systemd 共用一个文件）
//  4. 进程环境变量 `FORGE_*`（历史行为，始终最高）
//  5. CLI flag（由 cmd 层在 Load 之后覆盖）
//
// 配置目录取自 `FORGE_CONFIG_DIR`，默认 `<默认数据目录>/config`（即 `~/.forge/config`）。
// 目录不存在 = 完全维持历史行为（向后兼容）；文件存在但键名/取值非法 = 启动即报错（不静默忽略）。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Source 标识某个配置键的最终来源（`forge config` 展示 + 排障用）。
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "config.json"
	SourceEnvFile Source = "env"
	SourceEnv     Source = "env(process)"
	SourceFlag    Source = "flag"
)

// Sources 键（规范名，如 "addr"）→ 来源。
type Sources map[string]Source

// Config 是进程级配置。
type Config struct {
	// Addr HTTP 监听地址。
	Addr string
	// TranscriptDir 会话 wire transcript 落盘目录（M1 #5；空 = 禁用）。
	TranscriptDir string
	// DBPath SQLite 库文件路径（P1 落库：events/sessions/…；FORGE_DB 覆盖，默认 ~/.forge/forge.db）。
	DBPath string
	// StoreMode 持久层开关："sqlite" | "memory" | ""(auto：DB 文件已存在 → sqlite，否则 memory)。
	// FORGE_STORE 覆盖。
	StoreMode string
	// HandoffMaxChars bounded handoff memo 字符预算（§5.6/§18 D-3；FORGE_HANDOFF_MAX_CHARS
	// 覆盖，默认 6000；0 = 关闭 memo 兜底）。跨 runtime / 无原生 resume 时把历史压成摘要注入
	// 首条 prompt。
	HandoffMaxChars int
	// TurnTimeout 单轮 prompt 看门狗超时（0 = 关闭）。runtime 吞错/挂起（如 opencode 模型限流
	// 只记自己日志不上报）时，超时自动 cancel 并发终态事件，避免 busy 永久占用。
	TurnTimeout time.Duration
	// PermissionTimeout 挂起权限请求自动拒绝超时（默认 30min，对齐 AionUi F-PERM-01）。
	PermissionTimeout time.Duration
	// ElicitationTimeout 挂起结构化提问自动取消超时（默认 30min，对齐 AionUi）。
	ElicitationTimeout time.Duration
	// AgentDir Agent definitions 目录（P2：<id>.json 文件定义自定义 agent；FORGE_AGENTS 覆盖，
	// 默认 ~/.forge/agents；空 = 不启用 definitions 落盘，仅内存注册）。
	AgentDir string
	// CapabilityDir 能力数据面目录（P1：skills|mcp|tools/*.json → Skill/MCP/Tool 目录；
	// FORGE_CAPABILITIES 覆盖，默认 ~/.forge/capabilities；空 = 不启用文件数据面）。
	CapabilityDir string
	// SkillMaterializeDir skill 物化目标根目录（§5.4/§16.5；FORGE_SKILLS_MATERIALIZE_DIR 覆盖，
	// 默认空 = 关闭）。非空时 serve 启动装载能力后把 opencode SKILL.md 源 skill 物化到该目录
	// （<dir>/<skillID>/SKILL.md，opencode v1 自动扫描约定）。目标多为真实 opencode 用户目录，
	// 须显式开启（默认不写用户机器配置）。
	SkillMaterializeDir string
	// IdleTimeout 空闲回收阈值：StartAgent 暖进程闲置超过该时长自动停进程（P2；
	// FORGE_IDLE_TIMEOUT 覆盖，默认 5m；0 = 关闭回收）。
	IdleTimeout time.Duration
	// ReapInterval 空闲回收扫描间隔（P2；FORGE_REAP_INTERVAL 覆盖，默认 5m）。
	ReapInterval time.Duration
	// ProcRegistryPath runtime 子进程注册表路径（M3 决策 v4③：daemon 崩溃/SIGKILL 后，
	// 下次启动据此回收孤儿进程；FORGE_PROC_REGISTRY 覆盖，默认 <dir(DBPath)>/processes.json；
	// 取值 off/disable/none = 关闭登记与回收）。
	ProcRegistryPath string
	// ReapGrace 孤儿回收的 SIGTERM → SIGKILL 宽限期（FORGE_REAP_GRACE 覆盖，默认 2s）。
	ReapGrace time.Duration
	// ConfigDir 本次加载使用的配置目录（`forge config` 展示用；空 = 未解析）。
	ConfigDir string
}

// 默认超时（与 manager.DefaultPermissionTimeout/DefaultElicitationTimeout 对齐）。
const (
	DefaultPermissionTimeout  = 30 * time.Minute
	DefaultElicitationTimeout = 30 * time.Minute
	DefaultIdleTimeout        = 5 * time.Minute
	DefaultReapInterval       = 5 * time.Minute
	DefaultReapGrace          = 2 * time.Second
	DefaultHandoffMaxChars    = 6000
)

// Default 返回默认配置（不含依赖家目录的派生路径，派生值在解析阶段补齐）。
func Default() Config {
	return Config{
		Addr:               "127.0.0.1:10001",
		PermissionTimeout:  DefaultPermissionTimeout,
		ElicitationTimeout: DefaultElicitationTimeout,
		IdleTimeout:        DefaultIdleTimeout,
		ReapInterval:       DefaultReapInterval,
		ReapGrace:          DefaultReapGrace,
		HandoffMaxChars:    DefaultHandoffMaxChars,
	}
}

// knownKeys 支持的配置键（规范名 → 环境变量名）。config.json / env 文件可用规范名或 FORGE_* 名。
var knownKeys = map[string]string{
	"addr":                   "FORGE_ADDR",
	"transcripts":            "FORGE_TRANSCRIPTS",
	"db":                     "FORGE_DB",
	"store":                  "FORGE_STORE",
	"agents":                 "FORGE_AGENTS",
	"capabilities":           "FORGE_CAPABILITIES",
	"skills_materialize_dir": "FORGE_SKILLS_MATERIALIZE_DIR",
	"idle_timeout":           "FORGE_IDLE_TIMEOUT",
	"reap_interval":          "FORGE_REAP_INTERVAL",
	"turn_timeout":           "FORGE_TURN_TIMEOUT",
	"handoff_max_chars":      "FORGE_HANDOFF_MAX_CHARS",
	"permission_timeout":     "FORGE_PERMISSION_TIMEOUT",
	"elicitation_timeout":    "FORGE_ELICITATION_TIMEOUT",
	"proc_registry":          "FORGE_PROC_REGISTRY",
	"reap_grace":             "FORGE_REAP_GRACE",
}

// knownKeyList 稳定顺序的键名列表（错误信息用）。
func knownKeyList() string {
	keys := make([]string, 0, len(knownKeys))
	for k := range knownKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// normalizeKey 归一化键名：去空白、转小写、去可选 FORGE_ 前缀、- 转 _，并接受常见别名。
func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.TrimPrefix(k, "forge_")
	k = strings.ReplaceAll(k, "-", "_")
	switch k {
	case "transcript", "transcripts_dir", "transcript_dir":
		return "transcripts"
	case "db_path", "database":
		return "db"
	case "agent", "agent_dir", "agents_dir":
		return "agents"
	case "capability", "capability_dir", "capabilities_dir":
		return "capabilities"
	}
	return k
}

// resolver 收集各来源的键值（后写覆盖），并记录来源。
type resolver struct {
	values map[string]string
	src    map[string]Source
}

func newResolver() *resolver {
	return &resolver{values: map[string]string{}, src: map[string]Source{}}
}

func (r *resolver) set(key, val string, src Source) {
	r.values[key] = val
	r.src[key] = src
}

func (r *resolver) get(key string) (string, bool) {
	v, ok := r.values[key]
	return v, ok
}

func (r *resolver) sources() Sources {
	out := make(Sources, len(r.src))
	for k, v := range r.src {
		out[k] = v
	}
	return out
}

// ConfigDirFromEnv 解析配置目录：FORGE_CONFIG_DIR 优先，否则 <默认数据目录>/config。
func ConfigDirFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("FORGE_CONFIG_DIR")); v != "" {
		return v
	}
	return filepath.Join(defaultForgeHome(), "config")
}

// defaultForgeHome 默认数据目录（~/.forge；家目录不可得时回落到相对路径）。
func defaultForgeHome() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".forge")
	}
	return ".forge"
}

// LoadResolved 解析默认值 + 配置目录（config.json/env）+ 进程环境变量。
// dir 为空时用 ConfigDirFromEnv()。返回最终配置与各键来源；配置文件非法时返回错误。
func LoadResolved(dir string) (Config, Sources, error) {
	if dir == "" {
		dir = ConfigDirFromEnv()
	}
	r := newResolver()
	// 2) config.json
	if err := loadConfigJSON(filepath.Join(dir, "config.json"), r); err != nil {
		return Default(), nil, err
	}
	// 3) env 文件（dotenv）
	if err := loadEnvFile(filepath.Join(dir, "env"), r); err != nil {
		return Default(), nil, err
	}
	// 4) 进程环境变量（仅对已设置且非空的 FORGE_* 生效，保持历史语义）
	for key, envName := range knownKeys {
		if v := os.Getenv(envName); v != "" {
			r.set(key, v, SourceEnv)
		}
	}
	// FORGE_PROC_REGISTRY 特例：显式设置为空/off 也要覆盖文件配置（关闭登记）。
	if v, ok := os.LookupEnv("FORGE_PROC_REGISTRY"); ok {
		r.set("proc_registry", v, SourceEnv)
	}

	c, err := materialize(r)
	if err != nil {
		return Default(), nil, err
	}
	c.ConfigDir = dir
	return c, r.sources(), nil
}

// Load 兼容旧签名：读取配置；文件非法时回落默认值（仅测试/兼容路径使用；
// daemon 启动请用 LoadResolved，以便把配置错误暴露给操作员）。
func Load() Config {
	c, _, err := LoadResolved("")
	if err != nil {
		return Default()
	}
	return c
}

// materialize 把 resolver 的键值解析进 Config，并补齐依赖家目录/DB 路径的派生默认值。
func materialize(r *resolver) (Config, error) {
	c := Default()
	bad := func(key, why string) (Config, error) {
		return Default(), fmt.Errorf("配置项 %s: %s", key, why)
	}
	if v, ok := r.get("addr"); ok {
		c.Addr = v
	}
	if v, ok := r.get("store"); ok {
		switch strings.ToLower(v) {
		case "sqlite", "memory":
			c.StoreMode = strings.ToLower(v)
		default:
			// 历史语义：非法/空值视为 auto（不报错，避免旧环境变量把 daemon 卡死）。
			c.StoreMode = ""
		}
	}
	if v, ok := r.get("handoff_max_chars"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("handoff_max_chars", fmt.Sprintf("需为非负整数，得到 %q", v))
		}
		c.HandoffMaxChars = n
	}
	for _, item := range []struct {
		key string
		dst *time.Duration
	}{
		{"idle_timeout", &c.IdleTimeout},
		{"reap_interval", &c.ReapInterval},
		{"turn_timeout", &c.TurnTimeout},
		{"permission_timeout", &c.PermissionTimeout},
		{"elicitation_timeout", &c.ElicitationTimeout},
		{"reap_grace", &c.ReapGrace},
	} {
		v, ok := r.get(item.key)
		if !ok {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return bad(item.key, fmt.Sprintf("需为 Go duration 文本（如 30s/5m/1h），得到 %q", v))
		}
		if d < 0 {
			return bad(item.key, "不可为负")
		}
		*item.dst = d
	}
	if v, ok := r.get("skills_materialize_dir"); ok {
		c.SkillMaterializeDir = v
	}

	// 依赖家目录的派生默认值（未被任何来源显式设置时）。
	home := defaultForgeHome()
	if v, ok := r.get("transcripts"); ok {
		c.TranscriptDir = v
	} else {
		c.TranscriptDir = filepath.Join(home, "transcripts")
	}
	if v, ok := r.get("db"); ok {
		c.DBPath = v
	} else {
		c.DBPath = filepath.Join(home, "forge.db")
	}
	if v, ok := r.get("agents"); ok {
		c.AgentDir = v
	} else {
		c.AgentDir = filepath.Join(home, "agents")
	}
	if v, ok := r.get("capabilities"); ok {
		c.CapabilityDir = v
	} else {
		c.CapabilityDir = filepath.Join(home, "capabilities")
	}
	// proc_registry：显式设置优先；off/disable/none = 关闭；未设置时派生自 DB 目录。
	if v, ok := r.get("proc_registry"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "off", "disable", "disabled", "none":
			c.ProcRegistryPath = ""
		default:
			c.ProcRegistryPath = v
		}
	} else if c.DBPath != "" {
		c.ProcRegistryPath = filepath.Join(filepath.Dir(c.DBPath), "processes.json")
	}
	return c, nil
}

// loadConfigJSON 读取 <dir>/config.json（缺失 = 跳过；JSON 非法/键名未知/取值非法 = 报错）。
func loadConfigJSON(path string, r *resolver) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("%s 不是合法 JSON 对象: %w", path, err)
	}
	// 稳定顺序应用（同一文件内无覆盖语义，排序只为错误信息可复现）。
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		key := normalizeKey(k)
		if _, ok := knownKeys[key]; !ok {
			return fmt.Errorf("%s: 未知配置键 %q（支持: %s）", path, k, knownKeyList())
		}
		val, err := jsonScalar(obj[k])
		if err != nil {
			return fmt.Errorf("%s: %s: %w", path, k, err)
		}
		r.set(key, val, SourceFile)
	}
	return nil
}

// jsonScalar 把 JSON 标量转为字符串（string 原样；number/bool 字面量；null → 空串）。
func jsonScalar(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("只支持字符串/数字/布尔/null 标量，得到 %T", v)
	}
}

// loadEnvFile 读取 <dir>/env（dotenv 风格：# 注释、空行、KEY=VALUE、可选成对引号；缺失 = 跳过）。
func loadEnvFile(path string, r *resolver) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			return fmt.Errorf("%s:%d: 需为 KEY=VALUE 形式，得到 %q", path, i+1, line)
		}
		rawKey := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// 去掉成对引号（不处理转义：值里需要引号请写 config.json）。
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		key := normalizeKey(rawKey)
		if _, ok := knownKeys[key]; !ok {
			return fmt.Errorf("%s:%d: 未知配置键 %q（支持: %s）", path, i+1, rawKey, knownKeyList())
		}
		r.set(key, val, SourceEnvFile)
	}
	return nil
}
