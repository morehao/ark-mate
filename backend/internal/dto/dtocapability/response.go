package dtocapability

import "encoding/json"

// SkillResp Skill 目录条目。
type SkillResp struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Body        string `json:"body,omitempty"` // SKILL.md 内容（投影源）
	// Runtimes 适用 runtime 白名单；**空 = 全部**（未声明即不限制）。
	// 「放哪、什么格式」不在这里——那是 runtime 的投影声明（旧 sources 字段已退役）。
	Runtimes []string `json:"runtimes,omitempty"`
	// Scope 生效作用域（global|workspace；未声明 = global）。M1 只读不消费。
	Scope string `json:"scope,omitempty"`
	// Enabled 是否投放（§13.4 S4；未声明 enabled 的定义 = true）。禁用只影响投放，定义照旧保留。
	Enabled bool `json:"enabled"`
	// Warnings 装载期正文扫描的非 critical 告警（§13.2 S1）。**critical 的定义根本不会出现在这里**
	// ——它们在装载时就被拒载了；这些是「放行了但你该看一眼」的（引用敏感路径、可疑命令串联、超长）。
	Warnings []SkillWarningResp `json:"warnings,omitempty"`
}

// SkillWarningResp 一条正文扫描告警。severity = info|warning|critical（出现在本响应里的不会是
// critical），pattern 是稳定规则 id，message 是人话说明。
type SkillWarningResp struct {
	Severity string `json:"severity"`
	Pattern  string `json:"pattern"`
	Message  string `json:"message"`
}

// McpServerResp MCP 注册表条目。§13.3 M1/M6：**明文与密钥分开读回**——
// 明文 `env_keys` 给得出（非密钥），密钥侧一律**只给键名**（`secret_env_keys`/`secret_header_keys`），
// 连引用都不回（引用能反推"这台机器上有没有那个密钥"，没必要给）。header 明文键仍不读回（保持原立场）。
// `unresolved_secret_keys` 是"引用绑了但解析不了"（库里没有 `mcp_secrets` 那一行 / env 变量没设）——
// 这类半配置以前只能在会话启动的日志里撞见，现在列表就能看出来。
type McpServerResp struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Transport string   `json:"transport"` // stdio | sse | http | streamable_http
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
	// Scope 生效作用域（global|workspace；未声明 = global）。M1–M4 只读不消费。
	Scope string `json:"scope,omitempty"`
	// Enabled 是否注入（未声明 = true）。禁用只影响注入：定义、密钥引用、探测结果都保留。
	Enabled bool `json:"enabled"`
	// EnvKeys 明文环境变量的键（非密钥，可展示）。
	EnvKeys []string `json:"env_keys,omitempty"`
	// SecretEnvKeys 密钥型环境变量的键（**只有键名**）。
	SecretEnvKeys []string `json:"secret_env_keys,omitempty"`
	// SecretHeaderKeys 密钥型 header 的键（**只有键名**）。
	SecretHeaderKeys []string `json:"secret_header_keys,omitempty"`
	// UnresolvedSecretKeys 已绑定但当前解析不了的密钥键（库里缺那一行 / env 未设）。
	UnresolvedSecretKeys []string `json:"unresolved_secret_keys,omitempty"`
	// 以下为 live 探测状态（P2 §5.2；从未探测则零值/空）。
	ToolCount    int    `json:"tool_count"`                 // 该 server 当前归属的 Tool 条目数
	LastProbeAt  string `json:"last_probe_at,omitempty"`    // 最近一次探测时间（RFC3339）
	LastProbeErr string `json:"last_probe_error,omitempty"` // 最近一次探测失败原因（成功则空）
}

// McpProbeResp 一次 MCP live 探测（tools/list）的结果。
type McpProbeResp struct {
	ID            string     `json:"id"`
	Transport     string     `json:"transport"`
	ServerName    string     `json:"server_name,omitempty"`
	ServerVersion string     `json:"server_version,omitempty"`
	ProbedAt      string     `json:"probed_at"`
	Added         int        `json:"added"`      // 本轮写入的 Tool 条目
	Removed       int        `json:"removed"`    // 被本轮替换掉的旧条目
	Skipped       int        `json:"skipped"`    // 非法/无名条目（跳过，不臆造）
	ToolCount     int        `json:"tool_count"` // 探测后该 server 归属条目总数
	Tools         []ToolResp `json:"tools"`
}

// McpToolCallResp 一次 MCP 工具调用结果。
//
// Content 保持 MCP 联合类型原样（文本/图片/资源链接…）；Text 是文本块拼接的可读摘要；
// IsError 是 **工具级** 失败（服务端明确报告），与传输/协议错误（HTTP 信封 code≠0）区分。
type McpToolCallResp struct {
	ServerID          string            `json:"server_id"`
	Tool              string            `json:"tool"`
	IsError           bool              `json:"is_error"`
	Content           []json.RawMessage `json:"content"`
	Text              string            `json:"text,omitempty"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	ServerName        string            `json:"server_name,omitempty"`
	ServerVersion     string            `json:"server_version,omitempty"`
	DurationMS        int64             `json:"duration_ms"`
}

// McpResourcesResp resources/list（+ resources/templates/list）的实时结果。
// 资源与提示词**不做目录化**（不写 capabilities 目录、不落盘）：它们是 server 的实时视图，
// 且可能随权限/时间变化；每次请求现拉，语义最诚实。
type McpResourcesResp struct {
	ServerID       string                    `json:"server_id"`
	Resources      []McpResourceResp         `json:"resources"`
	Templates      []McpResourceTemplateResp `json:"templates,omitempty"`
	TemplatesError string                    `json:"templates_error,omitempty"`
	ServerName     string                    `json:"server_name,omitempty"`
	ServerVersion  string                    `json:"server_version,omitempty"`
}

type McpResourceResp struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

type McpResourceTemplateResp struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

// McpResourceReadResp resources/read 结果（text 与 blob 二选一，原样保真）。
type McpResourceReadResp struct {
	ServerID string               `json:"server_id"`
	URI      string               `json:"uri"`
	Contents []McpResourceContent `json:"contents"`
}

type McpResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// McpPromptsResp prompts/list 的实时结果。
type McpPromptsResp struct {
	ServerID   string          `json:"server_id"`
	Prompts    []McpPromptResp `json:"prompts"`
	ServerName string          `json:"server_name,omitempty"`
}

type McpPromptResp struct {
	Name        string                  `json:"name"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Arguments   []McpPromptArgumentResp `json:"arguments,omitempty"`
}

type McpPromptArgumentResp struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// McpPromptGetResp prompts/get 结果（content 保持 MCP 联合类型原样）。
type McpPromptGetResp struct {
	ServerID    string             `json:"server_id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Messages    []McpPromptMessage `json:"messages"`
}

type McpPromptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ToolResp Tool 目录条目。
type ToolResp struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Source      string          `json:"source"`               // builtin | mcp | observed
	McpServer   string          `json:"mcp_server,omitempty"` // source=mcp 时的归属 server id（按 agent 可见性过滤）
	Schema      json.RawMessage `json:"schema,omitempty"`     // 参数 JSON Schema（原样保真）
}

// SkillMaterializationResp 一个 skill 的物化体检（§13.4 S2：审计/复现面）。
// 只读：看每一处物化目标上"应该是什么、实际是什么"，不写盘也不修复（修复交给重校/启停）。
type SkillMaterializationResp struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// States 每处物化目标一条（按 root 排序）。
	States []SkillMaterializationState `json:"states"`
	// Notes 无法逐个给状态时的说明（如本进程没登记任何物化目标）。
	Notes []string `json:"notes,omitempty"`
}

// SkillMaterializationState 一处物化目标的现状。
//
// status 取值（每个都可判定）：
//
//	in_sync        产物与注册表一致（摘要自洽且等于期望载荷摘要）
//	missing        该物化但没有产物（含目录在但 SKILL.md 丢了）
//	tampered       产物被动过（内容/权限/多文件/软链逃逸…摘要对不上），或 sidecar 损坏
//	stale          产物是老版本写的 / 技能已禁用但副本还在 → 下一次重校会改写或撤回
//	disabled       技能已禁用且没有产物（期望状态，不是问题）
//	user_owned     目录存在但不带平台归属标记（用户自有/他方安装，平台不碰）
//	not_applicable 该 runtime 投影不出来（技能的 runtimes 白名单不含它，或它的 format 未实证）
//	untested       投影形态尚未经真实底座端到端实测（声明了、根也解得开，但没验证过底座真会读）
//	not_configured 该 runtime 没有解析出投影根（未声明投影 / 占位符变量未设置）
type SkillMaterializationState struct {
	Root    string `json:"root"`
	Runtime string `json:"runtime,omitempty"`
	Dir     string `json:"dir"`
	Status  string `json:"status"`
	// Hash 期望摘要（注册表侧算出来的载荷摘要；not_applicable 时为空）。
	Hash string `json:"hash,omitempty"`
	// ActualHash 磁盘实测摘要（仅在能算出来时给）。
	ActualHash string `json:"actual_hash,omitempty"`
	// WrittenAt 产物 sidecar 记录的写入时间（RFC3339；无 sidecar 则空）。
	WrittenAt string `json:"written_at,omitempty"`
	// Owners 归属标记里登记的投影方集合（共享 root 下可能有多个）。
	Owners []string `json:"owners,omitempty"`
	// Detail 人话说明（status 不够用时才给）。
	Detail string `json:"detail,omitempty"`
}

// SkillToggleResp 启停一个 skill 的结果（§13.4 S4）。
type SkillToggleResp struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Path 写回的定义定位串（文件路径 / `db:skill/<id>`；载体里没有它时请求直接失败，不会走到这里）。
	Path string `json:"path,omitempty"`
	// Written/Removed 立刻重校物化的结果：启用会写文件，禁用会**撤回**已物化副本。
	Written int `json:"written"`
	Removed int `json:"removed"`
	// MaterializeErrors 重校失败原因（定义已改成功，故不回滚——失败原因如实回带）。
	MaterializeErrors []string `json:"materialize_errors,omitempty"`
	// NextAction 这一步之后该知道的事（如「已物化副本已撤回」）。
	NextAction string `json:"next_action,omitempty"`
}

// ——— 写面结果（M2） ———

// SkillWriteResp 新建/覆盖 skill 定义的结果（POST /skills、PUT /skills/:id）。
type SkillWriteResp struct {
	ID string `json:"id"`
	// Path 定义的**定位串**（给人看的，不参与任何判据）：memory 模式 = 定义文件路径，
	// sqlite 模式 = `db:skill/<id>`（事实源是 `skills` 表的那一行，没有文件可指）。
	Path string `json:"path"`
	// Created true = 本次新建了定义（文件 / DB 行）；false = 覆盖写既有定义。
	Created bool `json:"created"`
	// Written/Removed 写面之后立刻重校物化的结果（新增/改正文会写，禁用会撤回）。
	Written int `json:"written"`
	Removed int `json:"removed"`
	// Warnings 正文扫描的非 critical 告警（critical 的定义在这里已经被拒了，不会返回）。
	Warnings []SkillWarningResp `json:"warnings,omitempty"`
	// MaterializeErrors 物化重校失败原因：**定义已经落盘成功，故不回滚**——失败原因如实回带，
	// 让用户知道"定义生效了，但某个 runtime 的副本没写出去"。
	MaterializeErrors []string `json:"materialize_errors,omitempty"`
	// NextAction 这一步之后该知道的事。
	NextAction string `json:"next_action,omitempty"`
}

// SkillDeleteResp 删除 skill 定义的结果（DELETE /skills/:id）。
type SkillDeleteResp struct {
	ID string `json:"id"`
	// Deleted 是否真的删掉了定义。**不存在 → false 且无错**：DELETE 的语义是"让它不在"，
	// 不是"证明它曾在"（幂等）。
	Deleted bool `json:"deleted"`
	// Path 被删定义的定位串（文件路径 / `db:skill/<id>`；Deleted=false 时仍回带，便于确认"本来就没有"）。
	Path string `json:"path,omitempty"`
	// Removed 撤回的已物化副本数（删定义必须一起撤回，否则底座还看得见它）。
	Removed int `json:"removed"`
	// MaterializeErrors 撤回失败原因（文件已删，不回滚）。
	MaterializeErrors []string `json:"materialize_errors,omitempty"`
	NextAction        string   `json:"next_action,omitempty"`
}

// McpWriteResp 新建/覆盖 MCP server 定义的结果（POST /mcps、PUT /mcps/:id）。
type McpWriteResp struct {
	ID string `json:"id"`
	// Path 定义的定位串（同 SkillWriteResp.Path：文件路径或 `db:mcp/<id>`）。
	Path    string `json:"path"`
	Created bool   `json:"created"`
	// UnresolvedSecretKeys 绑了密钥引用但当前解析不出来的键（只给键名）：
	// 定义本身合法，但注入时这些键拿不到值——现在说比会话启动时才炸好。
	UnresolvedSecretKeys []string `json:"unresolved_secret_keys,omitempty"`
	NextAction           string   `json:"next_action,omitempty"`
}

// McpDeleteResp 删除 MCP server 定义的结果（DELETE /mcps/:id）。
type McpDeleteResp struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
	// Path 被删定义的定位串（文件路径 / `db:mcp/<id>`）。
	Path string `json:"path,omitempty"`
	// ToolsRemoved 一并摘除的、归属该 server 的 mcp 来源工具数。
	// 必须一起摘：留着已删 server 的工具等于让用户看见一个调不通的能力。
	ToolsRemoved int    `json:"tools_removed"`
	NextAction   string `json:"next_action,omitempty"`
}
