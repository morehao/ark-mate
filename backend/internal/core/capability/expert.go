package capability

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ——— 专家定义（会话级能力预设；P0 定义面）———
//
// 专家是**一段命名的会话级预设**：推荐哪些技能、哪些 MCP、用哪个模型，P1 起可携带一段
// 角色设定（`instructions`，首轮以隐藏 user-context 注入）。它**不拥有**技能/MCP，只是引用
// id——技能与 MCP 的定义仍只有一份。详见设计
// `.dsh/docs/specs/2026-10-29-session-capability-selection-design.md`。
//
// 它沿用 skill/mcp 的定义形态：body 是原始 JSON（未知键原样保留）、revision 走 CAS、变更落
// `definition_revisions` 审计。字段与磁盘 JSON 一一对应。

// Expert 是专家定义的一个条目（平台唯一事实源）。
type Expert struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Avatar 内置图标键（spark|chart|code|pen|search|briefcase）；不接受 URL（本机无远程图片依赖）。
	Avatar string `json:"avatar,omitempty"`
	// Model 推荐模型（平台模型行 id `mdl_…`；空 = 不推荐）。行被删时按「推荐不可达」跳过并告警。
	Model string `json:"model,omitempty"`
	// Skills / Mcps 推荐的能力 id（去重升序；允许暂时指向不存在的定义，写面会给 warnings）。
	Skills []string `json:"skills,omitempty"`
	Mcps   []string `json:"mcps,omitempty"`
	// Enabled 是否可用于新会话；nil/未声明 = 启用。禁用不影响历史会话（会话在创建时已物化）。
	Enabled *bool `json:"enabled,omitempty"`
	// Tags 搜索/分类标签（≤8 个，每个 ≤20 字符）。
	Tags []string `json:"tags,omitempty"`
	// Instructions 角色设定（P1；≤4096 bytes）。非空时首轮以隐藏 user-context 注入一次。
	// 纯文本，不渲染 HTML；保存期拒绝危险标记（见 ValidateExpert）。
	Instructions string `json:"instructions,omitempty"`
	// SourceFile 派生字段（DB 化之后恒空；保留与 Skill.SourceFile 同形，便于 Library 判血缘）。
	SourceFile string `json:"-"`
}

// IsEnabled 报告专家当前是否可用于新会话（未声明 = 启用）。
func (e Expert) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }

// 专家头像的合法内置图标键。**不接受 URL**：本机无远程图片依赖，也避免 CSP 例外。
const (
	ExpertAvatarSpark     = "spark"
	ExpertAvatarChart     = "chart"
	ExpertAvatarCode      = "code"
	ExpertAvatarPen       = "pen"
	ExpertAvatarSearch    = "search"
	ExpertAvatarBriefcase = "briefcase"
)

// ValidExpertAvatars 合法头像键集合。
var ValidExpertAvatars = map[string]bool{
	ExpertAvatarSpark: true, ExpertAvatarChart: true, ExpertAvatarCode: true,
	ExpertAvatarPen: true, ExpertAvatarSearch: true, ExpertAvatarBriefcase: true,
}

// 专家定义的硬上限（设计 §接口约束）。
const (
	MaxExpertNameRunes         = 40
	MaxExpertDescriptionRunes  = 120
	MaxExpertTags              = 8
	MaxExpertTagRunes          = 20
	MaxExpertRefs              = 50
	MaxExpertInstructionsBytes = 4096
)

// expertDangerousMarkers 是角色设定里**必须拒绝**的危险标记（提示词注入/HTML 注释逃逸）。
var expertDangerousMarkers = []string{"</system-reminder>", "<!--", "-->", "{{", "}}", "\x00"}

// ValidateExpert 是专家定义的**唯一闸门**（纯函数；写面与装载共用）。
//
// 它会**规范化** skills/mcps（去空白、去重、升序），因此调用方应以本函数返回后的值为准落库。
// 拒绝即返回错误，上层翻成 `definition_invalid`(40001)。
func ValidateExpert(e *Expert) error {
	if e == nil {
		return fmt.Errorf("capability expert: 定义为空")
	}
	if e.ID == "" || !skillIDRE.MatchString(e.ID) || len(e.ID) > 64 {
		return fmt.Errorf("capability expert: id 非法（须小写字母/数字，用连字符分段，形如 data-analyst，≤64 位）: %q", e.ID)
	}
	name := strings.TrimSpace(e.Name)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxExpertNameRunes {
		return fmt.Errorf("capability expert %q: name 须为 1–%d 字符（当前 %d）", e.ID, MaxExpertNameRunes, n)
	}
	e.Name = name
	if n := utf8.RuneCountInString(e.Description); n > MaxExpertDescriptionRunes {
		return fmt.Errorf("capability expert %q: description 超过 %d 字符（当前 %d）", e.ID, MaxExpertDescriptionRunes, n)
	}
	if e.Avatar != "" && !ValidExpertAvatars[e.Avatar] {
		return fmt.Errorf("capability expert %q: avatar %q 非法（合法：spark|chart|code|pen|search|briefcase）", e.ID, e.Avatar)
	}
	if e.Model != "" && !strings.HasPrefix(e.Model, "mdl_") {
		return fmt.Errorf("capability expert %q: model %q 非法（须为平台模型行 id，形如 mdl_…）", e.ID, e.Model)
	}
	skills, err := normalizeExpertRefs("skills", e.ID, e.Skills)
	if err != nil {
		return err
	}
	e.Skills = skills
	mcps, err := normalizeExpertRefs("mcps", e.ID, e.Mcps)
	if err != nil {
		return err
	}
	e.Mcps = mcps
	if len(e.Tags) > MaxExpertTags {
		return fmt.Errorf("capability expert %q: tags 最多 %d 个（当前 %d）", e.ID, MaxExpertTags, len(e.Tags))
	}
	for _, t := range e.Tags {
		if utf8.RuneCountInString(strings.TrimSpace(t)) > MaxExpertTagRunes {
			return fmt.Errorf("capability expert %q: 标签 %q 超过 %d 字符", e.ID, t, MaxExpertTagRunes)
		}
	}
	if len(e.Instructions) > MaxExpertInstructionsBytes {
		return fmt.Errorf("capability expert %q: instructions 超过 %d 字节（当前 %d）", e.ID, MaxExpertInstructionsBytes, len(e.Instructions))
	}
	for _, m := range expertDangerousMarkers {
		if strings.Contains(e.Instructions, m) {
			return fmt.Errorf("capability expert %q: instructions 含危险标记 %q（拒绝保存：它会被当作首轮隐藏上下文注入）", e.ID, m)
		}
	}
	return nil
}

// normalizeExpertRefs 去空白、去重、升序；空项直接忽略，重复项静默去重（与"专家只填空缺"同向：
// 推荐集合是便利，不是用户手写的白名单，不必为空格/重复报错）。
func normalizeExpertRefs(kind, id string, in []string) ([]string, error) {
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
	if len(out) > MaxExpertRefs {
		return nil, fmt.Errorf("capability expert %q: %s 最多 %d 个（当前 %d）", id, kind, MaxExpertRefs, len(out))
	}
	sort.Strings(out)
	return out, nil
}
