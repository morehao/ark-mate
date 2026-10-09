package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ——— 投影契约（§投影契约）———
//
// 结论：**投影是 runtime 的属性，不是能力的属性**。
//
// 职责划分：投影形态（Root/Layout/Format）只在这里声明；条目侧只表达「给哪些 runtime 用」
// （`capability.Skill.Runtimes`），不表达位置与格式。因此不存在「两处声明同一件事」的优先级
// 问题——这也是删掉 `capability.SkillSource` 的原因。
//
// 类型与解析都放 `pkg/runtime`（P-1 决定）：`pkg/runtime` 零依赖 `pkg/capability`，
// 物化函数收平凡参数 (root, layout, format)，编排留给 internal/manager。

// ProjectionKind 可投影的能力种类。
type ProjectionKind string

const (
	// ProjectionSkill 是 skill 投影：平台把 `Skill` 的正文合成 SKILL.md 写到 runtime 原生目录。
	// ACP 不承载 skill（设计文档 ADR-3），故这条链完全在平台侧：物化 = 投影同步，执行 = runtime 原生。
	ProjectionSkill ProjectionKind = "skill"
	// ProjectionCommand 是斜杠命令投影（M5 起点）。**当前没有任何 adapter 声明它**——
	// opencode / dsh 的命令目录约定尚未实测（假设 B），在结论落地前不落代码、不落配置。
	ProjectionCommand ProjectionKind = "command"
)

// ValidProjectionKinds 合法 kind 集合（配置解析与守卫共用；未知 kind 报错不静默）。
var ValidProjectionKinds = map[ProjectionKind]bool{
	ProjectionSkill:   true,
	ProjectionCommand: true,
}

// 作用域取值（`Projection.Scope`）。
const (
	// ScopeGlobal 写到 runtime 的全局目录（与 cwd 无关，所有会话共享）。
	ScopeGlobal = "global"
	// ScopeWorkspace 写到会话 cwd 下的 runtime 原生目录（M1 只声明不消费，见下）。
	ScopeWorkspace = "workspace"
)

// ValidProjectionScopes 合法 scope 集合。
var ValidProjectionScopes = map[string]bool{ScopeGlobal: true, ScopeWorkspace: true}

// ——— 能力绑定范围（`Spec.CapabilityScope`）———
//
// 问题：runtime 的能力面（MCP 集、skill 目录扫描）在哪个粒度被确定？决定一个「代际」要独占
// 一个进程，还是能与别的代际共享。
//
// 三条纪律（P-2）：① 未实测一律声明 process；② **不对称取证**——只有「同进程同 cwd、新会话
// 换能力集生效」才能声明 session，只有「同进程同 cwd 不生效、换 cwd 生效」才能声明 directory，
// 禁止「看起来像」就声明；③ 绑定范围可能随底座版本变 → 记 TestedOnVersion，版本一变回落 process。
const (
	// CapabilityScopeProcess 能力面在**进程启动时**确定：换能力集必须换进程（默认，最粗）。
	CapabilityScopeProcess = "process"
	// CapabilityScopeDirectory 能力面按 **cwd** 确定：同进程换 cwd 可生效，同 cwd 内换会话不行。
	CapabilityScopeDirectory = "directory"
	// CapabilityScopeSession 能力面按**会话**确定：同进程同 cwd 下新会话即可用新能力集。
	CapabilityScopeSession = "session"
)

// ValidCapabilityScopes 合法能力绑定范围集合。
var ValidCapabilityScopes = map[string]bool{
	CapabilityScopeProcess:   true,
	CapabilityScopeDirectory: true,
	CapabilityScopeSession:   true,
}

// EffectiveCapabilityScope 返回**可依赖的**绑定范围：仅在实测通过（`Tested`）时返回声明值，
// 否则一律回落 process。
//
// 这是纪律③的落点：底座升级后忘了重测，声明值还可能留在 session/directory，但 `Tested`
// 会被人工置回 false（或版本号对不上）——此时必须按最粗粒度保守处理，不能拿旧结论省内存。
func (s Spec) EffectiveCapabilityScope() string {
	if !s.Tested || !ValidCapabilityScopes[s.CapabilityScope] {
		return CapabilityScopeProcess
	}
	return s.CapabilityScope
}

// LayoutIDToken 是落点模板里的 skill id 占位符（如 `<id>/SKILL.md`）。
const LayoutIDToken = "<id>"

// Projection 声明某 runtime 承载某类能力的原生形态。
type Projection struct {
	Kind ProjectionKind
	// Scope global | workspace。空 = global。
	// 注意：M1 只把 workspace 表达出来（数据面语义），**消费面（会话 cwd 下的投影）留到 M3**，
	// 因为它的落点取决于假设 B（底座原生 `.opencode/` 还是平台自有 `.ark-mate/`），尚未实测。
	Scope string
	// Root 默认根；支持 `${HOME}` / `${XDG_CONFIG_HOME}` 占位（此处**不展开**，展开归 Resolve）。
	// 空 = 该 kind 不投影。
	Root string
	// Layout 相对根的落点模板，必须含 `<id>`（如 `<id>/SKILL.md`）。
	// dsh 只扫根下**一层**（`<root>/<name>/SKILL.md` 与 `<root>/<name>.md`），故必须是扁平模板。
	Layout string
	// Format 内容合成格式，取值域同 capability.Format*（此处只存字符串：runtime 不依赖 capability）。
	Format string
	// Tested 是否经真实底座**端到端**实测过（未实测 → 投影状态报 untested，不臆造）。
	Tested bool
	// Note 实测结论：测了什么、在哪个版本上、还有什么没测。
	Note string
}

// ProjectionFor 取该 Spec 声明的某 kind 投影（ok=false = 未声明）。
func (s Spec) ProjectionFor(kind ProjectionKind) (Projection, bool) {
	for _, p := range s.Projections {
		if p.Kind == kind {
			return p, true
		}
	}
	return Projection{}, false
}

// UnverifiedReason 未实测时给用户看的一句话（Note 为空时兜底，保证界面不出现空原因）。
func (p Projection) UnverifiedReason() string {
	if p.Note != "" {
		return p.Note
	}
	return "该 runtime 的投影声明未附实测记录"
}

// ProjectionRootEnv 是 Resolve 展开占位符时的查环境函数（测试注入用；nil = os.LookupEnv）。
type ProjectionRootEnv func(name string) (string, bool)

// Resolve 解析某 runtime 在某 kind 上的**有效投影**（唯一解析链）。
//
// 解析链（按优先级）：
//  1. `overrides["<runtime>:<kind>"]`（部署显式覆盖 `MATE_PROJECTION_DIRS`，最高）；
//  2. `spec.Projections` 里该 kind 声明的 `Root`（adapter 默认根）；
//  3. 都没有 → 返回 ok=false（该 (runtime, kind) 投影**关闭**，调用方报 not_configured）。
//
// 覆盖只替换 **Root**，Layout/Format 仍取自声明——因此「只有覆盖、没有声明」不算投影成立
// （不知道该写什么形态），同样返回 ok=false。远程端点 runtime 就属于这一类。
//
// 命中后一律展开占位符并**校验**，任一项不合法即返回 error，不静默退回「看起来能用」的值：
// 未设置的 `${VAR}`、相对 root、空 root、含 `..` 或绝对路径的 Layout、缺 `<id>` 的 Layout、
// 空 Format、非法 Scope。
//
// 返回的 Projection 是**已展开**的完整形态：Root 为绝对路径，可直接与 Layout 的展开结果拼接。
//
// 计划里此函数的签名是 `(Spec, ProjectionKind) (Projection, bool)`，实际多了一个 error——
// 验收项「相对路径/空值**报错不静默**」用两值签名无法表达（要么 panic、要么吞掉）。
func Resolve(spec Spec, kind ProjectionKind, overrides map[string]string, env ProjectionRootEnv) (Projection, bool, error) {
	declared, hasDecl := spec.ProjectionFor(kind)
	key := string(spec.ID) + ":" + string(kind)
	override, hasOverride := overrides[key]
	if !hasDecl {
		// 未声明 = 该 runtime 不承载这类能力。覆盖不能凭空造出 Layout/Format。
		return Projection{}, false, nil
	}
	p := declared
	p.Kind = kind
	if p.Scope == "" {
		p.Scope = ScopeGlobal
	}
	if !ValidProjectionScopes[p.Scope] {
		return Projection{}, false, fmt.Errorf("runtime %s 的 %s 投影声明了非法 scope %q（合法：global|workspace）",
			spec.ID, kind, p.Scope)
	}
	root := p.Root
	if hasOverride {
		root = override
	}
	if strings.TrimSpace(root) == "" {
		// 声明的 Root 为空 = 该 kind 不投影（声明式关闭，不算错误）；
		// 但**显式覆盖成空**是配置错误（用户以为改了、其实关掉了），报错不静默。
		if hasOverride {
			return Projection{}, false, fmt.Errorf(
				"MATE_PROJECTION_DIRS 里 %s 的值为空：要么给一个绝对目录，要么删掉这一项（空值不等于「不投影」）", key)
		}
		return Projection{}, false, nil
	}
	expanded, err := ExpandProjectionRoot(root, env)
	if err != nil {
		return Projection{}, false, fmt.Errorf("runtime %s 的 %s 投影根 %q 无法解析：%w", spec.ID, kind, root, err)
	}
	if err := ValidateProjectionLayout(p.Layout); err != nil {
		return Projection{}, false, fmt.Errorf("runtime %s 的 %s 投影落点 %q 不合法：%w", spec.ID, kind, p.Layout, err)
	}
	if strings.TrimSpace(p.Format) == "" {
		return Projection{}, false, fmt.Errorf("runtime %s 的 %s 投影未声明 format（不知道该合成什么内容）", spec.ID, kind)
	}
	p.Root = expanded
	return p, true, nil
}

// ExpandProjectionRoot 展开投影根里的 `${HOME}` / `${XDG_CONFIG_HOME}` 占位符。
//
// 纪律：**变量未设置就报错**，不退回空串也不退回「家目录」猜值——静默猜出来的根会写到
// 用户没预期的目录，而这恰恰是「投影写错地方」最难排查的形态。$HOME 都没有的进程本就
// 不该开物化；XDG_CONFIG_HOME 没设的机器请在 `MATE_PROJECTION_DIRS` 里给绝对路径。
//
// 只认 `${...}` 形态：`$HOME` 这样不带花括号的写法一律当普通字符（避免与路径里的字面 `$` 混淆），
// 未闭合的 `${` 也报错。
func ExpandProjectionRoot(root string, env ProjectionRootEnv) (string, error) {
	if env == nil {
		env = os.LookupEnv
	}
	var b strings.Builder
	rest := root
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		tail := rest[i+len("${"):]
		j := strings.Index(tail, "}")
		if j < 0 {
			return "", fmt.Errorf("占位符未闭合（缺 `}`）：%q", rest[i:])
		}
		name := tail[:j]
		if name == "" {
			return "", fmt.Errorf("占位符名为空：%q", rest[i:i+len("${}")])
		}
		val, ok := env(name)
		if !ok || strings.TrimSpace(val) == "" {
			return "", fmt.Errorf("环境变量 %s 未设置（占位符只支持 ${HOME} / ${XDG_CONFIG_HOME}；"+
				"请在 MATE_PROJECTION_DIRS 里直接给绝对路径）", name)
		}
		b.WriteString(val)
		rest = tail[j+1:]
	}
	out := b.String()
	if !filepath.IsAbs(out) {
		return "", fmt.Errorf("展开后不是绝对路径：%q", out)
	}
	return filepath.Clean(out), nil
}

// ValidateProjectionLayout 校验落点模板：相对、非空、不含 `..`、必须含 `<id>`，
// 且 **`<id>` 必须落在目录段上**（即落点至少两层，父目录里含 `<id>`）。
//
// 为什么强制含 `<id>`：不含的话所有 skill 会写到**同一个文件**，后写的静默盖掉先写的——
// 这是「一条配置看起来生效了、其实只剩一个技能」的错误形态，必须在声明期就拦住。
//
// 为什么要求独占目录：`.mate-managed` 归属标记与摘要 sidecar 都要有地方落，而它们只能落在
// 平台可控的**目录**里。`<id>.md` 这种平铺模板看似更贴合某些底座（dsh 也扫 `<name>.md`），
// 但它让"这个文件是不是平台写的"无从判定，删除联动也就只能靠猜——所以不收。
func ValidateProjectionLayout(layout string) error {
	if strings.TrimSpace(layout) == "" {
		return fmt.Errorf("落点为空")
	}
	if filepath.IsAbs(layout) {
		return fmt.Errorf("落点必须是相对根的路径，不能是绝对路径")
	}
	if !strings.Contains(layout, LayoutIDToken) {
		return fmt.Errorf("落点必须含 %s 占位（否则所有技能会写到同一个文件）", LayoutIDToken)
	}
	for _, seg := range strings.Split(filepath.ToSlash(layout), "/") {
		if seg == ".." {
			return fmt.Errorf("落点不得含 `..`（会逃出投影根）")
		}
	}
	// 父目录必须含 <id>：它保证每个能力独占一个目录（归属标记与摘要 sidecar 的落点）。
	if dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(layout))); dir == "." || !strings.Contains(dir, LayoutIDToken) {
		return fmt.Errorf("落点必须在含 %s 的**目录**里（如 %s/SKILL.md）：归属标记与摘要 sidecar 需要落在这个目录，平铺成单个文件时它们无处可放",
			LayoutIDToken, LayoutIDToken)
	}
	return nil
}

// ApplyProjectionLayout 把落点模板实例化为相对路径（`<id>` → skill id）。
// 调用方须先用 ValidateProjectionLayout 校验（本函数只在 id 为空时报错）。
func ApplyProjectionLayout(layout, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("能力 id 为空，无法套用落点模板 %q", layout)
	}
	// id 直接当路径片段用：含分隔符的 id 会改变落点层级 → 拒绝（id 来自定义文件名/请求体，
	// 不校验的话 `a/b` 这类 id 能把产物写到投影根之外的地方）。
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("能力 id %q 含路径分隔符或为相对段，不能作为落点片段", id)
	}
	if err := ValidateProjectionLayout(layout); err != nil {
		return "", err
	}
	return filepath.FromSlash(strings.ReplaceAll(layout, LayoutIDToken, id)), nil
}

// ProjectionPath 拼出某能力投影后的**绝对文件路径**（root 已展开 + 落点已实例化）。
func ProjectionPath(p Projection, id string) (string, error) {
	rel, err := ApplyProjectionLayout(p.Layout, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Root, rel), nil
}

// ProjectionDir 拼出某能力投影后所在的**目录**（落点的父目录；`.mate-managed` 标记与
// 摘要 sidecar 都落在这里）。
func ProjectionDir(p Projection, id string) (string, error) {
	full, err := ProjectionPath(p, id)
	if err != nil {
		return "", err
	}
	return filepath.Dir(full), nil
}
