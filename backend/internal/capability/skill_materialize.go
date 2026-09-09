package capability

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// forgeMarker 标记目录由本平台物化（删除联动只动带标记目录，绝不动用户自有/他方内容）。
const forgeMarker = ".forge-managed"

// 已实证的物化 format（写入 <root>/<skillID>/SKILL.md；两种写法均保证 YAML frontmatter）：
//
// 2026-09-09 双底座实测（真实实现解析 forge 产物）：
//   - opencode v1.18.29（`opencode debug skill`，隔离 XDG_CONFIG_HOME）：无 frontmatter 的
//     SKILL.md **不加载**；有 name+description 的正常加载；`**/SKILL.md` 递归扫描；
//     name 与目录名不一致/含大写下划线仍加载（文档约束非强制）。
//   - dsh 0.1.2-rc.1（`dsh-skill-filesystem` 真实 provider）：无 frontmatter → 「missing YAML
//     frontmatter」丢弃；只有 name 或只有 description → 「frontmatter requires name and
//     description」丢弃；嵌套 `**/SKILL.md` 不扫描。
//
// 故两种 format 均合成/补全 frontmatter（name=skill id，description 退化 name→id）——
// 原样写入 body 的旧约定在两个底座都不会被加载，属实测证伪，不再保留。
const (
	// FormatSkillMD 目录包 `<id>/SKILL.md`（保证 YAML frontmatter）。
	FormatSkillMD = "SKILL.md"
	// FormatSkillMDFrontmatter 同上（显式写法，语义等价；便于定义文件自解释）。
	FormatSkillMDFrontmatter = "SKILL.md+frontmatter"
)

// renderSkillContent 按 format 生成物化内容；ok=false 表示该 source 不可物化（未实证 format）。
func renderSkillContent(s Skill, format string) (string, bool) {
	switch format {
	case FormatSkillMD, FormatSkillMDFrontmatter:
		return withFrontmatter(s), true
	default:
		return "", false
	}
}

// withFrontmatter 保证内容带 dsh 要求的 YAML frontmatter（name/description 必填）：
// body 已带 frontmatter 则只补缺失的必填键（用户内容保留），否则前置一段。
// description 取 skill.Description，空则退化为 name/id（dsh 缺 description 会丢弃整条）。
func withFrontmatter(s Skill) string {
	desc := s.Description
	if desc == "" {
		desc = s.Name
	}
	if desc == "" {
		desc = s.ID
	}
	name := s.ID
	body := s.Body
	if fm, rest, ok := splitFrontmatter(body); ok {
		if !hasFrontmatterKey(fm, "name") {
			fm += "name: " + yamlScalar(name) + "\n"
		}
		if !hasFrontmatterKey(fm, "description") {
			fm += "description: " + yamlScalar(desc) + "\n"
		}
		return "---\n" + fm + "---\n" + strings.TrimPrefix(rest, "\n")
	}
	return "---\nname: " + yamlScalar(name) + "\ndescription: " + yamlScalar(desc) + "\n---\n" + strings.TrimPrefix(body, "\n")
}

// splitFrontmatter 切出 YAML frontmatter 与其余正文（无 frontmatter 时 ok=false）。
func splitFrontmatter(body string) (fm, rest string, ok bool) {
	if !strings.HasPrefix(body, "---\n") {
		return "", "", false
	}
	idx := strings.Index(body[len("---\n"):], "\n---")
	if idx < 0 {
		return "", "", false
	}
	start := len("---\n")
	end := start + idx
	rest = body[end+len("\n---"):]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	return body[start:end] + "\n", rest, true
}

// hasFrontmatterKey 报告 frontmatter 是否已含某顶层键（行首 `key:`）。
func hasFrontmatterKey(fm, key string) bool {
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(strings.TrimRight(line, " \t"), key+":") {
			return true
		}
	}
	return false
}

// yamlScalar 输出安全的 YAML 标量（双引号包裹 + 转义；换行折为空格）。
func yamlScalar(v string) string {
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " ")
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// MaterializeSkill 把平台 skill 物化到目标根目录下该 runtime 的原生位置
// （design doc §5.4/§16.5：目录=平台事实源 → 物化=投影层同步 → 执行=runtime 原生）。
//
// 已实证约定（均写 `<root>/<skillID>/SKILL.md`，内容统一为「保证 YAML frontmatter」）：
//   - `format == "SKILL.md"` / `"SKILL.md+frontmatter"`：合成/补全 frontmatter（见上方实测）；
//   - 其它 format 未实证 → 跳过并告警，不臆造。
//
// root 由调用方决定（部署/运维策略；默认不写用户真实目录，见 §16.5 enable 决策）。
// 物化同时写 forge 归属标记（ReconcileSkills 的删除联动依据）。返回物化文件绝对路径；
// 多次调用幂等（覆盖写）。
func MaterializeSkill(root string, s Skill) (string, error) {
	if root == "" {
		return "", fmt.Errorf("skill 物化: root 为空")
	}
	if s.ID == "" {
		return "", fmt.Errorf("skill 物化: skill id 为空")
	}
	if s.Body == "" {
		return "", fmt.Errorf("skill 物化: skill %q body 为空（无事可写）", s.ID)
	}
	var content, format string
	var ok bool
	content, format, ok = materializedContent(s)
	if !ok {
		return "", fmt.Errorf("skill 物化: skill %q 无可物化 source（无已实证 format 源）", s.ID)
	}
	// 同一 skill 的多个 source 指向同一目标文件（<root>/<id>/SKILL.md）：取首个已实证 format，
	// 其余仅告警（避免歧义与「写多次/比一次」不一致）。
	for _, src := range s.Sources {
		if src.Format == format {
			continue
		}
		if _, renderable := renderSkillContent(s, src.Format); renderable {
			slog.Warn("skill 物化: 同一目标文件存在多种 format，采用首个已实证 format",
				"skill", s.ID, "used", format, "ignored", src.Format, "runtime", src.Runtime)
		}
	}
	dir := filepath.Join(root, s.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("skill 物化: 创建目录失败 %s: %w", dir, err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写 %s 失败: %w", p, err)
	}
	// 归属标记（删除联动依据）。
	marker := filepath.Join(root, s.ID, forgeMarker)
	if err := os.WriteFile(marker, []byte(s.ID+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写归属标记 %s 失败: %w", marker, err)
	}
	return p, nil
}

// skillUpToDate 报告 <root>/<id> 是否已按当前内容物化且带归属标记（重校幂等判定）。
func skillUpToDate(dir, content string) bool {
	if _, err := os.Stat(filepath.Join(dir, forgeMarker)); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	return err == nil && string(b) == content
}

// ReconcileSkills 使 <root> 与当前注册表一致（§16.5 删除联动 + 启动重校）：
//   - 为注册表里含 SKILL.md 源（已物化成功）的 skill 写缺失/更新（内容一致则跳过，幂等计数）；
//   - 删除 root 下「带 .forge-managed 标记且不在注册表」的目录（平台 skill 移除的联动清理）；
//   - 未带标记的目录（用户自有/他方内容）绝不动。
//
// 返回 (written, removed)。单条失败告警继续；root 空或不存在直接返回零值。
func ReconcileSkills(root string, current []Skill) (written, removed int, err error) {
	if root == "" {
		return 0, 0, nil
	}
	currentIDs := make(map[string]bool, len(current))
	for _, sk := range current {
		currentIDs[sk.ID] = true
	}
	for _, sk := range current {
		content, _, ok := materializedContent(sk)
		if !ok {
			continue
		}
		dir := filepath.Join(root, sk.ID)
		if skillUpToDate(dir, content) {
			continue
		}
		if _, mErr := MaterializeSkill(root, sk); mErr != nil {
			slog.Warn("skill 物化跳过", "skill", sk.ID, "err", mErr)
			continue
		}
		written++
	}
	entries, rErr := os.ReadDir(root)
	if rErr != nil {
		if os.IsNotExist(rErr) {
			return written, 0, nil
		}
		return written, 0, fmt.Errorf("skill 物化: 扫描 root 失败 %s: %w", root, rErr)
	}
	for _, e := range entries {
		if !e.IsDir() || currentIDs[e.Name()] {
			continue
		}
		marker := filepath.Join(root, e.Name(), forgeMarker)
		if _, mErr := os.Stat(marker); mErr != nil {
			continue // 无归属标记：用户自有/他方目录，不动
		}
		if dErr := os.RemoveAll(filepath.Join(root, e.Name())); dErr != nil {
			slog.Warn("skill 物化: 删除联动失败", "dir", e.Name(), "err", dErr)
			continue
		}
		removed++
	}
	return written, removed, nil
}

// materializedContent 返回该 skill 物化后的文件内容与所用 format（取首个已实证 format；
// 与 MaterializeSkill 的写入内容一致，用于重校幂等判定）。
func materializedContent(s Skill) (content, format string, ok bool) {
	for _, src := range s.Sources {
		if c, renderable := renderSkillContent(s, src.Format); renderable {
			return c, src.Format, true
		}
	}
	return "", "", false
}

// hasMaterializableSource 报告 skill 是否含可物化 source（已实证 format）。
func hasMaterializableSource(s Skill) bool {
	_, _, ok := materializedContent(s)
	return ok
}

// RemoveSkillDir 删除 root 下已物化的 skill 目录（幂等；目录不存在视为成功）。
func RemoveSkillDir(root, skillID string) error {
	if root == "" || skillID == "" {
		return nil
	}
	dir := filepath.Join(root, skillID)
	err := os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("skill 物化: 删除 %s 失败: %w", dir, err)
	}
	return nil
}
