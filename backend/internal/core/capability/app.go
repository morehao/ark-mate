package capability

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// ——— App 定义（应用体系一期 A+Slot；`DefinitionKindApp = "app"`）———
//
// App 是一等定义（`definition_revisions.kind = "app"`），把「组合型应用」与「实体型应用」都收敛到
// 一条定义上：一条 app 声明它引用的成员（专家/自动化/skill/mcp）与它拥有的实体，以及在固定扩展点
// 各自登记的一项（工具域 / App Slot / 页面 / 能力 scope / 归属 / 版本门）。设计见
// `.dsh/docs/specs/2026-11-09-app-system-design.md` §5.1。
//
// **与 expert 同构**（设计 D1）：基础字段（id/name/description/avatar/tags/enabled）与 expert 同款，
// id 词法与危险标记过滤都与 `ValidateExpert` 同口径——app 是「用户可编辑定义」族的又一员，校验
// 口径不另起一套。区别只在 app 多了 members/entities/tool_domains/slots/page/settings_schema/
// api_version 这些组合与扩展点字段。
//
// **它不向底座交付任何东西**（不物化/不注入，只影响平台内的默认值计算），故不进交付矩阵
// （见 `internal/archtest/delivery_matrix_test.go` 的排除理由）。

// App 是应用定义的一个条目（平台唯一事实源；JSON 定义与内存条目同一形状）。
type App struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Avatar 内置图标键（与 expert 同款：spark|chart|code|pen|search|briefcase）；不接受 URL。
	Avatar string   `json:"avatar,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	// Members 组合/归属：app 引用的既有成员定义 id（一等关系，落 `app_members`，见设计 §5.2）。
	// 成员只允许指向**非 app** 的既有定义（expert/automation/skill/mcp），app 不可嵌套 app（R2）。
	Members AppMembers `json:"members,omitempty"`
	// Entities 实体型应用携带的新表归属声明（§5.2.1，可选）。落 `app_members(member_kind='entity')`。
	// 每个 `kind` 必须编译期在 `appslot.KnownEntities` 里在册（写面校验，见 WP3）。
	Entities []AppEntity `json:"entities,omitempty"`
	// ToolDomains 绑定的 MCP 工具域前缀（§5.3）。域键为单段、不含下划线（前缀分派约束）。
	ToolDomains []string `json:"tool_domains,omitempty"`
	// Slots 携带的执行逻辑钩子（§5.3，App Slot）。`impl_id` 必须编译期在 `appslot.KnownImpls`
	// 白名单里在册（写面校验，见 WP3；守卫 `TestAppSlotImplWhitelisted` 是防退化服务定位图的根）。
	Slots []AppSlot `json:"slots,omitempty"`
	// Page 前端入口（§5.6）。nil = 无页面。
	Page *AppPage `json:"page,omitempty"`
	// SettingsSchema 应用级配置（复用 settings 表单机制）。nil / 空 = 无配置。
	SettingsSchema string `json:"settings_schema,omitempty"`
	// APIVersion 面向核心的应用 API 版本（兼容门，§5.7，caret 范围，如 "^1"）。
	APIVersion string `json:"api_version,omitempty"`
	// Enabled 是否启用；nil/未声明 = 启用（与 expert 同口径）。禁用不影响历史会话。
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled 报告 app 当前是否启用（未声明 = 启用，与 Expert.IsEnabled 同口径）。
func (a App) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

// AppMembers app 引用的既有成员定义 id 集合（组合/归属）。只允许非 app 的既有定义种类。
type AppMembers struct {
	Experts     []string `json:"experts,omitempty"`
	Automations []string `json:"automations,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Mcps        []string `json:"mcps,omitempty"`
}

// AppEntity 实体型应用声明的实体归属（`{kind, table, id_prefix}`，§5.2.1）。
//
// `kind` 是实体种类键，须在 `appslot.KnownEntities` 注册表里在册；`table` / `id_prefix` 由该
// 注册表声明兜底（app 定义只引用 kind，不自由指定物理表——防"数据凭空指定一个不存在的表"）。
type AppEntity struct {
	Kind     string `json:"kind"`
	Table    string `json:"table,omitempty"`
	IDPrefix string `json:"id_prefix,omitempty"`
}

// AppSlot app 携带的一个执行逻辑钩子（`{impl_id}`，§5.4）。
//
// `impl_id` 只能引用编译期 `appslot.RegisterImpl` 登记过的实现 id（写面校验在册与否，见 WP3）。
type AppSlot struct {
	ImplID string `json:"impl_id"`
}

// AppPage 前端页面入口（§5.6）。`component` 是编译期在包里的组件名（一期封闭静态包）。
type AppPage struct {
	NavLabel  string `json:"nav_label"`
	Route     string `json:"route"`
	Component string `json:"component"`
}

// ——— App 硬上限（与 expert 同量级；超出即 40001）———
const (
	MaxAppNameRunes        = 40
	MaxAppDescriptionRunes = 120
	MaxAppTags             = 8
	MaxAppTagRunes         = 20
	MaxAppIDLen            = 64
	// MaxAppMembersPerKind 每类成员引用的上限（防失控巨链，R2）。
	MaxAppMembersPerKind = 50
	MaxAppEntities       = 16
	MaxAppToolDomains    = 16
	MaxAppSlots          = 16
)

// AppAPI 是当前核心暴露的应用 API 主版本（§5.7）。app 的 api_version caret 与此比对。
const AppAPI = "1.0"

// appToolDomainRE 工具域前缀词法：单段、小写字母开头、只含小写字母数字（**不含下划线**——
// `platformmcp.New` 要求域键无下划线，否则前缀分派失败，§5.3 域键约束）。
var appToolDomainRE = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// appImplIDRE 实现 id 词法：允许 `app.sync` / `foo-bar.baz` 这类带点/连字符的分段命名
// （impl_id 是编译期白名单里的标识，不是一个文件/路径，词法只需稳定可读）。
var appImplIDRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.\-][a-z0-9]+)*$`)

// appEntityKindRE 实体种类键词法：小写字母开头、只含小写字母数字/下划线（与表名段同风格）。
var appEntityKindRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// appRouteRE 前端路由词法：以 `/` 开头，只含路径段字符（段内小写字母/数字/连字符）。
var appRouteRE = regexp.MustCompile(`^/[a-z0-9]+(?:[/-][a-z0-9]+)*/?$`)

// caretRE 解析 caret 版本范围 `^<major>` 或 `^<major>.<minor>`（如 `^1`、`^1.5`）。
var caretRE = regexp.MustCompile(`^\^(\d+)(?:\.(\d+))?$`)

// ValidateApp 是应用定义的**唯一闸门**（纯函数；写面与装载共用）。
//
// 它会**规范化**部分字段（members 各项去空白、去重、升序；name 去首尾空白），因此调用方应以本
// 函数返回后的值为准落库。拒绝即返回错误，上层翻成 `app_invalid`(40001)。
//
// 与 `ValidateExpert` **同口径**（设计 D1）：id 用同一个 `skillIDRE` 且 ≤64 位、name 1–40、
// description ≤120、tags ≤8×≤20、avatar 用同一套内置键、危险标记用同一组 `expertDangerousMarkers`。
//
// **这里只校验词法/上限，不校验白名单在册**：`entities[].kind ∈ appslot.KnownEntities` 与
// `slots[].impl_id ∈ appslot.KnownImpls` 是装配期/写面（WP3）的职责——那是"引用了不存在的实现"，
// 与"词法非法"是两类错误。本函数保持纯词法，便于 WP2 领域核心单测不依赖注册表。
func ValidateApp(a *App) error {
	if a == nil {
		return fmt.Errorf("capability app: 定义为空")
	}
	if a.ID == "" || !skillIDRE.MatchString(a.ID) || len(a.ID) > MaxAppIDLen {
		return fmt.Errorf("capability app: id 非法（须小写字母/数字，用连字符分段，形如 know-your-foo，≤%d 位）: %q", MaxAppIDLen, a.ID)
	}
	name := strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxAppNameRunes {
		return fmt.Errorf("capability app %q: name 须为 1–%d 字符（当前 %d）", a.ID, MaxAppNameRunes, n)
	}
	a.Name = name
	if n := utf8.RuneCountInString(a.Description); n > MaxAppDescriptionRunes {
		return fmt.Errorf("capability app %q: description 超过 %d 字符（当前 %d）", a.ID, MaxAppDescriptionRunes, n)
	}
	if a.Avatar != "" && !ValidExpertAvatars[a.Avatar] {
		return fmt.Errorf("capability app %q: avatar %q 非法（合法：spark|chart|code|pen|search|briefcase）", a.ID, a.Avatar)
	}
	if len(a.Tags) > MaxAppTags {
		return fmt.Errorf("capability app %q: tags 最多 %d 个（当前 %d）", a.ID, MaxAppTags, len(a.Tags))
	}
	for _, t := range a.Tags {
		if utf8.RuneCountInString(strings.TrimSpace(t)) > MaxAppTagRunes {
			return fmt.Errorf("capability app %q: 标签 %q 超过 %d 字符", a.ID, t, MaxAppTagRunes)
		}
	}

	// members：各类各项规范化（去空白/去重/升序）+ 上限。
	if err := normalizeAppMembers(a); err != nil {
		return err
	}

	// entities：词法 + 上限；kind 必须在册这一层不做（留 WP3 查 KnownEntities）。
	if len(a.Entities) > MaxAppEntities {
		return fmt.Errorf("capability app %q: entities 最多 %d 个（当前 %d）", a.ID, MaxAppEntities, len(a.Entities))
	}
	seenEntity := make(map[string]bool, len(a.Entities))
	for i := range a.Entities {
		e := &a.Entities[i]
		e.Kind = strings.TrimSpace(e.Kind)
		if e.Kind == "" || !appEntityKindRE.MatchString(e.Kind) {
			return fmt.Errorf("capability app %q: entities[%d].kind %q 非法（须小写字母/数字/下划线，且非空）", a.ID, i, e.Kind)
		}
		if seenEntity[e.Kind] {
			return fmt.Errorf("capability app %q: entities 里 kind %q 重复", a.ID, e.Kind)
		}
		seenEntity[e.Kind] = true
	}

	// tool_domains：域键单段、不含下划线（前缀分派约束，§5.3）+ 上限 + 去重。
	if len(a.ToolDomains) > MaxAppToolDomains {
		return fmt.Errorf("capability app %q: tool_domains 最多 %d 个（当前 %d）", a.ID, MaxAppToolDomains, len(a.ToolDomains))
	}
	seenDomain := make(map[string]bool, len(a.ToolDomains))
	domains := make([]string, 0, len(a.ToolDomains))
	for _, d := range a.ToolDomains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !appToolDomainRE.MatchString(d) {
			return fmt.Errorf("capability app %q: tool_domains 里的域键 %q 非法（须单段、小写字母开头、只含小写字母数字，不含下划线——前缀分派要求）", a.ID, d)
		}
		if seenDomain[d] {
			continue
		}
		seenDomain[d] = true
		domains = append(domains, d)
	}
	a.ToolDomains = domains

	// slots：impl_id 词法 + 上限 + 去重；在册与否留 WP3 查 KnownImpls。
	if len(a.Slots) > MaxAppSlots {
		return fmt.Errorf("capability app %q: slots 最多 %d 个（当前 %d）", a.ID, MaxAppSlots, len(a.Slots))
	}
	seenImpl := make(map[string]bool, len(a.Slots))
	for i := range a.Slots {
		id := strings.TrimSpace(a.Slots[i].ImplID)
		a.Slots[i].ImplID = id
		if id == "" || !appImplIDRE.MatchString(id) {
			return fmt.Errorf("capability app %q: slots[%d].impl_id %q 非法（须小写字母开头，可含点/连字符分段）", a.ID, i, id)
		}
		if seenImpl[id] {
			return fmt.Errorf("capability app %q: slots 里 impl_id %q 重复", a.ID, id)
		}
		seenImpl[id] = true
	}

	// page：三字段齐备（声明了 page 就必须有 nav_label/route/component）+ route 词法；危险标记过滤。
	if a.Page != nil {
		p := a.Page
		p.NavLabel = strings.TrimSpace(p.NavLabel)
		p.Route = strings.TrimSpace(p.Route)
		p.Component = strings.TrimSpace(p.Component)
		if p.NavLabel == "" || p.Route == "" || p.Component == "" {
			return fmt.Errorf("capability app %q: page 声明后 nav_label/route/component 都不能为空", a.ID)
		}
		if !appRouteRE.MatchString(p.Route) {
			return fmt.Errorf("capability app %q: page.route %q 非法（须以 / 开头，段内小写字母/数字/连字符）", a.ID, p.Route)
		}
	}

	// api_version：caret 范围（如 "^1" / "^1.5"）；空 = 默认 "^1"。
	if a.APIVersion == "" {
		a.APIVersion = "^1"
	}
	if _, _, ok := parseAppAPIVersion(a.APIVersion); !ok {
		return fmt.Errorf("capability app %q: api_version %q 非法（须是 caret 范围，形如 ^1 或 ^1.5）", a.ID, a.APIVersion)
	}

	// 危险标记过滤：复用 expert 的同一组集，作用于所有**文本承载**字段（settings_schema / page 文案）。
	for _, m := range expertDangerousMarkers {
		if strings.Contains(a.SettingsSchema, m) {
			return fmt.Errorf("capability app %q: settings_schema 含危险标记 %q（拒绝保存）", a.ID, m)
		}
		if a.Page != nil && (strings.Contains(a.Page.NavLabel, m) || strings.Contains(a.Page.Component, m)) {
			return fmt.Errorf("capability app %q: page 文案含危险标记 %q（拒绝保存）", a.ID, m)
		}
	}
	return nil
}

// normalizeAppMembers 对 members 各类去空白/去重/升序，并施加每类上限（R2 防失控）。
func normalizeAppMembers(a *App) error {
	var err error
	if a.Members.Experts, err = normalizeAppMemberRefs("experts", a.ID, a.Members.Experts); err != nil {
		return err
	}
	if a.Members.Automations, err = normalizeAppMemberRefs("automations", a.ID, a.Members.Automations); err != nil {
		return err
	}
	if a.Members.Skills, err = normalizeAppMemberRefs("skills", a.ID, a.Members.Skills); err != nil {
		return err
	}
	if a.Members.Mcps, err = normalizeAppMemberRefs("mcps", a.ID, a.Members.Mcps); err != nil {
		return err
	}
	return nil
}

// normalizeAppMemberRefs 去空白、去重、升序；空项忽略（与 `normalizeExpertRefs` 同一取向：
// 成员引用是便利声明，不为空格/重复报错），并施加每类上限。
func normalizeAppMemberRefs(kind, id string, in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > MaxAppMembersPerKind {
		return nil, fmt.Errorf("capability app %q: members.%s 最多 %d 个（当前 %d）", id, kind, MaxAppMembersPerKind, len(out))
	}
	sort.Strings(out)
	return out, nil
}

// parseAppAPIVersion 解析 caret 版本范围，返回 (major, minor, ok)。minor 缺省为 0。
func parseAppAPIVersion(v string) (major, minor int, ok bool) {
	m := caretRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(m[1], "%d", &major); err != nil {
		return 0, 0, false
	}
	if m[2] != "" {
		if _, err := fmt.Sscanf(m[2], "%d", &minor); err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}
