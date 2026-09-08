package capability

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// forgeMarker 标记目录由本平台物化（删除联动只动带标记目录，绝不动用户自有/他方内容）。
const forgeMarker = ".forge-managed"

// MaterializeSkill 把平台 skill 物化到目标根目录下该 runtime 的原生位置
// （design doc §5.4/§16.5：目录=平台事实源 → 物化=投影层同步 → 执行=runtime 原生）。
//
// 实测约定（2026-09-08，opencode v1.18.29）：opencode 自动扫描
// `~/.config/opencode/skills/<skill>/…`（无需任何 config 项；`config.skills` 为 v2 语法，
// v1 不识别——spike 实证）。故对 `format == "SKILL.md"` 的 source 物化为
// `<root>/<skillID>/SKILL.md`（body 即 SKILL.md 内容）。其它 format（如
// deepseekharness "agents/skills"）未实证 → 跳过并告警，不臆造。
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
	var written string
	for _, src := range s.Sources {
		if src.Format != "SKILL.md" {
			slog.Warn("skill 物化: 跳过未实测 format", "skill", s.ID, "runtime", src.Runtime, "format", src.Format)
			continue
		}
		dir := filepath.Join(root, s.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("skill 物化: 创建目录失败 %s: %w", dir, err)
		}
		p := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(p, []byte(s.Body), 0o644); err != nil {
			return "", fmt.Errorf("skill 物化: 写 %s 失败: %w", p, err)
		}
		written = p
	}
	if written == "" {
		return "", fmt.Errorf("skill 物化: skill %q 无可物化 source（无 SKILL.md format 源）", s.ID)
	}
	// 归属标记（删除联动依据）。
	marker := filepath.Join(root, s.ID, forgeMarker)
	if err := os.WriteFile(marker, []byte(s.ID+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写归属标记 %s 失败: %w", marker, err)
	}
	return written, nil
}

// skillUpToDate 报告 <root>/<id> 是否已按 body 物化且带归属标记（重校幂等判定）。
func skillUpToDate(dir, body string) bool {
	if _, err := os.Stat(filepath.Join(dir, forgeMarker)); err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	return err == nil && string(b) == body
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
		if !hasMaterializableSource(sk) {
			continue
		}
		dir := filepath.Join(root, sk.ID)
		if skillUpToDate(dir, sk.Body) {
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

// hasMaterializableSource 报告 skill 是否含可物化 source（SKILL.md format）。
func hasMaterializableSource(s Skill) bool {
	for _, src := range s.Sources {
		if src.Format == "SKILL.md" {
			return true
		}
	}
	return false
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
