// Package dtoapp 是「应用定义」（一期 A+Slot）REST 面（`/apps`）的请求/响应契约。
//
// 与 dtocapability 的专家定义面同构：写面用**原文 JSON 对象**（`map[string]json.RawMessage`，
// 键的出现与否有语义——未出现的键保留原值、`null` 删键），读面用强类型 `AppResp`。
// 应用定义本体（`capability.App`）的字段形状见 `internal/core/capability/app.go`。
package dtoapp

import "encoding/json"

// AppWriteReq 新建/覆盖一个应用定义（POST /apps、PUT /apps/:id）。
//
// 原文 JSON 对象的已知键：id / name / description / avatar / tags / members / entities /
// tool_domains / slots / page / settings_schema / api_version / enabled；未知键原样保留。
type AppWriteReq map[string]json.RawMessage

// AppToggleReq 启停一个应用（PATCH /apps/:id）。只认 enabled 一个开关。
type AppToggleReq struct {
	Enabled bool `json:"enabled"`
}

// AppResp 应用读面的一条（列表与详情共用）。
//
// 时间口径：`created_at` / `updated_at` 是 Unix 毫秒 int64（`objtime.Milli` 同量纲，前端
// `new Date(ms)` 零换算）。`revision` 是写面 CAS 的当前版本（= 该 app 最新审计 seq），
// 客户端 PUT 时带上它可触发「不中即 40901」。
//
// `has_page` 是 `page != nil` 的派生布尔（列表用它标「有前端入口」，不用回 page 全量）；
// `settings_schema` 只在详情返回（列表抹掉，避免列表膨胀）。
type AppResp struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	// Tagline 一句话能力句（v42，≤40 字）；空 = 前端回退到 description。
	Tagline string `json:"tagline,omitempty"`
	// Examples 可直接发送的示例话术（v42，≤6 条、每条 ≤100 字）。
	Examples []string `json:"examples,omitempty"`
	// UsageGuide 「怎么用」正文（v42，≤600 字）：适用场景 / 需要提供什么 / 产出 / 边界。
	UsageGuide string   `json:"usage_guide,omitempty"`
	Avatar     string   `json:"avatar,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Members 组合/归属：app 引用的既有成员定义 id。
	Members AppMembersResp `json:"members,omitempty"`
	// Entities 实体型应用携带的新表归属声明。
	Entities    []AppEntityResp `json:"entities,omitempty"`
	ToolDomains []string        `json:"tool_domains,omitempty"`
	Slots       []AppSlotResp   `json:"slots,omitempty"`
	Page        *AppPageResp    `json:"page,omitempty"`
	// SettingsSchema 应用级配置（仅详情返回；列表为空 → omitempty 不出现）。
	SettingsSchema string `json:"settings_schema,omitempty"`
	APIVersion     string `json:"api_version,omitempty"`
	// Enabled 是否启用。
	Enabled bool `json:"enabled"`
	// State 派生生命周期状态（WP-C6，设计 §11）：off/pending/active/failed/disposed。
	// 由「有无本地包 + enabled + 运行期 host 实时态」派生，非新落库列。
	State string `json:"state"`
	// Why state 的人话原因（failed/pending 时非空，如「子进程崩溃：exit status 3」）。
	Why string `json:"why,omitempty"`
	// Source 应用来源（v41 落库）：`self`=自建（含对话创建）/ `market`=市场下载 / 装包。
	// 持久元数据，读面从 apps.source 列直接读回；PUT body 不含它、更新不改它。
	Source string `json:"source"`
	// HasPage 是否声明了前端页面入口（列表用它标记）。
	HasPage bool `json:"has_page"`
	// Revision 写面当前版本（= 最新审计 seq；0 = 从未写过的旧值）。
	Revision int64 `json:"revision"`
	// CreatedAt / UpdatedAt Unix 毫秒。
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// AppMembersResp 应用引用的成员定义 id 集合。
type AppMembersResp struct {
	Experts     []string `json:"experts,omitempty"`
	Automations []string `json:"automations,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Mcps        []string `json:"mcps,omitempty"`
}

// AppEntityResp 实体归属声明（`{kind, table, id_prefix}`）。
type AppEntityResp struct {
	Kind     string `json:"kind"`
	Table    string `json:"table,omitempty"`
	IDPrefix string `json:"id_prefix,omitempty"`
}

// AppSlotResp 执行逻辑钩子（`{impl_id}`）。
type AppSlotResp struct {
	ImplID string `json:"impl_id"`
}

// AppPageResp 前端页面入口。
type AppPageResp struct {
	NavLabel  string `json:"nav_label"`
	Route     string `json:"route"`
	Component string `json:"component"`
}

// AppWriteResp 新建/覆盖应用定义的结果。
type AppWriteResp struct {
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
	// Created true = 本次新建；false = 覆盖既有定义。
	Created bool `json:"created"`
	// Revision 写入后的当前版本（= 最新审计 seq）。
	Revision int64 `json:"revision"`
}

// AppDeleteResp 删除应用定义的结果（不存在 → deleted:false 且无错：幂等）。
type AppDeleteResp struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// AppToggleResp 启停应用定义的结果。
type AppToggleResp struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}

// AppInstallReq 安装一个本地应用包（POST /apps/install）。dir = 来源包目录（含 manifest.json 的包根）；
// enable=true 时装完立即启用（起 host + apply slot）。
type AppInstallReq struct {
	Dir    string `json:"dir"`
	Enable bool   `json:"enable"`
}

// AppInstallResp 安装结果。
type AppInstallResp struct {
	ID        string `json:"id"`
	Dir       string `json:"dir"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
}

// AppUninstallResp 卸载结果（先停运行期 + 清包目录，再删定义）。
type AppUninstallResp struct {
	ID                string `json:"id"`
	Uninstalled       bool   `json:"uninstalled"`
	PackRemoved       bool   `json:"pack_removed"`
	DefinitionDeleted bool   `json:"definition_deleted"`
}
