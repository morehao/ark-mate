package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/morehao/golib/glog"
)

// Registry 是能力数据面的进程内聚合（Skill/MCP/Tool 三表），供 Service 查询与未来注入。
// 并发安全；List* 返回排序后的值拷贝（不暴露内部指针）。
type Registry struct {
	mu      sync.Mutex
	skills  map[string]*Skill
	mcps    map[string]*McpServer
	tools   map[string]*Tool
	experts map[string]*Expert
}

// LoadResult 汇报一次 LoadDir 的合并数量（各表 + 总计）。
type LoadResult struct {
	Skills int
	Mcps   int
	Tools  int
}

// Total 三类能力合计加载数。
func (r LoadResult) Total() int { return r.Skills + r.Mcps + r.Tools }

// 合法 id/name：字母数字开头，后接字母数字/_/./-，≤64（对齐 Agent id 约束风格）。
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// skillIDRE 是 skill id 的词法上限——**取自底座的硬契约，不是平台审美**。
//
// dsh 的 `@deepseek-ai/dsh-skill` 用 `SKILL_NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/` 校验 skill 文件
// frontmatter 里的 `name`，不合规只打一条 warn 就**静默丢弃整条**（dsh-skill-filesystem/lib/index.js）。
// 而平台物化时是把 id 原样写成 frontmatter 的 name（`MaterializedContent`），于是 id 不合规 =
// 平台自报 `in_sync`、底座永远不加载——是最难排查的一类"生效失败"。
//
// 底座之间宽容度不同（历史实测：某底座递归扫 `**/SKILL.md` 且只验 name 的存在性；dsh 不宽容），
// **取最严者（即交集）**便是这条判据。
// `nameRE` 仍是 MCP/tool 的规则（那些名字不喂给底座的 skill 校验，且历史上更宽）；
// 路径安全那道判据在 `manager.validDefinitionID`，与此处正交。
var skillIDRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{
		skills:  make(map[string]*Skill),
		mcps:    make(map[string]*McpServer),
		tools:   make(map[string]*Tool),
		experts: make(map[string]*Expert),
	}
}

// LoadDir 从目录加载能力定义（P1 数据面）：<dir>/skills|mcp|tools/*.json。
// 子目录缺失/文件坏/字段非法 → 跳过并告警（对齐 LoadAgentDefinitions 的容错语义）。
//
// `normalizeSkill`（可 nil）在**入表之前**改写每一条 skill 定义，供调用方做标识归一这类
// "本包不该知道"的翻译——典型用途：runtime 标识的历史别名（老定义里写着旧名字，而平台现在
// 拿新名字去比对，不归一那条技能就再也不投放）。**包边界同 `MaterializeSpec`**：本包不认识
// runtime，翻译由知道沿革的那一层传进来。
func (r *Registry) LoadDir(dir string, normalizeSkill func(*Skill)) LoadResult {
	if dir == "" {
		return LoadResult{}
	}
	return LoadResult{
		Skills: r.loadSkillsDir(filepath.Join(dir, "skills"), normalizeSkill),
		Mcps:   r.loadMcpsDir(filepath.Join(dir, "mcp")),
		Tools:  r.loadToolsDir(filepath.Join(dir, "tools")),
	}
}

// ValidateSkill 是 skill 定义的**唯一闸门**（纯校验，不碰注册表）。
//
// §13.2 S1：正文扫描，critical 直接拒（返回错误 → 装载路径告警跳过）。落点选在这一层而不是
// 物化层，是因为注册表是唯一入口——绕过它的路径（种子/测试/写面 CRUD）都不该存在；物化层只
// 负责把已经过闸的正文写到 runtime 目录。非 critical 告警不拒载，但记在条目上（`Skill.Warnings`）。
//
// 与 `RegisterSkill` 分开是 M6-3 的要求：写面必须**先校验、再提交、最后入表**（校验失败零写入；
// 提交失败不留"内存里有、盘上没有"的幻影）。合成一步就只能"先入表再回滚"，而回滚路径正是
// 最容易漏的那一条。校验是纯函数：同一份输入两次调用结果相同，所以"提交后入表必然成功"。
func ValidateSkill(s *Skill) error {
	if s == nil || s.ID == "" || !skillIDRE.MatchString(s.ID) || len(s.ID) > 64 {
		return fmt.Errorf("capability skill: id 非法（须小写字母/数字，用连字符分段，形如 my-skill，≤64 位；"+
			"底座 dsh 强制该词法，不合规会被静默丢弃）: %q", idOf(s))
	}
	// Scope：只认 global|workspace。未知取值报错而不是静默当 global——静默会把「我明明写了
	// workspace 想让它跟着项目走」变成「悄悄写进了全局目录」。
	if s.Scope != "" && !ValidSkillScopes[s.Scope] {
		return fmt.Errorf("capability skill %q: scope %q 非法（合法：global|workspace）", s.ID, s.Scope)
	}
	// Runtimes：去空白、去重，并拒绝重复声明（"我列了两次同一个 runtime id"通常意味着想表达别的）。
	seen := make(map[string]bool, len(s.Runtimes))
	cleaned := make([]string, 0, len(s.Runtimes))
	for _, r := range s.Runtimes {
		r = strings.TrimSpace(r)
		if r == "" {
			return fmt.Errorf("capability skill %q: runtimes 里有空项", s.ID)
		}
		if seen[r] {
			return fmt.Errorf("capability skill %q: runtimes 里 %q 重复", s.ID, r)
		}
		seen[r] = true
		cleaned = append(cleaned, r)
	}
	s.Runtimes = cleaned
	warnings := VerifyContent(s.Body)
	if HasCritical(warnings) {
		return criticalErr(s.ID, warnings)
	}
	s.Warnings = warnings
	return nil
}

// RegisterSkill 校验并注册/覆盖一条 skill（供平台种子、运行态或测试直接注入）。
// 写面请用 `ValidateSkill` + `RegisterSkill` 两步（见 ValidateSkill 的注释）。
func (r *Registry) RegisterSkill(s *Skill) error {
	if err := ValidateSkill(s); err != nil {
		return err
	}
	r.mu.Lock()
	r.skills[s.ID] = s
	r.mu.Unlock()
	return nil
}

// ValidateMcpServer 是 MCP server 定义的唯一闸门（纯校验，不碰注册表；理由同 ValidateSkill）。
func ValidateMcpServer(m *McpServer) error {
	if m == nil || m.ID == "" || !nameRE.MatchString(m.ID) {
		return fmt.Errorf("capability mcp: id 非法（须字母数字开头，≤64 位）")
	}
	if !ValidTransports[m.Transport] {
		return fmt.Errorf("capability mcp: transport %q 非法（stdio|sse|http|streamable_http）", m.Transport)
	}
	// stdio 注入命令须绝对路径：ACP 底座（如 dsh）按绝对路径拉起并严格校验，相对命令被拒
	// （session/new invalid_params）。注册/文件装载统一拦截，坏文件跳过告警。
	if m.Transport == TransportStdio && !filepath.IsAbs(m.Command) {
		return fmt.Errorf("capability mcp: stdio server %q 的 command 须为绝对路径（当前 %q）", m.ID, m.Command)
	}
	// §13.3 M1：明文与密钥分开校验。明文项不许空值（多半是写漏了），密钥项**只接受引用**
	// ——把明文密钥写进定义文件等于等它落盘，这正是这条规则存在的理由。
	if err := ValidatePlainBindings("env", m.Env); err != nil {
		return fmt.Errorf("%w（server %q）", err, m.ID)
	}
	if err := ValidatePlainBindings("headers", m.Headers); err != nil {
		return fmt.Errorf("%w（server %q）", err, m.ID)
	}
	if err := ValidateSecretBindings("secret_env", m.SecretEnv); err != nil {
		return fmt.Errorf("%w（server %q）", err, m.ID)
	}
	if err := ValidateSecretBindings("secret_headers", m.SecretHeaders); err != nil {
		return fmt.Errorf("%w（server %q）", err, m.ID)
	}
	warnCredentialShapedPlaintext(m)
	// 同名键同时出现在明文与密钥字典里 → 注入时谁赢没有好答案，拒绝（而不是悄悄定优先级）。
	for _, k := range KeysOf(m.SecretEnv) {
		if _, dup := m.Env[k]; dup {
			return fmt.Errorf("capability mcp: server %q 的 %q 同时在 env 与 secret_env 里（键不得重复）", m.ID, k)
		}
	}
	for _, k := range KeysOf(m.SecretHeaders) {
		if _, dup := m.Headers[k]; dup {
			return fmt.Errorf("capability mcp: server %q 的 %q 同时在 headers 与 secret_headers 里（键不得重复）", m.ID, k)
		}
	}
	return nil
}

// RegisterMcpServer 校验并注册/覆盖一个 MCP server。
func (r *Registry) RegisterMcpServer(m *McpServer) error {
	if err := ValidateMcpServer(m); err != nil {
		return err
	}
	r.mu.Lock()
	r.mcps[m.ID] = m
	r.mu.Unlock()
	return nil
}

// credentialishHeaderRE 明文 header 里"看起来像凭据"的键名（Authorization / X-Api-Key / *-Token …）。
var credentialishHeaderRE = regexp.MustCompile(`(?i)^(authorization|proxy-authorization|x-api-key|api-key|x-auth-token|x-token|.*[-_](token|secret|password|key))$`)

// warnCredentialShapedPlaintext 明文 header 命中"像凭据的键名"时提醒改放 secret_headers。
//
// **只提醒不拒绝**：这一层认不出"值是不是真凭据"（猜错就把正常配置挡在门外），
// 但键名是强信号——`Authorization` 里放明文 token 是最常见的走错门。自动识别密文形态
// （`sk-…`/`Bearer …`）属 §7 的"凭据卫生"，与出站 SSRF 那批一起做。
func warnCredentialShapedPlaintext(m *McpServer) {
	for _, k := range KeysOf(m.Headers) {
		if !credentialishHeaderRE.MatchString(k) {
			continue
		}
		if _, inSecret := m.SecretHeaders[k]; inSecret {
			continue
		}
		glog.Warnw(context.Background(), "capability mcp: 明文 header 的键名看起来像凭据，建议移到 secret_headers（值只接受 store:/env: 引用）",
			"server", m.ID, "header", k)
	}
}

// RegisterTool 注册/覆盖一个工具（平台 builtin / 观察兜底 / P2 MCP live 条目共用入口）。
func (r *Registry) RegisterTool(t *Tool) error {
	if t == nil || t.Name == "" || !nameRE.MatchString(t.Name) {
		return fmt.Errorf("capability tool: name 非法（须字母数字开头，≤64 位）: %q", nameOf(t))
	}
	if !ValidToolSources[t.Source] {
		return fmt.Errorf("capability tool: source %q 非法（builtin|mcp|observed）", t.Source)
	}
	r.mu.Lock()
	r.tools[t.Name] = t
	r.mu.Unlock()
	return nil
}

// SkillByID 按 id 取 skill（ok=false 表示未注册）。
func (r *Registry) SkillByID(id string) (Skill, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.skills[id]
	if !ok {
		return Skill{}, false
	}
	return *s, true
}

// SetSkillEnabled 就地改写某 skill 的启停位（返回改写后的条目；ok=false = 未注册）。
// 只动 Enabled 一个字段：正文/来源/告警原样保留——启停是 overlay，不是重新注册。
func (r *Registry) SetSkillEnabled(id string, enabled bool) (Skill, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.skills[id]
	if !ok {
		return Skill{}, false
	}
	v := enabled
	s.Enabled = &v
	return *s, true
}

// RemoveSkill 摘除一个 skill 条目（DELETE /skills/:id 与写盘回滚用）。ok=false = 本来就不在。
//
// 只摘内存条目：**文件由调用方删**（写面先落盘再改内存，回滚时顺序反过来）。
// 分开的理由是两条路径的失败语义不同——"文件删了但内存还在"是幻影能力，
// "内存删了但文件还在"会在下次装载时复活；调用方必须按自己的顺序显式处理。
func (r *Registry) RemoveSkill(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.skills[id]; !ok {
		return false
	}
	delete(r.skills, id)
	return true
}

// RemoveMcpServer 摘除一个 MCP server 条目（ok=false = 本来就不在）。
func (r *Registry) RemoveMcpServer(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.mcps[id]; !ok {
		return false
	}
	delete(r.mcps, id)
	return true
}

// RemoveToolsForMcpServer 摘除某 server 归属的 mcp 来源工具（返回摘除数）。
//
// 删定义时必须一起做：`/tools` 里留着已删 server 的工具，等于让用户看见一个调不通的能力
// （`AgentCapabilities` 的可见性过滤只看 transport 是否被候选 runtime 承载，看不出 server 已没了）。
func (r *Registry) RemoveToolsForMcpServer(serverID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for name, t := range r.tools {
		if t.McpServer == serverID && t.Source == ToolSourceMCP {
			delete(r.tools, name)
			n++
		}
	}
	return n
}

// ReplaceAll 原子替换整个目录：`from` 的内容整体换入，**一次临界区**完成。
//
// 为什么需要它（M7-5 并发验收抓到的真实窗口）：重新装载定义原本走"`Reset()` 再逐条注册"，
// 于是共享注册表上存在一个**可观测的中间态**——清空之后、注册完之前，读者看到的是空面或半装的面。
// 网关（M7）**每次** tools/list / tools/call 前都重装定义，并发调用下这个窗口必然被撞上，症状是
// "工具面突然少了几个/整条不见了"，而定义其实一个字都没改——这类"偶发、不可复现、看起来像远端
// 抖动"的故障最难查。替换之后，读者要么看到旧面、要么看到新面，没有第三种。
//
// `from` 之后应被丢弃（这里复制而不是共享底层 map，免得两边互相看见后续注册）。
func (r *Registry) ReplaceAll(from *Registry) {
	from.mu.Lock()
	skills := make(map[string]*Skill, len(from.skills))
	for k, v := range from.skills {
		skills[k] = v
	}
	mcps := make(map[string]*McpServer, len(from.mcps))
	for k, v := range from.mcps {
		mcps[k] = v
	}
	tools := make(map[string]*Tool, len(from.tools))
	for k, v := range from.tools {
		tools[k] = v
	}
	experts := make(map[string]*Expert, len(from.experts))
	for k, v := range from.experts {
		experts[k] = v
	}
	from.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills, r.mcps, r.tools, r.experts = skills, mcps, tools, experts
}

// Reset 清空三张表。
//
// **不要**再拿它当"重新装载定义"的起手式（`Reset()` + 逐条注册 = 在共享面上留一个空的/半装的
// 可观测中间态，M7-5 的并发用例实测撞上过）：重新装载请用 `ReplaceAll`。
// 现在它只用于"确实要清空"的场合（构造期与非装载路径）。
//
// 为什么需要它：`RegisterSkill`/`RegisterMcpServer`/`RegisterTool` 都是**按 id 覆盖**，
// 谁都不摘除"这次装载里没出现"的旧条目。装载源从目录换成 store 之后，"库里删了一行"
// 必须让那条能力从内存里**消失**，否则它会以幽灵定义的形式继续出现在列表里、继续被注入
// ——这正是设计文档 §3.5-⑤ 记录的缺陷（`RemoveSkill` 的注释里也写着"内存删了但文件还在
// 会复活"，那是另一个方向的同一件事）。
//
// 与 `RemoveXxx` 的分工：那几个是**单条**摘除（写面自己知道删了哪一条，且顺序有意义），
// Reset 是装载前的**清场**，只该由装载路径调用。
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills = make(map[string]*Skill)
	r.mcps = make(map[string]*McpServer)
	r.tools = make(map[string]*Tool)
	r.experts = make(map[string]*Expert)
}

// SupportedFormat 报告某物化 format 是否已实证（激活判定用：声明了 runtime 但 format 未实证
// = 该 runtime 物化不出来，要给用户一个人话原因，而不是让技能静默消失）。
func SupportedFormat(format string) bool {
	switch format {
	case FormatSkillMD, FormatSkillMDFrontmatter:
		return true
	default:
		return false
	}
}

// ListSkills 返回按 ID 排序的 skill 副本（含被禁用的：启停视图要能看见并恢复它们）。
func (r *Registry) ListSkills() []Skill {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListMcpServers 返回按 ID 排序的 MCP server 副本。
func (r *Registry) ListMcpServers() []McpServer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]McpServer, 0, len(r.mcps))
	for _, m := range r.mcps {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListTools 返回按 name 排序的工具副本。
func (r *Registry) ListTools() []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetMcpServer 按 id 取 MCP server（ok=false 表示未注册）。
func (r *Registry) GetMcpServer(id string) (McpServer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.mcps[id]
	if !ok {
		return McpServer{}, false
	}
	return *m, true
}

// RegisterExpert 校验并注册/覆盖一条专家定义（供装载与写面在**校验通过后**入表）。
//
// 与 RegisterSkill 同一条纪律：入表是写面序列的最后一步（先校验、再提交载体、最后入表），
// 这里再跑一次 `ValidateExpert` 作为最后一道防线——绕过注册表的路径不该存在。
func (r *Registry) RegisterExpert(e *Expert) error {
	if err := ValidateExpert(e); err != nil {
		return err
	}
	r.mu.Lock()
	r.experts[e.ID] = e
	r.mu.Unlock()
	return nil
}

// ExpertByID 按 id 取专家（ok=false 表示未注册）。
func (r *Registry) ExpertByID(id string) (Expert, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.experts[id]
	if !ok {
		return Expert{}, false
	}
	return *e, true
}

// RemoveExpert 摘除一条专家（装载清场/删除路径用；ok=false = 本来就不存在）。
func (r *Registry) RemoveExpert(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.experts[id]; !ok {
		return false
	}
	delete(r.experts, id)
	return true
}

// ListExperts 返回按 ID 排序的专家副本（含被禁用的：面板要能看见并恢复它们）。
func (r *Registry) ListExperts() []Expert {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Expert, 0, len(r.experts))
	for _, e := range r.experts {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ReplaceToolsForMcpServer 用一次 live 探测结果整体替换某 MCP server 归属的 Tool 条目
// （P2 §5.2：tools/list 是权威快照——同 server 上一轮的条目全删，再写入本轮；不动 builtin/
// observed/其它 server 的条目）。非法 name 跳过并计入 skipped。返回 (added, removed, skipped)。
func (r *Registry) ReplaceToolsForMcpServer(serverID string, tools []Tool) (int, int, int) {
	added, removed, skipped := 0, 0, 0
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.tools {
		if t.McpServer == serverID && t.Source == ToolSourceMCP {
			delete(r.tools, name)
			removed++
		}
	}
	for i := range tools {
		t := tools[i]
		if t.Name == "" || !nameRE.MatchString(t.Name) {
			skipped++
			continue
		}
		t.Source = ToolSourceMCP
		t.McpServer = serverID
		cp := t
		r.tools[cp.Name] = &cp
		added++
	}
	return added, removed, skipped
}

// idOf/nameOf nil 安全的错误回显。
func idOf(s *Skill) string {
	if s == nil {
		return ""
	}
	return s.ID
}

func nameOf(t *Tool) string {
	if t == nil {
		return ""
	}
	return t.Name
}

func (r *Registry) loadSkillsDir(dir string, normalizeSkill func(*Skill)) int {
	return scanDir(dir, "skill", func(b []byte, key, path string) error {
		var d Skill
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		if d.ID == "" {
			d.ID = key
		}
		d.SourceFile = path // 事实源文件（Library 的 origin/source_file 依据）
		if normalizeSkill != nil {
			normalizeSkill(&d)
		}
		if err := r.RegisterSkill(&d); err != nil {
			return err
		}
		// 旧键 `sources` 已被忽略（M1 起条目侧只声明 runtimes），而 encoding/json 丢未知键
		// 是**静默**的：后果是技能从「只给某一个 runtime」悄悄变成「给所有 runtime」——投放面被放宽，
		// 用户看不到任何迹象。所以装载时显式挂一条告警，把人引到 `mate capability migrate`。
		if hasLegacySourcesKey(b) {
			r.appendSkillWarning(d.ID, Warning{
				Severity: SeverityInfo,
				Pattern:  "legacy_sources_key",
				Message: "该定义仍含已退役的 `sources` 字段（已被忽略）：技能会投放到**所有**支持 skill 投影的 runtime，" +
					"而不是原来声明的那些。运行 `mate capability migrate` 把它转成 runtimes（值 = 原 sources 的 runtime 去重升序）",
			})
		}
		return nil
	})
}

// hasLegacySourcesKey 探测定义原文里是否还有 `sources` 键（只为给出一条告警，不参与解析）。
func hasLegacySourcesKey(b []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	_, ok := probe["sources"]
	return ok
}

// appendSkillWarning 给已注册的 skill 追加一条告警（派生信息，不改定义文件）。
func (r *Registry) appendSkillWarning(id string, w Warning) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.skills[id]; ok {
		s.Warnings = append(s.Warnings, w)
	}
}

func (r *Registry) loadMcpsDir(dir string) int {
	return scanDir(dir, "mcp", func(b []byte, key, path string) error {
		var d McpServer
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		if d.ID == "" {
			d.ID = key
		}
		d.SourceFile = path
		return r.RegisterMcpServer(&d)
	})
}

func (r *Registry) loadToolsDir(dir string) int {
	return scanDir(dir, "tool", func(b []byte, key, path string) error {
		var d Tool
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		if d.Name == "" {
			d.Name = key
		}
		d.SourceFile = path
		return r.RegisterTool(&d)
	})
}

// scanDir 扫描 dir 下 *.json，逐文件解码后交给 apply（键回退 = 文件名去 .json）。
func scanDir(dir, kind string, apply func(b []byte, key, path string) error) int {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		glog.Warnw(context.Background(), "capability: 扫描目录失败", "kind", kind, "dir", dir, "err", err)
		return 0
	}
	loaded := 0
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			glog.Warnw(context.Background(), "capability: 读取定义失败", "file", p, "err", err)
			continue
		}
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if err := apply(b, base, p); err != nil {
			glog.Warnw(context.Background(), "capability: 定义未采纳", "kind", kind, "file", p, "err", err)
			continue
		}
		loaded++
	}
	if loaded > 0 {
		glog.Infow(context.Background(), "capability: 目录已加载", "kind", kind, "count", loaded, "dir", dir)
	}
	return loaded
}
