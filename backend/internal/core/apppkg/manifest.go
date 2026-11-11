// Package apppkg 定义「运行期应用包」的清单（manifest）格式、解析与校验，以及本地应用目录的
// 扫描装载与完整性（sha256）校验。它是二期 C（运行期装卸）的地基：C1 只立「包长什么样、怎么校验、
// 怎么从目录把它读进来」，不碰加载/执行代码（那是 C2 的运行期 impl 来源与 C3 的隔离子进程）。
//
// # 与 App 定义模型的分工（设计 §17.1/§17.2）
//
// 本包与 `core/capability` 的 `App` 是**两块正交的东西**，刻意不重复：
//
//   - `capability.App` 描述「一个应用**引用/启用**什么」——它是平台内的定义（members/entities/
//     tool_domains/slots/page/api_version），落 `apps` + `app_members` 表，由写面校验。
//   - `apppkg.Manifest` 描述「一个**包提供**什么」——它是二进制之外的运行期产物随包带的分发清单
//     （提供哪些 impl、哪些工具域、可选页面、需要哪些权限、入口脚本、完整性哈希）。
//
// 二者**靠同一套 id 词法、同一个 `id` 值关联**（包 id = app 定义 id），但字段不互相镜像：manifest
// 不重复 App 的 members/entities，App 不重复 manifest 的 provides/permissions/entrypoint。C2 以后
// 装配期据「这个 app 是否来自包目录」决定它的 impl 来源。设计见
// `.dsh/docs/specs/2026-11-09-app-system-design.md` §17，契约继承见 §17.1、增量 §17.2。
//
// # 校验口径（一律复用既有事实源，不另起一套）
//
//   - id：与 `capability.App` 同口径 `^[a-z0-9]+(-[a-z0-9]+)*$`——**复用 `capability.ValidateApp`
//     本身**（见 validateManifestID），不在这里再写一份正则，避免两套口径漂移。
//   - api_version：caret 范围（如 `^1` / `^1.5`）+ 与核心主版本 `capability.AppAPI` 兼容——同样
//     **复用 `capability.ValidateApp` 的 caret 判定**（见 validateManifestAPIVersion），不重写。
//   - provides.tools[].domain：单段、下划线禁止（对齐 `platformmcp`/`infra/apptool` 的前缀分派约束）。
//   - permissions：白名单枚举（本文件集中声明，见 Permission）。
package apppkg

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Manifest 是一个运行期应用包随包携带的分发清单（`manifest.json`）。
//
// 它是「包提供什么」的唯一声明；App 定义（`capability.App`）是「引用/启用什么」，靠 ID 关联。
// 字段口径见包注释与设计 §17.2。
type Manifest struct {
	// ID 包标识，同时是关联 App 定义的键。词法与 App 同口径 `^[a-z0-9]+(-[a-z0-9]+)*$`，≤64 位。
	ID string `json:"id"`
	// Version 包自身的语义化版本（如 "1.2.0"）。本字段只做自述/分发治理（C8 用），C1 不强校验
	// 语义化格式（避免与未来分发策略耦合），但不可为空。
	Version string `json:"version"`
	// APIVersion 面向核心的应用 API 版本（兼容门），caret 范围（如 "^1"）。空 = 默认 "^1"。
	// 判定与 `capability.App.APIVersion` 同一套逻辑。
	APIVersion string `json:"api_version,omitempty"`
	// Requires 对核心的最低版本要求（min core version）。如 "1.0"。低于它 → 包 FAILED（C8 生效点，
	// 本字段在 C1 只解析/自述，落到要求判定归 C8）。空 = 不设门槛。
	Requires *ManifestRequires `json:"requires,omitempty"`
	// Provides 声明这个包提供什么（impl / 工具域 / 可选页面）。
	Provides ManifestProvides `json:"provides"`
	// Tagline 一句话能力句（≤40 字），随包装进应用定义的「怎么用」区；空 = 不提供。
	Tagline string `json:"tagline,omitempty"`
	// Examples 可直接发送的示例话术（≤6 条、每条 ≤100 字）。
	Examples []string `json:"examples,omitempty"`
	// UsageGuide 「怎么用」正文（≤600 字）：适用场景 / 需要提供什么 / 产出 / 边界。
	UsageGuide string `json:"usage_guide,omitempty"`
	// Permissions 声明这个包「需要读的能力」白名单枚举（读用量/providers 摘要、网络域、文件域…）。
	// 见 Permission；未知项一律拒绝。
	Permissions []Permission `json:"permissions,omitempty"`
	// Entrypoint 主机启动入口——相对包根的可执行脚本路径（C3 起 app-host 子进程按此拉起）。
	Entrypoint string `json:"entrypoint"`
	// Integrity 完整性声明（当前仅 sha256；签名链留 C8/未来）。
	Integrity ManifestIntegrity `json:"integrity"`
}

// ManifestRequires 是包对核心环境的最低版本要求（C8 门槛的输入）。
type ManifestRequires struct {
	// MinCoreVersion 核心最低版本（自述口径，如 "1.0.0"）。空 = 不设门槛。
	MinCoreVersion string `json:"min_core_version,omitempty"`
}

// ManifestProvides 是包对外「提供」的东西的集合。
type ManifestProvides struct {
	// Impls 提供的后端行为实现（对应 A+Slot 的 `appslot.Impl`；C 里这些 impl 来自运行期包）。
	Impls []ManifestImpl `json:"impls,omitempty"`
	// Tools 提供的工具域（每个域名下的一组工具名）。
	Tools []ManifestToolDomain `json:"tools,omitempty"`
	// Page 可选前端页面声明（C7 用沙箱 iframe 渲染）。
	Page *ManifestPage `json:"page,omitempty"`
}

// ManifestImpl 是包提供的一个后端行为实现声明（`{impl_id, kind}`）。
//
// ImplID 词法与 `capability.App` 的 `slots[].impl_id` 同口径（允许 `foo.sync` 这类点/连字符分段）；
// kind 目前只区分实现类别（"slot" / "entity" 等），留给 C2 的来源路由与 C5 的实体分派。
type ManifestImpl struct {
	ImplID string `json:"impl_id"`
	Kind   string `json:"kind"`
}

// ManifestToolDomain 是包提供的一个工具域声明（`{domain, tools[]}`）。
//
// domain 必须单段、不含下划线——`platformmcp`/`infra/apptool` 靠「域前缀 + 工具名」做前缀分派，
// 域键带下划线会让分派歧义（与 `capability.App.ToolDomains` 同一条约束）。
type ManifestToolDomain struct {
	Domain string   `json:"domain"`
	Tools  []string `json:"tools"`
}

// ManifestPage 是包可选提供的前端页面声明（C7 渲染用）。
type ManifestPage struct {
	NavLabel string `json:"nav_label"`
	Route    string `json:"route"`
}

// ManifestIntegrity 是包的完整性声明。当前仅 sha256（对 entrypoint 及声明文件求哈希）；
// 签名链（第三方作者身份）留 C8/未来。
type ManifestIntegrity struct {
	// SHA256 是 entrypoint 文件内容的十六进制 sha256（小写、64 位）。
	SHA256 string `json:"sha256"`
}

// Permission 是包可声明的能力白名单项（最小集，集中声明在此以便一处审计）。
//
// 只放「包需要读什么」的粗粒度枚举——C1 只做声明与校验，真正的授权收窄在 C7 的 postMessage
// 桥/沙箱里落地。未知值一律拒绝（fail-closed）。
type Permission string

const (
	// PermReadUsage 读平台用量摘要（tokens/开销等只读聚合）。
	PermReadUsage Permission = "read:usage"
	// PermReadProviders 读 providers 摘要（模型/网关能力面，只读）。
	PermReadProviders Permission = "read:providers"
	// PermNetDomain 允许网络出站到声明的域（域名细化留给 C7 的沙箱 connect-src 白名单）。
	PermNetDomain Permission = "net:domain"
	// PermFileDomain 允许在声明的文件域内读写（路径细化留给 C7 的沙箱桥）。
	PermFileDomain Permission = "file:domain"
)

// AllPermissions 是全部合法权限项（白名单唯一来源；新增权限只改这里）。
var AllPermissions = []Permission{PermReadUsage, PermReadProviders, PermNetDomain, PermFileDomain}

var validPermissions = func() map[Permission]bool {
	m := make(map[Permission]bool, len(AllPermissions))
	for _, p := range AllPermissions {
		m[p] = true
	}
	return m
}()

// ——— 词法（与 capability 对齐；不在本包重写 id / api_version 判定）———

// manifestImplIDRE 与 `capability` 的 `appImplIDRE` 同口径：小写字母开头，可含点/连字符分段。
var manifestImplIDRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.\-][a-z0-9]+)*$`)

// manifestImplKindRE impl 类别键：小写字母开头、只含小写字母数字/下划线（与 entity kind 同风格）。
var manifestImplKindRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// manifestToolDomainRE 工具域键：单段、小写字母开头、只含小写字母数字（**不含下划线**）。
var manifestToolDomainRE = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// manifestToolNameRE 工具名：与 MCP 工具名同风格（小写字母开头、可含下划线）。
var manifestToolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// manifestRouteRE 页面路由：以 `/` 开头、段内小写字母/数字/连字符（与 `capability` 的 appRouteRE 同）。
var manifestRouteRE = regexp.MustCompile(`^/[a-z0-9]+(?:[/-][a-z0-9]+)*/?$`)

// manifestSHA256RE 完整性哈希：小写十六进制、64 位。
var manifestSHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// 硬上限（防失控清单；超出即拒绝）。
const (
	MaxManifestImpls        = 64
	MaxManifestToolDomains  = 16
	MaxManifestToolsPerDom  = 64
	MaxManifestPermissions  = 16
	MaxManifestIDLen        = 64
	MaxManifestVersionRunes = 64
	MaxManifestEntrypoint   = 512
	// 使用说明（与 capability 同量级；超出即拒绝）。
	MaxManifestTaglineRunes = 40
	MaxManifestExamples     = 6
	MaxManifestExampleRunes = 100
	MaxManifestUsageRunes   = 600
)

// ParseManifest 从 JSON 字节解析清单并校验（解析 + Validate 一步到位）。
//
// 严格解码：未知字段拒绝（`DisallowUnknownFields`）——清单是对外分发契约，容忍未知字段会让
// 拼错字段名静默失效（比"启动即炸"难查得多）。
func ParseManifest(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("apppkg: 解析 manifest 失败: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate 是清单的**唯一闸门**（纯函数；解析后与装载前共用）。
//
// 它会规范化部分字段（ID/entrypoint 去首尾空白），因此调用方应以本函数返回后的值为准。
// 拒绝即返回带 `apppkg:` 前缀的 error，上层据此把包标记为 FAILED 并明示原因。
//
// 校验范围（词法/结构/枚举）：id 与 api_version 复用 `capability.ValidateApp`；其余词法在本文件。
// **不校验**「impl_id 是否在某来源在册」「min core version 是否满足」——那是 C2/C8 的职责（引用
// 不存在的实现、版本不足，与"清单本身不合法"是两类错误）。
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("apppkg: manifest 为空")
	}
	m.ID = strings.TrimSpace(m.ID)
	if err := validateManifestID(m.ID); err != nil {
		return err
	}

	// version：非空、去空白、上限（语义化格式交给 C8，不在 C1 硬卡，避免与分发策略耦合）。
	m.Version = strings.TrimSpace(m.Version)
	if m.Version == "" {
		return fmt.Errorf("apppkg %q: version 不能为空", m.ID)
	}
	if r := []rune(m.Version); len(r) > MaxManifestVersionRunes {
		return fmt.Errorf("apppkg %q: version 超过 %d 字符", m.ID, MaxManifestVersionRunes)
	}

	// api_version：复用 capability 的 caret + 兼容判定；空 = 默认 ^1（由复用函数处理）。
	if err := validateManifestAPIVersion(m.ID, &m.APIVersion); err != nil {
		return err
	}

	// entrypoint：必填、相对路径、不含上跳（`..`）/绝对路径——它是包根内的脚本，不是任意系统路径。
	m.Entrypoint = strings.TrimSpace(m.Entrypoint)
	if err := validateEntrypoint(m.ID, m.Entrypoint); err != nil {
		return err
	}

	// integrity.sha256：必填、64 位小写十六进制。
	m.Integrity.SHA256 = strings.TrimSpace(m.Integrity.SHA256)
	if m.Integrity.SHA256 == "" {
		return fmt.Errorf("apppkg %q: integrity.sha256 不能为空（完整性校验是安全基线，缺一不开工）", m.ID)
	}
	if !manifestSHA256RE.MatchString(m.Integrity.SHA256) {
		return fmt.Errorf("apppkg %q: integrity.sha256 %q 非法（须 64 位小写十六进制）", m.ID, m.Integrity.SHA256)
	}

	// permissions：白名单枚举 + 去重 + 上限。
	if err := m.validatePermissions(); err != nil {
		return err
	}

	// provides.impls：impl_id 词法 + kind 词法 + 去重 + 上限。
	if err := m.validateImpls(); err != nil {
		return err
	}

	// provides.tools：域名单段无下划线 + 工具名词法 + 去重 + 上限。
	if err := m.validateTools(); err != nil {
		return err
	}

	// provides.page：声明则三要素齐备 + route 词法。
	if err := m.validatePage(); err != nil {
		return err
	}

	// 使用说明：tagline / examples / usage_guide——与 capability.ValidateApp 同口径的长度/条数上限。
	if err := m.validateUsage(); err != nil {
		return err
	}

	return nil
}

// validateUsage 校验使用说明三字段（与 capability.App 同一套上限，超出即拒绝）。
func (m *Manifest) validateUsage() error {
	m.Tagline = strings.TrimSpace(m.Tagline)
	if r := []rune(m.Tagline); len(r) > MaxManifestTaglineRunes {
		return fmt.Errorf("apppkg %q: tagline 超过 %d 字符（当前 %d）", m.ID, MaxManifestTaglineRunes, len(r))
	}
	if len(m.Examples) > MaxManifestExamples {
		return fmt.Errorf("apppkg %q: examples 最多 %d 条（当前 %d）", m.ID, MaxManifestExamples, len(m.Examples))
	}
	examples := make([]string, 0, len(m.Examples))
	for i, ex := range m.Examples {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		if r := []rune(ex); len(r) > MaxManifestExampleRunes {
			return fmt.Errorf("apppkg %q: examples[%d] 超过 %d 字符（当前 %d）", m.ID, i, MaxManifestExampleRunes, len(r))
		}
		examples = append(examples, ex)
	}
	if len(examples) == 0 {
		m.Examples = nil
	} else {
		m.Examples = examples
	}
	m.UsageGuide = strings.TrimSpace(m.UsageGuide)
	if r := []rune(m.UsageGuide); len(r) > MaxManifestUsageRunes {
		return fmt.Errorf("apppkg %q: usage_guide 超过 %d 字符（当前 %d）", m.ID, MaxManifestUsageRunes, len(r))
	}
	return nil
}

// validateManifestID 复用 `capability.ValidateApp` 做 id 词法判定——用一个只带 id 的最小 App 走
// 一遍它的校验，错误里带 "id" 即判定不通过。这样 id 口径与 `capability` 严格同源，不另写正则。
func validateManifestID(id string) error {
	if id == "" {
		return fmt.Errorf("apppkg: id 不能为空")
	}
	if len(id) > MaxManifestIDLen {
		return fmt.Errorf("apppkg %q: id 超过 %d 位", id, MaxManifestIDLen)
	}
	// 最小 App：id 必填、name 必填（给个占位）；其余字段留空/默认，不会被 ValidateApp 拒。
	probe := &capabilityAppProbe{ID: id, Name: "x"}
	if err := validateViaCapability(probe); err != nil {
		return fmt.Errorf("apppkg %q: id 非法（须小写字母/数字，用连字符分段，形如 know-your-foo，≤%d 位）: %w", id, MaxManifestIDLen, err)
	}
	return nil
}

// validateManifestAPIVersion 复用 `capability.ValidateApp` 的 api_version caret 判定：空 = 默认 ^1。
// 它会把默认值写回 *v。语法不合法 → 拒绝。
//
// 兼容性（caret 与 `capability.AppAPI` 主版本是否相容）在本函数里按同一函数返回的 major 判定，
// 不重写 caret 解析——caret 语法完全交给 capability。
func validateManifestAPIVersion(id string, v *string) error {
	*v = strings.TrimSpace(*v)
	if *v == "" {
		*v = "^1"
	}
	probe := &capabilityAppProbe{ID: id, Name: "x", APIVersion: *v}
	if err := validateViaCapability(probe); err != nil {
		return fmt.Errorf("apppkg %q: api_version %q 非法（须 caret 范围，形如 ^1 或 ^1.5）: %w", id, *v, err)
	}
	// 回收 capability 规范化后的值（空时它会补 ^1）。
	*v = probe.APIVersion
	// 兼容门：caret 的 major 必须与核心主版本 major 一致（与 capability 的运行期兼容判定同口径）。
	if !apiVersionCompatibleWithCore(*v) {
		return fmt.Errorf("apppkg %q: api_version %q 与核心 API（%s）不兼容（caret major 须为 ^%s）", id, *v, capabilityAppAPI, capabilityAppAPIMajor())
	}
	return nil
}

// validateEntrypoint 校验入口脚本路径：相对、非空、不含上跳/绝对路径、长度上限。
func validateEntrypoint(id, ep string) error {
	if ep == "" {
		return fmt.Errorf("apppkg %q: entrypoint 不能为空", id)
	}
	if len(ep) > MaxManifestEntrypoint {
		return fmt.Errorf("apppkg %q: entrypoint 超过 %d 字符", id, MaxManifestEntrypoint)
	}
	if strings.HasPrefix(ep, "/") || strings.HasPrefix(ep, "\\") {
		return fmt.Errorf("apppkg %q: entrypoint %q 不能是绝对路径（须相对包根）", id, ep)
	}
	// 归一化分隔符后逐段检查：不允许 ".."（防越出包根）。
	segs := strings.FieldsFunc(ep, func(r rune) bool { return r == '/' || r == '\\' })
	if len(segs) == 0 {
		return fmt.Errorf("apppkg %q: entrypoint %q 非法", id, ep)
	}
	for _, s := range segs {
		if s == ".." {
			return fmt.Errorf("apppkg %q: entrypoint %q 不能含上跳段 %q（须留在包根内）", id, ep, "..")
		}
	}
	return nil
}

func (m *Manifest) validatePermissions() error {
	if len(m.Permissions) > MaxManifestPermissions {
		return fmt.Errorf("apppkg %q: permissions 最多 %d 个（当前 %d）", m.ID, MaxManifestPermissions, len(m.Permissions))
	}
	seen := make(map[Permission]bool, len(m.Permissions))
	out := make([]Permission, 0, len(m.Permissions))
	for _, p := range m.Permissions {
		if !validPermissions[p] {
			return fmt.Errorf("apppkg %q: permission %q 不在白名单（合法：%s）", m.ID, p, permissionListStr())
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	m.Permissions = out
	return nil
}

func (m *Manifest) validateImpls() error {
	if len(m.Provides.Impls) > MaxManifestImpls {
		return fmt.Errorf("apppkg %q: provides.impls 最多 %d 个（当前 %d）", m.ID, MaxManifestImpls, len(m.Provides.Impls))
	}
	seen := make(map[string]bool, len(m.Provides.Impls))
	for i := range m.Provides.Impls {
		im := &m.Provides.Impls[i]
		im.ImplID = strings.TrimSpace(im.ImplID)
		im.Kind = strings.TrimSpace(im.Kind)
		if im.ImplID == "" || !manifestImplIDRE.MatchString(im.ImplID) {
			return fmt.Errorf("apppkg %q: provides.impls[%d].impl_id %q 非法（须小写字母开头，可含点/连字符分段）", m.ID, i, im.ImplID)
		}
		if im.Kind == "" || !manifestImplKindRE.MatchString(im.Kind) {
			return fmt.Errorf("apppkg %q: provides.impls[%d].kind %q 非法（须小写字母/数字/下划线，且非空）", m.ID, i, im.Kind)
		}
		if seen[im.ImplID] {
			return fmt.Errorf("apppkg %q: provides.impls 里 impl_id %q 重复", m.ID, im.ImplID)
		}
		seen[im.ImplID] = true
	}
	return nil
}

func (m *Manifest) validateTools() error {
	if len(m.Provides.Tools) > MaxManifestToolDomains {
		return fmt.Errorf("apppkg %q: provides.tools 最多 %d 个域（当前 %d）", m.ID, MaxManifestToolDomains, len(m.Provides.Tools))
	}
	seenDomain := make(map[string]bool, len(m.Provides.Tools))
	for i := range m.Provides.Tools {
		d := &m.Provides.Tools[i]
		d.Domain = strings.TrimSpace(d.Domain)
		if d.Domain == "" || !manifestToolDomainRE.MatchString(d.Domain) {
			return fmt.Errorf("apppkg %q: provides.tools[%d].domain %q 非法（须单段、小写字母开头、只含小写字母数字，不含下划线——前缀分派要求）", m.ID, i, d.Domain)
		}
		if seenDomain[d.Domain] {
			return fmt.Errorf("apppkg %q: provides.tools 里域 %q 重复", m.ID, d.Domain)
		}
		seenDomain[d.Domain] = true
		if len(d.Tools) > MaxManifestToolsPerDom {
			return fmt.Errorf("apppkg %q: provides.tools[%d]（域 %s）工具最多 %d 个（当前 %d）", m.ID, i, d.Domain, MaxManifestToolsPerDom, len(d.Tools))
		}
		seenTool := make(map[string]bool, len(d.Tools))
		for j, tn := range d.Tools {
			tn = strings.TrimSpace(tn)
			if tn == "" || !manifestToolNameRE.MatchString(tn) {
				return fmt.Errorf("apppkg %q: provides.tools[%d].tools[%d] 名称 %q 非法（须小写字母开头、只含小写字母数字/下划线）", m.ID, i, j, tn)
			}
			if seenTool[tn] {
				return fmt.Errorf("apppkg %q: 域 %s 里工具 %q 重复", m.ID, d.Domain, tn)
			}
			seenTool[tn] = true
			d.Tools[j] = tn
		}
	}
	return nil
}

func (m *Manifest) validatePage() error {
	if m.Provides.Page == nil {
		return nil
	}
	p := m.Provides.Page
	p.NavLabel = strings.TrimSpace(p.NavLabel)
	p.Route = strings.TrimSpace(p.Route)
	if p.NavLabel == "" || p.Route == "" {
		return fmt.Errorf("apppkg %q: provides.page 声明后 nav_label/route 都不能为空", m.ID)
	}
	if !manifestRouteRE.MatchString(p.Route) {
		return fmt.Errorf("apppkg %q: provides.page.route %q 非法（须以 / 开头，段内小写字母/数字/连字符）", m.ID, p.Route)
	}
	return nil
}

// permissionListStr 返回白名单的可读列表（错误信息用）。
func permissionListStr() string {
	parts := make([]string, len(AllPermissions))
	for i, p := range AllPermissions {
		parts[i] = string(p)
	}
	return strings.Join(parts, " / ")
}
