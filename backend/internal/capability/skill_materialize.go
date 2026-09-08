package capability

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

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
// 返回物化文件绝对路径；多次调用幂等（覆盖写）。
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
	return written, nil
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
