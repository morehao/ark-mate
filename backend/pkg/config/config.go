// Package config 提供进程级配置加载。
//
// 优先级（后者覆盖前者）：
//  1. 内置默认值（Default()）
//  2. 配置目录 `config.json`（`.forge/config` 目录化；A4）
//  3. 配置目录 `env`（dotenv 风格 KEY=VALUE，便于与 `docker --env-file`/systemd 共用一个文件）
//  4. 进程环境变量 `FORGE_*`（历史行为，始终最高）
//  5. CLI flag（由 cmd 层在 Load 之后覆盖）
//
// 数据目录取自 `FORGE_HOME`，默认 `~/.forge`；配置目录取自 `FORGE_CONFIG_DIR`，默认
// `<数据目录>/config`（即 `~/.forge/config`）。这两个是**启动引导键**：只认进程环境变量，
// 必须在读配置文件之前就有确定值（理由见 Config.HomeDir）。
// 目录不存在 = 完全维持历史行为（向后兼容）；文件存在但键名/取值非法 = 启动即报错（不静默忽略）。
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morehao/ark-forge/pkg/runtime"
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
	// StoreMode 持久层开关："sqlite" | "memory" | ""。
	// 空串表示 auto（DB 文件已存在 → sqlite，否则 memory）：未设置与显式 FORGE_STORE=auto
	// 都落成它。非法取值在 materialize 里报错，不再静默回落。FORGE_STORE 覆盖。
	StoreMode string
	// HandoffMaxChars bounded handoff memo 字符预算（§5.6/§18 D-3；FORGE_HANDOFF_MAX_CHARS
	// 覆盖，默认 6000；0 = 关闭 memo 兜底）。跨 runtime / 无原生 resume 时把历史压成摘要注入
	// 首条 prompt。
	HandoffMaxChars int
	// GenerationLimit 同一配置（runtime + 原生权限 + provider）下允许并存的**能力代际**上限
	// （§13.4/风险表第 12 行；FORGE_GENERATION_LIMIT 覆盖，默认 4；**0 = 不限制**）。
	// 代际按能力指纹内容寻址，指纹不同 = 能力集不同 = 各自一个进程；上限是"猜粗了只是多花内存"
	// 这一侧的护栏：超限**带码拒绝新建**，绝不静默复用错的能力集（那才是安全 bug）。
	GenerationLimit int
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
	// AutomationDir 自动化定义目录（M4：<id>.json → Automation 注册表；FORGE_AUTOMATIONS 覆盖，
	// 默认 ~/.forge/automations；空 = 不启用文件数据面，写面显式报 automation_dir_unset）。
	AutomationDir string
	// AutomationTick 调度循环的 tick 间隔（FORGE_AUTOMATION_TICK 覆盖，默认 30s；**0 = 关闭调度**）。
	// 0 关闭而不是"最快跑"：调度会真的建会话、跑 agent，停机维护时需要一个明确的关闭开关，
	// 而不是靠把间隔调到很大来近似。
	AutomationTick time.Duration
	// WorkspaceDir 代建工作空间的根目录（FORGE_WORKSPACE_DIR 覆盖，默认 ~/.forge/workspaces）。
	// 建会话未指定 cwd 时在 <dir>/<会话id>/ 下建临时工作空间。**空 = 关闭代建**：
	// 此时未指定 cwd 直接报 workspace_required（绝不回落 serve 进程目录，见 manager.ResolveWorkspace）。
	WorkspaceDir string
	// WorkspaceRoots 允许作为工作空间的根白名单（FORGE_WORKSPACE_ROOTS，逗号分隔绝对路径；
	// 默认空 = 不限制，保持本机单操作员零配置）。非空时**显式 cwd 与目录浏览都收在根内**：
	// 浏览不能越过根向上。用途：容器化/对外提供服务时，目录浏览不再等价于"暴露整个 rootfs 结构"。
	//
	// 这里只存**显式配置**的值：平台代建根（WorkspaceDir）由 manager 在运行期隐式并进生效名单，
	// 因此"代建根不在白名单里"不再是配置错误（见 manager.SetWorkspacePolicy）。
	WorkspaceRoots []string
	// ProjectionDirs 投影根的部署级覆盖（`FORGE_PROJECTION_DIRS=runtime:kind=dir[,...]`，
	// config.json 可写 JSON 数组）。键是 `<runtime>:<kind>`，值是**绝对目录**。
	//
	// 它在投影解析链里优先级最高（见 pkg/runtime.Resolve）：覆盖只替换 Root，投影**形态**
	// （Layout/Format）仍由 adapter 声明——所以这里解决的是「默认根不适用本机」，
	// 不是「换个摆放方式」。默认空 = 全用 adapter 声明的原生根。
	//
	// 取代了旧的 skills_materialize_dir / skills_materialize_dirs：sparse 单 root 与
	// 「for 不 for runtime」这两套说法都被投影声明吸收，配置面只留一个键。
	ProjectionDirs map[string]string
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
	// HomeDir 数据目录（M3-3；FORGE_HOME 覆盖，默认 ~/.forge，家目录不可得时回落相对 `.forge`）。
	//
	// 为什么要有独立的键：CLI / 浏览器 / 桌面三个入口**共用一份数据目录**（设计 §481）——
	// 按入口分裂就会出现"桌面打开后会话不见了"。这里存的是**已解析**的值：transcripts / db /
	// agents / … 的默认位置都由它派生，各 runtime 子进程的配置隔离也以它为根（pkg/runtime.IsolationEnv）。
	//
	// 为什么它只吃**进程环境变量**、不进 config.json / env 文件的键值链（与 FORGE_CONFIG_DIR 同级）：
	// 数据目录决定配置目录的默认位置（`<数据目录>/config`），而配置文件又在配置目录里——让文件里的值
	// 参与决定"文件从哪读"是循环。它必须与 ConfigDirFromEnv() 用同一个事实源，否则会出现
	// "配置从 A 读、数据落 B"的分裂（那正是本键要消灭的形态）。
	//
	// 显式给的值必须是绝对路径（相对路径随进程 CWD 漂移，见 materialize）；未设置时的兜底不受此约束。
	HomeDir string
	// ConfigDir 本次加载使用的配置目录（`forge config` 展示用；空 = 未解析）。
	ConfigDir string
	// RemoteRuntimes 远程 runtime 声明（配置目录 runtimes.json；A5）。
	RemoteRuntimes []RemoteRuntime
	// WebDir 前端资产目录（M2-1；FORGE_WEB_DIR 覆盖，默认空）。
	// 空 = 用构建期内置资产；**非空即校验**（绝对路径 / 存在 / 目录 / 含 index.html），
	// 不合法就拒绝启动——显式配置配错时静默回落到内嵌旧资产，是"用户看到旧界面还以为没生效"的根源。
	WebDir string
	// AllowedOrigins 额外允许的请求来源（M3-5；FORGE_ALLOWED_ORIGINS，逗号分隔，config.json 可写 JSON 数组；
	// 默认空 = 只用内置 Origin 矩阵）。
	//
	// **这是给跨源开发/自建反代用的显式登记口，不是默认放开**：内置矩阵只放行「无 Origin」的壳内反代
	// /同源导航、`forge-app://app`（桌面直连）与核心**自身监听端口**上的 http 同源（见
	// internal/middleware/origin.go，设计 §7.2 的 Origin 矩阵行）。`make dev`（vite :5173 → 代理到核心）
	// 与自建反代（页面 origin ≠ 核心监听 origin）都落在矩阵之外，必须在这里指名登记。
	//
	// 取值必须是**纯 origin**（`scheme://host[:port]`，无路径/查询/片段，可带一个尾斜杠）；
	// 统一小写后比较，顺序无关（解析后排序去重）。非法取值启动即报错。
	//
	// 只在**回环监听**时生效：`--addr 0.0.0.0` 等非回环监听下 Host/Origin 白名单整体不启用
	// （设计 §9.4 明确 Host/Origin 白名单属于"对外产品化缺的四件事"之一，本期范围外），
	// 此时本键也不参与判定——启动日志会打一条 WARN 说清楚。
	AllowedOrigins []string
	// Token 独立态单操作员访问令牌（A6；FORGE_TOKEN 覆盖，默认空 = 不鉴权，仅回环可访问）。
	// 非空时除 /health 外的所有 REST 路由都要求 `Authorization: Bearer <token>`，
	// 事件流则由连接后的首帧 attach 携带同一令牌（M1-1 起令牌不进 URL）。
	// 桌面态不使用它：那两个面互斥的凭据由壳经 --auth-stdin 下发。
	Token string
	// NativePermission 原生权限映射开关（B5；FORGE_NATIVE_PERMISSION 覆盖，默认 auto）：
	//   auto = 把 agent 的 permission_policy 投影到底座原生权限开关；
	//   off  = 不注入（底座走自身默认，平台拦截面照常生效）。
	NativePermission string
	// McpAutoProbe MCP 自动探测模式（B4；FORGE_MCP_AUTO_PROBE 覆盖，默认 off）：
	//   off      = 不自动探测（历史行为：只由 POST /mcps/:id/probe 显式触发）；
	//   startup  = serve 启动后后台逐个探测全部已注册 MCP server（写 Tool 目录 + 探测状态）。
	// 默认 off 是有意的：stdio server 探测会**拉起用户配置的子进程**，不宜默认执行。
	McpAutoProbe string
}

// 原生权限映射开关取值。
const (
	NativePermissionAuto = "auto"
	NativePermissionOff  = "off"
)

// ValidNativePermissionModes 合法取值（错误提示用）。
var ValidNativePermissionModes = []string{NativePermissionAuto, NativePermissionOff}

// MCP 自动探测模式取值。
const (
	McpAutoProbeOff     = "off"
	McpAutoProbeStartup = "startup"
)

// ValidMcpAutoProbeModes 合法取值（错误提示用）。
var ValidMcpAutoProbeModes = []string{McpAutoProbeOff, McpAutoProbeStartup}

// 持久层模式取值。StoreModeAuto 是**显式**的 auto：Config.StoreMode 里它落成空串
// （空串同时表示"未设置"），真正的 auto 解析在 serve 装机时做——DB 文件已存在则 sqlite，
// 否则 memory（见 internal/app/app.go）。
const (
	StoreModeAuto   = "auto"
	StoreModeSQLite = "sqlite"
	StoreModeMemory = "memory"
)

// ValidStoreModes 合法取值（错误提示用）。
var ValidStoreModes = []string{StoreModeAuto, StoreModeSQLite, StoreModeMemory}

// RemoteRuntime 是一条「远程 ACP 端点」runtime 声明（配置目录 runtimes.json 的数组元素）。
type RemoteRuntime struct {
	// ID 注册表键（小写字母/数字/_/-；不得与内置 runtime 冲突，冲突由 serve 启动时拒绝）。
	ID string `json:"id"`
	// Name 展示名（空 = 用 ID）。
	Name string `json:"name"`
	// Transport 传输形态："http"（Streamable HTTP）| "ws"（WebSocket）。
	Transport string `json:"transport"`
	// Endpoint 端点 URL（http(s):// 对应 http，ws(s):// 对应 ws）。
	Endpoint string `json:"endpoint"`
	// Headers 附加请求头（鉴权等）。
	Headers map[string]string `json:"headers"`
}

// 默认超时（与 manager.DefaultPermissionTimeout/DefaultElicitationTimeout 对齐）。
const (
	DefaultPermissionTimeout  = 30 * time.Minute
	DefaultElicitationTimeout = 30 * time.Minute
	DefaultIdleTimeout        = 5 * time.Minute
	DefaultReapInterval       = 5 * time.Minute
	DefaultReapGrace          = 2 * time.Second
	DefaultHandoffMaxChars    = 6000
	// DefaultGenerationLimit 同一配置下的能力代际上限（风险表第 12 行：5 套能力 ≈ 0.5–1.5GB）。
	DefaultGenerationLimit = 4
	// DefaultAutomationTick 调度 tick 间隔（设计：100 条 automation 一次时区换算 ≪ 1ms，无 IO）。
	DefaultAutomationTick = 30 * time.Second
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
		GenerationLimit:    DefaultGenerationLimit,
		AutomationTick:     DefaultAutomationTick,
		McpAutoProbe:       McpAutoProbeOff,
		NativePermission:   NativePermissionAuto,
	}
}

// knownKeys 支持的配置键（规范名 → 环境变量名）。config.json / env 文件可用规范名或 FORGE_* 名。
var knownKeys = map[string]string{
	"addr":                "FORGE_ADDR",
	"transcripts":         "FORGE_TRANSCRIPTS",
	"db":                  "FORGE_DB",
	"store":               "FORGE_STORE",
	"agents":              "FORGE_AGENTS",
	"capabilities":        "FORGE_CAPABILITIES",
	"automations":         "FORGE_AUTOMATIONS",
	"automation_tick":     "FORGE_AUTOMATION_TICK",
	"workspace_dir":       "FORGE_WORKSPACE_DIR",
	"workspace_roots":     "FORGE_WORKSPACE_ROOTS",
	"projection_dirs":     "FORGE_PROJECTION_DIRS",
	"idle_timeout":        "FORGE_IDLE_TIMEOUT",
	"reap_interval":       "FORGE_REAP_INTERVAL",
	"turn_timeout":        "FORGE_TURN_TIMEOUT",
	"handoff_max_chars":   "FORGE_HANDOFF_MAX_CHARS",
	"generation_limit":    "FORGE_GENERATION_LIMIT",
	"permission_timeout":  "FORGE_PERMISSION_TIMEOUT",
	"elicitation_timeout": "FORGE_ELICITATION_TIMEOUT",
	"proc_registry":       "FORGE_PROC_REGISTRY",
	"reap_grace":          "FORGE_REAP_GRACE",
	"token":               "FORGE_TOKEN",
	"allowed_origins":     "FORGE_ALLOWED_ORIGINS",
	"web_dir":             "FORGE_WEB_DIR",
	"mcp_auto_probe":      "FORGE_MCP_AUTO_PROBE",
	"native_permission":   "FORGE_NATIVE_PERMISSION",
}

// knownListKeys 允许在 config.json 里写成 JSON 数组的多值键。
//
// 为什么需要它：单值键写成数组是配置错误（不静默取第一项），而多值键的数组形式不是"语法糖"——
// 逗号是这些键的分隔符，路径里真含逗号时逗号分隔写法**根本无法表达**，面板写回也依赖数组形式
// 才能与读回自洽（见 SettingsStore.SaveWorkspaceRoots）。
var knownListKeys = map[string]bool{
	"workspace_roots": true,
	"projection_dirs": true,
	"allowed_origins": true,
}

// renamedKeys 已改名/已移除的配置键 → 可操作的改法。
//
// 处置是**只识别、不兼容**（ADR-11 不留别名）：仍然报错——「非法键名启动即报错」这条规则不软化
// ——但把键认出来，给出新键名与等价写法。这样存量 `~/.forge/config/config.json` 里的旧键
// 不会变成一个「看不懂的未知键」，用户拿到的是出路而不是死胡同；配置面也不会多出一个语义重复的别名。
//
// 为什么必须单独一层：直接删键的话，旧键会掉进「未知配置键」的通用报错里，用户只能靠自己猜
// 新名字；而静默忽略更糟——物化会悄悄不发生，表现为「技能怎么不生效了」。
var renamedKeys = map[string]string{
	"skills_materialize_dir": "已改名为 FORGE_PROJECTION_DIRS。投影形态（放哪、什么格式）现在由 runtime 声明，" +
		"配置只能**覆盖根**，且必须指名是哪个 runtime 的哪类投影：`projection_dirs=opencode:skill=/x`" +
		"（原 `skills_materialize_dir=/x` 那种「不分 runtime」的兼容路径已不存在——因为两个底座的落点与格式本就不同）",
	"skills_materialize_dirs": "已改名为 FORGE_PROJECTION_DIRS，等价写法 `projection_dirs=opencode:skill=/x`" +
		"（原 `skills_materialize_dirs=opencode=/x` ≡ `projection_dirs=opencode:skill=/x`；键多了 `:kind` 一段，" +
		"目前 kind 只有 skill）",
}

// checkRenamedKey 在「未知键报错」之前拦一层，把已改名/已移除的键翻译成可操作的文案。
// 返回非空字符串 = 该键已被识别为旧键，调用方应直接以此为错误信息。
func checkRenamedKey(key string) string {
	if hint, ok := renamedKeys[key]; ok {
		return hint
	}
	return ""
}

// renamedEnvNames 旧键对应的进程环境变量名（进程环境只按 knownKeys 读取，
// 所以旧变量名不会被读到——必须显式检查，否则"设了旧变量但没生效"是静默的）。
var renamedEnvNames = map[string]string{
	"FORGE_SKILLS_MATERIALIZE_DIR":  "skills_materialize_dir",
	"FORGE_SKILLS_MATERIALIZE_DIRS": "skills_materialize_dirs",
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
// lists 单独存 JSON 数组形式的多值键（见 knownListKeys）：字符串形式的逗号分隔写法与它
// 语义相同但表达力更弱，两者在 materialize 里汇到同一条解析路径。
type resolver struct {
	values map[string]string
	lists  map[string][]string
	src    map[string]Source
}

func newResolver() *resolver {
	return &resolver{values: map[string]string{}, lists: map[string][]string{}, src: map[string]Source{}}
}

// set / setList 都做**形式互斥**：同一键后写的来源覆盖先写的，且两种形式（字符串 / 数组）
// 不会同时留下——否则"config.json 写数组、进程 env 写逗号串"会变成哪个先被 materialize 读到
// 就用哪个，优先级形同虚设。
func (r *resolver) set(key, val string, src Source) {
	delete(r.lists, key)
	r.values[key] = val
	r.src[key] = src
}

func (r *resolver) setList(key string, val []string, src Source) {
	delete(r.values, key)
	r.lists[key] = val
	r.src[key] = src
}

func (r *resolver) get(key string) (string, bool) {
	v, ok := r.values[key]
	return v, ok
}

func (r *resolver) list(key string) ([]string, bool) {
	v, ok := r.lists[key]
	return v, ok
}

func (r *resolver) sources() Sources {
	out := make(Sources, len(r.src))
	for k, v := range r.src {
		out[k] = v
	}
	return out
}

// ConfigDirFromEnv 解析配置目录：FORGE_CONFIG_DIR 优先，否则 <数据目录>/config。
//
// "数据目录"由 HomeDirFromEnv() 给出，所以 FORGE_HOME 一变，配置目录的默认位置跟着变——
// 这条关系必须保持：从配置目录反推数据目录（旧 metaInfo 的 filepath.Dir 做法）会让
// 「FORGE_CONFIG_DIR 与 FORGE_HOME 分家」的进程读到错误的数据目录。
func ConfigDirFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("FORGE_CONFIG_DIR")); v != "" {
		return v
	}
	return filepath.Join(HomeDirFromEnv(), "config")
}

// HomeDirFromEnv 解析数据目录：FORGE_HOME 优先，否则 ~/.forge。
//
// trim 后为空 = 未设置（与 FORGE_CONFIG_DIR 等键的语义一致：空串不是"数据目录是空路径"，
// 否则派生路径会拼成 "/transcripts" 这种根目录下的怪地方）。
// 家目录不可得时保留既有兜底（相对 `.forge`）：宁可落到 CWD 下，也不要拼出根目录路径。
//
// 只认进程环境变量的理由见 Config.HomeDir。
func HomeDirFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("FORGE_HOME")); v != "" {
		return v
	}
	return defaultForgeHome()
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
	// 已改名的旧环境变量：进程环境只按 knownKeys 读，故旧名必须显式检查——
	// 否则「设了 FORGE_SKILLS_MATERIALIZE_DIR 但物化没发生」是完全静默的。
	for envName, key := range renamedEnvNames {
		if _, set := os.LookupEnv(envName); set {
			return Default(), nil, fmt.Errorf("环境变量 %s %s", envName, renamedKeys[key])
		}
	}
	// FORGE_PROC_REGISTRY 特例：显式设置为空/off 也要覆盖文件配置（关闭登记）。
	if v, ok := os.LookupEnv("FORGE_PROC_REGISTRY"); ok {
		r.set("proc_registry", v, SourceEnv)
	}
	// FORGE_WORKSPACE_DIR 同理：显式设置为空/off 要能覆盖文件配置（关闭代建）。
	if v, ok := os.LookupEnv("FORGE_WORKSPACE_DIR"); ok {
		r.set("workspace_dir", v, SourceEnv)
	}

	c, err := materialize(r)
	if err != nil {
		return Default(), nil, err
	}
	remotes, err := loadRuntimesJSON(filepath.Join(dir, "runtimes.json"))
	if err != nil {
		return Default(), nil, err
	}
	c.RemoteRuntimes = remotes
	c.ConfigDir = dir
	return c, r.sources(), nil
}

// loadRuntimesJSON 读取 <dir>/runtimes.json（远程 runtime 数组；缺失 = 跳过）。
func loadRuntimesJSON(path string) ([]RemoteRuntime, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	var list []RemoteRuntime
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 JSON 数组: %w", path, err)
	}
	seen := map[string]bool{}
	for i, rt := range list {
		id := strings.TrimSpace(rt.ID)
		if id == "" {
			return nil, fmt.Errorf("%s[%d]: id 必填", path, i)
		}
		if !runtimeIDRe.MatchString(id) {
			return nil, fmt.Errorf("%s[%d]: id %q 非法（小写字母/数字/_/-，首字符为字母或数字）", path, i, rt.ID)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s[%d]: id %q 重复", path, i, id)
		}
		seen[id] = true
		tr := strings.ToLower(strings.TrimSpace(rt.Transport))
		switch tr {
		case "http", "streamable_http", "streamable-http", "https":
			tr = "http"
		case "ws", "wss", "websocket":
			tr = "ws"
		case "":
			return nil, fmt.Errorf("%s[%d]: transport 必填（http|ws）", path, i)
		default:
			return nil, fmt.Errorf("%s[%d]: transport %q 不支持（http|ws）", path, i, rt.Transport)
		}
		endpoint := strings.TrimSpace(rt.Endpoint)
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%s[%d]: endpoint %q 非法", path, i, rt.Endpoint)
		}
		switch tr {
		case "http":
			if u.Scheme != "http" && u.Scheme != "https" {
				return nil, fmt.Errorf("%s[%d]: http 传输的 endpoint 需为 http(s)://，得到 %q", path, i, rt.Endpoint)
			}
		case "ws":
			if u.Scheme != "ws" && u.Scheme != "wss" {
				return nil, fmt.Errorf("%s[%d]: ws 传输的 endpoint 需为 ws(s)://，得到 %q", path, i, rt.Endpoint)
			}
		}
		list[i] = RemoteRuntime{
			ID:        id,
			Name:      strings.TrimSpace(rt.Name),
			Transport: tr,
			Endpoint:  u.String(),
			Headers:   rt.Headers,
		}
	}
	return list, nil
}

// runtimeIDRe runtime id 形态（与内置 id 同构）。
var runtimeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

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
		switch strings.ToLower(strings.TrimSpace(v)) {
		case StoreModeAuto:
			// 显式 auto：等价于未设（按 DB 文件是否已存在决定），但它是合法取值——
			// 文档一直写 `FORGE_STORE=sqlite|memory|auto`，此前 auto 只靠 default 分支
			// 巧合生效，不认它反而会打脸自己的文档。
			c.StoreMode = ""
		case "sqlite", "memory":
			c.StoreMode = strings.ToLower(strings.TrimSpace(v))
		default:
			// 非法值**报错**，不再静默回落 auto。原注释声称"非法/空值视为 auto（避免旧环境
			// 变量把 daemon 卡死）"，但：（a）查历史，FORGE_STORE 从引入那版起的取值集就是
			// sqlite|memory|auto，**从未有过今天已非法的取值**，严格化不会淘汰任何曾经可用的
			// 配置；（b）store 定的是**持久化语义**，回落 auto 会按"DB 文件在不在"解析成
			// sqlite 或 memory——一个错字可能把用户悄悄丢进**相反**的模式（如 sqlit 在无 DB
			// 时进 memory，表现为"数据丢了"；memry 在有 DB 时进 sqlite，以为不落盘却落盘）；
			// （c）回落后的 `forge config` 输出与合法的 auto **逐字相同**，用户无从察觉值被丢弃。
			// 空值/未设不报错（无偏好），故这里只拦非法取值；同级枚举键
			// （native_permission / mcp_auto_probe）本来就是这么做的。
			return bad("store", fmt.Sprintf("需为 %s 之一，得到 %q",
				strings.Join(ValidStoreModes, "|"), v))
		}
	}
	if v, ok := r.get("handoff_max_chars"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("handoff_max_chars", fmt.Sprintf("需为非负整数，得到 %q", v))
		}
		c.HandoffMaxChars = n
	}
	if v, ok := r.get("generation_limit"); ok {
		// 负数无意义（"允许 -1 个代际"），报错而不是夹到 0：0 有明确含义（不限制），
		// 把非法值悄悄变成"不限制"正好是护栏失效的方向。
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return bad("generation_limit", fmt.Sprintf("需为非负整数（0 = 不限制），得到 %q", v))
		}
		c.GenerationLimit = n
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
		{"automation_tick", &c.AutomationTick},
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
	if v, ok := r.get("projection_dirs"); ok {
		dirs, err := parseProjectionDirs(v)
		if err != nil {
			return bad("projection_dirs", err.Error())
		}
		c.ProjectionDirs = dirs
	}
	if list, ok := r.list("projection_dirs"); ok {
		dirs, err := normalizeProjectionDirs(list)
		if err != nil {
			return bad("projection_dirs", err.Error())
		}
		c.ProjectionDirs = dirs
	}
	if v, ok := r.get("web_dir"); ok {
		c.WebDir = strings.TrimSpace(v)
	}
	if v, ok := r.get("token"); ok {
		c.Token = strings.TrimSpace(v)
	}
	// allowed_origins：额外允许的来源白名单（M3-5；逗号分隔或 JSON 数组，两种形式汇到同一条解析路径）。
	if list, ok := r.list("allowed_origins"); ok {
		origins, err := normalizeAllowedOrigins(list)
		if err != nil {
			return bad("allowed_origins", err.Error())
		}
		c.AllowedOrigins = origins
	} else if v, ok := r.get("allowed_origins"); ok {
		origins, err := parseAllowedOrigins(v)
		if err != nil {
			return bad("allowed_origins", err.Error())
		}
		c.AllowedOrigins = origins
	}
	if v, ok := r.get("native_permission"); ok {
		mode := strings.ToLower(strings.TrimSpace(v))
		switch mode {
		case NativePermissionAuto, "":
			c.NativePermission = NativePermissionAuto
		case NativePermissionOff:
			c.NativePermission = NativePermissionOff
		default:
			return bad("native_permission", fmt.Sprintf("需为 %s 之一，得到 %q",
				strings.Join(ValidNativePermissionModes, "|"), v))
		}
	}
	if v, ok := r.get("mcp_auto_probe"); ok {
		mode := strings.ToLower(strings.TrimSpace(v))
		switch mode {
		case McpAutoProbeStartup:
			c.McpAutoProbe = McpAutoProbeStartup
		case McpAutoProbeOff, "":
			c.McpAutoProbe = McpAutoProbeOff
		default:
			return bad("mcp_auto_probe", fmt.Sprintf("需为 %s 之一，得到 %q",
				strings.Join(ValidMcpAutoProbeModes, "|"), v))
		}
	}

	// 数据目录（M3-3）：FORGE_HOME 优先，默认 ~/.forge。
	//
	// 显式给的 FORGE_HOME 必须是绝对路径：相对路径按**进程 CWD** 解析，而 CLI（用户 shell 的 CWD）与
	// 桌面（壳的 CWD，通常是 `/`）的 CWD 不同——同一个环境变量会静默打开两份数据目录，正是设计 §481
	// 要消灭的"桌面打开后会话不见了"。未设置时的兜底（家目录不可得 → 相对 `.forge`）不是用户给的路径，
	// 不适用这条约束（见 HomeDirFromEnv）。
	if v, ok := os.LookupEnv("FORGE_HOME"); ok {
		if t := strings.TrimSpace(v); t != "" && !filepath.IsAbs(t) {
			return bad("FORGE_HOME", fmt.Sprintf(
				"需为绝对路径（相对路径随进程 CWD 漂移，三端可能打开不同数据目录；家目录请写全路径），得到 %q", v))
		}
	}
	home := HomeDirFromEnv()
	c.HomeDir = home

	// 依赖数据目录的派生默认值（未被任何来源显式设置时）。
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
	if v, ok := r.get("automations"); ok {
		c.AutomationDir = v
	} else {
		c.AutomationDir = filepath.Join(home, "automations")
	}
	// workspace_dir：显式设置优先（off/disable/none/空 = 关闭代建）；未设置时派生自数据目录。
	if v, ok := r.get("workspace_dir"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "off", "disable", "disabled", "none", "":
			c.WorkspaceDir = ""
		default:
			c.WorkspaceDir = v
		}
	} else {
		c.WorkspaceDir = filepath.Join(home, "workspaces")
	}
	// workspace_roots：白名单（逗号分隔绝对路径或 JSON 数组，空 = 不限制）。
	// 只校验**形式**（非空 + 绝对）；代建根的隐式并入是**运行期**行为（manager），配置层不参与。
	if list, ok := r.list("workspace_roots"); ok {
		roots, err := normalizeWorkspaceRoots(list)
		if err != nil {
			return bad("workspace_roots", err.Error())
		}
		c.WorkspaceRoots = roots
	} else if v, ok := r.get("workspace_roots"); ok {
		roots, err := parseWorkspaceRoots(v)
		if err != nil {
			return bad("workspace_roots", err.Error())
		}
		c.WorkspaceRoots = roots
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
		if hint := checkRenamedKey(key); hint != "" {
			return fmt.Errorf("%s: 配置键 %q %s", path, k, hint)
		}
		if _, ok := knownKeys[key]; !ok {
			return fmt.Errorf("%s: 未知配置键 %q（支持: %s）", path, k, knownKeyList())
		}
		// 数组形式只对多值键开放：单值键写成数组是配置错误，静默取第一项会让运维以为生效了。
		if arr, isArr := obj[k].([]any); isArr {
			if !knownListKeys[key] {
				return fmt.Errorf("%s: %s: 该键不支持数组值（只支持字符串/数字/布尔/null 标量）", path, k)
			}
			items, err := jsonStringList(arr)
			if err != nil {
				return fmt.Errorf("%s: %s: %w", path, k, err)
			}
			r.setList(key, items, SourceFile)
			continue
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

// jsonStringList 把 JSON 数组转为字符串列表（只接受字符串元素：把数字/布尔字符串化当路径用
// 没有意义，与其静默产出怪值不如直接报错）。
func jsonStringList(arr []any) ([]string, error) {
	out := make([]string, 0, len(arr))
	for i, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("第 %d 项需为字符串，得到 %T", i, item)
		}
		out = append(out, s)
	}
	return out, nil
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
		if hint := checkRenamedKey(key); hint != "" {
			return fmt.Errorf("%s:%d: 配置键 %q %s", path, i+1, rawKey, hint)
		}
		if _, ok := knownKeys[key]; !ok {
			return fmt.Errorf("%s:%d: 未知配置键 %q（支持: %s）", path, i+1, rawKey, knownKeyList())
		}
		r.set(key, val, SourceEnvFile)
	}
	return nil
}

// parseWorkspaceRoots 解析允许的工作区根白名单（逗号分隔绝对路径；config.json 可写 JSON 数组）。
func parseWorkspaceRoots(v string) ([]string, error) {
	return normalizeWorkspaceRoots(strings.Split(v, ","))
}

// normalizeWorkspaceRoots 规范化白名单条目（两种书写形式汇到这一条路径）。
// 规则：跳过空项；每项必须是绝对路径（相对路径无法作为边界，且后续比较基于真身）；
// 去重（同一目录写两次只会让错误信息变长）。
func normalizeWorkspaceRoots(items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !filepath.IsAbs(item) {
			return nil, fmt.Errorf("需为绝对路径，得到 %q", item)
		}
		clean := filepath.Clean(item)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	// 全空（如显式设为空格）= 不限制：与"未设置"同义，不报错（便于用 env 临时放开）。
	return out, nil
}

// parseProjectionDirs 解析 `runtime:kind=dir[,runtime:kind=dir]`（FORGE_PROJECTION_DIRS）。
func parseProjectionDirs(v string) (map[string]string, error) {
	return normalizeProjectionDirs(strings.Split(v, ","))
}

// normalizeProjectionDirs 规范化投影覆盖条目（config.json 可写 JSON 数组，逗号是列表分隔符）。
//
// 规则（每条都对应一种「看起来生效了其实没有」的形态，故一律报错不静默）：
//   - 键必须是 `<runtime>:<kind>`，kind 属于 runtime.ValidProjectionKinds（写错 kind 会让覆盖
//     永远匹配不上，而用户只会看到"我设了根、物化还是写到老地方"）；
//   - 值必须是非空**绝对**目录（相对目录是相对谁？没有答案 → 拒绝）；
//   - 同一个 `<runtime>:<kind>` 不可重复声明（后者覆盖前者会静默改语义）。
//
// 分隔细节：键与值按**第一个** `=` 切开（这样目录里含 `=` 也能表达），而目录里含 `,` 请用
// config.json 的数组形式（逗号是这个键的列表分隔符，逗号串写法无法表达含逗号的路径）。
func normalizeProjectionDirs(items []string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]string{}
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		key, dir, hasEq := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		dir = strings.TrimSpace(dir)
		if !hasEq {
			return nil, fmt.Errorf("需为 `<runtime>:<kind>=<绝对目录>` 列表（如 opencode:skill=/tmp/oc），得到 %q", item)
		}
		runtimeID, kind, hasColon := strings.Cut(key, ":")
		runtimeID = strings.TrimSpace(runtimeID)
		kind = strings.TrimSpace(kind)
		if !hasColon || runtimeID == "" || kind == "" {
			return nil, fmt.Errorf("条目 %q 的键需为 `<runtime>:<kind>`（如 opencode:skill），得到 %q", item, key)
		}
		if !runtime.ValidProjectionKinds[runtime.ProjectionKind(kind)] {
			return nil, fmt.Errorf("条目 %q 的 kind %q 不是可投影的能力种类（合法：%s）",
				item, kind, projectionKindList())
		}
		if dir == "" {
			return nil, fmt.Errorf("条目 %q 的目录为空（要恢复成 adapter 声明的默认根，请删掉这一项）", item)
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("条目 %q 的目录必须是绝对路径，得到 %q", item, dir)
		}
		canonical := runtimeID + ":" + string(runtime.ProjectionKind(kind))
		if prev, dup := seen[canonical]; dup {
			return nil, fmt.Errorf("条目 %q 与 %q 重复声明了同一个投影 %q", item, prev, canonical)
		}
		seen[canonical] = item
		out[canonical] = filepath.Clean(dir)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("至少一项 `<runtime>:<kind>=<绝对目录>`")
	}
	return out, nil
}

// projectionKindList 可投影种类的稳定列表（错误文案用）。
func projectionKindList() string {
	kinds := make([]string, 0, len(runtime.ValidProjectionKinds))
	for k := range runtime.ValidProjectionKinds {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	return strings.Join(kinds, "|")
}
