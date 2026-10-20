package skill

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/morehao/ark-mate/internal/core/code"
)

// ——— 技能包导入（D1：POST /skills/import 的解析与校验） ———
//
// 只收 `SKILL.md`：v1 刻意拒绝额外的脚本/资源文件——"导入成功但附件没落盘"是**静默丢失**，
// 比明确拒绝更糟。后续支持附件时，这里再加一个"按相对路径落盘"的实现，而不是把限制删掉。
//
// 解析全程只读内存：不解压到磁盘、不执行包内任何内容、不跟随符号链接。落盘由
// `Plane.WriteSkill` 统一负责（正文扫描 critical → 40001 且零字节落盘）。

const (
	// MaxPackageBytes 技能包上限（压缩包本身与解压后的 SKILL.md 都不得超过它）。
	MaxPackageBytes = 20 << 20
	// maxPackageEntries 条目数上限（防"几万个小文件"拖垮解析）。
	maxPackageEntries = 64
	// maxCompressionRatio 压缩比上限（只在解压后 > 1 MiB 时生效，避免小文件的正常压缩被误判）。
	maxCompressionRatio = 100
	// packageSkillMD 包内唯一允许的正文文件。
	packageSkillMD = "SKILL.md"
)

// Package 一个技能包的解析结果（正文 = 整个 SKILL.md，含 front matter）。
type Package struct {
	// ID 由 front matter 的 `name` 规范化而来；它同时是定义文件名与投放目录名。
	ID string
	// Name 展示名：front matter 有 `title` 时取它，否则取 `name`（平台自己的物化也是这么写的）。
	Name string
	// Description 必填（底座缺 description 会丢弃整条技能）。
	Description string
	// Body SKILL.md 原文（已把 CRLF 折成 LF）。
	Body string
}

// ParsePackage 解析一个 zip 技能包。
//
// 结构：`SKILL.md` 必须在根目录或**唯一一层**包装目录内；其余普通文件一律拒绝
// （macOS 的 `__MACOSX/`、`.DS_Store` 这类打包元数据例外，忽略它们而不是让用户
// 为一个 Finder 生成的目录重新打包）。
func ParsePackage(r io.ReaderAt, size int64) (*Package, error) {
	if size <= 0 {
		return nil, fmt.Errorf("技能包是空文件")
	}
	if size > MaxPackageBytes {
		return nil, fmt.Errorf("技能包 %d 字节，超过 %d 字节上限", size, MaxPackageBytes)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("不是合法的 zip：%w", err)
	}
	if len(zr.File) > maxPackageEntries {
		return nil, fmt.Errorf("技能包条目数 %d 超过上限 %d", len(zr.File), maxPackageEntries)
	}

	var skillFile *zip.File
	for _, f := range zr.File {
		// 目录条目以 `/` 结尾（`zip -r` 会写它们）：先去掉尾斜杠再判路径，
		// 否则 `pkg/` 会被 `path.Clean` 成 `pkg` 从而误报成 zip slip。
		rawName := f.Name
		isDir := strings.HasSuffix(rawName, "/")
		name := strings.TrimSuffix(rawName, "/")
		if name == "" {
			continue
		}
		if strings.Contains(name, `\`) {
			return nil, fmt.Errorf("条目路径含反斜杠（拒绝 Windows 风格路径）：%q", name)
		}
		if strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("条目路径是绝对路径：%q", name)
		}
		clean := path.Clean(name)
		if clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("条目路径越界（zip slip）：%q", name)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("技能包不允许符号链接：%q", name)
		}
		if isDir || f.FileInfo().IsDir() {
			continue
		}
		if isPackageMetadata(clean) {
			continue
		}
		if path.Base(clean) != packageSkillMD {
			return nil, fmt.Errorf("v1 技能包只允许 SKILL.md（额外的脚本/资源会被拒绝）：发现 %q", clean)
		}
		if skillFile != nil {
			return nil, fmt.Errorf("技能包里有多个 SKILL.md（只允许根目录或唯一一层目录内的一份）")
		}
		if dir := path.Dir(clean); dir != "." && strings.Contains(dir, "/") {
			return nil, fmt.Errorf("SKILL.md 只能位于根目录或唯一一层目录内：%q", clean)
		}
		skillFile = f
	}
	if skillFile == nil {
		return nil, fmt.Errorf("技能包里没有 SKILL.md（根目录或唯一一层目录内）")
	}
	if skillFile.UncompressedSize64 > MaxPackageBytes {
		return nil, fmt.Errorf("SKILL.md 解压后 %d 字节，超过 %d 字节上限", skillFile.UncompressedSize64, MaxPackageBytes)
	}
	if skillFile.UncompressedSize64 > 1<<20 && skillFile.CompressedSize64 > 0 &&
		skillFile.UncompressedSize64/skillFile.CompressedSize64 > maxCompressionRatio {
		return nil, fmt.Errorf("SKILL.md 压缩比异常（疑似 zip bomb）：%d → %d 字节",
			skillFile.CompressedSize64, skillFile.UncompressedSize64)
	}

	rc, err := skillFile.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 SKILL.md 失败：%w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxPackageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 SKILL.md 失败：%w", err)
	}
	if len(raw) > MaxPackageBytes {
		return nil, fmt.Errorf("SKILL.md 超过 %d 字节上限", MaxPackageBytes)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.ContainsRune(text, 0) {
		return nil, fmt.Errorf("SKILL.md 含 NUL 字节（不是文本技能包）")
	}

	fm, _, ok := splitPackageFrontmatter(text)
	if !ok {
		return nil, fmt.Errorf("SKILL.md 缺少 YAML front matter（须以 --- 开头并以 --- 结束）")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Title       string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		return nil, fmt.Errorf("SKILL.md 的 front matter 不是合法 YAML：%w", err)
	}
	name := strings.TrimSpace(meta.Name)
	desc := strings.TrimSpace(meta.Description)
	if name == "" {
		return nil, fmt.Errorf("SKILL.md 的 front matter 缺少 name（它是技能 id，形如 weekly-report）")
	}
	if desc == "" {
		return nil, fmt.Errorf("SKILL.md 的 front matter 缺少 description（底座缺它会丢弃整条技能）")
	}
	id := normalizePackageID(name)
	if id == "" {
		return nil, fmt.Errorf("front matter 的 name %q 无法规范化为合法 id（需要小写英文字母或数字）", name)
	}
	if len(id) > 64 {
		return nil, fmt.Errorf("front matter 的 name 规范化后长度 %d 超过 64", len(id))
	}
	display := strings.TrimSpace(meta.Title)
	if display == "" {
		display = name
	}
	return &Package{ID: id, Name: display, Description: desc, Body: text}, nil
}

// ImportPackage 解析并写入一个技能包：解析失败 → 40001 `definition_invalid`；
// 同名 id 已存在 → 40901 `skill_exists`（**不覆盖**）；正文 critical → 40001 且零字节落盘。
//
// 写面不新开一条路：解析结果被折成与 `POST /skills` 相同的 `SkillWriteReq` 后调用 `WriteSkill`，
// 这样"上传"与"手填"经过的闸门逐条相同。
func (m *Plane) ImportPackage(ctx context.Context, r io.ReaderAt, size int64) (*WriteResult, error) {
	pkg, err := ParsePackage(r, size)
	if err != nil {
		return nil, code.NewError(code.ClassRequest, "definition_invalid", "技能包被拒绝："+err.Error(), err)
	}
	req := WriteRequest{
		"id":          rawJSONValue(pkg.ID),
		"name":        rawJSONValue(pkg.Name),
		"description": rawJSONValue(pkg.Description),
		"body":        rawJSONValue(pkg.Body),
		// 空数组 = 全部 runtime（升级安全默认），不是「没有投放对象」。
		"runtimes": rawJSONValue([]string{}),
	}
	return m.WriteSkill(ctx, "", req, true)
}

// rawJSONValue 把值折成定义写面的原文形态（`SkillWriteReq` 只收 json.RawMessage）。
func rawJSONValue(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// 只会是编程错误（这些值全是字符串/字符串数组），返回 null 让写面按"删键"处理。
		return json.RawMessage("null")
	}
	return b
}

// isPackageMetadata 打包工具生成的元数据（不是用户资源）：忽略而不是拒绝。
func isPackageMetadata(name string) bool {
	base := path.Base(name)
	if strings.HasPrefix(name, "__MACOSX/") {
		return true
	}
	return base == ".DS_Store" || base == "Thumbs.db" || base == "desktop.ini" || strings.HasPrefix(base, "._")
}

// splitPackageFrontmatter 切出 YAML front matter 与其余正文（要求以 `---\n` 开头）。
func splitPackageFrontmatter(s string) (fm, rest string, ok bool) {
	const open = "---\n"
	if !strings.HasPrefix(s, open) {
		return "", "", false
	}
	idx := strings.Index(s[len(open):], "\n---")
	if idx < 0 {
		return "", "", false
	}
	start := len(open)
	end := start + idx
	fm = s[start:end]
	rest = strings.TrimPrefix(s[end+len("\n---"):], "\n")
	return fm, rest, true
}

// normalizePackageID 把 front matter 的 name 折成 skill id 词法（小写字母/数字/连字符）。
//
// 非 id 词法字符（中文等）直接丢弃：id 是**底座的硬契约**，不能在平台侧放宽。需要中文展示名
// 时用 front matter 的 `title`——那是给人看的，不参与落点与词法。
func normalizePackageID(name string) string {
	var b strings.Builder
	lastSep := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_' || r == '.' || unicode.IsSpace(r):
			if !lastSep && b.Len() > 0 {
				b.WriteByte('-')
				lastSep = true
			}
		default:
			// 丢弃（中文、emoji 等非 id 词法字符）。
		}
	}
	return strings.Trim(b.String(), "-")
}
